package ingest

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/tadjov/sengi/internal/datapackage"
	"github.com/tadjov/sengi/internal/ingest/datamapping"
	"github.com/tadjov/sengi/internal/ingest/reader"
	"github.com/tadjov/sengi/internal/progress"
	"github.com/tadjov/sengi/internal/types/document"
)

// PreprocessOptions with an empty Version publishes datapackage.DefaultVersion.
type PreprocessOptions struct {
	Raw        *datapackage.Opened
	Mapping    *datamapping.DataMapper
	OutputRoot string
	Version    string
	ProducedBy datapackage.ProducedBy
	// Progress counts raw records as they are read; nil counts nothing.
	Progress progress.Counter
}

// Preprocess publishes to OutputRoot/<version> and keeps a version already there.
func Preprocess(ctx context.Context, opts PreprocessOptions) (datapackage.Published, error) {
	format, err := opts.Mapping.Output.ResolvedFormat()
	if err != nil {
		return datapackage.Published{}, err
	}
	lang, err := opts.Mapping.Constants.ResolvedLanguage()
	if err != nil {
		return datapackage.Published{}, err
	}
	identity, err := newArticleIdentity(opts.Mapping.Identity)
	if err != nil {
		return datapackage.Published{}, err
	}

	raw := opts.Raw.Package
	pkg := datapackage.Package{
		Name:          canonicalName(raw.Name),
		ArticleSchema: datapackage.ArticleSchema,
		Language:      lang,
		Identity:      describeIdentity(opts.Mapping.Identity, identity.idKind),
		ProducedBy:    &opts.ProducedBy,
		Raw:           &datapackage.RawRef{Name: raw.Name, Version: raw.Version},
		Sources:       httpsSources(raw.Sources),
		Resources: []datapackage.Resource{{
			Name:   datapackage.ArticlesResource,
			Path:   datapackage.ArticlesResource + "." + string(format),
			Format: string(format),
		}},
	}

	builder := &canonicalBuilder{
		raw:      opts.Raw,
		mapper:   reader.NewArticleMapper(opts.Mapping, lang),
		identity: identity,
		report:   datapackage.Report{Dropped: map[string]int64{}, Deduped: map[string]int64{}},
		counter:  progress.Or(opts.Progress),
	}
	return builder.publish(ctx, opts.OutputRoot, opts.Version, pkg)
}

func (b *canonicalBuilder) publish(ctx context.Context, outputRoot, version string, pkg datapackage.Package) (datapackage.Published, error) {
	return datapackage.PublishStaged(ctx, datapackage.StagedPublish{
		ParentDir: outputRoot,
		Version:   version,
		Write: func(stagingDir string) (datapackage.Package, error) {
			articles := pkg.Resources[0]
			if err := b.writeArticlesFile(ctx, filepath.Join(stagingDir, articles.Path), datamapping.OutputFormat(articles.Format)); err != nil {
				return datapackage.Package{}, err
			}
			pkg.Report = &b.report
			return pkg, nil
		},
	})
}

func canonicalName(rawName string) string {
	return strings.TrimSuffix(rawName, "-raw") + "-canonical"
}

// Data Package v2 forbids relative paths that leave the package folder.
func httpsSources(sources []datapackage.Source) []datapackage.Source {
	var kept []datapackage.Source
	for _, s := range sources {
		if strings.HasPrefix(s.Path, "https://") {
			kept = append(kept, s)
		}
	}
	return kept
}

func (b *canonicalBuilder) writeArticlesFile(ctx context.Context, path string, format datamapping.OutputFormat) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", path, closeErr)
		}
	}()
	buffered := bufio.NewWriterSize(f, 1<<20)
	writer := newCanonicalWriter(buffered, format)

	if err := b.writeTo(ctx, writer); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish %s: %w", path, err)
	}
	if err := buffered.Flush(); err != nil {
		return fmt.Errorf("flush %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", path, err)
	}
	return nil
}

func newCanonicalWriter(w io.Writer, format datamapping.OutputFormat) CanonicalWriter {
	if format == datamapping.OutputJSONL {
		return NewJsonlCanonicalWriter(w)
	}
	return NewParquetCanonicalWriter(w)
}

