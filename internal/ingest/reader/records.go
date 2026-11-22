package reader

import (
	"fmt"
	"io"
	"iter"
)

// RecordReader yields records in source order; it keeps going past a
// *MalformedRecordError, and any other error ends it.
type RecordReader interface {
	Records() iter.Seq2[map[string]string, error]
}

// RowCounter is a reader whose format records its row count, so counting reads no rows.
type RowCounter interface {
	NumRows() int64
}

// File is what a resource is read from; parquet needs offsets and the size.
type File interface {
	io.Reader
	io.ReaderAt
	Size() int64
}

func Open(format string, file File) (RecordReader, error) {
	switch format {
	case "csv":
		return NewCSVReader(file), nil
	case "jsonl":
		return NewJSONLReader(file), nil
	case "parquet":
		parquet, err := NewParquetReader(file, file.Size())
		if err != nil {
			return nil, fmt.Errorf("open as parquet: %w", err)
		}
		return parquet, nil
	}
	return nil, fmt.Errorf("format %q, want csv, jsonl or parquet", format)
}

type MalformedRecordError struct {
	Line int64
	Err  error
}

func (e *MalformedRecordError) Error() string {
	return fmt.Sprintf("malformed record at line %d: %v", e.Line, e.Err)
}

func (e *MalformedRecordError) Unwrap() error { return e.Err }
