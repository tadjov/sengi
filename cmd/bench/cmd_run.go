package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tadjov/sengi/internal/bench/engine"
	"github.com/tadjov/sengi/internal/bench/meta"
	"github.com/tadjov/sengi/internal/bench/report"
	"github.com/tadjov/sengi/internal/bench/rungate"
	"github.com/tadjov/sengi/internal/bench/runner"
	"github.com/tadjov/sengi/internal/bench/spec"
	"github.com/tadjov/sengi/internal/bench/trackctx"
	"github.com/tadjov/sengi/internal/cli"
)

type runFlags struct {
	trackArg    string
	specPath    string
	judgments   string
	output      string
	kValues     string
	jobs        string // comma-separated job name filter
	warmup      int
	iters       int
	latencyOnly bool
}

func newRunCmd() *cobra.Command {
	var f runFlags
	cmd := &cobra.Command{
		Use:   "run [track]",
		Short: "Execute the benchmark + produce a report",
		Long: `Runs every job in the track's spec.yaml against its engines, computes IR
metrics + latency, writes a JSON report under <track>/reports/.

The track arg is a path — absolute, or relative to --track-root (default: cwd) —
or a glob, which runs every matching track and writes one report per track.

The judgments file used for scoring resolves in this order:
  1. --judgments <name|path>   (CLI override)
  2. spec.defaults.judgments   (per-track default; a relative path starts at
                                the spec's folder)
  3. none → the run fails before measuring anything

A missing judgments file fails the run the same way, as do judgments graded
against another pool. --latency-only measures latency without judgments; its
report is marked latency_only and never becomes latest.json.

The report is always written. bench run then exits non-zero and leaves
latest.json alone when the run failed:
  • a (query, engine) execution errored
  • a (query, engine) cell ranked differently in a later iteration than in
    the first
  • it measured a (query, engine) cell the pool dropped (--allow-partial)
  • it ran a query the pool holds no entry for
  • it measured an engine the pool never saw
  • it sent a (query, engine) cell another request than the pool did, as
    after editing a template or a param
  • a query's text, category or filters differ from its pool entry, which
    is what the judges graded
  • the judgments leave pooled docs unjudged
  • an engine ranks an ungraded doc within the pool depth

latest.json also stays put, with exit 0, unless the run took every spec job at
the spec's warmup, iterations and K, from the track's own spec.yaml and
spec.defaults.judgments.`,
		Example: `  bench run tracks/global-news-dataset/fts_quality
  bench run tracks/global-news-dataset/news_semantic
  bench run 'tracks/global-news-dataset/*'        # every paradigm of the dataset
  bench run tracks/global-news-dataset/fts_quality --judgments claude-api`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeRun(cmd, f, args)
		},
	}
	cmd.Flags().StringVar(&f.trackArg, "track", "", "Track path (relative to --track-root)")
	cmd.Flags().StringVar(&f.specPath, "spec", "", "Override spec.yaml path")
	cmd.Flags().StringVar(&f.judgments, "judgments", "", "Strategy name (e.g. keyword) or annotations YAML path")
	cmd.Flags().StringVar(&f.output, "output", "", "Override report path (default: <track>/reports/<run_id>.json)")
	cmd.Flags().StringVar(&f.kValues, "k", "3,5,10", "K cut-offs for NDCG/P/R/F1")
	cmd.Flags().StringVar(&f.jobs, "jobs", "", "Comma-separated job names to run (default: all jobs in spec)")
	cmd.Flags().IntVar(&f.warmup, "warmup", 0, "Warmup iterations")
	cmd.Flags().IntVar(&f.iters, "iterations", 0, "Measured iterations (0 = spec.runs.iterations)")
	cmd.Flags().BoolVar(&f.latencyOnly, "latency-only", false, "Measure latency without judgments; the report never becomes latest.json")
	return cmd
}

func executeRun(cmd *cobra.Command, f runFlags, args []string) error {
	ks, err := parseKList(f.kValues)
	if err != nil {
		return err
	}
	in := trackInputs(f.trackArg, args)
	in.SpecPath = f.specPath
	in.OutputPath = f.output
	return forEachTrack(cmd.Context(), cmd.OutOrStdout(), in, func(tr *trackctx.Track) error {
		return runTrack(cmd, f, ks, tr)
	})
}

