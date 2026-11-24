package judgment

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/bench/meta"
)

func TestContentExcerpt(t *testing.T) {
	tests := []struct {
		name    string
		content string
		runes   int
		want    string
	}{
		{name: "no longer than the cut", content: "Žar ptica", runes: 9, want: "Žar ptica"},
		{name: "longer than the cut, counted in runes", content: "Žar ptica", runes: 3, want: "Žar..."},
		{name: "no cut", content: strings.Repeat("ž", 5000), runes: 0, want: strings.Repeat("ž", 5000)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ContentExcerpt(tt.content, tt.runes))
		})
	}
}

func TestCheckContentCut(t *testing.T) {
	readingAt := func(strategy StrategyKind, contentRunes int) meta.Judge {
		return meta.Judge{Strategy: string(strategy), ContentRunes: contentRunes}
	}
	tests := []struct {
		name    string
		judge   meta.Judge
		cut     int
		wantErr []string
	}{
		{name: "llm at the same cut", judge: readingAt(StrategyLLM, 400), cut: 400},
		{name: "llm at another cut", judge: readingAt(StrategyLLM, 400), cut: 600, wantErr: []string{"400", "600"}},
		{name: "jev reading whole content against a cut", judge: readingAt(StrategyJev, 0), cut: 600, wantErr: []string{"whole", "600"}},
		{name: "hand grades at another cut", judge: readingAt(StrategyManual, 600), cut: 0, wantErr: []string{"600", "whole"}},
		{name: "a judge that reads no excerpt", judge: readingAt(StrategyBM25, 0), cut: 600},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckContentCut(tt.judge, tt.cut)
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, want := range tt.wantErr {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}
