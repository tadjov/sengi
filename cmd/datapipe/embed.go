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
	"github.com/tadjov/sengi/internal/progress"
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
	Embeddings       string
	TruncateTo       int
	Out              string
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
		Short: "Embed a canonical package's articles through an embedding API, or cut an embeddings package to fewer dimensions",
		Long: `embed verifies a canonical package, embeds each article's lead-v1 text through
Ollama and publishes the vectors as an embeddings package under
datasets/<ds>/embeddings/<tag-dir>/<version>/, one row per canonical row in order.

Ollama comes from the environment: EMBEDDING_BASE_URL, EMBEDDING_MODEL and
EMBEDDING_DIM, as the search API reads them.

With --embeddings and --truncate-to N it embeds nothing: it verifies an embeddings
package and keeps each vector's first N dimensions, L2-renormalised, which is a
Matryoshka-trained model's N-dim embedding. A corpus package lands under
datasets/<ds>/embeddings/<model>@N/<version>/, a track's query vectors in --out.`,
		Example: "  datapipe embed --canonical datasets/cc-news/canonical/v1/datapackage.json\n" +
			"  datapipe embed --embeddings datasets/cc-news/embeddings/qwen3-embedding-0.6b@1024/v1/datapackage.json --truncate-to 512",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := env.LoadDotEnv(os.Getenv("ENV")); err != nil {
				slog.Info("Skipping .env environment variables", "error", err)
			}
			return runEmbed(cmd.Context(), cmd.OutOrStdout(), opts)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&opts.Canonical, "canonical", "", "local canonical package descriptor under datasets/<ds>/canonical/<version>/")
	flags.StringVar(&opts.Embeddings, "embeddings", "", "embeddings package descriptor to cut with --truncate-to, instead of embedding --canonical")
	flags.IntVar(&opts.TruncateTo, "truncate-to", 0, "cut each --embeddings vector to its first N dimensions, L2-renormalised; N must be below the package's dim")
	flags.StringVar(&opts.Out, "out", "", "folder for query vectors cut from a track's trec/query-vectors/; it must not exist yet")
	flags.StringVar(&opts.Version, "version", datapackage.DefaultVersion, "embeddings package version, a folder under embeddings/<tag-dir>/ that must not exist yet")
	flags.IntVar(&opts.BatchSize, "batch-size", embedding.DefaultBatchSize, "articles per embedding request; --canonical only")
	flags.IntVar(&opts.MaxTokens, "max-tokens", defaultMaxTokens, "input tokens per article, sent to Ollama as num_ctx; a longer lead is cut; --canonical only")
	flags.StringVar(&opts.HuggingFaceModel, "hf-model", "", "Hugging Face repo the model is built from; required unless EMBEDDING_MODEL is "+embedding.DefaultModel)
	flags.IntVar(&opts.Limit, "limit", 0, "embed only the first N rows, as a trial with its own --version; 0 embeds every row")
	return cmd
}

func runEmbed(ctx context.Context, out io.Writer, opts embedOptions) error {
	if err := opts.validate(); err != nil {
		return err
	}
	if opts.Embeddings != "" {
		return runTruncate(ctx, out, opts)
	}
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
		articles, err := countArticles(ctx, inputs.articles, inputs.articlesFormat)
		if err != nil {
			return cli.Summary{}, fmt.Errorf("count articles of canonical package %s: %w", run.corpus.Canonical.Name, err)
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
	tagDir := filepath.Join(embeddingsDir, tagDirName(embedder.Model()))
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
	if (o.Canonical == "") == (o.Embeddings == "") {
		return errors.New("pass exactly one of --canonical, to embed its articles, and --embeddings, to cut its vectors")
	}
	if o.Embeddings != "" {
		return o.validateTruncation()
	}
	if o.TruncateTo != 0 {
		return errors.New("--truncate-to cuts an embeddings package; pass it with --embeddings, not --canonical")
	}
	if o.Out != "" {
		return errors.New("--out takes cut query vectors; pass it with --embeddings, not --canonical")
	}
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

func (o embedOptions) validateTruncation() error {
	switch {
	case o.TruncateTo < 1:
		return fmt.Errorf("--embeddings needs --truncate-to N with N at least 1, got %d", o.TruncateTo)
	case o.Limit != 0:
		return errors.New("--limit trials an embedding run; --embeddings embeds nothing")
	case o.HuggingFaceModel != "":
		return errors.New("--hf-model names the model --canonical embeds with; --embeddings keeps its source's")
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

// tagDirName is the folder a tag's packages live in; ':' is not portable in a path.
func tagDirName(tag string) string {
	return strings.ReplaceAll(tag, ":", "-")
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
	return publishStaged(ctx, stagedPackage{
		Package: datapackage.Package{
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
		},
		ParentDir: run.tagDir,
		Version:   run.opts.Version,
		WriteResource: func(staging string) error {
			return writeEmbeddings(ctx, filepath.Join(staging, embeddingsFile), run)
		},
	})
}

// stagedPackage is published as Version under ParentDir once WriteResource has written
// its one resource into the staging folder.
type stagedPackage struct {
	Package       datapackage.Package
	ParentDir     string
	Version       string
	WriteResource func(staging string) error
}

func publishStaged(ctx context.Context, p stagedPackage) (datapackage.Published, error) {
	var published datapackage.Published
	err := withStaging(p.ParentDir, func(staging string) error {
		if err := p.WriteResource(staging); err != nil {
			return err
		}
		var err error
		published, err = datapackage.Publish(ctx, p.Package, datapackage.PublishOptions{StagingDir: staging, ParentDir: p.ParentDir, Version: p.Version})
		if err != nil {
			return fmt.Errorf("publish embeddings package into %s: %w", p.ParentDir, err)
		}
		return nil
	})
	return published, err
}

// withStaging fills a new folder under parent and removes it afterwards unless fill
// renamed it into place, so a failed or cancelled run leaves nothing behind.
func withStaging(parent string, fill func(staging string) error) (err error) {
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create embeddings output folder: %w", err)
	}
	staging, err := os.MkdirTemp(parent, ".staging-*")
	if err != nil {
		return fmt.Errorf("create staging package folder: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(staging); removeErr != nil && err == nil {
			err = fmt.Errorf("remove staging package folder: %w", removeErr)
		}
	}()
	// MkdirTemp creates the folder 0700, and fill publishes it by renaming it.
	if err := os.Chmod(staging, 0o755); err != nil {
		return fmt.Errorf("open up staging package folder: %w", err)
	}
	return fill(staging)
}

// writeEmbeddings holds one chunk of articles and vectors in memory at a time.
func writeEmbeddings(ctx context.Context, path string, run embeddingsRun) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	writer := parquet.NewGenericWriter[embeddingRow](f)
	counter := progress.Or(run.progress)
	for chunk, err := range articleChunks(run.corpus.Articles, run.opts.BatchSize*batchesPerChunk, run.opts.Limit) {
		if err != nil {
			return err
		}
		rows := make([]embeddingRow, 0, len(chunk))
		// One request per batch, so the count moves as each one returns rather than once per chunk.
		for batch := range slices.Chunk(chunk, run.opts.BatchSize) {
			vecs, err := run.embedder.EmbedDocuments(ctx, batch)
			if err != nil {
				return err
			}
			for _, vec := range vecs {
				rows = append(rows, embeddingRow{ID: vec.ID.String(), Embedding: vec.Embedding})
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
