package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/parquet-go/parquet-go"
	"github.com/spf13/cobra"
	"github.com/tadjov/sengi/internal/cli"
	"github.com/tadjov/sengi/internal/datapackage"
	"github.com/tadjov/sengi/internal/embedding"
	"github.com/tadjov/sengi/internal/ingest"
	"github.com/tadjov/sengi/internal/progress"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/pkg/config/env"
)

const (
	embeddingsFile        = "embeddings.parquet"
	parquetMediaType      = "application/vnd.apache.parquet"
	batchesPerChunk       = 64
	embedProducingTool    = "datapipe embed"
	leadRecipe            = "lead-v1"
	lastTokenPooling      = "last_token"
	defaultMaxTokens      = 512
	defaultEmbedBatchSize = 64
)

type embedOptions struct {
	Canonical        string
	Version          string
	BatchSize        int
	MaxTokens        int
	HuggingFaceModel string
	// Limit embeds only the first Limit rows; 0 embeds every row.
	Limit int
}

func newEmbedCmd() *cobra.Command {
	var opts embedOptions
	cmd := &cobra.Command{
		Use:   "embed",
		Short: "Embed a canonical package's articles through an embedding API into an embeddings package",
		Long: `embed verifies a canonical package, embeds each article's lead-v1 text through
Ollama and publishes the vectors as an embeddings package under
datasets/<ds>/embeddings/<tag-dir>/<version>/, one row per canonical row in order.

Ollama comes from the environment: EMBEDDING_BASE_URL, EMBEDDING_MODEL and
EMBEDDING_DIM, as the search API reads them.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := env.LoadDotEnv(os.Getenv("ENV")); err != nil {
				slog.Info("Skipping .env environment variables", "error", err)
			}
			return runEmbed(cmd.Context(), cmd.OutOrStdout(), opts)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&opts.Canonical, "canonical", "", "local canonical package descriptor under datasets/<ds>/canonical/<version>/")
	flags.StringVar(&opts.Version, "version", datapackage.DefaultVersion, "embeddings package version, a folder under embeddings/<tag-dir>/ that must not exist yet")
	flags.IntVar(&opts.BatchSize, "batch-size", defaultEmbedBatchSize, "articles per embedding request")
	flags.IntVar(&opts.MaxTokens, "max-tokens", defaultMaxTokens, "input tokens per article, sent to Ollama as num_ctx; a longer lead is cut")
	flags.StringVar(&opts.HuggingFaceModel, "hf-model", "", "Hugging Face repo the model is built from; required unless EMBEDDING_MODEL is "+embedding.DefaultModel)
	flags.IntVar(&opts.Limit, "limit", 0, "embed only the first N rows, as a trial with its own --version; 0 embeds every row")
	_ = cmd.MarkFlagRequired("canonical")
	return cmd
}

func runEmbed(ctx context.Context, out io.Writer, opts embedOptions) error {
	run, err := prepareEmbeddings(ctx, opts)
	if err != nil {
		return err
	}
	packaged, err := ingest.OpenCorpus(ctx, ingest.CorpusOptions{Canonical: opts.Canonical})
	if err != nil {
		return err
	}
	defer packaged.Close()
	run.corpus = packaged.Corpus(ctx)

	command := cli.Command{
		Title: "sengi embed",
		Fields: []cli.Field{
			{Label: "canonical", Value: run.corpus.Canonical.Name + "@" + run.corpus.Canonical.Version},
			{Label: "model", Value: run.embedding.Tag},
			cli.IntField("batch", opts.BatchSize),
			cli.IntField("max tokens", opts.MaxTokens),
			{Label: "output", Value: filepath.Join(run.tagDir, opts.Version)},
		},
		Label: "embed",
		Unit:  "articles",
	}
	return command.Run(out, func(p *cli.Progress) (cli.Summary, error) {
		articles, err := packaged.ArticleCount(ctx)
		if err != nil {
			return cli.Summary{}, err
		}
		if opts.Limit > 0 {
			articles = min(articles, int64(opts.Limit))
		}
		p.SetTotal(articles)
		run.progress = p
		published, err := publishEmbeddings(ctx, run)
		if err != nil {
			return cli.Summary{}, err
		}
		return cli.Summary{
			Message: "Embeddings published",
			Fields: []cli.Field{
				{Label: "version", Value: published.Package.Version},
				{Label: "hash", Value: published.Package.Resources[0].Hash},
				{Label: "package", Value: published.Dir},
			},
		}, nil
	})
}

type embeddingsRun struct {
	embedder  *embedding.Embedder
	embedding datapackage.Embedding
	corpus    storage.Corpus
	tagDir    string
	opts      embedOptions
	// progress counts articles as each embedding request returns; nil counts nothing.
	progress progress.Counter
}

// prepareEmbeddings describes the run, and refuses it before any article is read or embedded.
func prepareEmbeddings(ctx context.Context, opts embedOptions) (embeddingsRun, error) {
	if err := opts.validate(); err != nil {
		return embeddingsRun{}, err
	}
	embeddingsDir, err := derivedEmbeddingsDir(opts.Canonical)
	if err != nil {
		return embeddingsRun{}, err
	}
	api, err := embeddingAPIFromEnv()
	if err != nil {
		return embeddingsRun{}, err
	}
	sourceRepo, err := huggingFaceModelOf(api.model, opts.HuggingFaceModel)
	if err != nil {
		return embeddingsRun{}, err
	}
	embedder := embedding.NewEmbedder(api.client,
		embedding.WithModel(api.model),
		embedding.WithDim(api.dim),
		embedding.WithDocumentOptions(map[string]any{"num_ctx": opts.MaxTokens}),
	)
	tagDir := filepath.Join(embeddingsDir, strings.ReplaceAll(embedder.Model(), ":", "-"))
	if err := refuseExistingVersion(filepath.Join(tagDir, opts.Version)); err != nil {
		return embeddingsRun{}, err
	}
	model, err := api.client.DescribeModel(ctx, api.model)
	if err != nil {
		return embeddingsRun{}, err
	}

	block := datapackage.Embedding{
		Model:            api.model,
		HuggingFaceModel: sourceRepo,
		Revision:         model.Digest,
		Tag:              embedder.Model(),
		Dim:              api.dim,
		Recipe:           leadRecipe,
		Pooling:          lastTokenPooling,
		Normalized:       true,
		MaxTokens:        opts.MaxTokens,
		BatchSize:        opts.BatchSize,
		DType:            strings.ToLower(model.QuantizationLevel),
	}
	if opts.Limit > 0 {
		limit := opts.Limit
		block.Limit = &limit
	}
	return embeddingsRun{embedder: embedder, embedding: block, tagDir: tagDir, opts: opts}, nil
}

func (o embedOptions) validate() error {
	if o.BatchSize < 1 {
		return fmt.Errorf("--batch-size must be at least 1, got %d", o.BatchSize)
	}
	if o.MaxTokens < 1 {
		return fmt.Errorf("--max-tokens must be at least 1, got %d", o.MaxTokens)
	}
	if o.Limit < 0 {
		return fmt.Errorf("--limit must not be negative, got %d", o.Limit)
	}
	return nil
}

type embeddingAPI struct {
	client *embedding.OllamaClient
	model  string
	dim    int
}

func embeddingAPIFromEnv() (embeddingAPI, error) {
	cfg, err := embedding.LoadConfigFromEnv()
	if err != nil {
		return embeddingAPI{}, err
	}
	if cfg.BaseURL == "" {
		return embeddingAPI{}, errors.New("EMBEDDING_BASE_URL is not set")
	}
	client, err := embedding.NewOllamaClient(cfg.BaseURL)
	if err != nil {
		return embeddingAPI{}, fmt.Errorf("embedding client for %s: %w", cfg.BaseURL, err)
	}
	return embeddingAPI{client: client, model: cmp.Or(cfg.Model, embedding.DefaultModel), dim: cfg.Dim}, nil
}

func huggingFaceModelOf(model, flagValue string) (string, error) {
	switch {
	case flagValue != "":
		return flagValue, nil
	case model == embedding.DefaultModel:
		return embedding.DefaultHuggingFaceModel, nil
	}
	return "", fmt.Errorf("--hf-model is required with EMBEDDING_MODEL %s; only %s has a known source repo", model, embedding.DefaultModel)
}

// refuseExistingVersion fails before embedding; Publish would refuse the version only after it.
func refuseExistingVersion(dir string) error {
	_, err := os.Stat(dir)
	if err == nil {
		return fmt.Errorf("embeddings version %s already exists; pass another --version", dir)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("check embeddings version %s: %w", dir, err)
	}
	return nil
}

// derivedEmbeddingsDir maps datasets/<ds>/canonical/<version>/ to datasets/<ds>/embeddings/.
func derivedEmbeddingsDir(canonical string) (string, error) {
	root, err := datasetRoot(canonical)
	if err != nil {
		return "", fmt.Errorf("%w; embed from a local copy", err)
	}
	return filepath.Join(root, "embeddings"), nil
}

func publishEmbeddings(ctx context.Context, run embeddingsRun) (datapackage.Published, error) {
	canonical := run.corpus.Canonical
	pkg := datapackage.Package{
		Name:       canonical.Name + "-embeddings",
		Language:   run.corpus.Language,
		ProducedBy: &datapackage.ProducedBy{Tool: embedProducingTool},
		Embedding:  &run.embedding,
		Canonical:  &datapackage.Ref{Name: canonical.Name, Version: canonical.Version, ResourceHash: canonical.ResourceHash},
		Resources: []datapackage.Resource{{
			Name:      datapackage.EmbeddingsResource,
			Path:      embeddingsFile,
			Format:    "parquet",
			MediaType: parquetMediaType,
		}},
	}
	return datapackage.PublishStaged(ctx, datapackage.StagedPublish{
		ParentDir: run.tagDir,
		Version:   run.opts.Version,
		Write: func(stagingDir string) (datapackage.Package, error) {
			return pkg, writeEmbeddings(ctx, filepath.Join(stagingDir, embeddingsFile), run)
		},
	})
}

// writeEmbeddings holds one chunk of articles and vectors in memory at a time.
func writeEmbeddings(ctx context.Context, path string, run embeddingsRun) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	writer := parquet.NewGenericWriter[ingest.EmbeddingRow](f)
	counter := progress.Or(run.progress)
	for chunk, err := range articleChunks(run.corpus.Articles, run.opts.BatchSize*batchesPerChunk, run.opts.Limit) {
		if err != nil {
			return err
		}
		rows := make([]ingest.EmbeddingRow, 0, len(chunk))
		// One request per batch, so the count moves as each one returns rather than once per chunk.
		for batch := range slices.Chunk(chunk, run.opts.BatchSize) {
			vecs, err := run.embedder.EmbedDocuments(ctx, batch)
			if err != nil {
				return err
			}
			for _, vec := range vecs {
				rows = append(rows, ingest.EmbeddingRow{ID: vec.ID.String(), Embedding: vec.Embedding})
			}
			counter.Add(int64(len(batch)))
		}
		if _, err := writer.Write(rows); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		// The writer buffers every page in memory until a row group ends.
		if err := writer.Flush(); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// articleChunks yields the first limit articles, or all of them when limit is 0, in chunks of size.
func articleChunks(articles iter.Seq2[document.Article, error], size, limit int) iter.Seq2[[]document.Article, error] {
	return func(yield func([]document.Article, error) bool) {
		chunk := make([]document.Article, 0, size)
		read := 0
		for article, err := range articles {
			if err != nil {
				yield(nil, err)
				return
			}
			if limit > 0 && read == limit {
				break
			}
			chunk = append(chunk, article)
			read++
			if len(chunk) == size {
				if !yield(chunk, nil) {
					return
				}
				chunk = make([]document.Article, 0, size)
			}
		}
		if len(chunk) > 0 {
			yield(chunk, nil)
		}
	}
}
