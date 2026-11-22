package pool

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/bench/meta"
)

func TestCheckJudgedAgainst(t *testing.T) {
	current := &PoolFile{Meta: meta.Meta{RunID: "2026-10-17T10-00-00-pool-aaaaaa"}}

	tests := []struct {
		name          string
		judgedAgainst string
		wantErr       string
	}{
		{"same pool", current.Meta.RunID, ""},
		{"regenerated pool", "2026-10-01T10-00-00-pool-bbbbbb", "2026-10-01T10-00-00-pool-bbbbbb"},
		{"judgments predating the binding", "", "re-judge"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := current.CheckJudgedAgainst(tt.judgedAgainst)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
