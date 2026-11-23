package report

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/tadjov/sengi/pkg/irmetrics"
)

// jobView is one job's tables, built once for every renderer. Each renderer
// picks the tables it shows and styles the marks its own way.
type jobView struct {
	Name string
	// Judged is false for a job with no relevance judgments, whose quality
	// tables hold nothing to read.
	Judged       bool
	Metrics      tableView
	ByCategory   []tableView
	Latency      tableView
	ServerTime   tableView
	Significance tableView
	PerQuery     tableView
}

type tableView struct {
	Title   string
	Note    string
	Columns []column
	Rows    [][]cell
}

type column struct {
	Title string
	Align alignment
	Group columnGroup
}

type alignment int

const (
	alignLeft alignment = iota
	alignRightInTerminal
	alignRight
)

type columnGroup int

const (
	ungrouped columnGroup = iota
	ndcgColumns
	precisionColumns
)

type cell struct {
	Text string
	Mark mark
	// Spread is the sample stddev behind a mean, empty when there is none.
	Spread string
}

type mark int

const (
	unmarked mark = iota
	markBest
	markAbsent
	markSignificant
	markWeaklySignificant
	markNotSignificant
	markSucceeded
	markFailed
)

func (t tableView) heading() string {
	if t.Note == "" {
		return t.Title
	}
	return fmt.Sprintf("%s (%s)", t.Title, t.Note)
}

func (t tableView) hasRows() bool { return len(t.Rows) > 0 }

func (j jobView) qualityTables() []tableView {
	return append([]tableView{j.Metrics}, j.ByCategory...)
}

// timingTables leaves server time out when no engine reports one.
func (j jobView) timingTables() []tableView {
	if !j.ServerTime.hasRows() {
		return []tableView{j.Latency}
	}
	return []tableView{j.Latency, j.ServerTime}
}

func jobViews(r *Report) []jobView {
	views := make([]jobView, 0, len(r.Jobs))
	for _, jr := range r.Jobs {
		views = append(views, newJobView(&jr, r.Config.KValues))
	}
	return views
}

func newJobView(jr *JobReport, kValues []int) jobView {
	view := jobView{
		Name:         jr.JobName,
		Judged:       hasAnyJudgments(jr),
		Metrics:      metricsTable("Aggregated Results", jr.Aggregated, kValues),
		Latency:      latencyTable(jr.Aggregated),
		ServerTime:   serverTimeTable(jr.Aggregated),
		Significance: significanceTable(jr.Significance),
		PerQuery:     perQueryTable(jr.PerQuery, kValues),
	}
	for _, cr := range jr.ByCategory {
		title := fmt.Sprintf("Category: %s (%d queries)", cr.Category, cr.QueryCount)
		view.ByCategory = append(view.ByCategory, metricsTable(title, cr.Aggregated, kValues))
	}
	return view
}

func hasAnyJudgments(jr *JobReport) bool {
	for _, agg := range jr.Aggregated {
		if agg.JudgedCount > 0 {
			return true
		}
	}
	for _, e := range jr.PerQuery {
		if e.Judged {
			return true
		}
	}
	return false
}

// meanScore is one averaged metric column. value reports false for an engine
// that never recorded the metric, which then cannot be the column's best.
type meanScore struct {
	column column
	value  func(AggregatedEntry) (float64, bool)
	spread func(AggregatedEntry) float64
}

