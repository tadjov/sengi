package reader

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCSVReader_Records(t *testing.T) {
	malformed := func(t *testing.T, err error) {
		t.Helper()
		var target *MalformedRecordError
		require.ErrorAs(t, err, &target)
	}

	tests := []struct {
		name  string
		input string
		want  []map[string]string
		// wantErr checks the error yielded in place of the record at the same position in want.
		wantErr map[int]func(*testing.T, error)
	}{
		{
			name:  "keys every row by the header, in file order",
			input: "id,title\n1,Go Concurrency\n2,Understanding Interfaces\n",
			want: []map[string]string{
				{"id": "1", "title": "Go Concurrency"},
				{"id": "2", "title": "Understanding Interfaces"},
			},
		},
		{
			name:  "keeps a quoted comma and newline inside one value",
			input: "id,title\n1,\"Storms, then\nsun\"\n",
			want:  []map[string]string{{"id": "1", "title": "Storms, then\nsun"}},
		},
		{
			name:  "keeps an empty value as an empty string",
			input: "id,title\n1,\n",
			want:  []map[string]string{{"id": "1", "title": ""}},
		},
		{
			name:    "reports a row of the wrong width as malformed and reads on",
			input:   "id,title\n1,One\n2\n3,Three\n",
			want:    []map[string]string{{"id": "1", "title": "One"}, nil, {"id": "3", "title": "Three"}},
			wantErr: map[int]func(*testing.T, error){1: malformed},
		},
		{
			name:  "yields nothing for a header without rows",
			input: "id,title\n",
		},
		{
			name:  "yields nothing for an empty file",
			input: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []map[string]string
			for record, err := range NewCSVReader(strings.NewReader(tt.input)).Records() {
				if check, ok := tt.wantErr[len(got)]; ok {
					check(t, err)
				} else {
					require.NoError(t, err)
				}
				got = append(got, record)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCSVReader_RecordsStopsWhenTheCallerDoes(t *testing.T) {
	reader := NewCSVReader(strings.NewReader("id\n1\n2\n3\n"))

	var got []string
	for record, err := range reader.Records() {
		require.NoError(t, err)
		got = append(got, record["id"])
		break
	}

	assert.Equal(t, []string{"1"}, got)
}
