package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

const (
	cliName = "datapipe"
	// cliVersion is the release a package lock names; bump it when a stage writes something new.
	cliVersion = "0.0.0"
	cliShort   = "Sengi data pipeline: preprocess a dataset, embed it, load it into an engine"
	cliLong    = `datapipe moves news data from a raw dataset into one search engine.

Stages:
  preprocess           turn a raw data package into a canonical data package, as
                       the dataset's preprocess.yaml says
  embed                embed a canonical package's articles through an embedding
                       API into an embeddings package
  load                 load a canonical package, and optionally an embeddings
                       package built from it, into native, paradedb, tiger or es

Typical flow:
  datapipe preprocess datasets/<name>/preprocess.yaml --version v1
  datapipe embed --canonical datasets/<name>/canonical/<version>/datapackage.json
  datapipe load --engine native \
      --canonical datasets/<name>/canonical/<version>/datapackage.json \
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
	}
	root.AddCommand(
		newPreprocessCmd(),
		newEmbedCmd(),
		newLoadCmd(),
	)
	return root
}
