package reader

import (
	"slices"
	"strings"
	"testing"
)

const sampleJSONL = `{"id":"abc123","title":"Test Article","author":"Jane Doe","content":"Some content"}
{"id":"def456","title":"Second Article","author":"","content":"More content"}
`

func TestJSONLReader_Records(t *testing.T) {
	var ids []string
	for record, err := range NewJSONLReader(strings.NewReader(sampleJSONL)).Records() {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		ids = append(ids, record["id"])
	}

	if want := []string{"abc123", "def456"}; !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
}

func TestJSONLReader_SkipsNullValues(t *testing.T) {
	input := `{"id":"x1","title":"Article","nullable":null}` + "\n"
	jr := NewJSONLReader(strings.NewReader(input))
	for record, err := range jr.Records() {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, exists := record["nullable"]; exists {
			t.Error("null field should be skipped")
		}
	}
}
