package reader

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"iter"
)

type JSONLReader struct {
	reader io.Reader
}

func NewJSONLReader(reader io.Reader) *JSONLReader {
	return &JSONLReader{reader: reader}
}

const BufferSize = 10 * 1024 * 1024 // 10 MB

func (jr *JSONLReader) Records() iter.Seq2[map[string]string, error] {
	return func(yield func(map[string]string, error) bool) {
		scanner := bufio.NewScanner(jr.reader)
		scanner.Buffer(make([]byte, BufferSize), BufferSize)
		var line int64
		for scanner.Scan() {
			line++
			if len(scanner.Bytes()) == 0 {
				continue
			}
			record, err := decodeJSONLine(scanner.Bytes())
			if err != nil {
				err = &MalformedRecordError{Line: line, Err: err}
			}
			if !yield(record, err) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			yield(nil, fmt.Errorf("read jsonl line %d: %w", line+1, err))
		}
	}
}

func decodeJSONLine(line []byte) (map[string]string, error) {
	var raw map[string]any
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, fmt.Errorf("failed to decode JSON line: %w", err)
	}
	record := make(map[string]string, len(raw))
	for k, v := range raw {
		switch s := v.(type) {
		case string:
			record[k] = s
		case nil:
		case map[string]any, []any:
			// Nested values stay JSON so canonical metadata survives a flat record.
			nested, err := json.Marshal(s)
			if err != nil {
				return nil, fmt.Errorf("failed to re-encode %q: %w", k, err)
			}
			record[k] = string(nested)
		default:
			record[k] = fmt.Sprintf("%v", v)
		}
	}
	return record, nil
}