// newRunConfig resolves the measurement knobs for a run: an explicit flag beats
// the spec default, and both parallelism axes stay serial.
func newRunConfig(f runFlags, bs *spec.BenchSpec, ks []int, kFromFlag bool) runner.Config {
	cfg := runner.Config{
		KValues:            ks,
		RelevanceThreshold: bs.Metrics.RelevanceThreshold,
		WarmupRuns:         firstNonZero(f.warmup, bs.Runs.Warmup),
		Runs:               firstNonZero(f.iters, bs.Runs.Iterations),
		Seed:               rand.Uint64(),
		QueryParallelism:   runner.QueryParallelismSerial,
		EngineParallelism:  runner.EngineParallelismSerial,
	}
	if len(bs.Metrics.KValues) > 0 && !kFromFlag {
		cfg.KValues = bs.Metrics.KValues
	}
	return cfg
}

func runTrack(cmd *cobra.Command, f runFlags, ks []int, tr *trackctx.Track) error {
	bs, err := spec.LoadFromFile(tr.Spec)
	if err != nil {
		return fmt.Errorf("load spec: %w", err)
	}

	judgments, err := loadRunJudgments(f, bs, tr)
	if err != nil {
		return err
	}

	runCfg := newRunConfig(f, bs, ks, cmd.Flags().Changed("k"))
	runCfg.Judgments = judgments.Grades
	runCfg.RankingDepth = judgments.Pool.Meta.PoolDepth
	if err := spec.CheckKValues(runCfg.KValues, judgments.Pool.Meta.PoolDepth); err != nil {
		return fmt.Errorf("k values %v: %w", runCfg.KValues, err)
	}

	printSpecWarnings(cmd.OutOrStdout(), bs)

	// Apply --jobs filter: keep only the named jobs.
	specJobCount := len(bs.Jobs)
	var jobFilter []string
	if f.jobs != "" {
		wanted := make(map[string]bool)
		for _, name := range strings.Split(f.jobs, ",") {
			if n := strings.TrimSpace(name); n != "" {
				wanted[n] = true
			}
		}
		jobFilter = slices.Sorted(maps.Keys(wanted))
		filtered := bs.Jobs[:0]
		for _, job := range bs.Jobs {
			if wanted[job.Name] {
				filtered = append(filtered, job)
			}
		}
		if len(filtered) == 0 {
			var names []string
			for n := range wanted {
				names = append(names, n)
			}
			return fmt.Errorf("--jobs filter matched no jobs (wanted: %s)", strings.Join(names, ", "))
		}
		bs.Jobs = filtered
	}

	// Read before measuring, so an edit made while the engines answer cannot
	// fail the run or lose its report.
	suiteQueries, err := collectSuiteQueries(bs)
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

	corpus, err := collectCorpusInfo(cmd.Context(), executors)
	if err != nil {
		return err
	}
	indexes := collectIndexProvenance(cmd.Context(), cmd.OutOrStdout(), executors)

	var result *runner.BenchmarkResult
	var elapsed time.Duration
	err = cli.Command{Label: "run " + tr.Name(), Unit: "queries"}.Run(cmd.OutOrStdout(), func(p *cli.Progress) (cli.Summary, error) {
		runCfg.Progress = p
		start := time.Now()
		var runErr error
		result, runErr = runner.New(runCfg).RunAll(cmd.Context(), bs, executors)
		elapsed = time.Since(start)
		return cli.Summary{}, runErr
	})
	if err != nil {
		return fmt.Errorf("run benchmark: %w", err)
	}

	rpt := report.Generate(result, &report.GenerateOptions{
		Spec:    bs,
		Corpus:  corpus,
		Indexes: indexes,
	})
	rpt.Provenance.SpecID = bs.ID
	rpt.Provenance.Sources = &meta.Sources{
		Spec:      tr.Relative(tr.Spec),
		Suites:    loadedSuitePaths(bs, tr),
		Pool:      tr.Relative(tr.Pool),
		Judgments: tr.Relative(judgments.Path),
	}
	if judgments.Meta != nil {
		rpt.Provenance.PoolRunID = judgments.Pool.Meta.RunID
		rpt.Provenance.JudgmentsRunID = judgments.Meta.RunID
		rpt.Provenance.CarriedOver = judgments.Meta.CarriedOver
		rpt.Provenance.Judge = judgments.Meta.Judge
		rpt.Provenance.GradedCount = judgments.Meta.GradedCount
		unjudged := judgments.UnjudgedCount()
		rpt.Provenance.UnjudgedCount = &unjudged
		rpt.Provenance.PoolRef = tr.Relative(tr.Pool)
	}
	rpt.Settings = report.RunSettings{
		Warmup:       runCfg.WarmupRuns,
		Iterations:   runCfg.Runs,
		Seed:         runCfg.Seed,
		QueryTimeout: bs.Runs.QueryTimeout,
		JobFilter:    jobFilter,
		LatencyOnly:  f.latencyOnly,
		EngineParams: declaredEngineParams(bs),
	}
	failures := rungate.Failures(result, judgments, suiteQueries)
	rpt.Settings.Incomplete = append(slices.Clone(failures),
		specConformance{
			specOverridden:      f.specPath != "",
			judgmentsOverridden: f.judgments != "" && !samePath(judgments.Path, tr.DefaultJudgmentsPath(bs.Defaults.Judgments)),
			latencyOnly:         f.latencyOnly,
			ranEveryJob:         len(bs.Jobs) == specJobCount,
			measuredAsSpecified: measuredAsSpecified(runCfg, bs),
		}.deviations()...)

	report.WriteTable(rpt, cmd.OutOrStdout())
	fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", cli.Muted.Sprint("Elapsed:"), elapsed.Round(time.Millisecond))

	outPath := f.output
	if outPath == "" {
		outPath = tr.ReportPath(rpt.Provenance.RunID)
	}
	if err := report.WriteJSON(rpt, outPath); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	cli.Done(cmd.OutOrStdout(), "Report written: "+outPath)

	if len(failures) > 0 {
		return fmt.Errorf("%s\n  • report written to %s; latest.json was not moved", strings.Join(failures, "\n"), outPath)
	}
	switch {
	case len(rpt.Settings.Incomplete) > 0:
		cli.Warn(cmd.OutOrStdout(), "latest.json not moved: "+strings.Join(rpt.Settings.Incomplete, "; "))
	case f.output != "":
	default:
		if err := report.WriteLatestPointer(tr.LatestReportPath(), outPath); err != nil {
			cli.Warn(cmd.OutOrStdout(), fmt.Sprintf("could not update latest.json pointer: %v", err))
		}
	}
	return nil
}

