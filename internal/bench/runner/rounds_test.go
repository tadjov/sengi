package runner

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/tusker/internal/bench/engine"
	"github.com/tadjov/tusker/internal/bench/spec"
)

// roundScriptedExecutor answers every call of round i with latencies[i] and
// serverTimes[i]; a round is callsPerRound calls.
type roundScriptedExecutor struct {
	name          string
	callsPerRound int
	latencies     []time.Duration
	serverTimes   []time.Duration
	calls         int
}

func (e *roundScriptedExecutor) Execute(context.Context, engine.Request) (*engine.Execution, error) {
	round := e.calls / e.callsPerRound
	e.calls++
	server := e.serverTimes[round]
	return &engine.Execution{Latency: e.latencies[round], ServerTime: &server}, nil
}

func (e *roundScriptedExecutor) Name() string { return e.name }
func (e *roundScriptedExecutor) Close() error { return nil }

func TestRunAll_KeepsEveryRoundAndPoolsThemAll(t *testing.T) {
	cfg := DefaultConfig()
	cfg.WarmupRuns = 0
	cfg.Runs = 2
	cfg.Rounds = 3
	pg := &roundScriptedExecutor{
		name:          "pg",
		callsPerRound: cfg.Runs,
		latencies:     []time.Duration{30 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond},
		serverTimes:   []time.Duration{3 * time.Millisecond, 1 * time.Millisecond, 2 * time.Millisecond},
	}

	result, err := New(cfg).RunAll(context.Background(), oneJobTrack(t, onePostgresQuery, map[string]spec.Engine{"pg": postgres}, nil),
		map[string]engine.Executor{"pg": pg})

	require.NoError(t, err)
	qr := result.Jobs[0].Results["q1"]["pg"]
	require.NoError(t, qr.Error)
	require.Len(t, qr.RoundLatencies, 3)
	for i, want := range []time.Duration{30 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond} {
		assert.Equal(t, want, qr.RoundLatencies[i].P50(), "round %d", i+1)
		assert.Equal(t, 2, qr.RoundLatencies[i].SampleCount, "round %d", i+1)
	}
	assert.Equal(t, 6, qr.Latency.SampleCount, "every round's iterations pooled")
	assert.Equal(t, 10*time.Millisecond, qr.Latency.Min)
	assert.Equal(t, 30*time.Millisecond, qr.Latency.Max)
	assert.Equal(t, 20*time.Millisecond, qr.Latency.Mean)
	require.NotNil(t, qr.ServerLatency)
	assert.Equal(t, 6, qr.ServerLatency.SampleCount, "every round's server times pooled")
	assert.Equal(t, 1*time.Millisecond, qr.ServerLatency.Min)
	assert.Equal(t, 3*time.Millisecond, qr.ServerLatency.Max)
}
