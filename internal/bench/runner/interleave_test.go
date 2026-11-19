package runner

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/tusker/internal/bench/engine"
	"github.com/tadjov/tusker/internal/bench/spec"
)

const twoPostgresQueries = `queries:
  - id: q1
    engines:
      pg-a: { query: "SELECT id FROM articles WHERE 1 = 1" }
      pg-b: { query: "SELECT id FROM articles WHERE 1 = 1" }
      pg-c: { query: "SELECT id FROM articles WHERE 1 = 1" }
  - id: q2
    engines:
      pg-a: { query: "SELECT id FROM articles WHERE 2 = 2" }
      pg-b: { query: "SELECT id FROM articles WHERE 2 = 2" }
      pg-c: { query: "SELECT id FROM articles WHERE 2 = 2" }
`

var queryOfStatement = map[string]string{
	"SELECT id FROM articles WHERE 1 = 1": "q1",
	"SELECT id FROM articles WHERE 2 = 2": "q2",
}

var threePostgresEngines = map[string]spec.Engine{"pg-a": postgres, "pg-b": postgres, "pg-c": postgres}

type execution struct {
	engine  string
	queryID string
}

// executionLog records every execution of every engine sharing it, in order.
type executionLog struct {
	mu         sync.Mutex
	executions []execution
}

func (l *executionLog) executor(name string) *loggingExecutor {
	return &loggingExecutor{name: name, log: l}
}

func (l *executionLog) of(queryID string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var engines []string
	for _, e := range l.executions {
		if e.queryID == queryID {
			engines = append(engines, e.engine)
		}
	}
	return engines
}

type loggingExecutor struct {
	name string
	log  *executionLog
}

func (e *loggingExecutor) Execute(_ context.Context, req engine.Request) (*engine.Execution, error) {
	e.log.mu.Lock()
	defer e.log.mu.Unlock()
	e.log.executions = append(e.log.executions, execution{engine: e.name, queryID: queryOfStatement[req.Query]})
	return &engine.Execution{Latency: time.Millisecond}, nil
}

func (e *loggingExecutor) Name() string { return e.name }
func (e *loggingExecutor) Close() error { return nil }

// benchRunConfig measures as bench run does: one query and one engine at a time.
func benchRunConfig(seed uint64, warmup, iterations int) Config {
	cfg := DefaultConfig()
	cfg.EngineParallelism = EngineParallelismSerial
	cfg.Seed = seed
	cfg.WarmupRuns = warmup
	cfg.Runs = iterations
	return cfg
}

func runLogged(t *testing.T, cfg Config) *executionLog {
	t.Helper()
	log := &executionLog{}
	executors := map[string]engine.Executor{}
	for name := range threePostgresEngines {
		executors[name] = log.executor(name)
	}
	_, err := New(cfg).RunAll(context.Background(), oneJobTrack(t, twoPostgresQueries, threePostgresEngines, nil), executors)
	require.NoError(t, err)
	return log
}

// drawnOrder is the order engineOrder draws for one step over the job's engines.
func drawnOrder(step orderStep) []string {
	cells := []*cell{{engine: "pg-a"}, {engine: "pg-b"}, {engine: "pg-c"}}
	var names []string
	for _, c := range engineOrder(cells, step) {
		names = append(names, c.engine)
	}
	return names
}

func TestRunAll_WarmsEveryEngineThenRunsEachIterationOnEveryEngineInTheSeedsOrder(t *testing.T) {
	const seed = 42
	log := runLogged(t, benchRunConfig(seed, 1, 3))

	for _, queryID := range []string{"q1", "q2"} {
		executions := log.of(queryID)
		require.Len(t, executions, 3*(1+3), "%s: one warmup and three iterations on each engine", queryID)
		steps := slices.Collect(slices.Chunk(executions, 3))
		assert.Equal(t, drawnOrder(orderStep{seed: seed, queryID: queryID, phase: warmupPhase, number: 1}), steps[0], "%s warmup", queryID)
		for iteration := 1; iteration <= 3; iteration++ {
			step := steps[iteration]
			assert.ElementsMatch(t, []string{"pg-a", "pg-b", "pg-c"}, step, "%s iteration %d runs on every engine before the next", queryID, iteration)
			assert.Equal(t, drawnOrder(orderStep{seed: seed, queryID: queryID, phase: iterationPhase, number: iteration}), step, "%s iteration %d", queryID, iteration)
		}
	}
}

func TestRunAll_TheSameSeedRunsTheSameOrderAndAnotherSeedAnother(t *testing.T) {
	sequence := func(seed uint64) [][]string {
		log := runLogged(t, benchRunConfig(seed, 1, 3))
		return [][]string{log.of("q1"), log.of("q2")}
	}

	first := sequence(1)

	assert.Equal(t, first, sequence(1))
	assert.NotEqual(t, first, sequence(2))
}

// scriptedLatencyExecutor answers its i-th call with latencies[i].
type scriptedLatencyExecutor struct {
	name      string
	latencies []time.Duration
	calls     int
}

func (e *scriptedLatencyExecutor) Execute(context.Context, engine.Request) (*engine.Execution, error) {
	latency := e.latencies[e.calls]
	e.calls++
	server := latency / 10
	return &engine.Execution{Latency: latency, ServerTime: &server}, nil
}

func (e *scriptedLatencyExecutor) Name() string { return e.name }
func (e *scriptedLatencyExecutor) Close() error { return nil }

