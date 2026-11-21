package agreement

import (
	"cmp"
	"fmt"
	"math"
	"slices"

	"github.com/google/uuid"
	"github.com/tadjov/sengi/internal/bench/judgment"
	"github.com/tadjov/sengi/internal/bench/metrics"
	"github.com/tadjov/sengi/internal/bench/report"
)

// RescoreOptions configures Rescore.
type RescoreOptions struct {
	Report *report.Report
	Sets   []Set
	// Reference is the label whose engine order every set's is compared with.
	Reference string
	KValues   []int
	Threshold int
}

// Rescoring is a run report's engine means recomputed under each judgment set.
type Rescoring struct {
	ReportRunID string `json:"report_run_id"`
	// ReportSet is the label of the set the report was scored with.
	ReportSet string `json:"report_set"`
	// Reference is empty when the reference set was refused, leaving no τ.
	Reference string         `json:"reference"`
	Metrics   []string       `json:"metrics"`
	LeftOut   []Exclusion    `json:"left_out,omitempty"`
	Refused   []Exclusion    `json:"refused,omitempty"`
	Jobs      []JobRescoring `json:"jobs"`
}

// Exclusion is a metric or set left out of a rescoring, and why.
type Exclusion struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// JobRescoring is one job's engines ranked under each rescored set.
type JobRescoring struct {
	Job     string            `json:"job"`
	Engines []string          `json:"engines"`
	Queries int               `json:"queries"`
	Metrics []MetricRescoring `json:"metrics"`
}

// MetricRescoring is one metric's engine order under each rescored set.
type MetricRescoring struct {
	Metric string        `json:"metric"`
	Sets   []EngineOrder `json:"sets"`
}

// EngineOrder is the engines' means under one set, best first, with τ-b against the reference.
type EngineOrder struct {
	Label string             `json:"label"`
	Means map[string]float64 `json:"means"`
	Order []string           `json:"order"`
	TauB  Statistic          `json:"tau_b"`
}

// Rescore ranks each job's engines under every set that covers the top k.
func Rescore(opts RescoreOptions) (Rescoring, error) {
	if err := requireRankings(opts.Report); err != nil {
		return Rescoring{}, err
	}
	own := slices.IndexFunc(opts.Sets, func(s Set) bool { return s.File.Meta.RunID == opts.Report.Provenance.JudgmentsRunID })
	if own < 0 {
		return Rescoring{}, fmt.Errorf("report %s was scored with judgments %s (%s); pass them in --judgments so its scores can be reproduced first",
			opts.Report.Provenance.RunID, opts.Report.Provenance.JudgmentsRunID, reportJudgmentsPath(opts.Report))
	}
	grades := make([]map[string]map[uuid.UUID]int, len(opts.Sets))
	for i, s := range opts.Sets {
		grades[i] = gradesByQuery(s.File)
	}
	result := Rescoring{ReportRunID: opts.Report.Provenance.RunID, ReportSet: opts.Sets[own].Label}
	ranked := ranking{reference: -1}
	for i, s := range opts.Sets {
		if reason := opts.uncoveredTopK(grades[i]); reason != "" {
			result.Refused = append(result.Refused, Exclusion{Name: s.Label, Reason: reason})
			continue
		}
		ranked.sets = append(ranked.sets, rescoredSet{label: s.Label, scores: opts.scoreJobs(grades[i])})
	}
	ranked.metrics, result.LeftOut = opts.reproducedMetrics(rescoredSet{label: opts.Sets[own].Label, scores: opts.scoreJobs(grades[own])})
	for _, m := range ranked.metrics {
		result.Metrics = append(result.Metrics, m.name)
	}
	if ranked.reference = slices.IndexFunc(ranked.sets, func(s rescoredSet) bool { return s.label == opts.Reference }); ranked.reference >= 0 {
		result.Reference = opts.Reference
	}
	for j, job := range opts.Report.Jobs {
		result.Jobs = append(result.Jobs, ranked.job(job, j))
	}
	return result, nil
}

type rescoredSet struct {
	label string
	// scores hold each job's scored cells.
	scores []map[cell]metrics.ScoreSet
}

// reproducedMetrics keeps metrics the report's own set reproduces, so rescores stay comparable.
func (o RescoreOptions) reproducedMetrics(own rescoredSet) (kept []metric, leftOut []Exclusion) {
	for _, m := range catalogue(o.KValues) {
		if reason := o.irreproducible(m, own); reason != "" {
			leftOut = append(leftOut, Exclusion{Name: m.name, Reason: reason})
			continue
		}
		kept = append(kept, m)
	}
	return kept, leftOut
}

