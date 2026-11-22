package ingest

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"

	"github.com/google/uuid"
	"github.com/parquet-go/parquet-go"
	"github.com/tadjov/sengi/internal/datapackage"
	"github.com/tadjov/sengi/internal/embedding"
	"github.com/tadjov/sengi/internal/ingest/reader"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/internal/types/language"
)

// CorpusOptions locates the packages a corpus is read from; Embeddings is optional.
type CorpusOptions struct {
	Canonical  string
	Embeddings string
	// Trial accepts an embeddings package that covers only the first articles.
	Trial bool
}

// PackagedCorpus is a verified canonical package, with the embeddings package built from it.
type PackagedCorpus struct {
	canonical    datapackage.Package
	articles     *datapackage.ResourceFile
	articlesHash string
	records      reader.RecordReader
	embeddings   *datapackage.Package
	vectors      *datapackage.ResourceFile
	vectorsHash  string
}

// OpenCorpus checks both descriptors before it reads either resource.
func OpenCorpus(ctx context.Context, opts CorpusOptions) (*PackagedCorpus, error) {
	corpus := &PackagedCorpus{}
	if err := corpus.open(ctx, opts); err != nil {
		corpus.Close()
		return nil, err
	}
	return corpus, nil
}

func (c *PackagedCorpus) open(ctx context.Context, opts CorpusOptions) error {
	canonical, err := datapackage.Open(ctx, opts.Canonical)
	if err != nil {
		return fmt.Errorf("open canonical package: %w", err)
	}
	c.canonical = canonical.Package
	if c.canonical.ArticleSchema != datapackage.ArticleSchema {
		return fmt.Errorf("canonical package %s has article schema %q, want %q", c.canonical.Name, c.canonical.ArticleSchema, datapackage.ArticleSchema)
	}
	if _, err := language.Parse(string(c.canonical.Language)); err != nil {
		return fmt.Errorf("canonical package %s: %w", c.canonical.Name, err)
	}
	articles, listed := c.canonical.Resource(datapackage.ArticlesResource)
	if !listed {
		return &datapackage.ResourceNotFoundError{Package: c.canonical.Name, Name: datapackage.ArticlesResource}
	}
	if articles.Format != "parquet" && articles.Format != "jsonl" {
		return fmt.Errorf("canonical resource format %q: want parquet or jsonl", articles.Format)
	}
	c.articlesHash = articles.Hash

	var embeddings *datapackage.Opened
	if opts.Embeddings != "" {
		if embeddings, err = datapackage.Open(ctx, opts.Embeddings); err != nil {
			return fmt.Errorf("open embeddings package: %w", err)
		}
		c.embeddings = &embeddings.Package
		if err := checkEmbeddings(embeddings.Package, articles.Hash, opts.Trial); err != nil {
			return err
		}
		vectors, listed := embeddings.Package.Resource(datapackage.EmbeddingsResource)
		if !listed {
			return &datapackage.ResourceNotFoundError{Package: embeddings.Package.Name, Name: datapackage.EmbeddingsResource}
		}
		c.vectorsHash = vectors.Hash
	}

	if c.articles, err = canonical.Resource(ctx, datapackage.ArticlesResource); err != nil {
		return fmt.Errorf("verify canonical package %s: %w", c.canonical.Name, err)
	}
	if c.records, err = reader.Open(articles.Format, c.articles); err != nil {
		return fmt.Errorf("canonical package %s: %w", c.canonical.Name, err)
	}
	if embeddings != nil {
		if c.vectors, err = embeddings.Resource(ctx, datapackage.EmbeddingsResource); err != nil {
			return fmt.Errorf("verify embeddings package %s: %w", embeddings.Package.Name, err)
		}
		if !opts.Trial {
			return c.refusePartialVectors(ctx)
		}
	}
	return nil
}

// checkEmbeddings refuses vectors from another canonical resource, and an unrequested trial.
func checkEmbeddings(pkg datapackage.Package, canonicalHash string, trial bool) error {
	if pkg.Embedding == nil || pkg.Embedding.Tag == "" {
		return fmt.Errorf("embeddings package %s does not record its model tag", pkg.Name)
	}
	if pkg.Canonical == nil {
		return fmt.Errorf("embeddings package %s does not name its canonical package", pkg.Name)
	}
	if pkg.Canonical.ResourceHash != canonicalHash {
		return fmt.Errorf("embeddings package %s was built from canonical resource %s, not %s",
			pkg.Name, pkg.Canonical.ResourceHash, canonicalHash)
	}
	if limit := pkg.Embedding.Limit; limit != nil && !trial {
		return fmt.Errorf("embeddings package %s is a trial of its first %d rows; pass --trial to load it", pkg.Name, *limit)
	}
	return nil
}

