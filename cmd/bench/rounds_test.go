package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// roundsTrack is the judged lineage track with every search logged. b answers
// q1 with drifted once it has served driftAfter searches of it since the log's
// last reset; nil drifted keeps every ranking.
func roundsTrack(t *testing.T, drifted func(relevant, other uuid.UUID) []uuid.UUID, driftAfter int) (lineageTrack, *searchLog) {
	t.Helper()
	relevant, other := uuid.New(), uuid.New()
	log := &searchLog{}
	b := &fakeIndex{count: 10, ranked: map[string][]uuid.UUID{"q1": {other, relevant}, "q2": {other}}, log: log, driftAfter: driftAfter}
	if drifted != nil {
		b.drifted = map[string][]uuid.UUID{"q1": drifted(relevant, other)}
	}
	cluster := fakeCluster(t, map[string]*fakeIndex{
		"a": {count: 10, ranked: map[string][]uuid.UUID{"q1": {relevant, other}, "q2": {relevant}}, log: log},
		"b": b,
	})
	tr := newLineageTrack(t, cluster)
	tr.writeJudgments(t, tr.pool(t).Meta.RunID, map[string]map[uuid.UUID]int{
		"q1": {relevant: 3, other: 0},
		"q2": {relevant: 2, other: 0},
	})
	log.reset()
	return tr, log
}

func TestRun_EveryRoundWarmsUpAndRotatesTheEngineOrder(t *testing.T) {
	tr, log := roundsTrack(t, nil, 0)

	_, err := execute(t, newRunCmd(), tr.dir, "--jobs", "both", "--rounds", "3")
	require.NoError(t, err)

	// The spec warms up once and measures twice: three searches a cell a round.
	assert.Equal(t, []string{
		"a", "a", "a", "b", "b", "b",
		"b", "b", "b", "a", "a", "a",
		"a", "a", "a", "b", "b", "b",
	}, log.indexesSearchedFor("q1"))
}

func TestRun_ReportKeepsEveryRoundAndTheMedianOfTheirP50(t *testing.T) {
	tr, _ := roundsTrack(t, nil, 0)

	_, err := execute(t, newRunCmd(), tr.dir, "--rounds", "3")
	require.NoError(t, err)

	rpt := tr.latestReport(t)
	require.NotNil(t, rpt, "a run in rounds is the spec's own run")
	assert.Equal(t, 3, rpt.Settings.Rounds)
	for _, job := range rpt.Jobs {
		for _, agg := range job.Aggregated {
			require.Len(t, agg.Rounds, 3, "%s/%s", job.JobName, agg.EngineName)
			p50s := []int64{int64(agg.Rounds[0].P50()), int64(agg.Rounds[1].P50()), int64(agg.Rounds[2].P50())}
			slices.Sort(p50s)
			require.NotNil(t, agg.AcrossRounds)
			assert.EqualValues(t, p50s[1], agg.AcrossRounds.P50.Median, "%s/%s", job.JobName, agg.EngineName)
			assert.EqualValues(t, p50s[0], agg.AcrossRounds.P50.Min, "%s/%s", job.JobName, agg.EngineName)
			assert.EqualValues(t, p50s[2], agg.AcrossRounds.P50.Max, "%s/%s", job.JobName, agg.EngineName)
		}
	}
}

func TestRun_ACellRankedDifferentlyInALaterRoundFailsTheRun(t *testing.T) {
	// Round 1 is warmup and two measured searches; every later search swaps
	// b's two docs for q1.
	tr, _ := roundsTrack(t, func(relevant, other uuid.UUID) []uuid.UUID { return []uuid.UUID{relevant, other} }, 3)
	before := seedLatest(t, tr)

	_, err := execute(t, newRunCmd(), tr.dir, "--rounds", "3")

	require.Error(t, err)
	assert.ErrorContains(t, err, "both:q1/b (round 2, 3)")
	assert.NotContains(t, err.Error(), "q1/a")
	assertLatestUnchanged(t, tr, before)
	reports := tr.reports(t)
	require.Len(t, reports, 1, "the report is still written")
	assert.True(t, slices.ContainsFunc(reports[0].Settings.Incomplete, func(reason string) bool {
		return strings.Contains(reason, "both:q1/b")
	}), "settings.incomplete = %v, want the unstable cell named", reports[0].Settings.Incomplete)
}

func TestRun_RoundsBelowOneAreRejected(t *testing.T) {
	tr := judgedTrack(t, nil)

	_, err := execute(t, newRunCmd(), tr.dir, "--rounds", "0")

	assert.ErrorContains(t, err, "--rounds")
	assert.Empty(t, tr.reports(t))
}