// ranking orders each job's engines per set; reference is -1 when its set was refused.
type ranking struct {
	metrics   []metric
	sets      []rescoredSet
	reference int
}

func (rk ranking) job(job report.JobReport, index int) JobRescoring {
	grid := jobGrid{engines: engines(job), queries: judgedQueries(job)}
	jr := JobRescoring{Job: job.JobName, Engines: grid.engines, Queries: len(grid.queries)}
	for _, m := range rk.metrics {
		means := make([][]float64, len(rk.sets))
		for r, s := range rk.sets {
			means[r] = m.engineMeans(grid, s.scores[index])
		}
		mr := MetricRescoring{Metric: m.name}
		for r, s := range rk.sets {
			order := EngineOrder{
				Label: s.label,
				Means: make(map[string]float64, len(jr.Engines)),
				Order: bestFirst(jr.Engines, means[r]),
				TauB:  Statistic(math.NaN()),
			}
			for e, engine := range jr.Engines {
				order.Means[engine] = means[r][e]
			}
			if rk.reference >= 0 {
				order.TauB = Statistic(metrics.KendallTauB(means[r], means[rk.reference]))
			}
			mr.Sets = append(mr.Sets, order)
		}
		jr.Metrics = append(jr.Metrics, mr)
	}
	return jr
}

func reportJudgmentsPath(r *report.Report) string {
	if r.Provenance.Sources == nil {
		return "no path recorded"
	}
	return r.Provenance.Sources.Judgments
}

// requireRankings refuses a report whose cells returned docs without a stored ranking.
func requireRankings(r *report.Report) error {
	for _, job := range r.Jobs {
		for _, e := range job.PerQuery {
			if e.Error == "" && e.ReturnedCount > 0 && len(e.Ranking) == 0 {
				return fmt.Errorf("report %s stores no ranking for %s/%s/%s, so it cannot be rescored", r.Provenance.RunID, job.JobName, e.QueryID, e.EngineName)
			}
		}
	}
	return nil
}

func gradesByQuery(file *judgment.File) map[string]map[uuid.UUID]int {
	out := make(map[string]map[uuid.UUID]int)
	for key, grade := range gradesOf(file) {
		if out[key.queryID] == nil {
			out[key.queryID] = make(map[uuid.UUID]int)
		}
		out[key.queryID][key.docID] = grade
	}
	return out
}

type cell struct {
	queryID, engine string
}

func (o RescoreOptions) scoreJobs(grades map[string]map[uuid.UUID]int) []map[cell]metrics.ScoreSet {
	out := make([]map[cell]metrics.ScoreSet, len(o.Report.Jobs))
	for j, job := range o.Report.Jobs {
		out[j] = make(map[cell]metrics.ScoreSet)
		for _, e := range job.PerQuery {
			if e.Judged && e.Error == "" {
				out[j][cell{queryID: e.QueryID, engine: e.EngineName}] = metrics.ComputeAll(rankedIDs(e.Ranking), grades[e.QueryID], o.KValues, o.Threshold)
			}
		}
	}
	return out
}

func rankedIDs(ranking []report.RankedDoc) []uuid.UUID {
	ids := make([]uuid.UUID, len(ranking))
	for i, doc := range ranking {
		ids[i] = doc.DocID
	}
	return ids
}

func (o RescoreOptions) uncoveredTopK(grades map[string]map[uuid.UUID]int) string {
	k := slices.Max(o.KValues)
	missing, first := 0, ""
	for _, job := range o.Report.Jobs {
		for _, e := range job.PerQuery {
			if !e.Judged || e.Error != "" {
				continue
			}
			for rank, doc := range e.Ranking[:min(k, len(e.Ranking))] {
				if _, ok := grades[e.QueryID][doc.DocID]; ok {
					continue
				}
				if missing == 0 {
					first = fmt.Sprintf("%s at rank %d of %s/%s/%s", doc.DocID, rank+1, job.JobName, e.QueryID, e.EngineName)
				}
				missing++
			}
		}
	}
	if missing == 0 {
		return ""
	}
	return fmt.Sprintf("leaves %d of the rankings' top-%d docs ungraded, first %s", missing, k, first)
}