type canonicalBuilder struct {
	raw      *datapackage.Opened
	mapper   *reader.ArticleMapper
	identity *articleIdentity
	report   datapackage.Report
	counter  progress.Counter
}

const writeBatchSize = 1000

func (b *canonicalBuilder) writeTo(ctx context.Context, w CanonicalWriter) error {
	sources, err := b.openSources(ctx)
	defer sources.close()
	if err != nil {
		return err
	}
	b.counter.SetTotal(sources.records())

	batch := make([]document.Article, 0, writeBatchSize)
	for _, source := range sources {
		err := b.eachArticle(ctx, source, func(article document.Article) error {
			batch = append(batch, article)
			if len(batch) < writeBatchSize {
				return nil
			}
			err := b.flush(w, batch)
			batch = batch[:0]
			return err
		})
		if err != nil {
			return err
		}
	}
	if err := b.flush(w, batch); err != nil {
		return fmt.Errorf("write articles: %w", err)
	}
	if b.report.Written == 0 {
		return fmt.Errorf("no article survived mapping out of %d records read: %v", b.report.Read, b.report.Dropped)
	}
	return nil
}

func (b *canonicalBuilder) flush(w CanonicalWriter, batch []document.Article) error {
	if err := w.Write(batch); err != nil {
		return err
	}
	b.counter.Detail("written", b.report.Written, "deduped", b.report.DedupedTotal())
	return nil
}

// rawSource is a raw resource opened, and its hash verified, before preprocessing starts.
type rawSource struct {
	resource datapackage.Resource
	file     *datapackage.ResourceFile
	records  reader.RecordReader
}

type rawSources []rawSource

// openSources opens every resource up front, so their footers can total the records before the first one is read.
func (b *canonicalBuilder) openSources(ctx context.Context) (rawSources, error) {
	sources := make(rawSources, 0, len(b.raw.Package.Resources))
	for _, resource := range b.raw.Package.Resources {
		file, err := b.raw.Resource(ctx, resource.Name)
		if err != nil {
			return sources, fmt.Errorf("open raw resource %s: %w", resource.Name, err)
		}
		records, err := reader.Open(resource.Format, file)
		if err != nil {
			file.Close()
			return sources, fmt.Errorf("raw resource %s: %w", resource.Name, err)
		}
		sources = append(sources, rawSource{resource: resource, file: file, records: records})
	}
	return sources, nil
}

// records is 0, unknown, when any source is in a format that does not record its row count.
func (s rawSources) records() int64 {
	var total int64
	for _, source := range s {
		counted, ok := source.records.(reader.RowCounter)
		if !ok {
			return 0
		}
		total += counted.NumRows()
	}
	return total
}

func (s rawSources) close() {
	for _, source := range s {
		source.file.Close()
	}
}

func (b *canonicalBuilder) eachArticle(ctx context.Context, source rawSource, emit func(document.Article) error) error {
	resource := source.resource
	for record, err := range source.records.Records() {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		b.report.Read++
		b.counter.Add(1)
		var malformed *reader.MalformedRecordError
		if errors.As(err, &malformed) {
			b.report.Dropped[dropMalformed]++
			continue
		}
		if err != nil {
			return fmt.Errorf("read raw resource %s: %w", resource.Name, err)
		}
		article, err := b.mapper.Map(record)
		var drop *reader.MappingDropError
		if errors.As(err, &drop) {
			slog.Debug("dropped record", "resource", resource.Name, "error", err)
			b.report.Dropped[string(drop.Reason)+"/"+drop.Target]++
			continue
		}
		if err != nil {
			return fmt.Errorf("map record from %s: %w", resource.Name, err)
		}
		rule, kept, err := b.identity.admit(&article)
		if err != nil {
			return fmt.Errorf("identify %s: %w", article.URL, err)
		}
		if !kept {
			b.report.Deduped[rule]++
			continue
		}
		b.report.Written++
		if err := emit(article); err != nil {
			return fmt.Errorf("write articles: %w", err)
		}
	}
	return nil
}

const dropMalformed = "malformed_record"
