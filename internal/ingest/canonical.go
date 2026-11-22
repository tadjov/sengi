package ingest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/parquet-go/parquet-go"
	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/internal/types/language"
)

type CanonicalWriter interface {
	Write(articles []document.Article) error
	Close() error
}

// canonicalRow is an article as a canonical package stores it. Parquet and JSONL share its
// column names, and an empty optional value has one form in each: NULL in parquet, absent in JSONL.
type canonicalRow struct {
	ID              string     `json:"id" parquet:"id"`
	URL             string     `json:"url" parquet:"url"`
	Title           string     `json:"title" parquet:"title"`
	Description     string     `json:"description,omitempty" parquet:"description,optional"`
	Content         string     `json:"content,omitempty" parquet:"content,optional"`
	PublishedAt     string     `json:"publishedAt,omitempty" parquet:"publishedAt,optional"`
	Language        string     `json:"language" parquet:"language"`
	PublisherDomain string     `json:"publisherDomain,omitempty" parquet:"publisherDomain,optional"`
	Metadata        jsonObject `json:"metadata,omitempty" parquet:"metadata,optional"`
}

// jsonObject is JSON text: a string column in parquet, a nested object in JSONL.
type jsonObject string

func (o jsonObject) MarshalJSON() ([]byte, error) {
	return []byte(o), nil
}

func (o *jsonObject) UnmarshalJSON(data []byte) error {
	*o = jsonObject(data)
	return nil
}

func toCanonicalRow(a document.Article) (canonicalRow, error) {
	row := canonicalRow{
		ID:              a.ID.String(),
		URL:             a.URL,
		Title:           a.Title,
		Description:     a.Description,
		Content:         a.Content,
		Language:        string(a.Language),
		PublisherDomain: a.PublisherDomain,
	}
	if !a.PublishedAt.IsZero() {
		row.PublishedAt = a.PublishedAt.Format(time.RFC3339)
	}
	metadata, err := json.Marshal(a.Metadata)
	if err != nil {
		return canonicalRow{}, fmt.Errorf("marshal metadata of article %s: %w", a.ID, err)
	}
	if !bytes.Equal(metadata, []byte("{}")) {
		row.Metadata = jsonObject(metadata)
	}
	return row, nil
}

// article inverts toCanonicalRow exactly, numbers in Metadata.Extra included, so a row
// read back and written again is unchanged.
func (r canonicalRow) article() (document.Article, error) {
	id, err := uuid.Parse(r.ID)
	if err != nil {
		return document.Article{}, fmt.Errorf("article id %q: %w", r.ID, err)
	}
	lang, err := language.Parse(r.Language)
	if err != nil {
		return document.Article{}, fmt.Errorf("article %s: %w", id, err)
	}
	a := document.Article{
		ID:              id,
		URL:             r.URL,
		Title:           r.Title,
		Description:     r.Description,
		Content:         r.Content,
		Language:        lang,
		PublisherDomain: r.PublisherDomain,
	}
	if r.PublishedAt != "" {
		if a.PublishedAt, err = time.Parse(time.RFC3339, r.PublishedAt); err != nil {
			return document.Article{}, fmt.Errorf("article %s: publishedAt: %w", id, err)
		}
	}
	if r.Metadata != "" {
		decoder := json.NewDecoder(bytes.NewReader([]byte(r.Metadata)))
		decoder.UseNumber()
		if err := decoder.Decode(&a.Metadata); err != nil {
			return document.Article{}, fmt.Errorf("article %s: metadata: %w", id, err)
		}
	}
	return a, nil
}

// rowWriter writes one resource of a canonical package.
type rowWriter[T any] interface {
	Write(rows []T) error
	Close() error
}

// A fixed row group size keeps the parquet bytes deterministic.
const canonicalRowGroupRows = 10_000

type parquetRowWriter[T any] struct {
	w *parquet.GenericWriter[T]
}

func newParquetRowWriter[T any](w io.Writer) *parquetRowWriter[T] {
	return &parquetRowWriter[T]{w: parquet.NewGenericWriter[T](w, parquet.MaxRowsPerRowGroup(canonicalRowGroupRows))}
}

func (p *parquetRowWriter[T]) Write(rows []T) error {
	_, err := p.w.Write(rows)
	return err
}

func (p *parquetRowWriter[T]) Close() error {
	return p.w.Close()
}

type jsonlRowWriter[T any] struct {
	enc *json.Encoder
}

func newJSONLRowWriter[T any](w io.Writer) *jsonlRowWriter[T] {
	return &jsonlRowWriter[T]{enc: json.NewEncoder(w)}
}

func (j *jsonlRowWriter[T]) Write(rows []T) error {
	for _, row := range rows {
		if err := j.enc.Encode(row); err != nil {
			return err
		}
	}
	return nil
}

func (j *jsonlRowWriter[T]) Close() error {
	return nil
}

// articleWriter writes articles as canonical rows.
type articleWriter struct {
	rows rowWriter[canonicalRow]
}

func NewParquetCanonicalWriter(w io.Writer) CanonicalWriter {
	return articleWriter{rows: newParquetRowWriter[canonicalRow](w)}
}

func NewJsonlCanonicalWriter(w io.Writer) CanonicalWriter {
	return articleWriter{rows: newJSONLRowWriter[canonicalRow](w)}
}

func (a articleWriter) Write(articles []document.Article) error {
	rows := make([]canonicalRow, len(articles))
	for i, article := range articles {
		row, err := toCanonicalRow(article)
		if err != nil {
			return err
		}
		rows[i] = row
	}
	return a.rows.Write(rows)
}

func (a articleWriter) Close() error {
	return a.rows.Close()
}

// readRows streams the rows of a parquet file in batches.
func readRows[T any](r io.ReaderAt, size int64, each func([]T) error) error {
	file, err := parquet.OpenFile(r, size)
	if err != nil {
		return err
	}
	reader := parquet.NewGenericReader[T](file)
	defer reader.Close()
	batch := make([]T, writeBatchSize)
	for {
		n, err := reader.Read(batch)
		if n > 0 {
			if eachErr := each(batch[:n]); eachErr != nil {
				return eachErr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
