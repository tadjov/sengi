package language

import "testing"

func TestParse_AcceptsOnlyISO6393English(t *testing.T) {
	tests := []struct {
		input   string
		want    Code
		wantErr bool
	}{
		{input: "eng", want: English},
		{input: "en", wantErr: true},
		{input: "english", wantErr: true},
		{input: "ENG", wantErr: true},
		{input: "srp", wantErr: true},
		{input: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := Parse(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Parse(%q) = %q, want an error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("Parse(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestAnalysis_IsEmptyOnlyForAnUnparseableCode(t *testing.T) {
	if got := Code("srp").Analysis(); got != (Analysis{}) {
		t.Errorf("srp analysis = %+v, want none", got)
	}

	got := English.Analysis()
	if got.PostgresConfig == "" || got.ParadeTokenizer == "" || got.TigerTextConfig == "" || got.ElasticsearchAnalyzer == "" {
		t.Errorf("eng analysis = %+v, want every engine named", got)
	}
}
