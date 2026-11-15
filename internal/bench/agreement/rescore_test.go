package agreement_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/tusker/internal/bench/agreement"
	"github.com/tadjov/tusker/internal/bench/meta"
	"github.com/tadjov/tusker/internal/bench/report"
)

func ranking(docs ...uuid.UUID) []report.RankedDoc {
	out := make([]report.RankedDoc, len(docs))
	for i, doc := range docs {
		out[i] = report.RankedDoc{DocID: doc, Score: float64(len(docs) - i)}
	}
	return out
}

// rescoreFixture is one job of engines a and b over q1 and q2, scored with
// the set "own" at k = 2 and threshold 1. Its stored P@2 and RR are worked by
// hand, and b's stored RR on q1 is wrong.
type rescoreFixture struct {
	report        *report.Report
	d1, d2, d3, e uuid.UUID
}

func newRescoreFixture() rescoreFixture {
	fx := rescoreFixture{d1: uuid.New(), d2: uuid.New(), d3: uuid.New(), e: uuid.New()}
	fx.report = &report.Report{
		SchemaVersion: 1,
		Provenance:    meta.Meta{RunID: "run-1", JudgmentsRunID: "judge-own", Sources: &meta.Sources{Judgments: "/elsewhere/annotations.own.yaml"}},
		Jobs: []report.JobReport{{
			JobName:    "fts",
			Aggregated: []report.AggregatedEntry{{EngineName: "a"}, {EngineName: "b"}},
			PerQuery: []report.Entry{
				{QueryID: "q1", EngineName: "a", Judged: true, Precision: map[int]float64{2: 0.5}, RR: 1, ReturnedCount: 3, Ranking: ranking(fx.d1, fx.d2, fx.d3)},
				{QueryID: "q1", EngineName: "b", Judged: true, Precision: map[int]float64{2: 1}, RR: 0.5, ReturnedCount: 3, Ranking: ranking(fx.d3, fx.d1, fx.d2)},
				{QueryID: "q2", EngineName: "a", Judged: true, Precision: map[int]float64{2: 0}, RR: 0, ReturnedCount: 1, Ranking: ranking(fx.e)},
				{QueryID: "q2", EngineName: "b", Judged: true, Error: "timeout"},
			},
		}},
	}
	return fx
}

func (fx rescoreFixture) sets() []agreement.Set {
	return []agreement.Set{
		set("own", graded("q1", map[uuid.UUID]int{fx.d1: 3, fx.d2: 0, fx.d3: 1}), graded("q2", map[uuid.UUID]int{fx.e: 0})),
		set("other", graded("q1", map[uuid.UUID]int{fx.d1: 0, fx.d2: 3, fx.d3: 0}), graded("q2", map[uuid.UUID]int{fx.e: 0})),
		set("sample", graded("q1", map[uuid.UUID]int{fx.d1: 3, fx.d2: 0}), graded("q2", map[uuid.UUID]int{fx.e: 0})),
	}
}

func (fx rescoreFixture) rescore(t *testing.T) agreement.Rescoring {
	t.Helper()
	got, err := agreement.Rescore(agreement.RescoreOptions{Report: fx.report, Sets: fx.sets(), Reference: "own", KValues: []int{2}, Threshold: 1})
	require.NoError(t, err)
	return got
}

func excluded(exclusions []agreement.Exclusion) map[string]string {
	out := map[string]string{}
	for _, e := range exclusions {
		out[e.Name] = e.Reason
	}
	return out
}

func TestRescore_RescoresOnlyTheMetricsTheReportsOwnSetReproduces(t *testing.T) {
	got := newRescoreFixture().rescore(t)

	assert.Equal(t, "own", got.ReportSet)
	assert.Equal(t, []string{"P@2"}, got.Metrics)
	leftOut := excluded(got.LeftOut)
	assert.Contains(t, leftOut["MRR"], "fts/q1/b")
	assert.Contains(t, leftOut, "NDCG@2")
	assert.Contains(t, leftOut, "MAP")
}

func TestRescore_RefusesASetMissingATopKDoc(t *testing.T) {
	fx := newRescoreFixture()

	got := fx.rescore(t)

	refused := excluded(got.Refused)
	assert.Equal(t, []string{"sample"}, keys(refused))
	assert.Contains(t, refused["sample"], fx.d3.String())
	for _, job := range got.Jobs {
		for _, metric := range job.Metrics {
			for _, order := range metric.Sets {
				assert.NotEqual(t, "sample", order.Label)
			}
		}
	}
}

func TestRescore_MeansOrdersAndTauPerJob(t *testing.T) {
	got := newRescoreFixture().rescore(t)

	require.Len(t, got.Jobs, 1)
	job := got.Jobs[0]
	assert.Equal(t, []string{"a", "b"}, job.Engines)
	require.Len(t, job.Metrics, 1)
	byLabel := map[string]agreement.EngineOrder{}
	for _, order := range job.Metrics[0].Sets {
		byLabel[order.Label] = order
	}
	// A failed cell scores 0 and still counts toward the mean, as in the report.
	assert.Equal(t, map[string]float64{"a": 0.25, "b": 0.5}, byLabel["own"].Means)
	assert.Equal(t, []string{"b", "a"}, byLabel["own"].Order)
	assert.Equal(t, map[string]float64{"a": 0.25, "b": 0}, byLabel["other"].Means)
	assert.Equal(t, []string{"a", "b"}, byLabel["other"].Order)
	assert.InDelta(t, -1, float64(byLabel["other"].TauB), 1e-12)
}

func TestRescore_NeedsTheReportsOwnSet(t *testing.T) {
	fx := newRescoreFixture()

	_, err := agreement.Rescore(agreement.RescoreOptions{Report: fx.report, Sets: fx.sets()[1:], Reference: "other", KValues: []int{2}, Threshold: 1})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "judge-own")
	assert.Contains(t, err.Error(), "/elsewhere/annotations.own.yaml")
}

func TestRescore_NeedsStoredRankings(t *testing.T) {
	fx := newRescoreFixture()
	fx.report.Jobs[0].PerQuery[0].Ranking = nil

	_, err := agreement.Rescore(agreement.RescoreOptions{Report: fx.report, Sets: fx.sets(), Reference: "own", KValues: []int{2}, Threshold: 1})

	assert.ErrorContains(t, err, "ranking")
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