func meanScores(kValues []int) []meanScore {
	recorded := func(read func(AggregatedEntry) float64) func(AggregatedEntry) (float64, bool) {
		return func(agg AggregatedEntry) (float64, bool) { return read(agg), true }
	}
	scores := make([]meanScore, 0, 3*len(kValues)+3)
	for _, k := range kValues {
		scores = append(scores, meanScore{
			column: column{Title: ndcgHeader(k, NDCGGain), Align: alignRightInTerminal, Group: ndcgColumns},
			value:  recorded(func(agg AggregatedEntry) float64 { return agg.NDCG[k] }),
			spread: func(agg AggregatedEntry) float64 { return agg.NDCGStddev[k] },
		})
	}
	for _, k := range kValues {
		scores = append(scores, meanScore{
			column: column{Title: ndcgHeader(k, LinearNDCGGain), Align: alignRightInTerminal},
			value: func(agg AggregatedEntry) (float64, bool) {
				v, ok := agg.NDCGLinear[k]
				return v, ok
			},
		})
	}
	for _, k := range kValues {
		scores = append(scores, meanScore{
			column: column{Title: fmt.Sprintf("P@%d", k), Align: alignRightInTerminal, Group: precisionColumns},
			value:  recorded(func(agg AggregatedEntry) float64 { return agg.Precision[k] }),
		})
	}
	return append(scores,
		meanScore{
			column: column{Title: "MAP", Align: alignRightInTerminal},
			value:  recorded(func(agg AggregatedEntry) float64 { return agg.MAP }),
		},
		meanScore{
			column: column{Title: "MRR", Align: alignRightInTerminal},
			value:  recorded(func(agg AggregatedEntry) float64 { return agg.MRR }),
		},
		meanScore{
			column: column{Title: "Bpref", Align: alignRightInTerminal},
			value:  recorded(func(agg AggregatedEntry) float64 { return agg.MBpref }),
		},
	)
}

// best is the highest value among the engines with judged queries; ties share it.
func (s meanScore) best(aggregated []AggregatedEntry) float64 {
	best := math.Inf(-1)
	for _, agg := range aggregated {
		if agg.JudgedCount == 0 {
			continue
		}
		if v, ok := s.value(agg); ok && v > best {
			best = v
		}
	}
	return best
}

func (s meanScore) cell(agg AggregatedEntry, best float64) cell {
	v, _ := s.value(agg)
	c := cell{Text: fmt.Sprintf("%.4f", v)}
	if v == best {
		c.Mark = markBest
	}
	if s.spread != nil {
		if sd := s.spread(agg); sd > 0 {
			c.Spread = fmt.Sprintf("%.4f", sd)
		}
	}
	return c
}

func metricsTable(title string, aggregated []AggregatedEntry, kValues []int) tableView {
	scores := meanScores(kValues)
	t := tableView{Title: title, Columns: []column{{Title: "Engine"}}}
	best := make([]float64, len(scores))
	for i, score := range scores {
		t.Columns = append(t.Columns, score.column)
		best[i] = score.best(aggregated)
	}
	t.Columns = append(t.Columns,
		column{Title: fmt.Sprintf("Coverage@%d", largestK(kValues)), Align: alignRightInTerminal},
		column{Title: "Judged"},
		column{Title: "Errors"},
	)

	for _, agg := range aggregated {
		row := []cell{{Text: agg.EngineName}}
		if agg.JudgedCount > 0 {
			for i, score := range scores {
				row = append(row, score.cell(agg, best[i]))
			}
			row = append(row, cell{Text: fmtRecorded(agg.JudgedCoverage)})
		} else {
			for range len(scores) + 1 {
				row = append(row, cell{Text: "N/A", Mark: markAbsent})
			}
		}
		row = append(row,
			cell{Text: fmt.Sprintf("%d/%d", agg.JudgedCount, agg.QueryCount)},
			cell{Text: fmt.Sprintf("%d/%d", agg.ErrorCount, agg.QueryCount)},
		)
		t.Rows = append(t.Rows, row)
	}
	return t
}

