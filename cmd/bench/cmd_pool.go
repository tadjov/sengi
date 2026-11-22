package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tadjov/sengi/internal/bench/engine"
	"github.com/tadjov/sengi/internal/bench/meta"
	"github.com/tadjov/sengi/internal/bench/pool"
	"github.com/tadjov/sengi/internal/bench/runner"
	"github.com/tadjov/sengi/internal/bench/spec"
	"github.com/tadjov/sengi/internal/bench/suite"
	"github.com/tadjov/sengi/internal/bench/trackctx"
	"github.com/tadjov/sengi/internal/cli"
	"github.com/tadjov/sengi/internal/storage"
)

type poolFlags struct {
	trackArg     string
	specPath     string
	output       string
	depth        int
	allowPartial bool
}

func newPoolCmd() *cobra.Command {
	var f poolFlags
	cmd := &cobra.Command{
		Use:   "pool [track]",
		Short: "Run queries through all engines, write a TREC-style pool",
		Long: `Generates a deduplicated pool of candidate docs per query, ready to be
judged. Output goes to <track>/trec/pool.yaml by default; override
with --output for ad-hoc files.

The pool file carries a meta block (run_id, tool, engines, depth) so later
artifacts can attest which pool they were derived from.

Any (query, engine) execution that errors fails the pool, since its documents
would be missing from it. --allow-partial writes the pool anyway, lists only
the engines that answered in meta.engines and records the failed pairs in
meta.dropped_executions.`,
		Example: `  bench pool tracks/cc-news/fts
  bench pool tracks/cc-news/fts --depth 50
  bench pool --track tracks/cc-news/fts --output /tmp/adhoc-pool.yaml`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return executePool(cmd, f, args)
		},
	}
	cmd.Flags().StringVar(&f.trackArg, "track", "", "Track path (relative to --track-root)")
	cmd.Flags().StringVar(&f.specPath, "spec", "", "Override spec.yaml path")
	cmd.Flags().IntVar(&f.depth, "depth", 0, "Top-K per engine (0 = spec.defaults.pool_depth or 100)")
	cmd.Flags().StringVar(&f.output, "output", "", "Override pool output path")
	cmd.Flags().BoolVar(&f.allowPartial, "allow-partial", false,
		"Write the pool even when some (query, engine) executions failed, recording them in meta.dropped_executions")
	return cmd
}

func executePool(cmd *cobra.Command, f poolFlags, args []string) error {
	in := trackInputs(f.trackArg, args)
	in.SpecPath = f.specPath
	in.OutputPath = f.output
	return forEachTrack(cmd.Context(), cmd.OutOrStdout(), in, func(tr *trackctx.Track) error {
		return poolTrack(cmd, f, tr)
	})
}

