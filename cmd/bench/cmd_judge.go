package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/tadjov/sengi/internal/bench/judgment"
	"github.com/tadjov/sengi/internal/bench/meta"
	"github.com/tadjov/sengi/internal/bench/pool"
	"github.com/tadjov/sengi/internal/bench/spec"
	"github.com/tadjov/sengi/internal/bench/trackctx"
	"github.com/tadjov/sengi/internal/cli"
	"github.com/tadjov/sengi/internal/embedding"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/storage/factory"
	"github.com/tadjov/sengi/internal/storage/pg"
)

type judgeFlags struct {
	trackArg       string
	poolPath       string
	output         string
	strategy       string
	pg             string
	concurrency    int
	batchSize      int
	batchSeed      uint64
	resume         bool
	carryOver      string
	carryOverPool  string
	allowUnjudged  bool
	force          bool
	provider       string
	model          string
	effort         string
	apiBaseURL     string
	cliBinary      string
	embeddingBase  string
	embeddingModel string
}

// articleReaderOpener opens the store bench judge reads pooled articles from.
type articleReaderOpener func(cmd *cobra.Command, pgConn string) (storage.Reader, error)

type judgeCommand struct {
	flags      judgeFlags
	openReader articleReaderOpener
}

func newJudgeCmd() *cobra.Command { return newJudgeCmdWithReader(openArticleReader) }

