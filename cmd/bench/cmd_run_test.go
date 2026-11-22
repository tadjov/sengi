package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/bench/runner"
	"github.com/tadjov/sengi/internal/bench/spec"
	"github.com/tadjov/sengi/internal/bench/trackctx"
)

func TestLoadRunJudgments_NoneConfiguredNeedsLatencyOnly(t *testing.T) {
	tr := newLineageTrack(t, "http://unused.invalid")
	resolved, err := trackctx.Resolve(trackctx.Inputs{TrackArg: tr.dir})
	require.NoError(t, err)

	_, err = loadRunJudgments(runFlags{}, &spec.BenchSpec{}, resolved)
	require.Error(t, err, "a spec without default judgments and no --judgments has nothing to score against")
	assert.Contains(t, err.Error(), "--latency-only")

	judgments, err := loadRunJudgments(runFlags{latencyOnly: true}, &spec.BenchSpec{}, resolved)
	require.NoError(t, err)
	assert.Nil(t, judgments.Grades)
}

func TestNewRunConfig_MeasuresOneQueryAndOneEngineAtATime(t *testing.T) {
	cfg := newRunConfig(runFlags{}, &spec.BenchSpec{}, []int{10}, false)

	assert.Equal(t, runner.QueryParallelismSerial, cfg.QueryParallelism,
		"overlapping queries contend for the same engine")
	assert.Equal(t, runner.EngineParallelismSerial, cfg.EngineParallelism,
		"a zero here makes the runner fan out to every engine in the job, "+
			"so one engine's warmup overlaps another's measured iterations")
}

func TestNewRunConfig_FlagBeatsSpecDefault(t *testing.T) {
	bs := &spec.BenchSpec{}
	bs.Runs.Warmup = 1
	bs.Runs.Iterations = 3

	tests := []struct {
		name       string
		flags      runFlags
		wantWarmup int
		wantRuns   int
	}{
		{"spec defaults when flags unset", runFlags{}, 1, 3},
		{"flags win when set", runFlags{warmup: 2, iters: 10}, 2, 10},
		{"partial flags fall back per field", runFlags{iters: 10}, 1, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newRunConfig(tt.flags, bs, []int{10}, false)
			assert.Equal(t, tt.wantWarmup, cfg.WarmupRuns)
			assert.Equal(t, tt.wantRuns, cfg.Runs)
		})
	}
}

func TestNewRunConfig_SpecKValuesLoseToAnExplicitFlag(t *testing.T) {
	bs := &spec.BenchSpec{}
	bs.Metrics.KValues = []int{3, 5, 10}

	fromSpec := newRunConfig(runFlags{}, bs, []int{20}, false)
	assert.Equal(t, []int{3, 5, 10}, fromSpec.KValues, "spec k_values apply when --k was not passed")

	fromFlag := newRunConfig(runFlags{}, bs, []int{20}, true)
	assert.Equal(t, []int{20}, fromFlag.KValues, "--k must override the spec")
}

// A query-vectors package covers one suite, so --jobs narrowing a two-suite
// spec to that suite's job must run.
func TestRun_JobsFilterNarrowsTheQueryVectorsSuite(t *testing.T) {
	t.Setenv("EMBEDDING_BASE_URL", "")
	cluster := fakeCluster(t, map[string]*fakeIndex{"a": {count: 1, ranked: map[string][]uuid.UUID{"q1": {uuid.New()}}}})
	track := copyQueryVectorsTrack(t)
	require.NoError(t, os.WriteFile(filepath.Join(track, "spec.yaml"), fmt.Appendf(nil, `schema_version: 1
id: two-suites
kind: fts
engines:
  a:
    type: elasticsearch
    connection: %q
    index: a
    params:
      embedding_model: %q
runs:
  warmup: 0
  iterations: 1
jobs:
  - name: sem
    suite: suite.yaml
    engines: [a]
  - name: other
    suite: other.yaml
    engines: [a]
`, cluster, queryVectorsFixtureTag), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(track, "suite.yaml"), []byte(`schema_version: 1
id: fixture-semantic
queries:
  - id: q1
    text: "climate change policy"
    engines:
      a: { query: '{"query": {"match": {"title": "q1"}}}' }
  - id: q2
    text: "central bank interest rates"
    engines:
      a: { query: '{"query": {"match": {"title": "q2"}}}' }
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(track, "other.yaml"), []byte(`schema_version: 1
id: other
queries:
  - id: o1
    description: "something else"
    engines:
      a: { query: '{"query": {"match": {"title": "o1"}}}' }
`), 0o644))

	out, err := execute(t, newRunCmd(), track, "--jobs", "sem", "--latency-only")

	require.NoError(t, err, out)
}
