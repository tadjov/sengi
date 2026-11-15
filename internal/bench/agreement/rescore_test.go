package agreement_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/tusker/internal/bench/agreement"
	"github.com/tadjov/tusker/internal/bench/judgment"
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

// rescoreFixture: engines a and b on q1 and q2, k = 2, threshold 1; b's stored q1 RR is wrong.
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

// Both means are 0.2, though 0.1+0.2+0.3 and 0.3+0.2+0.1 are different floats.
func TestRescore_EqualMeansTieWhateverOrderTheyWereSummedIn(t *testing.T) {
	var entries []judgment.Entry
	var cells []report.Entry
	for q, relevant := range [][2]int{{1, 3}, {2, 2}, {3, 1}} {
		entry := judgment.Entry{QueryID: fmt.Sprintf("q%d", q+1)}
		for e, engine := range []string{"a", "b"} {
			var docs []uuid.UUID
			for rank := range 10 {
				doc := uuid.New()
				grade := 0
				if rank < relevant[e] {
					grade = 3
				}
				entry.Docs = append(entry.Docs, judgment.GradedDoc{DocID: doc, Grade: grade})
				docs = append(docs, doc)
			}
			cells = append(cells, report.Entry{QueryID: entry.QueryID, EngineName: engine, Judged: true, ReturnedCount: 10,
				Precision: map[int]float64{10: float64(relevant[e]) / 10}, Ranking: ranking(docs...)})
		}
		entries = append(entries, entry)
	}
	rpt := &report.Report{
		Provenance: meta.Meta{RunID: "run-1", JudgmentsRunID: "judge-own"},
		Jobs:       []report.JobReport{{JobName: "fts", Aggregated: []report.AggregatedEntry{{EngineName: "a"}, {EngineName: "b"}}, PerQuery: cells}},
	}

	got, err := agreement.Rescore(agreement.RescoreOptions{Report: rpt, Sets: []agreement.Set{set("own", entries...), set("again", entries...)}, Reference: "own", KValues: []int{10}, Threshold: 1})
	require.NoError(t, err)

	require.Equal(t, []string{"P@10"}, got.Metrics)
	sets := got.Jobs[0].Metrics[0].Sets
	assert.Equal(t, sets[0].Means["a"], sets[0].Means["b"])
	assert.True(t, math.IsNaN(float64(sets[1].TauB)), "τ-b over two tied engines is undefined")
}

func TestRescore_ScoresEachJobFromItsOwnCells(t *testing.T) {
	fx := newRescoreFixture()
	// Engine c ranks d1 then d2 on q1: one of two relevant at threshold 1.
	fx.report.Jobs = append(fx.report.Jobs, report.JobReport{
		JobName:    "solo",
		Aggregated: []report.AggregatedEntry{{EngineName: "c"}},
		PerQuery: []report.Entry{
			{QueryID: "q1", EngineName: "c", Judged: true, Precision: map[int]float64{2: 0.5}, RR: 1, ReturnedCount: 2, Ranking: ranking(fx.d1, fx.d2)},
		},
	})

	got := fx.rescore(t)

	require.Len(t, got.Jobs, 2)
	assert.Equal(t, "solo", got.Jobs[1].Job)
	assert.Equal(t, map[string]float64{"c": 0.5}, got.Jobs[1].Metrics[0].Sets[0].Means)
}