func latencyTable(aggregated []AggregatedEntry) tableView {
	t := tableView{Title: "Latency Statistics", Columns: []column{{Title: "Engine"}}}
	for _, title := range []string{"Min", "p50", "p75", "p90", "p95", "p99", "Max", "Mean", "Stddev"} {
		t.Columns = append(t.Columns, column{Title: title, Align: alignRightInTerminal})
	}
	t.Columns = append(t.Columns, column{Title: "Samples", Align: alignRight}, column{Title: "Excluded", Align: alignRight})

	fastest := time.Duration(math.MaxInt64)
	for _, agg := range aggregated {
		if p50 := agg.Latency.P50(); p50 > 0 && p50 < fastest {
			fastest = p50
		}
	}
	for _, agg := range aggregated {
		s := agg.Latency
		p50 := cell{Text: FormatDuration(s.P50())}
		if s.P50() == fastest {
			p50.Mark = markBest
		}
		t.Rows = append(t.Rows, []cell{
			{Text: agg.EngineName},
			{Text: FormatDuration(s.Min)},
			p50,
			{Text: FormatDuration(s.P75())},
			{Text: FormatDuration(s.P90())},
			{Text: FormatDuration(s.P95())},
			{Text: FormatDuration(s.P99())},
			{Text: FormatDuration(s.Max)},
			{Text: FormatDuration(s.Mean)},
			{Text: FormatDuration(s.Stddev)},
			{Text: strconv.Itoa(s.SampleCount)},
			{Text: strconv.Itoa(agg.LatencyExcludedQueries)},
		})
	}
	return t
}

// serverTimeTable lists only the engines that report a server time.
func serverTimeTable(aggregated []AggregatedEntry) tableView {
	t := tableView{
		Title: "Server Time (reported by the engine)",
		Columns: []column{
			{Title: "Engine"},
			{Title: "p50", Align: alignRightInTerminal},
			{Title: "p95", Align: alignRightInTerminal},
			{Title: "p99", Align: alignRightInTerminal},
			{Title: "Mean", Align: alignRightInTerminal},
			{Title: "Samples", Align: alignRight},
		},
	}
	for _, agg := range aggregated {
		s := agg.ServerLatency
		if s == nil {
			continue
		}
		t.Rows = append(t.Rows, []cell{
			{Text: agg.EngineName},
			{Text: fmtServerTime(s.P50())},
			{Text: fmtServerTime(s.P95())},
			{Text: fmtServerTime(s.P99())},
			{Text: fmtServerTime(s.Mean)},
			{Text: strconv.Itoa(s.SampleCount)},
		})
	}
	return t
}

// fmtServerTime shows a reported 0 instead of the dash FormatDuration uses for absent.
func fmtServerTime(d time.Duration) string {
	if d == 0 {
		return "0.00ms"
	}
	return FormatDuration(d)
}

const starsLegend = "* p<0.05 · ** p<0.01"

func significanceTable(pairs []PairwiseSignificance) tableView {
	t := tableView{
		Title: "Statistical Significance",
		Note:  "Wilcoxon signed-rank, two-tailed, Holm-adjusted across engine pairs",
		Columns: []column{
			{Title: "Engine A"},
			{Title: "Engine B"},
			{Title: "Metric"},
			{Title: "Δ A−B [95% CI]", Align: alignRightInTerminal},
			{Title: "W", Align: alignRightInTerminal},
			{Title: "p-value", Align: alignRightInTerminal},
			{Title: "p (Holm)", Align: alignRightInTerminal},
			{Title: "Sig"},
		},
	}
	for _, s := range pairs {
		w, p, adjusted := "—", "—", "—"
		if !s.Untestable {
			w, p, adjusted = fmt.Sprintf("%.1f", s.W), fmt.Sprintf("%.4f", s.P), fmtRecorded(s.AdjustedP)
		}
		t.Rows = append(t.Rows, []cell{
			{Text: s.EngineA},
			{Text: s.EngineB},
			{Text: s.Metric},
			{Text: fmtMeanDifference(s.MeanDifference)},
			{Text: w},
			{Text: p},
			{Text: adjusted},
			verdict(s),
		})
	}
	return t
}

