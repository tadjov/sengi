package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/parquet-go/parquet-go"
	"github.com/tadjov/sengi/internal/cli"
	"github.com/tadjov/sengi/internal/embedding"
	"github.com/tadjov/sengi/internal/ingest"
	"github.com/tadjov/sengi/internal/progress"
	"github.com/tadjov/sengi/internal/storage/queryvectors"
	"github.com/tadjov/sengi/internal/types/language"
	"github.com/tadjov/sengi/pkg/datapackage"
)

// truncation cuts one embeddings package's vectors to dim.
type truncation struct {
	derived  datapackage.Package
	resource string
	dim      int
	// queryVectors are written to out; a corpus package is published as version under tagDir.
	queryVectors bool
	out          string
	tagDir       string
	version      string
}

func runTruncate(ctx context.Context, out io.Writer, opts embedOptions) error {
	source, err := datapackage.Open(ctx, opts.Embeddings)
	if err != nil {
		return fmt.Errorf("open embeddings package: %w", err)
	}
	cut, err := planTruncation(source.Package, opts)
	if err != nil {
		return err
	}
	vectors, err := source.Resource(ctx, cut.resource)
	if err != nil {
		return fmt.Errorf("verify embeddings package %s: %w", source.Package.Name, err)
	}
	defer vectors.Close()

	command := cli.Command{
		Title: "sengi embed",
		Fields: []cli.Field{
			{Label: "embeddings", Value: source.Location},
			{Label: "model", Value: cut.derived.Embedding.Tag},
			{Label: "output", Value: cut.output()},
		},
		Label: "truncate",
		Unit:  "vectors",
	}
	return command.Run(out, func(p *cli.Progress) (cli.Summary, error) {
		published, err := cut.publish(ctx, vectors, p)
		if err != nil {
			return cli.Summary{}, err
		}
		return cli.Summary{
			Message: "Embeddings truncated",
			Fields: []cli.Field{
				{Label: "tag", Value: published.Package.Embedding.Tag},
				{Label: "hash", Value: published.Package.Resources[0].Hash},
				{Label: "package", Value: published.Dir},
			},
		}, nil
	})
}

// planTruncation refuses the run before the source's resource is read.
func planTruncation(pkg datapackage.Package, opts embedOptions) (truncation, error) {
	if pkg.Language != "" {
		if _, err := language.Parse(pkg.Language); err != nil {
			return truncation{}, fmt.Errorf("embeddings package %s: %w", pkg.Name, err)
		}
	}
	if pkg.Embedding == nil {
		return truncation{}, fmt.Errorf("embeddings package %s records no embedding to cut", pkg.Name)
	}
	if opts.TruncateTo >= pkg.Embedding.Dim {
		return truncation{}, fmt.Errorf("--truncate-to %d must be below the %d dimensions of embeddings package %s",
			opts.TruncateTo, pkg.Embedding.Dim, pkg.Name)
	}
	switch {
	case pkg.Canonical != nil && pkg.Suite == nil:
		return planCorpusTruncation(pkg, opts)
	case pkg.Suite != nil && pkg.Canonical == nil:
		return planQueryVectorsTruncation(pkg, opts)
	}
	return truncation{}, fmt.Errorf("embeddings package %s must name either its canonical package or its suite", pkg.Name)
}

func planCorpusTruncation(source datapackage.Package, opts embedOptions) (truncation, error) {
	if opts.Out != "" {
		return truncation{}, fmt.Errorf("--out takes a track's query vectors; embeddings package %s is a corpus's and goes under datasets/<ds>/embeddings/", source.Name)
	}
	resource, err := describedResource(source, datapackage.EmbeddingsResource)
	if err != nil {
		return truncation{}, err
	}
	embeddingsDir, err := embeddingsDirOf(opts.Embeddings)
	if err != nil {
		return truncation{}, err
	}
	derived := derivedPackage(source, resource, opts.TruncateTo)
	cut := truncation{
		derived:  derived,
		resource: resource.Name,
		dim:      opts.TruncateTo,
		tagDir:   filepath.Join(embeddingsDir, tagDirName(derived.Embedding.Tag)),
		version:  opts.Version,
	}
	if err := refuseExistingVersion(cut.output()); err != nil {
		return truncation{}, err
	}
	return cut, nil
}

func planQueryVectorsTruncation(source datapackage.Package, opts embedOptions) (truncation, error) {
	if opts.Out == "" {
		return truncation{}, fmt.Errorf("query-vectors package %s needs --out: the bench reads a track's query vectors from trec/query-vectors/ only", source.Name)
	}
	resource, err := describedResource(source, datapackage.QueryVectorsResource)
	if err != nil {
		return truncation{}, err
	}
	out := filepath.Clean(opts.Out)
	if err := refuseExistingOut(out); err != nil {
		return truncation{}, err
	}
	return truncation{
		derived:      derivedPackage(source, resource, opts.TruncateTo),
		resource:     resource.Name,
		dim:          opts.TruncateTo,
		queryVectors: true,
		out:          out,
	}, nil
}