func poolTrack(cmd *cobra.Command, f poolFlags, tr *trackctx.Track) error {
	bs, err := spec.LoadFromFile(tr.Spec)
	if err != nil {
		return fmt.Errorf("load spec: %w", err)
	}

	depth := f.depth
	if depth == 0 {
		depth = bs.Defaults.PoolDepth
	}
	if depth == 0 {
		depth = 100
	}

	runCfg := runner.Config{
		KValues:          []int{depth},
		Runs:             1,
		QueryParallelism: runner.QueryParallelismUnlimited,
	}

	printSpecWarnings(cmd.OutOrStdout(), bs)

	queries, err := collectSuiteQueries(bs)
	if err != nil {
		return fmt.Errorf("load suite queries: %w", err)
	}

	vectorStore, closeVectorStore, err := buildQueryVectorStore(cmd.Context(), bs, tr)
	if err != nil {
		return fmt.Errorf("build vector store: %w", err)
	}
	defer closeVectorStore()
	if err := requireEmbedder(bs, vectorStore); err != nil {
		return err
	}
	runCfg.VectorStore = vectorStore

	executors, cleanup, err := engine.CreateFromSpec(cmd.Context(), bs.Engines)
	if err != nil {
		return fmt.Errorf("create executors: %w", err)
	}
	defer cleanup()
	canonical, err := loadedCanonical(cmd.Context(), cmd.OutOrStdout(), executors)
	if err != nil {
		return err
	}
	if canonical == nil {
		cli.Warn(cmd.OutOrStdout(), "no engine records a corpus load, so the pool names no canonical package and carry-over can't check it")
	}

	var result *runner.BenchmarkResult
	err = cli.Command{Label: "pool " + tr.Name(), Unit: "queries"}.Run(cmd.OutOrStdout(), func(p *cli.Progress) (cli.Summary, error) {
		runCfg.Progress = p
		var runErr error
		result, runErr = runner.New(runCfg).RunAll(cmd.Context(), bs, executors)
		return cli.Summary{}, runErr
	})
	if err != nil {
		return fmt.Errorf("pool run: %w", err)
	}
	dropped := pool.DroppedExecutions(result)
	if len(dropped) > 0 && !f.allowPartial {
		return incompletePoolError(dropped)
	}

	pf := pool.FromResult(result, queries, depth)
	pf.Meta = meta.New("pool")
	pf.Meta.SpecID = bs.ID
	pf.Meta.PoolDepth = depth
	pf.Meta.Engines = pool.AnsweredEngines(result, specEngines(bs))
	pf.Meta.DroppedExecutions = dropped
	pf.Meta.Canonical = canonical

	outPath := f.output
	if outPath == "" {
		outPath = tr.Pool
	}
	for _, se := range pool.ShallowEngines(pf, depth) {
		cli.Warn(cmd.OutOrStdout(), fmt.Sprintf(
			"engine %s contributed at most %d documents to any query; its pool is %d deep, not the %d stamped on meta.pool_depth",
			se.Name, se.MaxReturned, se.MaxReturned, depth))
	}

	if err := pool.WritePoolFile(pf, outPath); err != nil {
		return fmt.Errorf("write pool: %w", err)
	}
	cli.Done(cmd.OutOrStdout(), fmt.Sprintf("Pool written: %s  (queries=%d  run_id=%s)", outPath, len(pf.Queries), pf.Meta.RunID))
	return nil
}

// loadedCanonical reads which canonical package the engines had loaded.
func loadedCanonical(ctx context.Context, w io.Writer, executors map[string]engine.Executor) (*storage.PackageRef, error) {
	loads := make(map[string]storage.PackageRef, len(executors))
	for name, exec := range executors {
		describer, ok := exec.(engine.IndexDescriber)
		if !ok {
			continue
		}
		description, err := describeIndex(ctx, describer)
		if err != nil {
			cli.Warn(w, fmt.Sprintf("canonical package of %q not recorded: %v", name, err))
			continue
		}
		if load := description.Load(); load != nil {
			loads[name] = load.Canonical
		}
	}
	return pool.SharedCanonical(loads)
}

// specEngines lists the engines the spec's jobs run, in spec order.
func specEngines(bs *spec.BenchSpec) []string {
	var engines []string
	for _, job := range bs.Jobs {
		for _, name := range job.Engines {
			if !slices.Contains(engines, name) {
				engines = append(engines, name)
			}
		}
	}
	return engines
}

// collectSuiteQueries reads every job's suite queries by id. A query without
// text fails: the judges grade it, so a pool must carry it and a run compares
// it with the pool's.
func collectSuiteQueries(bs *spec.BenchSpec) (map[string]suite.Query, error) {
	queries := make(map[string]suite.Query)
	seen := make(map[string]struct{})
	for _, job := range bs.Jobs {
		if _, ok := seen[job.Suite]; ok {
			continue
		}
		seen[job.Suite] = struct{}{}
		ls, err := suite.LoadFromFile(job.Suite)
		if err != nil {
			return nil, fmt.Errorf("load suite %q for job %q: %w", job.Suite, job.Name, err)
		}
		for _, q := range ls.Suite.Queries {
			if q.Text == "" {
				return nil, fmt.Errorf("query %q in suite %s has no text: give it the string a user would type", q.ID, job.Suite)
			}
			queries[q.ID] = q
		}
	}
	return queries, nil
}

func incompletePoolError(dropped []meta.DroppedExecution) error {
	cells := make([]string, 0, len(dropped))
	for _, d := range dropped {
		cells = append(cells, fmt.Sprintf("%s/%s (%s)", d.QueryID, d.Engine, d.Error))
	}
	return fmt.Errorf("%d (query, engine) executions failed, so the pool would miss their documents: %s\n"+
		"  • fix the engines and re-run, or pass --allow-partial to write the pool without them",
		len(dropped), strings.Join(cells, "; "))
}