func (c *PackagedCorpus) refusePartialVectors(ctx context.Context) error {
	vectors, err := parquet.OpenFile(c.vectors, c.vectors.Size())
	if err != nil {
		return fmt.Errorf("read embeddings package %s: %w", c.embeddings.Name, err)
	}
	articles, err := c.ArticleCount(ctx)
	if err != nil {
		return err
	}
	if vectors.NumRows() < articles {
		return fmt.Errorf("embeddings package %s holds %d vectors for %d articles; pass --trial to load a partial package",
			c.embeddings.Name, vectors.NumRows(), articles)
	}
	return nil
}

// ArticleCount leaves the articles unread, so Corpus still yields them from the first.
func (c *PackagedCorpus) ArticleCount(ctx context.Context) (int64, error) {
	if counter, ok := c.records.(reader.RowCounter); ok {
		return counter.NumRows(), nil
	}
	scanner := bufio.NewScanner(io.NewSectionReader(c.articles, 0, c.articles.Size()))
	scanner.Buffer(make([]byte, reader.BufferSize), reader.BufferSize)
	var n int64
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if len(scanner.Bytes()) > 0 {
			n++
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("count articles of canonical package %s: %w", c.canonical.Name, err)
	}
	return n, nil
}

// Corpus reads the articles and vectors once, in file order.
func (c *PackagedCorpus) Corpus(ctx context.Context) storage.Corpus {
	corpus := storage.Corpus{
		Articles:  c.readArticles(ctx),
		Language:  c.canonical.Language,
		Canonical: storage.PackageRef{Name: c.canonical.Name, Version: c.canonical.Version, ResourceHash: c.articlesHash},
	}
	if c.embeddings != nil {
		corpus.ModelTag = c.embeddings.Embedding.Tag
		corpus.Vectors = readVectors(ctx, c.vectors, corpus.ModelTag)
		corpus.Embeddings = &storage.PackageRef{Name: c.embeddings.Name, Version: c.embeddings.Version, ResourceHash: c.vectorsHash}
	}
	return corpus
}

func (c *PackagedCorpus) Close() {
	for _, f := range []*datapackage.ResourceFile{c.articles, c.vectors} {
		if f != nil {
			_ = f.Close()
		}
	}
}

// readArticles keeps file order, which the embeddings follow.
func (c *PackagedCorpus) readArticles(ctx context.Context) iter.Seq2[document.Article, error] {
	return func(yield func(document.Article, error) bool) {
		row := 0
		for record, err := range c.records.Records() {
			if ctxErr := ctx.Err(); ctxErr != nil {
				yield(document.Article{}, ctxErr)
				return
			}
			if err != nil {
				yield(document.Article{}, fmt.Errorf("read canonical row %d: %w", row, err))
				return
			}
			article, err := reader.CanonicalArticle(record)
			if err != nil {
				yield(document.Article{}, fmt.Errorf("canonical row %d: %w", row, err))
				return
			}
			if !yield(article, nil) {
				return
			}
			row++
		}
	}
}

// EmbeddingRow needs ,list to read pyarrow's 3-level LIST encoding.
type EmbeddingRow struct {
	ID        string    `parquet:"id"`
	Embedding []float32 `parquet:"embedding,list"`
}

const vectorReadBatch = 256

func readVectors(ctx context.Context, file *datapackage.ResourceFile, tag string) iter.Seq2[embedding.Vec, error] {
	return func(yield func(embedding.Vec, error) bool) {
		rows := parquet.NewGenericReader[EmbeddingRow](file)
		defer rows.Close()
		position := 0
		for {
			if err := ctx.Err(); err != nil {
				yield(embedding.Vec{}, err)
				return
			}
			// A fresh batch per read: the caller may keep the vectors.
			batch := make([]EmbeddingRow, vectorReadBatch)
			n, readErr := rows.Read(batch)
			for _, row := range batch[:n] {
				id, err := uuid.Parse(row.ID)
				if err != nil {
					yield(embedding.Vec{}, fmt.Errorf("embeddings row %d: %w", position, err))
					return
				}
				if !yield(embedding.Vec{ID: id, Model: tag, Embedding: row.Embedding}, nil) {
					return
				}
				position++
			}
			if errors.Is(readErr, io.EOF) {
				return
			}
			if readErr != nil {
				yield(embedding.Vec{}, fmt.Errorf("read embeddings row %d: %w", position, readErr))
				return
			}
		}
	}
}