func newJudgeCmdWithReader(openReader articleReaderOpener) *cobra.Command {
	var f judgeFlags
	cmd := &cobra.Command{
		Use:   "judge [track]",
		Short: "Grade a pool file with the chosen strategy",
		Long: `Grades every (query, doc) pair in the track's pool using one of:

  keyword     — deterministic token-overlap baseline (no network, no LLM)
  bm25        — pool-local Okapi BM25 (no network; rewards rare terms)
  vector      — the pool's ten docs nearest the query by cosine grade 3, the
                rest 0; doc vectors from PG (--pg), query vectors from
                trec/query-vectors, else Ollama (EMBEDDING_BASE_URL)
  hybrid      — BM25 + vector fusion, vectors as for vector
  llm         — LLM-as-judge, one doc per request (--batch N groups N);
                pick the endpoint with --provider
  jev         — TypeSafe's Jev, one doc per request, a cross-judge of llm;
                needs TYPESAFE_API_KEY (cmd/bench/.env), grades with the pinned
                ` + judgment.DefaultJevModel + ` unless --model names another jev-<major>.<minor>.<patch>
  manual      — writes grade:-1 placeholders for hand grading; re-running keeps
                existing hand grades and adds placeholders for new pool docs
                (--force starts over)

The llm strategy is vendor-neutral: --provider names the endpoint
(` + strings.Join(judgment.KnownProviders(), ", ") + `), and registering a new
provider is all it takes to add Codex or ChatGPT.

--model takes a full model id or a shorthand for the current generation
(` + strings.Join(judgment.ClaudeModelAliases(), ", ") + `) and defaults to ` + judgment.DefaultJudgeModel + `,
the model of the final cc-news judgments; --model haiku is the cheap option for
trial runs. --effort sets how much the model may think before it grades
(default low). The resolved id, the effort, the provider and the prompt version
are recorded in the artifact's judge block, so qrels always name the grader
that produced them.

Output goes to <track>/trec/annotations.<strategy>.yaml, where <strategy> is the
strategy for heuristic and jev judges and the provider for llm judges.
Multiple strategies live side-by-side; switch which one bench run scores
against via --judgments <name>.

Resumable: re-run with the same --strategy and --resume to skip docs already
graded. bm25, vector and hybrid grade the whole pool at once, so they skip only
queries that are fully graded and regrade the rest whole. Atomic writes mean
Ctrl-C is safe.

After a re-pool, --carry-over <annotations> --carry-over-pool <their pool>
reuses each old grade whose judge and query are unchanged and grades only the
new docs. Take the old pool from git; batched and whole-pool judges can't carry
over, and neither can a pool built on another canonical package.`,
		Example: `  bench judge tracks/global-news-dataset/fts_quality --strategy keyword
  bench judge tracks/global-news-dataset/fts_quality --strategy llm --provider claude-cli --model sonnet
  bench judge tracks/global-news-dataset/fts_quality --strategy llm --provider claude-api --model sonnet --batch 20 --resume
  bench judge --pool /tmp/p.yaml --strategy keyword --output /tmp/a.yaml
  bench judge tracks/cc-news/fuzzy --strategy llm --carry-over /tmp/old-annotations.yaml --carry-over-pool /tmp/old-pool.yaml
  bench judge tracks/cc-news/fts --strategy jev --pool /tmp/sample.yaml --output /tmp/a.jev.yaml`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return judgeCommand{flags: f, openReader: openReader}.execute(cmd, args)
		},
	}
	cmd.Flags().StringVar(&f.trackArg, "track", "", "Track path (relative to --track-root)")
	cmd.Flags().StringVar(&f.poolPath, "pool", "", "Override pool YAML path")
	cmd.Flags().StringVar(&f.output, "output", "", "Override annotations output path")
	cmd.Flags().StringVar(&f.strategy, "strategy", string(judgment.StrategyKeyword), "Judge strategy")
	cmd.Flags().StringVar(&f.pg, "pg", "", "Postgres connection (or set PG_CONNECTION_STRING)")
	cmd.Flags().IntVar(&f.concurrency, "concurrency", 4, "Parallel Grade calls (per-doc strategies)")
	cmd.Flags().IntVar(&f.batchSize, "batch", 0, "LLM docs per request (default one); --strategy llm only")
	cmd.Flags().Uint64Var(&f.batchSeed, "seed", 1, "Seed that shuffles each query's pool into --batch batches")
	cmd.Flags().BoolVar(&f.resume, "resume", false, "Skip docs already graded in --output; bm25, vector and hybrid skip only fully graded queries")
	cmd.Flags().StringVar(&f.carryOver, "carry-over", "", "Reuse grades from these older judgments where judge and query are unchanged")
	cmd.Flags().StringVar(&f.carryOverPool, "carry-over-pool", "", "The pool the --carry-over judgments graded")
	cmd.Flags().BoolVar(&f.allowUnjudged, "allow-unjudged", false, "Exit 0 even when some pooled docs end unjudged; a query with no usable terms still fails")
	cmd.Flags().BoolVar(&f.force, "force", false, "Manual only: discard existing hand grades and rewrite the placeholders")
	cmd.Flags().StringVar(&f.provider, "provider", "",
		"LLM endpoint for --strategy llm (default "+string(judgment.DefaultProvider)+"): "+strings.Join(judgment.KnownProviders(), " | "))
	cmd.Flags().StringVar(&f.model, "model", judgment.DefaultJudgeModel,
		"LLM judge model id or alias ("+strings.Join(judgment.ClaudeModelAliases(), " | ")+"); haiku for cheap trial runs; "+
			"jev takes a pinned jev-<major>.<minor>.<patch> and defaults to "+judgment.DefaultJevModel)
	cmd.Flags().StringVar(&f.effort, "effort", string(judgment.EffortLow),
		"Effort the llm judge's model grades at: "+strings.Join(judgment.KnownEfforts(), " | "))
	cmd.Flags().StringVar(&f.apiBaseURL, "api-base", "", "API base URL: Anthropic's for claude-api, TypeSafe's for jev")
	cmd.Flags().StringVar(&f.cliBinary, "cli-binary", "", "claude CLI binary path")
	cmd.Flags().StringVar(&f.embeddingBase, "embedding-base", "", "Embedding endpoint for vector/hybrid (or EMBEDDING_BASE_URL)")
	cmd.Flags().StringVar(&f.embeddingModel, "embedding-model", "", "Embedding model for vector/hybrid (or EMBEDDING_MODEL)")
	return cmd
}

func (j judgeCommand) execute(cmd *cobra.Command, args []string) error {
	in := trackInputs(j.flags.trackArg, args)
	in.PoolPath = j.flags.poolPath
	in.OutputPath = j.flags.output
	return forEachTrack(cmd.Context(), cmd.OutOrStdout(), in, func(tr *trackctx.Track) error {
		return j.judgeTrack(cmd, tr)
	})
}