// specConformance is how closely a run followed the one its spec defines.
type specConformance struct {
	specOverridden      bool
	judgmentsOverridden bool
	latencyOnly         bool
	ranEveryJob         bool
	measuredAsSpecified bool
}

// deviations lists how the run departs from the one its spec defines.
func (c specConformance) deviations() []string {
	var deviations []string
	if c.specOverridden {
		deviations = append(deviations, "--spec replaced the track's spec.yaml")
	}
	if c.judgmentsOverridden {
		deviations = append(deviations, "--judgments named other judgments than spec.defaults.judgments")
	}
	if c.latencyOnly {
		deviations = append(deviations, "latency-only run, scored against no judgments")
	}
	if !c.ranEveryJob {
		deviations = append(deviations, "--jobs left out some of the spec's jobs")
	}
	if !c.measuredAsSpecified {
		deviations = append(deviations, "--warmup, --iterations or --k differ from spec.yaml")
	}
	return deviations
}

// measuredAsSpecified reports whether cfg measures with the spec's own warmup,
// iterations and K, however they were spelled on the command line. The spec
// loader fills all three, so the spec is compared as loaded.
func measuredAsSpecified(cfg runner.Config, bs *spec.BenchSpec) bool {
	return cfg.WarmupRuns == bs.Runs.Warmup &&
		cfg.Runs == bs.Runs.Iterations &&
		slices.Equal(cfg.KValues, bs.Metrics.KValues)
}

func loadedSuitePaths(bs *spec.BenchSpec, tr *trackctx.Track) []string {
	var suites []string
	for _, job := range bs.Jobs {
		if suite := tr.Relative(job.Suite); !slices.Contains(suites, suite) {
			suites = append(suites, suite)
		}
	}
	return suites
}

