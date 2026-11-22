package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tadjov/sengi/internal/env"
)

const (
	cliName  = "datapipe"
	cliShort = "Sengi data pipeline: preprocess a dataset, embed it, load it into an engine"
	cliLong  = `datapipe moves news data from a raw dataset into one search engine.

Stages:
  preprocess           map a raw data package into a canonical data package
  embed                embed a canonical package's articles through an embedding
                       API into an embeddings package
  load                 load a canonical package, and optionally an embeddings
                       package built from it, into native, paradedb, tiger or es

Typical flow:
  datapipe preprocess datasets/<name>/raw \
      --mapping datasets/<name>/mapping.yaml --output datasets/<name>/canonical
  datapipe embed datasets/<name>/canonical/<version>/datapackage.json
  datapipe load datasets/<name>/canonical/<version>/datapackage.json --engine native \
      --embeddings datasets/<name>/embeddings/<tag-dir>/<version>/datapackage.json
`
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		slog.Error("datapipe failed", "error", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           cliName,
		Short:         cliShort,
		Long:          cliLong,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(*cobra.Command, []string) {
			if err := env.LoadDotEnv(".env"); err != nil {
				slog.Info("Skipping .env environment variables", "error", err)
			}
		},
	}
	root.AddCommand(
		newPreprocessCmd(),
		newEmbedCmd(),
		newLoadCmd(),
	)
	return root
}
