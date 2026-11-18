package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/tusker/internal/bench/runner"
)

// serverTimedRun has es report a server time of took on q1 and q2, and pg
// report none.
func serverTimedRun(took time.Duration) *runner.BenchmarkResult {
	judged := map[string]int{"11111111-1111-1111-1111-111111111111": 1}
	results := map[string]map[string]runner.QueryResult{}
	for _, qID := range []string{"q1", "q2"} {
		es := answered(qID, "es", 0.5, 20*time.Millisecond)
		server := runner.ComputeLatencyStats([]time.Duration{took, took})
		es.ServerLatency = &server
		results[qID] = map[string]runner.QueryResult{"pg": answered(qID, "pg", 0.5, 30*time.Millisecond), "es": es}
	}
	return &runner.BenchmarkResult{
		Config: runner.Config{
			KValues:            []int{10},
			RelevanceThreshold: 1,
			Judgments:          map[string]map[string]int{"q1": judged, "q2": judged},
		},
		Jobs: []*runner.JobResult{{
			JobName:     "server",
			EngineNames: []string{"pg", "es"},
			QueryOrder:  []string{"q1", "q2"},
			Results:     results,
		}},
	}
}

func TestGenerate_ServerTimeOnlyForTheEnginesThatReportOne(t *testing.T) {
	agg := aggregatedByEngine(Generate(serverTimedRun(7*time.Millisecond), nil).Jobs[0].Aggregated)

	require.NotNil(t, agg["es"].ServerLatency)
	assert.Equal(t, 7*time.Millisecond, agg["es"].ServerLatency.P50())
	assert.Equal(t, 20*time.Millisecond, agg["es"].Latency.P50(), "the client time stays beside it")
	assert.Nil(t, agg["pg"].ServerLatency)
}

func TestWriters_ShowServerTimeOnlyForTheEnginesThatReportOne(t *testing.T) {
	rpt := Generate(serverTimedRun(7*time.Millisecond), nil)
	var terminal, markdown bytes.Buffer
	WriteTable(rpt, &terminal)
	WriteMarkdown(rpt, &markdown)
	page, err := RenderHTML(rpt)
	require.NoError(t, err)
	raw, err := json.Marshal(rpt)
	require.NoError(t, err)

	formats := []struct {
		name, out, esRow, pgRow string
	}{
		{name: "terminal", out: terminal.String(), esRow: `│ es\s+│\s+7\.00ms\s+│`, pgRow: `│ pg\s+│`},
		{name: "markdown", out: markdown.String(), esRow: `\| es \| 7\.00ms \|`, pgRow: `\| pg \|`},
		{name: "html", out: string(page), esRow: `<td>es</td>\s*<td>7\.00ms</td>`, pgRow: `<td>pg</td>`},
	}
	for _, f := range formats {
		_, serverSection, found := strings.Cut(f.out, "Server Time")
		require.True(t, found, "%s has no server time section", f.name)
		serverSection, _, _ = strings.Cut(serverSection, "Significance")
		assert.Regexp(t, f.esRow, serverSection, f.name)
		assert.NotRegexp(t, f.pgRow, serverSection, f.name)
	}
	var decoded struct {
		Jobs []struct {
			Aggregated []map[string]any
		}
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	for _, agg := range decoded.Jobs[0].Aggregated {
		_, recorded := agg["ServerLatency"]
		assert.Equal(t, agg["EngineName"] == "es", recorded, "engine %v", agg["EngineName"])
	}
}

func TestWriters_ShowAReportedZeroServerTime(t *testing.T) {
	var terminal bytes.Buffer

	WriteTable(Generate(serverTimedRun(0), nil), &terminal)

	_, serverSection, found := strings.Cut(terminal.String(), "Server Time")
	require.True(t, found)
	assert.Regexp(t, `│ es\s+│\s+0\.00ms\s+│`, serverSection)
}

func TestWriters_NoServerTimeSectionWhenNoEngineReportsOne(t *testing.T) {
	run := serverTimedRun(7 * time.Millisecond)
	for _, byEngine := range run.Jobs[0].Results {
		es := byEngine["es"]
		es.ServerLatency = nil
		byEngine["es"] = es
	}
	rpt := Generate(run, nil)
	var terminal, markdown bytes.Buffer
	WriteTable(rpt, &terminal)
	WriteMarkdown(rpt, &markdown)

	assert.NotContains(t, terminal.String(), "Server Time")
	assert.NotContains(t, markdown.String(), "Server Time")
}

func TestWriteJSON_KeepsEachCellsMedianIteration(t *testing.T) {
	qr := answered("q1", "pg", 0.5, 0)
	qr.Latency = runner.ComputeLatencyStats([]time.Duration{10 * time.Millisecond, 60 * time.Millisecond, 20 * time.Millisecond})
	run := &runner.BenchmarkResult{
		Config: runner.Config{KValues: []int{10}, RelevanceThreshold: 1},
		Jobs: []*runner.JobResult{{
			JobName: "median", EngineNames: []string{"pg"}, QueryOrder: []string{"q1"},
			Results: map[string]map[string]runner.QueryResult{"q1": {"pg": qr}},
		}},
	}

	raw, err := json.Marshal(Generate(run, nil))
	require.NoError(t, err)

	var decoded struct {
		Jobs []struct {
			PerQuery []struct {
				Latency struct {
					Median time.Duration `json:"median"`
				}
			}
		}
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, 20*time.Millisecond, decoded.Jobs[0].PerQuery[0].Latency.Median, "the middle iteration, not the 30 ms mean")
}
