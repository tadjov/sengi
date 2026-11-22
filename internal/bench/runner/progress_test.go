package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/bench/engine"
	"github.com/tadjov/sengi/internal/bench/spec"
	"github.com/tadjov/sengi/internal/progress/progresstest"
)

// progressWatcher records how many queries the counter called done when each execution starts.
type progressWatcher struct {
	name    string
	counter *progresstest.Recorder
	mu      sync.Mutex
	doneAt  []int64
}

func (e *progressWatcher) Execute(context.Context, engine.Request) (*engine.Execution, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.doneAt = append(e.doneAt, e.counter.Count())
	return &engine.Execution{}, nil
}

func (e *progressWatcher) Name() string { return e.name }
func (e *progressWatcher) Close() error { return nil }

func twoJobTrack(t *testing.T, firstQueries, secondQueries int) *spec.BenchSpec {
	t.Helper()
	dir := t.TempDir()
	suite := func(name string, queries int) string {
		var b strings.Builder
		fmt.Fprintf(&b, "schema_version: 1\nid: %s\nqueries:\n", name)
		for i := range queries {
			fmt.Fprintf(&b, "  - id: %s-q%d\n    engines:\n      pg: { query: \"SELECT id FROM articles\" }\n", name, i)
		}
		path := filepath.Join(dir, name+".yaml")
		require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o644))
		return path
	}
	return &spec.BenchSpec{
		ID:      "progress",
		Engines: map[string]spec.Engine{"pg": postgres},
		Jobs: []spec.Job{
			{Name: "first", Suite: suite("first", firstQueries), Engines: []string{"pg"}},
			{Name: "second", Suite: suite("second", secondQueries), Engines: []string{"pg"}},
		},
	}
}

func TestRunAll_CountsEveryJobsQueriesAgainstTheirTotal(t *testing.T) {
	tests := []struct {
		name        string
		parallelism int
	}{
		{"serial", QueryParallelismSerial},
		{"all queries at once", QueryParallelismUnlimited},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			counter := &progresstest.Recorder{}
			cfg := DefaultConfig()
			cfg.QueryParallelism = tt.parallelism
			cfg.Progress = counter

			_, err := New(cfg).RunAll(context.Background(), twoJobTrack(t, 2, 3), map[string]engine.Executor{
				"pg": &progressWatcher{name: "pg", counter: counter},
			})

			require.NoError(t, err)
			assert.Equal(t, int64(5), counter.Total())
			assert.Equal(t, counter.Total(), counter.Count())
		})
	}
}

func TestRunAll_CountsAQueryOnlyAfterItsLastMeasuredExecution(t *testing.T) {
	counter := &progresstest.Recorder{}
	cfg := DefaultConfig()
	cfg.WarmupRuns, cfg.Runs = 1, 2
	cfg.Progress = counter
	pg := &progressWatcher{name: "pg", counter: counter}

	_, err := New(cfg).RunAll(context.Background(), twoJobTrack(t, 2, 1), map[string]engine.Executor{"pg": pg})

	require.NoError(t, err)
	executionsPerQuery := cfg.WarmupRuns + cfg.Runs
	require.Len(t, pg.doneAt, 3*executionsPerQuery)
	for i, done := range pg.doneAt {
		assert.Equal(t, int64(i/executionsPerQuery), done, "execution %d of the run saw the wrong count", i)
	}
}

func TestRunAll_WithoutACounterStillRuns(t *testing.T) {
	cfg := DefaultConfig()
	pg := &recordingExecutor{name: "pg"}

	_, err := New(cfg).RunAll(context.Background(), twoJobTrack(t, 1, 1), map[string]engine.Executor{"pg": pg})

	require.NoError(t, err)
	assert.NotEmpty(t, pg.seen)
}