func TestRunAll_PoolsEveryIterationAndKeepsTheCellsMedian(t *testing.T) {
	pg := &scriptedLatencyExecutor{name: "pg", latencies: []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 60 * time.Millisecond}}

	result, err := New(benchRunConfig(1, 0, 3)).RunAll(context.Background(),
		oneJobTrack(t, onePostgresQuery, map[string]spec.Engine{"pg": postgres}, nil), map[string]engine.Executor{"pg": pg})

	require.NoError(t, err)
	latency := result.Jobs[0].Results["q1"]["pg"].Latency
	assert.Equal(t, 3, latency.SampleCount, "every iteration pooled")
	assert.Equal(t, 10*time.Millisecond, latency.Min)
	assert.Equal(t, 60*time.Millisecond, latency.Max)
	assert.Equal(t, 20*time.Millisecond, latency.Median, "the middle sample, not the 30 ms mean")
	server := result.Jobs[0].Results["q1"]["pg"].ServerLatency
	require.NotNil(t, server)
	assert.Equal(t, 3, server.SampleCount, "every iteration's server time pooled")
}

// overlapDetector notes when an execution starts while another is in flight.
type overlapDetector struct {
	name     string
	inFlight *atomic.Int32
	overlaps *atomic.Int32
}

func (e overlapDetector) Execute(context.Context, engine.Request) (*engine.Execution, error) {
	if e.inFlight.Add(1) > 1 {
		e.overlaps.Add(1)
	}
	defer e.inFlight.Add(-1)
	time.Sleep(time.Millisecond)
	return &engine.Execution{Latency: time.Millisecond}, nil
}

func (e overlapDetector) Name() string { return e.name }
func (e overlapDetector) Close() error { return nil }

func TestRunAll_NoTwoMeasuredExecutionsOverlap(t *testing.T) {
	var inFlight, overlaps atomic.Int32
	executors := map[string]engine.Executor{}
	for name := range threePostgresEngines {
		executors[name] = overlapDetector{name: name, inFlight: &inFlight, overlaps: &overlaps}
	}

	_, err := New(benchRunConfig(7, 1, 3)).RunAll(context.Background(),
		oneJobTrack(t, twoPostgresQueries, threePostgresEngines, nil), executors)

	require.NoError(t, err)
	assert.Zero(t, overlaps.Load())
}

// rerankingExecutor returns first before its rerankAfter-th call and then
// afterwards, as an unstable tie would.
type rerankingExecutor struct {
	name        string
	first, then []uuid.UUID
	rerankAfter int
	calls       int
}

func (e *rerankingExecutor) Execute(context.Context, engine.Request) (*engine.Execution, error) {
	e.calls++
	ranked := e.first
	if e.calls > e.rerankAfter {
		ranked = e.then
	}
	return &engine.Execution{RankedDocIDs: ranked, Latency: time.Millisecond}, nil
}

func (e *rerankingExecutor) Name() string { return e.name }
func (e *rerankingExecutor) Close() error { return nil }

func TestRunAll_NamesTheIterationsWhoseRankingDiffersFromTheFirst(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	tests := []struct {
		name        string
		rerankAfter int
		want        []int
	}{
		{name: "changes in iteration 2", rerankAfter: 2, want: []int{2, 3}},
		{name: "changes in iteration 3", rerankAfter: 3, want: []int{3}},
		{name: "never changes", rerankAfter: 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// One warmup call precedes the three iterations.
			pg := &rerankingExecutor{name: "pg", first: []uuid.UUID{a, b}, then: []uuid.UUID{b, a}, rerankAfter: tt.rerankAfter}

			result, err := New(benchRunConfig(1, 1, 3)).RunAll(context.Background(),
				oneJobTrack(t, onePostgresQuery, map[string]spec.Engine{"pg": postgres}, nil), map[string]engine.Executor{"pg": pg})

			require.NoError(t, err)
			assert.Equal(t, tt.want, result.Jobs[0].Results["q1"]["pg"].UnstableIterations)
		})
	}
}

func TestRunAll_ComparesRankingsCutAtTheRankingDepth(t *testing.T) {
	a, b, c, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	tests := []struct {
		name string
		then []uuid.UUID
		want []int
	}{
		{name: "a swap below the depth", then: []uuid.UUID{a, b, d, c}},
		{name: "a swap within the depth", then: []uuid.UUID{b, a, c, d}, want: []int{2, 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := benchRunConfig(1, 1, 3)
			cfg.RankingDepth = 2
			// One warmup call precedes the three iterations.
			pg := &rerankingExecutor{name: "pg", first: []uuid.UUID{a, b, c, d}, then: tt.then, rerankAfter: 2}

			result, err := New(cfg).RunAll(context.Background(),
				oneJobTrack(t, onePostgresQuery, map[string]spec.Engine{"pg": postgres}, nil), map[string]engine.Executor{"pg": pg})

			require.NoError(t, err)
			assert.Equal(t, tt.want, result.Jobs[0].Results["q1"]["pg"].UnstableIterations)
		})
	}
}

func TestRunAll_RunsTheQueriesInSuiteOrderOneAtATime(t *testing.T) {
	var suite strings.Builder
	suite.WriteString("queries:\n")
	var want []string
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(&suite, "  - id: q%d\n    engines:\n      pg: { query: \"SELECT id FROM articles WHERE %d = %d\" }\n", i, i, i)
		want = append(want, fmt.Sprintf("SELECT id FROM articles WHERE %d = %d", i, i))
	}
	pg := &recordingExecutor{name: "pg"}

	_, err := New(benchRunConfig(1, 0, 1)).RunAll(context.Background(),
		oneJobTrack(t, suite.String(), map[string]spec.Engine{"pg": postgres}, nil), map[string]engine.Executor{"pg": pg})

	require.NoError(t, err)
	assert.Equal(t, want, pg.seen)
}
