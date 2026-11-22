package pg

import "testing"

func TestIsValidGUCName(t *testing.T) {
	valid := []string{"hnsw.ef_search", "enable_indexscan", "work_mem", "ivfflat.probes"}
	for _, name := range valid {
		if !isValidGUCName(name) {
			t.Errorf("isValidGUCName(%q) = false, want true", name)
		}
	}

	invalid := []string{"", "work mem", "a;DROP TABLE articles", "quoted\"name", "semi;colon"}
	for _, name := range invalid {
		if isValidGUCName(name) {
			t.Errorf("isValidGUCName(%q) = true, want false", name)
		}
	}
}

func TestQuoteLiteral(t *testing.T) {
	tests := map[string]string{
		"200":       "'200'",
		"off":       "'off'",
		"it's":      "'it''s'",
		"'; DROP--": "'''; DROP--'",
	}
	for in, want := range tests {
		if got := quoteLiteral(in); got != want {
			t.Errorf("quoteLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}
