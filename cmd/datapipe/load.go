package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/tadjov/sengi/internal/cli"
	"github.com/tadjov/sengi/internal/ingest"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/storage/factory"
	"github.com/tadjov/sengi/pkg/config/env"
)

const (
	defaultLoadBatchSize   = 1_000
	defaultLoadConcurrency = 1
)

type loadOptions struct {
	Engine      string
	Canonical   string
	Embeddings  string
	SkipIndex   bool
	Trial       bool
	BatchSize   int
	Concurrency int
	RecordsDir  string
}

func newLoadCmd() *cobra.Command {
	var opts loadOptions
	cmd := &cobra.Command{
		Use:   "load",
		Short: "Load a canonical package, and optionally its embeddings, into one engine",
		Long: `load verifies a canonical package, and an embeddings package built from it,
loads both into an empty engine and builds the engine's indexes after the data.
The load record goes to the engine and to datasets/<ds>/loads/<engine>/.

The connection comes from the environment: PG_CONNECTION_STRING for native,
paradedb and tiger, ES_ADDRESSES and ES_INDEX_NAME for es.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := env.LoadDotEnv(os.Getenv("ENV")); err != nil {
				slog.Info("Skipping .env environment variables", "error", err)
			}
			return runLoad(cmd.Context(), cmd.OutOrStdout(), opts)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&opts.Engine, "engine", "", "engine to load: native, paradedb, tiger or es")
	flags.StringVar(&opts.Canonical, "canonical", "", "canonical package descriptor: a path, https:// or s3://")
	flags.StringVar(&opts.Embeddings, "embeddings", "", "embeddings package descriptor built from the canonical package")
	flags.BoolVar(&opts.SkipIndex, "skip-index", false, "stop after the data: build no index and write no load record")
	flags.BoolVar(&opts.Trial, "trial", false, "accept an embeddings package from a --limit trial")
	flags.IntVar(&opts.BatchSize, "batch-size", defaultLoadBatchSize, "rows per COPY or bulk request, the same on every engine")
	flags.IntVar(&opts.Concurrency, "concurrency", defaultLoadConcurrency, "batches written at once: COPY connections on Postgres, bulk requests in flight on Elasticsearch")
	flags.StringVar(&opts.RecordsDir, "records-dir", "", "directory for the load record; derived from a local canonical path by default")
	_ = cmd.MarkFlagRequired("engine")
	_ = cmd.MarkFlagRequired("canonical")
	return cmd
}

func runLoad(ctx context.Context, out io.Writer, opts loadOptions) error {
	engine, err := storage.ParseEngine(opts.Engine)
	if err != nil {
		return err
	}
	recordsDir := opts.RecordsDir
	if recordsDir == "" && !opts.SkipIndex {
		if recordsDir, err = derivedRecordsDir(opts.Canonical, engine); err != nil {
			return err
		}
	}

	packaged, err := ingest.OpenCorpus(ctx, ingest.CorpusOptions{Canonical: opts.Canonical, Embeddings: opts.Embeddings, Trial: opts.Trial})
	if err != nil {
		return err
	}
	defer packaged.Close()
	corpus := packaged.Corpus(ctx)

	storageCfg, err := factory.LoadEnvFor(engine.Type())
	if err != nil {
		return fmt.Errorf("load %s connection settings: %w", engine, err)
	}
	loader, release, err := factory.NewCorpusLoader(ctx, factory.LoaderConfig{Engine: engine, Pg: storageCfg.Pg, Es: storageCfg.Es})
	if err != nil {
		return err
	}
	defer release()

	command := cli.Command{Title: "sengi load", Fields: loadFields(engine, corpus, opts), Label: "load", Unit: "rows"}
	return command.Run(out, func(p *cli.Progress) (cli.Summary, error) {
		record, err := loader.LoadCorpus(ctx, corpus, storage.LoadOptions{
			BatchSize:   opts.BatchSize,
			Concurrency: opts.Concurrency,
			SkipIndex:   opts.SkipIndex,
			Progress:    p,
		})
		if err != nil {
			return cli.Summary{}, fmt.Errorf("load %s: %w", engine, err)
		}

		fields := []cli.Field{
			{Label: "articles", Value: fmt.Sprint(record.Articles)},
			{Label: "vectors", Value: fmt.Sprint(record.Vectors)},
		}
		for _, stage := range record.Stages {
			fields = append(fields, cli.Field{Label: stage.Name, Value: fmt.Sprintf("%.1fs", stage.Seconds)})
		}
		fields = append(fields, cli.Field{Label: "total", Value: fmt.Sprintf("%.1fs", record.TotalSeconds)})
		if opts.SkipIndex {
			return cli.Summary{
				Message: "Corpus loaded without indexes",
				Fields:  fields,
				Notes:   []cli.Note{{Text: "no load record written, so this corpus is not benchmarkable; drop it and reload without --skip-index", Warning: true}},
				Timed:   true,
			}, nil
		}
		path, err := writeLoadRecord(recordsDir, record)
		if err != nil {
			return cli.Summary{}, err
		}
		return cli.Summary{Message: "Corpus loaded", Fields: append(fields, cli.Field{Label: "record", Value: path}), Timed: true}, nil
	})
}

// derivedRecordsDir maps datasets/<ds>/canonical/<version>/ to datasets/<ds>/loads/<engine>/.
func derivedRecordsDir(canonical string, engine storage.Engine) (string, error) {
	root, err := datasetRoot(canonical)
	if err != nil {
		return "", fmt.Errorf("%w; pass --records-dir", err)
	}
	return filepath.Join(root, "loads", string(engine)), nil
}

// writeLoadRecord never replaces an existing record.
func writeLoadRecord(dir string, record storage.LoadRecord) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode load record: %w", err)
	}
	// Windows forbids ':' in file names.
	path := filepath.Join(dir, record.StartedAt.UTC().Format("2006-01-02T15-04-05.000000Z")+".json")
	tmp, err := os.CreateTemp(dir, ".load-record-*")
	if err != nil {
		return "", fmt.Errorf("create load record: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write load record: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("write load record: %w", err)
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("publish load record %s: %w", path, err)
	}
	return path, nil
}

func loadFields(engine storage.Engine, corpus storage.Corpus, opts loadOptions) []cli.Field {
	fields := []cli.Field{
		{Label: "engine", Value: string(engine)},
		{Label: "canonical", Value: corpus.Canonical.Name + "@" + corpus.Canonical.Version},
	}
	if corpus.Embeddings != nil {
		fields = append(fields, cli.Field{Label: "embeddings", Value: corpus.Embeddings.Name + "@" + corpus.Embeddings.Version})
	}
	indexes := "built after the data"
	if opts.SkipIndex {
		indexes = "skipped"
	}
	return append(fields, cli.IntField("batch", opts.BatchSize), cli.IntField("concurrency", opts.Concurrency), cli.Field{Label: "indexes", Value: indexes})
}
