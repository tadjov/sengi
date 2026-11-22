package reader

import (
	"encoding/csv"
	"errors"
	"io"
	"iter"
)

type CSVReader struct {
	reader io.Reader
}

func NewCSVReader(reader io.Reader) *CSVReader {
	return &CSVReader{
		reader: reader,
	}
}

func (cr *CSVReader) Records() iter.Seq2[map[string]string, error] {
	return func(yield func(map[string]string, error) bool) {
		csvReader := csv.NewReader(cr.reader)
		headers, err := csvReader.Read()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			yield(nil, err)
			return
		}
		for {
			row, err := csvReader.Read()
			if errors.Is(err, io.EOF) {
				return
			}
			var parseErr *csv.ParseError
			if errors.As(err, &parseErr) {
				if !yield(nil, &MalformedRecordError{Line: int64(parseErr.StartLine), Err: err}) {
					return
				}
				continue
			}
			if err != nil {
				yield(nil, err)
				return
			}
			record := make(map[string]string, len(headers))
			for i, h := range headers {
				record[h] = row[i]
			}
			if !yield(record, nil) {
				return
			}
		}
	}
}