func (j judgeCommand) judgeTrack(cmd *cobra.Command, tr *trackctx.Track) error {
	f := j.flags
	poolPath := f.poolPath
	if poolPath == "" {
		poolPath = tr.Pool
	}
	pf, err := pool.ReadPoolFile(poolPath)
	if err != nil {
		return fmt.Errorf("read pool: %w", err)
	}
	poolRef := tr.Relative(poolPath)

	kind, provider, err := resolveJudge(f)
	if err != nil {
		return err
	}
	if f.carryOver != "" && kind == judgment.StrategyManual {
		return errors.New("--carry-over doesn't apply to --strategy manual, which already keeps hand grades")
	}
	if f.force && kind != judgment.StrategyManual {
		return fmt.Errorf("--force applies only to --strategy manual; %s rewrites its file on every run without --resume", kind)
	}
	if f.batchSize > 0 && kind != judgment.StrategyLLM {
		return fmt.Errorf("--batch applies only to --strategy llm; %s grades without batches", kind)
	}
	// llm judges file under their provider, so two vendors can grade one track.
	outPath := f.output
	if outPath == "" {
		outPath = tr.JudgmentsPath(judgment.JudgmentSetName(kind, provider))
	}

	// Manual grades nothing, so it needs no article reader.
	if kind == judgment.StrategyManual {
		var existing *judgment.File
		if !f.force {
			if existing, err = judgment.ReadFileIfExists(outPath); err != nil {
				return fmt.Errorf("read existing manual judgments: %w", err)
			}
			if existing != nil && existing.Judge().Strategy != string(judgment.StrategyManual) {
				return fmt.Errorf("%s was graded by %q, not by hand: merging would relabel its grades as manual; pass --force to replace it",
					outPath, existing.Judge())
			}
		}
		jf, err := manualJudgments{pool: pf, poolRef: poolRef, path: outPath}.write(existing)
		if err != nil {
			return err
		}
		cli.Done(cmd.OutOrStdout(), fmt.Sprintf("Manual template written: %s  (queries=%d)", outPath, len(jf.Queries)))
		return nil
	}

	opts := judgment.StrategyOptions{
		Provider:    provider,
		Model:       f.model,
		APIBaseURL:  f.apiBaseURL,
		CLIBinary:   f.cliBinary,
		Concurrency: f.concurrency,
	}
	// Only an omitted --model means jev's pin, since the default is a claude id.
	if kind == judgment.StrategyJev && !cmd.Flags().Changed("model") {
		opts.Model = judgment.DefaultJevModel
	}
	if kind == judgment.StrategyLLM {
		if opts.Effort, err = judgment.ParseEffort(f.effort); err != nil {
			return fmt.Errorf("--effort: %w", err)
		}
	}
	if kind == judgment.StrategyVector || kind == judgment.StrategyHybrid {
		store, model, closeStore, err := buildVectorStore(cmd.Context(), f, tr)
		if err != nil {
			return err
		}
		defer closeStore()
		opts.VectorStore = store
		opts.EmbeddingModel = model
	}

	strat, err := judgment.NewStrategy(kind, opts)
	if err != nil {
		return err
	}

	reader, err := j.openReader(cmd, f.pg)
	if err != nil {
		return err
	}

	cfg := judgment.RunnerConfig{
		Strategy:    strat,
		Reader:      reader,
		Concurrency: f.concurrency,
		BatchSize:   f.batchSize,
		BatchSeed:   f.batchSeed,
	}
	// Every flush carries this meta, so an interrupted run's file names its grader.
	judge := cfg.Judge()
	runMeta := meta.New("judge")
	runMeta.Judge = &judge
	runMeta.PoolRef = poolRef
	runMeta.PoolRunID = pf.Meta.RunID
	runMeta.RelevanceScale = []int{0, 1, 2, 3}
	var carried *judgment.File
	if f.carryOver != "" {
		carry, err := carryOverGrades(f, pf, judge, strat)
		if err != nil {
			return err
		}
		carried = carry.Grades
		runMeta.CarriedOver = &meta.CarriedOver{
			JudgmentsRunID: carry.Grades.Meta.RunID,
			PoolRunID:      carry.Grades.Meta.PoolRunID,
			Grades:         carry.Count,
		}
		msg := fmt.Sprintf("Carry-over: %d grades from %s", carry.Count, carry.Grades.Meta.RunID)
		if len(carry.ChangedQueries) > 0 {
			msg += fmt.Sprintf("; changed queries graded again: %s", strings.Join(carry.ChangedQueries, ", "))
		}
		if !carry.CorpusChecked {
			msg += "; corpus not checked, a pool names no canonical package"
		}
		cli.Warn(cmd.OutOrStdout(), msg)
	}
	var existing *judgment.File
	if f.resume {
		if existing, err = judgment.ReadFileIfExists(outPath); err != nil {
			return fmt.Errorf("load prior judgments: %w", err)
		}
		if existing != nil {
			if err := checkResumeCompat(existing, judge); err != nil {
				return err
			}
			if err := checkPoolBinding(existing.Meta.PoolRunID, pf); err != nil {
				return fmt.Errorf("--resume: %w\n  • re-run without --resume to grade the current pool from scratch", err)
			}
			// A resumed file keeps the grades an earlier run carried over.
			if runMeta.CarriedOver == nil {
				runMeta.CarriedOver = existing.Meta.CarriedOver
			}
			cli.Warn(cmd.OutOrStdout(), fmt.Sprintf("Resume: loaded %d prior queries from %s", len(existing.Queries), outPath))
		}
	}
	cfg.Existing = judgment.MergeGrades(carried, existing)
	writer := judgment.NewIncrementalWriter(outPath, runMeta)
	writer.Seed(cfg.Existing, pf)
	cfg.Sink = writer.Append
	var final *judgment.File
	var runErr error
	err = cli.Command{Label: "judge " + tr.Name(), Unit: "docs"}.Run(cmd.OutOrStdout(), func(p *cli.Progress) (cli.Summary, error) {
		out := p.Above(cmd.OutOrStdout())
		cfg.OnQueryStart = func(qid string, total, skipped int) {
			if skipped > 0 {
				fmt.Fprintf(out, "%s grading %d docs %s\n",
					cli.Accent.Sprintf("[%s]", qid), total-skipped,
					cli.Muted.Sprintf("(%d already done)", skipped))
			} else {
				fmt.Fprintf(out, "%s grading %d docs\n",
					cli.Accent.Sprintf("[%s]", qid), total)
			}
		}
		cfg.OnQueryDone = func(qp judgment.QueryProgress) {
			fmt.Fprintf(out, "%s %s graded=%d skipped=%d unjudged=%d %s\n",
				cli.Accent.Sprintf("[%s]", qp.QueryID),
				cli.Success.Sprint("done:"),
				qp.Graded, qp.Skipped, qp.Unjudged,
				cli.Muted.Sprint(formatHistogram(qp.Histogram)))
		}
		cfg.Progress = p
		final, runErr = judgment.NewRunner(cfg).Run(cmd.Context(), pf)
		if runErr != nil && !judgment.FailsWholeQuery(runErr) {
			return cli.Summary{}, fmt.Errorf("judge run: %w", runErr)
		}
		return cli.Summary{}, nil
	})
	if err != nil {
		return err
	}

	// Run keeps only the queries still in the pool.
	final.Meta = runMeta
	if err := judgment.WriteFile(final, outPath); err != nil {
		return fmt.Errorf("finalise judgments: %w", err)
	}

	cli.Done(cmd.OutOrStdout(), fmt.Sprintf("Judgments written: %s  (judge=%s  queries=%d  run_id=%s)",
		outPath, judge, len(final.Queries), final.Meta.RunID))
	if runErr != nil {
		return fmt.Errorf("judge run: %w", runErr)
	}
	return requireJudged(final, f.allowUnjudged)
}