type metric struct {
	name   string
	scored func(metrics.ScoreSet) float64
	stored func(report.Entry) (float64, bool)
}

func catalogue(kValues []int) []metric {
	var out []metric
	atK := func(format string, scored func(metrics.ScoreSet) map[int]float64, stored func(report.Entry) map[int]float64) {
		for _, k := range kValues {
			out = append(out, metric{
				name:   fmt.Sprintf(format, k),
				scored: func(s metrics.ScoreSet) float64 { return scored(s)[k] },
				stored: func(e report.Entry) (float64, bool) {
					v, ok := stored(e)[k]
					return v, ok
				},
			})
		}
	}
	atK("NDCG@%d", func(s metrics.ScoreSet) map[int]float64 { return s.NDCG }, func(e report.Entry) map[int]float64 { return e.NDCG })
	atK("NDCG@%d (gain rel)", func(s metrics.ScoreSet) map[int]float64 { return s.NDCGLinear }, func(e report.Entry) map[int]float64 { return e.NDCGLinear })
	atK("P@%d", func(s metrics.ScoreSet) map[int]float64 { return s.Precision }, func(e report.Entry) map[int]float64 { return e.Precision })
	atK("R@%d", func(s metrics.ScoreSet) map[int]float64 { return s.Recall }, func(e report.Entry) map[int]float64 { return e.Recall })
	atK("F1@%d", func(s metrics.ScoreSet) map[int]float64 { return s.F1 }, func(e report.Entry) map[int]float64 { return e.F1 })
	whole := func(name string, scored func(metrics.ScoreSet) float64, stored func(report.Entry) float64) {
		out = append(out, metric{name: name, scored: scored, stored: func(e report.Entry) (float64, bool) { return stored(e), true }})
	}
	whole("MAP", func(s metrics.ScoreSet) float64 { return s.AP }, func(e report.Entry) float64 { return e.AP })
	whole("MRR", func(s metrics.ScoreSet) float64 { return s.RR }, func(e report.Entry) float64 { return e.RR })
	whole("Bpref", func(s metrics.ScoreSet) float64 { return s.Bpref }, func(e report.Entry) float64 { return e.Bpref })
	return out
}

func (o RescoreOptions) irreproducible(m metric, own rescoredSet) string {
	for j, job := range o.Report.Jobs {
		for _, e := range job.PerQuery {
			if !e.Judged || e.Error != "" {
				continue
			}
			where := fmt.Sprintf("%s/%s/%s", job.JobName, e.QueryID, e.EngineName)
			stored, ok := m.stored(e)
			if !ok {
				return fmt.Sprintf("the report stores no %s for %s", m.name, where)
			}
			if scored := m.scored(own.scores[j][cell{queryID: e.QueryID, engine: e.EngineName}]); scored != stored {
				return fmt.Sprintf("under %s, %s scores %v where the report stores %v", own.label, where, scored, stored)
			}
		}
	}
	return ""
}

func engines(job report.JobReport) []string {
	out := make([]string, len(job.Aggregated))
	for i, agg := range job.Aggregated {
		out[i] = agg.EngineName
	}
	return out
}

func judgedQueries(job report.JobReport) []string {
	var out []string
	for _, e := range job.PerQuery {
		if e.Judged && !slices.Contains(out, e.QueryID) {
			out = append(out, e.QueryID)
		}
	}
	return out
}

// meanPrecision rounds means so sums that differ only in float noise tie.
const meanPrecision = 1e12

// jobGrid is a job's engines and the judged queries its means average over.
type jobGrid struct {
	engines, queries []string
}

// engineMeans scores a failed or missing cell 0, as the report's means do.
func (m metric) engineMeans(grid jobGrid, scores map[cell]metrics.ScoreSet) []float64 {
	means := make([]float64, len(grid.engines))
	for i, engine := range grid.engines {
		var sum float64
		for _, queryID := range grid.queries {
			if s, ok := scores[cell{queryID: queryID, engine: engine}]; ok {
				sum += m.scored(s)
			}
		}
		means[i] = math.Round(sum/float64(len(grid.queries))*meanPrecision) / meanPrecision
	}
	return means
}

func bestFirst(engines []string, means []float64) []string {
	order := make([]int, len(engines))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int {
		return cmp.Or(cmp.Compare(means[b], means[a]), cmp.Compare(engines[a], engines[b]))
	})
	out := make([]string, len(order))
	for i, e := range order {
		out[i] = engines[e]
	}
	return out
}
