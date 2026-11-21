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
	"strings"

	"github.com/parquet-go/parquet-go"
	"github.com/spf13/cobra"
	"github.com/tadjov/sengi/internal/cli"
	"github.com/tadjov/sengi/internal/datapackage"
	"github.com/tadjov/sengi/internal/embedding"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/pkg/config/env"
)

const (
	embeddingsFile     = "embeddings.parquet"
	parquetMediaType   = "application/vnd.apache.parquet"
	batchesPerChunk    = 64
	embedProducingTool = "datapipe embed"
	leadRecipe         = "lead-v1"
	lastTokenPooling   = "last_token"
	defaultMaxTokens   = 512
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
	flags.IntVar(&opts.BatchSize, "batch-size", embedding.DefaultBatchSize, "articles per embedding request")
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
	inputs, err := openLoadInputs(ctx, loadOptions{Canonical: opts.Canonical})
	if err != nil {
		return err
	}
	defer inputs.Close()
	run.corpus = inputs.corpus(ctx)

	cli.Header(out, "sengi embed",
		cli.Field{Label: "canonical", Value: run.corpus.Canonical.Name + "@" + run.corpus.Canonical.Version},
		cli.Field{Label: "model", Value: run.embedding.Tag},
		cli.IntField("batch", opts.BatchSize),
		cli.IntField("max tokens", opts.MaxTokens),
		cli.Field{Label: "output", Value: filepath.Join(run.tagDir, opts.Version)},
	)
	published, err := publishEmbeddings(ctx, run)
	if err != nil {
		return err
	}
	cli.Summary(out, "Embeddings published",
		cli.Field{Label: "version", Value: published.Package.Version},
		cli.Field{Label: "hash", Value: published.Package.Resources[0].Hash},
		cli.Field{Label: "package", Value: published.Dir},
	)
	return nil
}

type embeddingsRun struct {
	embedder  *embedding.Embedder
	embedding datapackage.Embedding
	corpus    storage.Corpus
	tagDir    string
	opts      embedOptions
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
		embedding.WithExecutorModel(api.model),
		embedding.WithDim(api.dim),
		embedding.WithBatchSize(opts.BatchSize),
		embedding.WithDocumentOptions(map[string]any{"num_ctx": opts.MaxTokens}),
	)
	tagDir := filepath.Join(embeddingsDir, strings.ReplaceAll(embedder.Model(), ":", "-"))
	if err := refuseExistingVersion(filepath.Join(tagDir, opts.Version)); err != nil {
		return embeddingsRun{}, err
	}
	model, err := api.describer.DescribeModel(ctx, api.model)
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
	client    embedding.Client
	describer embedding.ModelDescriber
	model     string
	dim       int
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
	return embeddingAPI{client: client, describer: client, model: cmp.Or(cfg.Model, embedding.DefaultModel), dim: cfg.Dim}, nil
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

// publishEmbeddings stages the package so a failed or cancelled run leaves nothing behind.
func publishEmbeddings(ctx context.Context, run embeddingsRun) (_ datapackage.Published, err error) {
	if err := os.MkdirAll(run.tagDir, 0o755); err != nil {
		return datapackage.Published{}, fmt.Errorf("create embeddings output folder: %w", err)
	}
	staging, err := os.MkdirTemp(run.tagDir, ".staging-*")
	if err != nil {
		return datapackage.Published{}, fmt.Errorf("create staging package folder: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(staging); removeErr != nil && err == nil {
			err = fmt.Errorf("remove staging package folder: %w", removeErr)
		}
	}()

	if err := writeEmbeddings(ctx, filepath.Join(staging, embeddingsFile), run); err != nil {
		return datapackage.Published{}, err
	}
	// MkdirTemp creates the folder 0700, and Publish renames this very folder.
	if err := os.Chmod(staging, 0o755); err != nil {
		return datapackage.Published{}, fmt.Errorf("open up staging package folder: %w", err)
	}
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
	published, err := datapackage.Publish(ctx, pkg, datapackage.PublishOptions{StagingDir: staging, ParentDir: run.tagDir, Version: run.opts.Version})
	if err != nil {
		return datapackage.Published{}, fmt.Errorf("publish embeddings package into %s: %w", run.tagDir, err)
	}
	return published, nil
}

// writeEmbeddings holds one chunk of articles and vectors in memory at a time.
func writeEmbeddings(ctx context.Context, path string, run embeddingsRun) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	writer := parquet.NewGenericWriter[embeddingRow](f)
	for chunk, err := range articleChunks(run.corpus.Articles, run.opts.BatchSize*batchesPerChunk, run.opts.Limit) {
		if err != nil {
			return err
		}
		vecs, err := run.embedder.EmbedDocuments(ctx, chunk)
		if err != nil {
			return err
		}
		rows := make([]embeddingRow, len(vecs))
		for i, vec := range vecs {
			rows[i] = embeddingRow{ID: vec.ID.String(), Embedding: vec.Embedding}
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
