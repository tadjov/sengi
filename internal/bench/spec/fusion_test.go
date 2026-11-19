package spec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fusionLegs = `engines:
  pg-gin:
    type: postgres
    connection: "postgresql://localhost/test"
  pg-hnsw:
    type: postgres
    connection: "postgresql://localhost/test"
  hybrid-fused:
    type: fusion
`

func fusionSpec(fusionBlock string) string {
	return validSpecHeader + fusionLegs + fusionBlock + `
jobs:
  - name: hybrid
    suite: suite.yaml
    engines: [hybrid-fused]
`
}

func TestParse_AFusionEngineNamesItsLegsAndMergeSettings(t *testing.T) {
	s, err := Parse([]byte(fusionSpec(`    fusion: { method: rrf, rank_constant: 60, window: 100, legs: [pg-gin, pg-hnsw] }`)))

	require.NoError(t, err)
	assert.Equal(t, &Fusion{Method: FusionRRF, RankConstant: 60, Window: 100, Legs: []string{"pg-gin", "pg-hnsw"}},
		s.Engines["hybrid-fused"].Fusion)
	assert.Equal(t, QueryBinding{Fusion: s.Engines["hybrid-fused"].Fusion}, s.QueryBinding("hybrid-fused"))
}

func TestParse_RejectsAFusionEngineItCannotRun(t *testing.T) {
	tests := []struct {
		name    string
		engine  string
		wantErr string
	}{
		{"no fusion block", "", "no fusion block"},
		{"unknown method", `    fusion: { method: linear, rank_constant: 60, window: 100, legs: [pg-gin, pg-hnsw] }`, "linear"},
		{"zero rank constant", `    fusion: { method: rrf, rank_constant: 0, window: 100, legs: [pg-gin, pg-hnsw] }`, "rank_constant"},
		{"zero window", `    fusion: { method: rrf, rank_constant: 60, window: 0, legs: [pg-gin, pg-hnsw] }`, "window"},
		{"one leg", `    fusion: { method: rrf, rank_constant: 60, window: 100, legs: [pg-gin] }`, "two or more legs"},
		{"undeclared leg", `    fusion: { method: rrf, rank_constant: 60, window: 100, legs: [pg-gin, es-knn] }`, "es-knn"},
		{"leg listed twice", `    fusion: { method: rrf, rank_constant: 60, window: 100, legs: [pg-gin, pg-gin] }`, "twice"},
		{"a connection of its own", `    connection: "postgresql://localhost/test"
    fusion: { method: rrf, rank_constant: 60, window: 100, legs: [pg-gin, pg-hnsw] }`, "only a fusion block"},
		{"a container of its own", `    container: pg-native-news
    fusion: { method: rrf, rank_constant: 60, window: 100, legs: [pg-gin, pg-hnsw] }`, "only a fusion block"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(fusionSpec(tt.engine)))

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestParse_RejectsAFusionLegThatIsAFusionEngine(t *testing.T) {
	yaml := validSpecHeader + fusionLegs + `    fusion: { method: rrf, rank_constant: 60, window: 100, legs: [pg-gin, pg-hnsw] }
  fused-twice:
    type: fusion
    fusion: { method: rrf, rank_constant: 60, window: 100, legs: [hybrid-fused, pg-gin] }
jobs:
  - name: hybrid
    suite: suite.yaml
    engines: [fused-twice]
`
	_, err := Parse([]byte(yaml))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "itself a fusion engine")
}

func TestParse_RejectsAFusionBlockOnAnotherEngineType(t *testing.T) {
	yaml := validSpecHeader + `engines:
  pg-gin:
    type: postgres
    connection: "postgresql://localhost/test"
    fusion: { method: rrf, rank_constant: 60, window: 100, legs: [pg-gin, pg-gin] }
jobs:
  - name: lexical
    suite: suite.yaml
    engines: [pg-gin]
`
	_, err := Parse([]byte(yaml))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "only a fusion engine")
}

func TestEnginesRead_ReplacesAFusionEngineWithItsLegs(t *testing.T) {
	s, err := Parse([]byte(fusionSpec(`    fusion: { method: rrf, rank_constant: 60, window: 100, legs: [pg-gin, pg-hnsw] }`)))
	require.NoError(t, err)

	read := s.EnginesRead(Job{Engines: []string{"pg-hnsw", "hybrid-fused"}})

	assert.ElementsMatch(t, []string{"pg-hnsw", "pg-gin"}, read)
}