func carryOverGrades(f judgeFlags, current *pool.PoolFile, judge meta.Judge, strat judgment.Strategy) (judgment.CarriedGrades, error) {
	if f.carryOverPool == "" {
		return judgment.CarriedGrades{}, errors.New("--carry-over needs --carry-over-pool, the pool those judgments graded")
	}
	if _, wholePool := strat.(judgment.PoolStrategy); wholePool {
		return judgment.CarriedGrades{}, fmt.Errorf("%s grades each doc against the whole pool, so its grades can't be carried over", strat.Name())
	}
	prior, err := judgment.ReadFile(f.carryOver)
	if err != nil {
		return judgment.CarriedGrades{}, fmt.Errorf("read --carry-over: %w", err)
	}
	priorPool, err := pool.ReadPoolFile(f.carryOverPool)
	if err != nil {
		return judgment.CarriedGrades{}, fmt.Errorf("read --carry-over-pool: %w", err)
	}
	carried, err := judgment.CarryOver(judgment.CarryOverInput{Prior: prior, PriorPool: priorPool, Pool: current, Judge: judge})
	if err != nil {
		return judgment.CarriedGrades{}, fmt.Errorf("--carry-over: %w", err)
	}
	return carried, nil
}

// resolveJudge maps --strategy and --provider to what the strategy factory needs.
func resolveJudge(f judgeFlags) (judgment.StrategyKind, judgment.Provider, error) {
	kind := judgment.StrategyKind(f.strategy)
	switch kind {
	case judgment.StrategyLLM:
		return kind, judgment.Provider(f.provider), nil
	case judgment.StrategyKeyword, judgment.StrategyBM25, judgment.StrategyVector,
		judgment.StrategyHybrid, judgment.StrategyJev, judgment.StrategyManual:
		if f.provider != "" {
			return "", "", fmt.Errorf("--provider applies only to --strategy llm, not %s", kind)
		}
		return kind, "", nil
	}

	// A provider in --strategy is the old spelling; point to the explicit form.
	if _, p, err := judgment.ParseSelector(f.strategy); err == nil {
		return "", "", fmt.Errorf("%q names a provider, not a strategy — use --strategy llm --provider %s",
			f.strategy, p)
	}
	return "", "", fmt.Errorf("unknown strategy %q (known: %s)",
		f.strategy, strings.Join(judgment.KnownJudgeStrategies(), ", "))
}

