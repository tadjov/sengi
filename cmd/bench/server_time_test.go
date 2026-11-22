package main

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/bench/report"
)

func TestRun_ReportsElasticsearchTookBesideTheClientTime(t *testing.T) {
	relevant, other := uuid.New(), uuid.New()
	took := 7
	cluster := fakeCluster(t, map[string]*fakeIndex{
		"a": {count: 10, took: &took, ranked: map[string][]uuid.UUID{"q1": {relevant, other}, "q2": {relevant}}},
		"b": {count: 10, took: &took, ranked: map[string][]uuid.UUID{"q1": {other, relevant}, "q2": {other}}},
	})
	tr := newLineageTrack(t, cluster)
	tr.judgeEveryPooledDoc(t, tr.pool(t), 1)

	_, err := execute(t, newRunCmd(), tr.dir)
	require.NoError(t, err)

	for _, job := range tr.latestReport(t).Jobs {
		agg := aggregatedEngine(t, job, "a")
		require.NotNil(t, agg.ServerLatency, "job %s", job.JobName)
		assert.Equal(t, 7*time.Millisecond, agg.ServerLatency.P50(), "job %s", job.JobName)
		assert.Positive(t, agg.Latency.P50(), "job %s keeps the client time", job.JobName)
	}
}

func aggregatedEngine(t *testing.T, job report.JobReport, engineName string) report.AggregatedEntry {
	t.Helper()
	for _, agg := range job.Aggregated {
		if agg.EngineName == engineName {
			return agg
		}
	}
	require.Failf(t, "engine not aggregated", "job %s has no %s", job.JobName, engineName)
	return report.AggregatedEntry{}
}
