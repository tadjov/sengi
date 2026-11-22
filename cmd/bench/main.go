package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tadjov/sengi/internal/bench/judgment"
	"github.com/tadjov/sengi/internal/bench/spec"
	"github.com/tadjov/sengi/internal/cli"
	"github.com/tadjov/sengi/pkg/config/env"
)

func init() {
	// spec validates judgment-set names without importing judgment.
	spec.KnownStrategies = judgment.KnownStrategies
}

const (
	cliName  = "bench"
	cliShort = "Search engine quality + latency benchmark"
	cliLong  = `bench evaluates full-text, vector, and hybrid search queries against multiple
engines (Postgres, ParadeDB, Elasticsearch, the sengi API), produces
IR-quality metrics (NDCG, MAP, MRR, Bpref, P/R/F1) and latency statistics.

Typical pipeline (pass the track path as a positional arg):

  1. bench init <track>              scaffold a track folder
  2. bench validate <track>          dry-run every query through each engine
  3. bench pool <track>              gather candidate docs → trec/pool.yaml
  4. bench judge <track>             grade with keyword (default) or LLM strategy
  5. bench run <track>               execute + report (reads spec.defaults.judgments)
  6. bench export <track> --format html    shareable HTML report
     bench export <track> --format qrels  TREC qrels for trec_eval / R / Python

  bench status <track>               see where you left off
  bench diff runs <track>            compare latest two runs
  bench diff judgments <track> --judgments a,b
                                     compare judgment sets
  bench clean  <track>               remove old report files (keep N newest)

A track is any folder holding spec.yaml + suite.yaml + trec/ — nothing above it
is assumed, so tracks/global-news-dataset/fts_quality and benches/b1 are both
fine. The arg is an ordinary path: absolute, or relative to the track root.
validate/pool/judge/run/status also take a glob — quote it:
bench run 'tracks/global-news-dataset/*'.

--track-root <dir> (or BENCH_TRACK_ROOT) is where relative track paths start,
defaulting to the current directory.

  BENCH_TRACK_ROOT=tracks/global-news-dataset bench run news_fuzzy
  BENCH_TRACK_ROOT=tracks/global-news-dataset bench run '*'
`
)

func main() {
	// Loaded before the flags are defined: --track-root defaults to BENCH_TRACK_ROOT.
	if err := env.LoadDotEnv(os.Getenv("ENV"), "cmd/bench/.env"); err != nil {
		slog.Info("Skipping .env environment variables", "error", err)
	}
	root := &cobra.Command{
		Use:           cliName,
		Short:         cliShort,
		Long:          cliLong,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&trackRoot, "track-root", os.Getenv(trackRootEnv),
		"Base directory for relative track paths (default $"+trackRootEnv+", else cwd)")
	root.AddCommand(
		newRunCmd(),
		newPoolCmd(),
		newJudgeCmd(),
		newGradeCmd(),
		newValidateCmd(),
		newInitCmd(),
		newShowCmd(),
		newExportCmd(),
		newStatusCmd(),
		newDiffCmd(),
		newCleanCmd(),
		newReportCmd(), // top-level alias for bench show report
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	if err := root.ExecuteContext(ctx); err != nil {
		// Printed, not logged: slog would escape the hint lines' newlines.
		fmt.Fprintf(os.Stderr, "%s %v\n", cli.Failure.Sprint("Error:"), err)
		os.Exit(1)
	}
}