// checkResumeCompat refuses to resume a file another judge graded.
func checkResumeCompat(prior *judgment.File, now meta.Judge) error {
	was := prior.Judge()
	if (was == meta.Judge{}) {
		return nil
	}
	differences := was.Differences(now)
	if len(differences) == 0 {
		return nil
	}
	return fmt.Errorf(
		"--resume judge mismatch: the existing file was graded by another judge (%s)\n"+
			"  • re-run without --resume to re-grade cleanly under the new judge",
		strings.Join(differences, "; "))
}

// buildVectorStore reads query vectors from the track's package, else Ollama,
// and doc vectors from Postgres.
func buildVectorStore(ctx context.Context, f judgeFlags, tr *trackctx.Track) (storage.VectorStore, string, func(), error) {
	noop := func() {}
	pgConn := envOrFlag("PG_CONNECTION_STRING", f.pg)
	if pgConn == "" {
		return nil, "", noop, fmt.Errorf("vector/hybrid judging requires --pg or PG_CONNECTION_STRING")
	}
	packageDir, found, err := queryVectorsPackage(tr)
	if err != nil {
		return nil, "", noop, err
	}
	if found {
		bs, err := spec.LoadFromFile(tr.Spec)
		if err != nil {
			return nil, "", noop, fmt.Errorf("load spec to check query vectors: %w", err)
		}
		store, closeStore, err := openQueryVectors(ctx, queryVectorsSource{spec: bs, packageDir: packageDir, postgres: pgConn})
		if err != nil {
			return nil, "", noop, err
		}
		return store, store.Model(), closeStore, nil
	}
	baseURL := envOrFlag("EMBEDDING_BASE_URL", f.embeddingBase)
	if baseURL == "" {
		return nil, "", noop, fmt.Errorf("vector/hybrid judging requires trec/query-vectors, or --embedding-base or EMBEDDING_BASE_URL (ollama endpoint)")
	}
	client, err := embedding.NewOllamaClient(baseURL)
	if err != nil {
		return nil, "", noop, fmt.Errorf("embedding client: %w", err)
	}
	dim, err := embedding.DimFromEnv()
	if err != nil {
		return nil, "", noop, err
	}
	store, err := factory.NewVectorStore(ctx, factory.VectorStoreConfig{
		PgConnStr:       pgConn,
		EmbeddingClient: client,
		Model:           envOrFlag("EMBEDDING_MODEL", f.embeddingModel),
		Dim:             dim,
	})
	if err != nil {
		return nil, "", noop, err
	}
	return store, store.Model(), noop, nil
}