// verdict reads untestable for a pair with fewer than 4 non-zero differences,
// which Wilcoxon cannot test.
func verdict(s PairwiseSignificance) cell {
	switch {
	case s.Untestable:
		return cell{Text: "untestable", Mark: markNotSignificant}
	case s.Stars == "**":
		return cell{Text: s.Stars, Mark: markSignificant}
	case s.Stars == "*":
		return cell{Text: s.Stars, Mark: markWeaklySignificant}
	default:
		return cell{Text: "ns", Mark: markNotSignificant}
	}
}

func fmtMeanDifference(d *irmetrics.Interval) string {
	if d == nil {
		return "—"
	}
	return fmt.Sprintf("%+.4f [%+.4f, %+.4f]", d.Mean, d.Low, d.High)
}

// perQueryTable scores each query at the largest K.
func perQueryTable(entries []Entry, kValues []int) tableView {
	k := largestK(kValues)
	t := tableView{
		Title: "Per-Query Results",
		Columns: []column{
			{Title: "Query"},
			{Title: "Engine"},
			{Title: ndcgHeader(k, NDCGGain), Align: alignRightInTerminal},
			{Title: fmt.Sprintf("P@%d", k), Align: alignRightInTerminal},
			{Title: "AP", Align: alignRightInTerminal},
			{Title: "RR", Align: alignRightInTerminal},
			{Title: "Bpref", Align: alignRightInTerminal},
			{Title: "Ret", Align: alignRight},
			{Title: "Corpus", Align: alignRightInTerminal},
			{Title: "p50", Align: alignRightInTerminal},
			{Title: "p95"},
			{Title: "Status"},
		},
	}
	absent := cell{Text: "—", Mark: markAbsent}
	for _, e := range entries {
		ap, rr, bpref := absent, absent, absent
		if e.Judged {
			ap = cell{Text: fmt.Sprintf("%.4f", e.AP)}
			rr = cell{Text: fmt.Sprintf("%.4f", e.RR)}
			bpref = cell{Text: fmt.Sprintf("%.4f", e.Bpref)}
		}
		corpusMatches := absent
		if e.CorpusMatches != nil {
			corpusMatches = cell{Text: strconv.FormatInt(*e.CorpusMatches, 10)}
		}
		status := cell{Text: "OK", Mark: markSucceeded}
		if e.Error != "" {
			status = cell{Text: "ERR", Mark: markFailed}
		}
		t.Rows = append(t.Rows, []cell{
			{Text: e.QueryID},
			{Text: e.EngineName},
			{Text: fmtScore(e.NDCG, k)},
			{Text: fmtScore(e.Precision, k)},
			ap, rr, bpref,
			{Text: strconv.Itoa(e.ReturnedCount)},
			corpusMatches,
			{Text: FormatDuration(e.Latency.P50())},
			{Text: FormatDuration(e.Latency.P95())},
			status,
		})
	}
	return t
}

// Every NDCG header names its gain, because the two can order engines
// differently. NDCGGain is the bench's own; LinearNDCGGain is trec_eval's
// ndcg_cut, which ignores the relevance threshold.
const (
	NDCGGain       = "2^rel-1"
	LinearNDCGGain = "rel"
)

func ndcgHeader(k int, gain string) string {
	return fmt.Sprintf("NDCG@%d (gain %s)", k, gain)
}

func largestK(kValues []int) int {
	if len(kValues) == 0 {
		return 0
	}
	return slices.Max(kValues)
}

func fmtScore(scores map[int]float64, k int) string {
	if scores == nil {
		return "N/A"
	}
	return fmt.Sprintf("%.4f", scores[k])
}

// fmtRecorded renders a metric that was not recorded as "—", not 0.
func fmtRecorded(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.4f", *v)
}

func FormatDuration(d time.Duration) string {
	if d == 0 {
		return "-"
	}
	if d < time.Millisecond {
		return fmt.Sprintf("%.1fµs", float64(d.Microseconds()))
	}
	if d < time.Second {
		return fmt.Sprintf("%.2fms", float64(d.Microseconds())/1000)
	}
	return fmt.Sprintf("%.2fs", d.Seconds())
}