// derivedPackage describes source's vectors cut to dim; Write fills in the resource's bytes and hash.
func derivedPackage(source datapackage.Package, resource datapackage.Resource, dim int) datapackage.Package {
	block := *source.Embedding
	block.Dim = dim
	block.Tag = embedding.Tag(block.Model, dim)
	block.Normalized = true

	derived := source
	derived.Version = ""
	derived.ProducedBy = &datapackage.ProducedBy{Tool: embedProducingTool}
	derived.Embedding = &block
	derived.DerivedFrom = &datapackage.DerivedFrom{
		Name:         source.Name,
		Version:      source.Version,
		Tag:          source.Embedding.Tag,
		ResourceHash: resource.Hash,
	}
	resource.Bytes, resource.Hash = 0, ""
	derived.Resources = []datapackage.Resource{resource}
	return derived
}

func (c truncation) output() string {
	if c.queryVectors {
		return c.out
	}
	return filepath.Join(c.tagDir, c.version)
}

func (c truncation) publish(ctx context.Context, source *datapackage.ResourceFile, counter progress.Counter) (datapackage.Published, error) {
	writeResource := func(staging string) error {
		rows := rowTruncation{
			source:   source,
			path:     filepath.Join(staging, filepath.FromSlash(c.derived.Resources[0].Path)),
			dim:      c.dim,
			progress: counter,
		}
		if c.queryVectors {
			return truncateRows(ctx, rows, func(r *queryvectors.Row) *[]float32 { return &r.Embedding })
		}
		return truncateRows(ctx, rows, func(r *ingest.EmbeddingRow) *[]float32 { return &r.Embedding })
	}
	if c.queryVectors {
		return publishQueryVectors(ctx, c, writeResource)
	}
	return publishStaged(ctx, stagedPackage{Package: c.derived, ParentDir: c.tagDir, Version: c.version, WriteResource: writeResource})
}

// publishQueryVectors renames the staged package to out: the bench reads one unversioned folder per track.
func publishQueryVectors(ctx context.Context, c truncation, writeResource func(staging string) error) (datapackage.Published, error) {
	var published datapackage.Published
	err := withStaging(filepath.Dir(c.out), func(staging string) error {
		if err := writeResource(staging); err != nil {
			return err
		}
		pkg, err := datapackage.Write(ctx, staging, c.derived)
		if err != nil {
			return fmt.Errorf("describe query vectors in %s: %w", staging, err)
		}
		if err := os.Rename(staging, c.out); err != nil {
			return fmt.Errorf("publish query vectors as %s: %w", c.out, err)
		}
		published = datapackage.Published{Dir: c.out, Package: pkg}
		return nil
	})
	return published, err
}

// truncateChunkRows matches the row groups datapipe embed writes.
const truncateChunkRows = embedding.DefaultBatchSize * batchesPerChunk

// rowTruncation streams source's rows to path with each vector cut to dim.
type rowTruncation struct {
	source   *datapackage.ResourceFile
	path     string
	dim      int
	progress progress.Counter
}

// truncateRows holds one chunk of rows in memory at a time; every column R reads but the vector is copied unchanged.
func truncateRows[R any](ctx context.Context, t rowTruncation, vectorOf func(*R) *[]float32) error {
	f, err := os.Create(t.path)
	if err != nil {
		return fmt.Errorf("create %s: %w", t.path, err)
	}
	defer f.Close()
	reader := parquet.NewGenericReader[R](t.source)
	defer reader.Close()
	writer := parquet.NewGenericWriter[R](f)
	counter := progress.Or(t.progress)
	counter.SetTotal(reader.NumRows())
	position := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := make([]R, truncateChunkRows)
		n, readErr := reader.Read(chunk)
		for i := range chunk[:n] {
			vector := vectorOf(&chunk[i])
			cut, err := embedding.Truncate(*vector, t.dim)
			if err != nil {
				return fmt.Errorf("cut row %d: %w", position, err)
			}
			*vector = cut
			position++
		}
		if n > 0 {
			if _, err := writer.Write(chunk[:n]); err != nil {
				return fmt.Errorf("write %s: %w", t.path, err)
			}
			// The writer buffers every page in memory until a row group ends.
			if err := writer.Flush(); err != nil {
				return fmt.Errorf("write %s: %w", t.path, err)
			}
			counter.Add(int64(n))
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read row %d: %w", position, readErr)
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("write %s: %w", t.path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", t.path, err)
	}
	return nil
}

// embeddingsDirOf maps datasets/<ds>/embeddings/<tag-dir>/<version>/ to datasets/<ds>/embeddings/.
func embeddingsDirOf(source string) (string, error) {
	if strings.Contains(source, "://") {
		return "", fmt.Errorf("embeddings package %s is remote; embed from a local copy", source)
	}
	dir := source
	if filepath.Ext(dir) == ".json" {
		dir = filepath.Dir(dir)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", source, err)
	}
	embeddingsDir := filepath.Dir(filepath.Dir(dir))
	if filepath.Base(embeddingsDir) != "embeddings" {
		return "", fmt.Errorf("embeddings package %s is not under datasets/<ds>/embeddings/<tag-dir>/<version>/; embed from a local copy", source)
	}
	return embeddingsDir, nil
}

func refuseExistingOut(dir string) error {
	_, err := os.Stat(dir)
	if err == nil {
		return fmt.Errorf("--out %s already exists; pass a folder that does not", dir)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("check --out %s: %w", dir, err)
	}
	return nil
}

func describedResource(pkg datapackage.Package, name string) (datapackage.Resource, error) {
	if resource, ok := pkg.Resource(name); ok {
		return resource, nil
	}
	return datapackage.Resource{}, &datapackage.ResourceNotFoundError{Package: pkg.Name, Name: name}
}