func declaredEngineParams(bs *spec.BenchSpec) map[string]map[string]any {
	params := make(map[string]map[string]any)
	for name, eng := range bs.Engines {
		if len(eng.Params) > 0 {
			params[name] = eng.Params
		}
	}
	return params
}

// loadRunJudgments resolves --judgments, else spec.defaults.judgments, and
// refuses a run with neither unless it is latency-only, so an unscored report
// never stands in for the track's results.
func loadRunJudgments(f runFlags, bs *spec.BenchSpec, tr *trackctx.Track) (*rungate.Judgments, error) {
	if f.latencyOnly {
		if f.judgments != "" {
			return nil, errors.New("--latency-only scores nothing, so --judgments would be ignored; pass one of them")
		}
		return rungate.LatencyOnly(), nil
	}

	explicit := f.judgments != ""
	path := tr.JudgmentsPath(f.judgments)
	if !explicit {
		path = tr.DefaultJudgmentsPath(bs.Defaults.Judgments)
	}
	if path == "" {
		return nil, errors.New("no judgments to score against: set spec.defaults.judgments or pass --judgments\n" +
			"  • or pass --latency-only to measure latency without scoring")
	}
	return rungate.Load(rungate.Source{Path: path, Explicit: explicit, PoolPath: tr.Pool})
}

// collectCorpusInfo counts each engine's corpus and fails when an engine
// cannot be counted, is empty, or disagrees with the others, since their
// metrics would then describe different collections.
func collectCorpusInfo(ctx context.Context, executors map[string]engine.Executor) (report.CorpusInfo, error) {
	counts := make(map[string]int64)
	for name, exec := range executors {
		counter, ok := exec.(engine.CorpusCounter)
		if !ok {
			continue
		}
		n, err := counter.CorpusCount(ctx)
		if err != nil {
			return report.CorpusInfo{}, fmt.Errorf("count the corpus of engine %q: %w", name, err)
		}
		counts[name] = n
	}
	if len(counts) == 0 {
		return report.CorpusInfo{}, nil
	}

	var empty []string
	for _, name := range slices.Sorted(maps.Keys(counts)) {
		if counts[name] == 0 {
			empty = append(empty, name)
		}
	}
	if len(empty) > 0 {
		return report.CorpusInfo{}, fmt.Errorf("engines %s index 0 documents; load the corpus before running", strings.Join(empty, ", "))
	}

	var first int64
	seen := false
	for _, n := range counts {
		if !seen {
			first, seen = n, true
			continue
		}
		if n != first {
			return report.CorpusInfo{}, fmt.Errorf("engines index different document counts %v, so their metrics are not comparable", counts)
		}
	}
	return report.CorpusInfo{PerEngine: counts, DocCount: first}, nil
}

// indexDescribeTimeout bounds each engine's provenance read, so an engine that
// does not answer delays the run by this much rather than stalling it.
const indexDescribeTimeout = 15 * time.Second

// collectIndexProvenance records, for every engine, how it indexed the corpus.
// A failed read is warned about and kept in the engine's block as its error.
func collectIndexProvenance(ctx context.Context, w io.Writer, executors map[string]engine.Executor) map[string]report.IndexProvenance {
	provenance := make(map[string]report.IndexProvenance, len(executors))
	for name, exec := range executors {
		describer, ok := exec.(engine.IndexDescriber)
		if !ok {
			provenance[name] = report.IndexProvenance{Error: "engine cannot describe its index"}
			continue
		}
		description, err := describeIndex(ctx, describer)
		if err != nil {
			cli.Warn(w, fmt.Sprintf("index provenance for %q not recorded: %v", name, err))
			provenance[name] = report.IndexProvenance{Error: err.Error()}
			continue
		}
		provenance[name] = report.IndexProvenance{IndexDescription: *description}
	}
	return provenance
}

func describeIndex(ctx context.Context, describer engine.IndexDescriber) (*engine.IndexDescription, error) {
	ctx, cancel := context.WithTimeout(ctx, indexDescribeTimeout)
	defer cancel()
	return describer.DescribeIndex(ctx)
}

func firstNonZero(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}