// openArticleReader opens the Postgres reader for pooled articles.
func openArticleReader(cmd *cobra.Command, pgConn string) (storage.Reader, error) {
	conn := envOrFlag("PG_CONNECTION_STRING", pgConn)
	if conn == "" {
		return nil, fmt.Errorf("reading pooled articles requires --pg or PG_CONNECTION_STRING")
	}
	reader, err := factory.NewReader(cmd.Context(), factory.StorageConfig{
		Type: storage.PG,
		Pg:   &pg.PoolConfig{ConnStr: conn},
	})
	if err != nil {
		return nil, fmt.Errorf("create reader: %w", err)
	}
	return reader, nil
}

// buildManualJudgments adds a -1 placeholder for each pooled doc without a hand
// grade and keeps every existing grade.
func buildManualJudgments(pf *pool.PoolFile, existing *judgment.File) *judgment.File {
	jf := &judgment.File{}
	if existing != nil {
		jf.Queries = existing.Queries
	}
	entryIndex := make(map[string]int, len(jf.Queries))
	for i, qe := range jf.Queries {
		entryIndex[qe.QueryID] = i
	}
	for _, entry := range pf.Queries {
		i, ok := entryIndex[entry.QueryID]
		if !ok {
			i = len(jf.Queries)
			entryIndex[entry.QueryID] = i
			jf.Queries = append(jf.Queries, judgment.Entry{QueryID: entry.QueryID})
		}
		known := make(map[uuid.UUID]bool, len(jf.Queries[i].Docs))
		for _, d := range jf.Queries[i].Docs {
			known[d.DocID] = true
		}
		for _, d := range entry.Docs {
			if !known[d.DocID] {
				jf.Queries[i].Docs = append(jf.Queries[i].Docs, judgment.GradedDoc{DocID: d.DocID, Grade: judgment.GradeUnjudged})
			}
		}
	}
	return jf
}

// manualJudgments is a hand-grading file bound to the pool it was built from.
type manualJudgments struct {
	pool *pool.PoolFile
	// poolRef is relative to the track.
	poolRef string
	path    string
}

// write merges existing (nil starts over) with placeholders for missing docs and
// writes the file.
func (m manualJudgments) write(existing *judgment.File) (*judgment.File, error) {
	jf := buildManualJudgments(m.pool, existing)
	judge := judgment.NewManualStrategy().Describe()
	jf.Meta = meta.New("judge")
	jf.Meta.Judge = &judge
	jf.Meta.PoolRef = m.poolRef
	jf.Meta.PoolRunID = m.pool.Meta.RunID
	if err := judgment.WriteFile(jf, m.path); err != nil {
		return nil, fmt.Errorf("write manual judgments: %w", err)
	}
	return jf, nil
}

// requireJudged fails on an ungraded doc, which bench run would score as non-relevant.
func requireJudged(jf *judgment.File, allowUnjudged bool) error {
	_, unjudged := jf.GradeCounts()
	if unjudged == 0 || allowUnjudged {
		return nil
	}
	return fmt.Errorf("%d pooled docs are unjudged: re-run with --resume to retry them, or pass --allow-unjudged to keep the file as is", unjudged)
}

func formatHistogram(h map[int]int) string {
	if len(h) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("(")
	for g := 3; g >= 0; g-- {
		if g < 3 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "%d:%d", g, h[g])
	}
	b.WriteString(")")
	return b.String()
}
