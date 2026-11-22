package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rerankingTrack has b swap q1's docs from its third search on, so only the second iteration ranks differently.
func rerankingTrack(t *testing.T) lineageTrack {
	t.Helper()
	relevant, other := uuid.New(), uuid.New()
	log := &searchLog{}
	b := &fakeIndex{
		count:      10,
		ranked:     map[string][]uuid.UUID{"q1": {other, relevant}, "q2": {other}},
		log:        log,
		drifted:    map[string][]uuid.UUID{"q1": {relevant, other}},
		driftAfter: 2,
	}
	cluster := fakeCluster(t, map[string]*fakeIndex{
		"a": {count: 10, ranked: map[string][]uuid.UUID{"q1": {relevant, other}, "q2": {relevant}}},
		"b": b,
	})
	tr := newLineageTrack(t, cluster)
	tr.writeJudgments(t, tr.pool(t).Meta.RunID, map[string]map[uuid.UUID]int{
		"q1": {relevant: 3, other: 0},
		"q2": {relevant: 2, other: 0},
	})
	log.reset()
	return tr
}

func TestRun_ACellRankedDifferentlyInALaterIterationFailsTheRun(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "judged"},
		{name: "latency only, whole rankings", args: []string{"--latency-only"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := rerankingTrack(t)
			before := seedLatest(t, tr)

			_, err := execute(t, newRunCmd(), append([]string{tr.dir}, tt.args...)...)

			require.Error(t, err)
			assert.ErrorContains(t, err, "both:q1/b (iteration 2)")
			assert.NotContains(t, err.Error(), "q1/a")
			assertLatestUnchanged(t, tr, before)
			reports := tr.reports(t)
			require.Len(t, reports, 1, "the report is still written")
			assert.True(t, slices.ContainsFunc(reports[0].Settings.Incomplete, func(reason string) bool {
				return strings.Contains(reason, "both:q1/b")
			}), "settings.incomplete = %v, want the unstable cell named", reports[0].Settings.Incomplete)
		})
	}
}
