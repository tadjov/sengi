package reader

import (
	"errors"
	"io"
	"iter"
	"strings"

	"github.com/parquet-go/parquet-go"
)

const parquetRowBatchSize = 256

type ParquetReader struct {
	file    *parquet.File
	columns []string
}

func (p *ParquetReader) NumRows() int64 {
	var n int64
	for _, rg := range p.file.RowGroups() {
		n += rg.NumRows()
	}
	return n
}

func NewParquetReader(r io.ReaderAt, size int64) (*ParquetReader, error) {
	f, err := parquet.OpenFile(r, size)
	if err != nil {
		return nil, err
	}

	paths := f.Schema().Columns()
	cols := make([]string, 0, len(paths))
	for _, path := range paths {
		cols = append(cols, strings.Join(path, "."))
	}

	return &ParquetReader{
		file:    f,
		columns: cols,
	}, nil
}

var errStopRecords = errors.New("stop reading records")

func (p *ParquetReader) Records() iter.Seq2[map[string]string, error] {
	return func(yield func(map[string]string, error) bool) {
		for _, rg := range p.file.RowGroups() {
			err := p.eachRecord(rg, func(record map[string]string) error {
				if !yield(record, nil) {
					return errStopRecords
				}
				return nil
			})
			if errors.Is(err, errStopRecords) {
				return
			}
			if err != nil {
				yield(nil, err)
				return
			}
		}
	}
}

// eachRecord converts each batch before the next ReadRows call, which recycles the buffers byte arrays point into.
func (p *ParquetReader) eachRecord(rg parquet.RowGroup, fn func(map[string]string) error) (err error) {
	rows := rg.Rows()
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	buf := make([]parquet.Row, parquetRowBatchSize)
	for {
		n, readErr := rows.ReadRows(buf)

		// ReadRows may return rows together with io.EOF.
		for _, row := range buf[:n] {
			if fnErr := fn(rowToRecord(row, p.columns)); fnErr != nil {
				return fnErr
			}
		}

		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		if n == 0 {
			return nil
		}
	}
}

func rowToRecord(row parquet.Row, columns []string) map[string]string {
	record := make(map[string]string, len(columns))
	for _, v := range row {
		col := v.Column()
		if col < 0 || col >= len(columns) || v.IsNull() {
			continue
		}

		name := columns[col]
		if prev, seen := record[name]; seen {
			record[name] = prev + "," + v.String()
			continue
		}
		record[name] = v.String()
	}
	return record
}
