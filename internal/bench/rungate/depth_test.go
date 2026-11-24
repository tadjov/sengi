package rungate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/bench/meta"
)

func TestCheckRelevantDepth(t *testing.T) {
	gradedBy := func(judge meta.Judge) *Judgments {
		return &Judgments{Meta: &meta.Meta{Judge: &judge}}
	}
	vectorAt := func(depth int) *Judgments { return gradedBy(meta.Judge{Strategy: "vector", RelevantDepth: depth}) }
	tests := []struct {
		name      string
		judgments *Judgments
		kValues   []int
		wantErr   []string
	}{
		{name: "K up to the vector judge's depth", judgments: vectorAt(4), kValues: []int{2, 4}},
		{name: "K deeper than the vector judge's depth", judgments: vectorAt(4), kValues: []int{2, 8}, wantErr: []string{"4", "8"}},
		{name: "a judge that grades no depth", judgments: gradedBy(meta.Judge{Strategy: "llm"}), kValues: []int{20}},
		{name: "latency-only", judgments: LatencyOnly(), kValues: []int{20}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.judgments.CheckRelevantDepth(tt.kValues)
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
