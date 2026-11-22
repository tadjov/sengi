package reader

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/parquet-go/parquet-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type parquetTestRow struct {
	ID     string   `parquet:"id"`
	Title  string   `parquet:"title"`
	Author *string  `parquet:"author,optional"`
	Year   int64    `parquet:"year"`
	Tags   []string `parquet:"tags,list"`
}

// buildParquet writes rows in batches, flushing after each one so the file ends
// up with len(batches) row groups.
func buildParquet(t *testing.T, batches ...[]parquetTestRow) *bytes.Reader {
	t.Helper()

	var buf bytes.Buffer
	w := parquet.NewGenericWriter[parquetTestRow](&buf)
	for _, batch := range batches {
		_, err := w.Write(batch)
		require.NoError(t, err)
		require.NoError(t, w.Flush())
	}
	require.NoError(t, w.Close())

	return bytes.NewReader(buf.Bytes())
}

func newTestParquetReader(t *testing.T, batches ...[]parquetTestRow) *ParquetReader {
	t.Helper()

	data := buildParquet(t, batches...)
	r, err := NewParquetReader(data, data.Size())
	require.NoError(t, err)
	return r
}

func ptr(s string) *string { return &s }

func TestParquetReader_Columns(t *testing.T) {
	r := newTestParquetReader(t, []parquetTestRow{{ID: "1", Title: "Go"}})

	assert.Equal(t, []string{"id", "title", "author", "year", "tags.list.element"}, r.columns)
}

func TestParquetReader_Records(t *testing.T) {
	r := newTestParquetReader(t, []parquetTestRow{
		{ID: "1", Title: "Go Concurrency", Author: ptr("John Doe"), Year: 2023},
		{ID: "2", Title: "Understanding Interfaces", Author: nil, Year: 2024},
	})

	records := readRecords(t, r)
	require.Len(t, records, 2)

	assert.Equal(t, map[string]string{
		"id":     "1",
		"title":  "Go Concurrency",
		"author": "John Doe",
		"year":   "2023",
	}, records[0])

	// A null author is omitted rather than rendered as "<null>", so the mapper
	// treats it as an absent optional field.
	assert.Equal(t, map[string]string{
		"id":    "2",
		"title": "Understanding Interfaces",
		"year":  "2024",
	}, records[1])
}

func TestParquetReader_ReadRepeatedColumn(t *testing.T) {
	r := newTestParquetReader(t, []parquetTestRow{
		{ID: "1", Title: "Go", Tags: []string{"politics", "us"}},
	})

	records := readRecords(t, r)
	require.Len(t, records, 1)

	assert.Equal(t, "politics,us", records[0]["tags.list.element"])
}

func TestParquetReader_ReadAcrossRowGroups(t *testing.T) {
	r := newTestParquetReader(t,
		[]parquetTestRow{{ID: "1", Title: "one"}},
		[]parquetTestRow{{ID: "2", Title: "two"}},
		[]parquetTestRow{{ID: "3", Title: "three"}},
	)
	require.Len(t, r.file.RowGroups(), 3)

	records := readRecords(t, r)
	require.Len(t, records, 3)
	assert.Equal(t, []string{"1", "2", "3"}, []string{records[0]["id"], records[1]["id"], records[2]["id"]})
}

func TestParquetReader_ReadBatchBoundary(t *testing.T) {
	// Exercises the ReadRows loop past a single batch, where the reader returns
	// rows together with io.EOF on the final call.
	rows := make([]parquetTestRow, parquetRowBatchSize+7)
	for i := range rows {
		rows[i] = parquetTestRow{ID: fmt.Sprint(i), Title: fmt.Sprintf("title %d", i)}
	}
	r := newTestParquetReader(t, rows)

	records := readRecords(t, r)
	assert.Len(t, records, parquetRowBatchSize+7)
}

func TestParquetReader_OpenInvalidFile(t *testing.T) {
	data := bytes.NewReader([]byte("this is not parquet"))

	_, err := NewParquetReader(data, data.Size())
	assert.Error(t, err)
}

func TestRowToRecord_IgnoresOutOfRangeColumns(t *testing.T) {
	row := parquet.Row{
		parquet.ValueOf("kept").Level(0, 0, 0),
		parquet.ValueOf("dropped").Level(0, 0, 9),
	}

	assert.Equal(t, map[string]string{"title": "kept"}, rowToRecord(row, []string{"title"}))
}

func readRecords(t *testing.T, r *ParquetReader) []map[string]string {
	t.Helper()
	var records []map[string]string
	for record, err := range r.Records() {
		require.NoError(t, err)
		records = append(records, record)
	}
	return records
}
