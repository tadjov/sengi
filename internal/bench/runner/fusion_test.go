package runner

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/tusker/internal/bench/engine"
	"github.com/tadjov/tusker/internal/bench/spec"
)

type rankingExecutor struct {
	ranking []uuid.UUID
	mu      sync.Mutex
	seen    []string
}

func (e *rankingExecutor) Execute(_ context.Context, req engine.Request) (*engine.Execution, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seen = append(e.seen, req.Query)
	return &engine.Execution{RankedDocIDs: e.ranking}, nil
}

func (e *rankingExecutor) Name() string { return "ranking" }
func (e *rankingExecutor) Close() error { return nil }

var rrf60 = spec.Fusion{Method: spec.FusionRRF, RankConstant: 60, Window: 100, Legs: []string{"lexical", "vector"}}

// fusionTrack is a track whose only job runs a fusion engine; its legs are
// declared but run in no job of their own.
func fusionTrack(t *testing.T, queries string) *spec.BenchSpec {
	t.Helper()
	suitePath := filepath.Join(t.TempDir(), "suite.yaml")
	require.NoError(t, os.WriteFile(suitePath, []byte("schema_version: 1\nid: fusion_suite\n"+queries), 0o644))
	fusion := rrf60
	return &spec.BenchSpec{
		ID: "fusion",
		Engines: map[string]spec.Engine{
			"lexical": {Type: spec.EnginePostgres},
			"vector":  {Type: spec.EnginePostgres},
			"hybrid":  {Type: spec.EngineFusion, Fusion: &fusion},
		},
		Jobs: []spec.Job{{Name: "hybrid", Suite: suitePath, Engines: []string{"hybrid"}}},
	}
}

func TestLoadSuites_ChecksAFusionEnginesLegBlocks(t *testing.T) {
	bs := fusionTrack(t, `queries:
  - id: q1
    engines:
      lexical: "SELECT id FROM articles"
`)

	_, err := LoadSuites(bs)

	require.Error(t, err)
	assert.Contains(t, err.Error(), `"vector"`)
}

func TestRunJob_AFusionEngineFusesWhatEachLegsOwnBlockReturns(t *testing.T) {
	bs := fusionTrack(t, `queries:
  - id: q1
    engines:
      lexical: "SELECT id FROM articles WHERE lexical"
      vector: "SELECT id FROM articles WHERE vector"
`)
	suites, err := LoadSuites(bs)
	require.NoError(t, err)
	dialects, err := Dialects(bs)
	require.NoError(t, err)
	a, b, c := uuid.UUID{0xa}, uuid.UUID{0xb}, uuid.UUID{0xc}
	lexical := &rankingExecutor{ranking: []uuid.UUID{a, b}}
	vector := &rankingExecutor{ranking: []uuid.UUID{c, b}}
	executors := map[string]engine.Executor{"lexical": lexical, "vector": vector}
	executors["hybrid"] = engine.NewFusionExecutor("hybrid", rrf60, executors)

	result, err := New(DefaultConfig()).RunJob(context.Background(), JobRequest{
		Track:     bs.ID,
		Job:       bs.Jobs[0],
		Suite:     suites[bs.Jobs[0].Suite],
		Executors: executors,
		Bindings:  QueryBindings(bs),
		Dialects:  dialects,
	})

	require.NoError(t, err)
	hybrid := result.Results["q1"]["hybrid"]
	require.NoError(t, hybrid.Error)
	// b is second in both legs: 2/62 beats a and c, which tie at 1/61.
	assert.Equal(t, []uuid.UUID{b, a, c}, hybrid.RankedDocIDs)
	assert.Contains(t, lexical.seen, "SELECT id FROM articles WHERE lexical")
	assert.Contains(t, vector.seen, "SELECT id FROM articles WHERE vector")
}

// The digest is the run's record of what an engine was asked; a fusion
// engine's answer also depends on how it merges.
func TestRequestDigest_AFusionRequestChangesWithItsMergeSettings(t *testing.T) {
	bs := fusionTrack(t, `queries:
  - id: q1
    engines:
      lexical: "SELECT id FROM articles WHERE lexical"
      vector: "SELECT id FROM articles WHERE vector"
`)
	suites, err := LoadSuites(bs)
	require.NoError(t, err)
	dialects, err := Dialects(bs)
	require.NoError(t, err)
	ls := suites[bs.Jobs[0].Suite]
	digestWith := func(rankConstant int) string {
		fusion := rrf60
		fusion.RankConstant = rankConstant
		bindings := QueryBindings(bs)
		bindings["hybrid"] = spec.QueryBinding{Fusion: &fusion}
		request, err := BuildRequest(RequestInput{
			Engine: "hybrid", Query: &ls.Suite.Queries[0], Track: bs.ID, Suite: ls, Bindings: bindings, Dialects: dialects,
		})
		require.NoError(t, err)
		digest, err := requestDigest(request)
		require.NoError(t, err)
		return digest
	}

	assert.NotEqual(t, digestWith(60), digestWith(10))
}
