package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/tusker/internal/bench/runner"
)

// roundsRun has one engine answer one query in a round per entry of
// roundLatencies, each round's every iteration taking that long.
func roundsRun(roundLatencies ...time.Duration) *runner.BenchmarkResult {
	qr := answered("q1", "pg", 0.5, 0)
	qr.RoundLatencies = nil
	var pooled []time.Duration
	for _, latency := range roundLatencies {
		iterations := []time.Duration{latency, latency}
		qr.RoundLatencies = append(qr.RoundLatencies, runner.ComputeLatencyStats(iterations))
		pooled = append(pooled, iterations...)
	}
	qr.Latency = runner.ComputeLatencyStats(pooled)
	return &runner.BenchmarkResult{
		Config: runner.Config{KValues: []int{10}, RelevanceThreshold: 1},
		Jobs: []*runner.JobResult{{
			JobName:     "rounds",
			EngineNames: []string{"pg"},
			QueryOrder:  []string{"q1"},
			Results:     map[string]map[string]runner.QueryResult{"q1": {"pg": qr}},
		}},
	}
}

func TestGenerate_KeepsEveryRoundAndTheirMedianWithItsSpread(t *testing.T) {
	agg := Generate(roundsRun(30*time.Millisecond, 10*time.Millisecond, 20*time.Millisecond), nil).Jobs[0].Aggregated[0]

	require.Len(t, agg.Rounds, 3)
	assert.Equal(t, 30*time.Millisecond, agg.Rounds[0].P50(), "rounds keep their order")
	require.NotNil(t, agg.AcrossRounds)
	assert.Equal(t, Spread{Median: 20 * time.Millisecond, Min: 10 * time.Millisecond, Max: 30 * time.Millisecond}, agg.AcrossRounds.P50)
	assert.Equal(t, Spread{Median: 20 * time.Millisecond, Min: 10 * time.Millisecond, Max: 30 * time.Millisecond}, agg.AcrossRounds.P99)
}

func TestWriters_ShowTheMedianOfTheRoundsWithItsSpread(t *testing.T) {
	rpt := Generate(roundsRun(30*time.Millisecond, 10*time.Millisecond, 20*time.Millisecond), nil)
	var terminal, markdown bytes.Buffer
	WriteTable(rpt, &terminal)
	WriteMarkdown(rpt, &markdown)
	page, err := RenderHTML(rpt)
	require.NoError(t, err)

	for name, out := range map[string]string{"terminal": terminal.String(), "markdown": markdown.String(), "html": string(page)} {
		_, roundsSection, found := strings.Cut(out, "Latency Across Rounds")
		require.True(t, found, "%s has no rounds section", name)
		assert.Contains(t, roundsSection, "20.00ms [10.00ms, 30.00ms]", name)
	}
}

func TestWriters_OneRoundShowsNoSpread(t *testing.T) {
	rpt := Generate(roundsRun(10*time.Millisecond), nil)
	var terminal, markdown bytes.Buffer
	WriteTable(rpt, &terminal)
	WriteMarkdown(rpt, &markdown)
	page, err := RenderHTML(rpt)
	require.NoError(t, err)

	for name, out := range map[string]string{"terminal": terminal.String(), "markdown": markdown.String(), "html": string(page)} {
		assert.NotContains(t, out, "Latency Across Rounds", name)
	}
}
