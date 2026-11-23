package report

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fatih/color"
	"github.com/tadjov/sengi/internal/bench/runner"
	"github.com/tadjov/sengi/pkg/irmetrics"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func makeJudgedScores(ndcg10 float64) irmetrics.ScoreSet {
	return irmetrics.ScoreSet{
		Judged:    true,
		NDCG:      map[int]float64{10: ndcg10},
		Precision: map[int]float64{10: 0.3},
		Recall:    map[int]float64{10: 0.4},
		F1:        map[int]float64{10: 0.34},
		AP:        ndcg10 * 0.9,
		RR:        1.0,
		Bpref:     ndcg10 * 0.8,
	}
}

func makeBenchmarkResult(engines []string, queryIDs []string, scores map[string]map[string]irmetrics.ScoreSet) *runner.BenchmarkResult {
	jr := &runner.JobResult{
		JobName:     "test_job",
		EngineNames: engines,
		QueryOrder:  queryIDs,
		Results:     make(map[string]map[string]runner.QueryResult),
	}
	for _, qid := range queryIDs {
		jr.Results[qid] = make(map[string]runner.QueryResult)
		for _, eng := range engines {
			sc := irmetrics.ScoreSet{}
			if scores != nil {
				if qScores, ok := scores[qid]; ok {
					sc = qScores[eng]
				}
			}
			jr.Results[qid][eng] = runner.QueryResult{
				QueryID:    qid,
				EngineName: eng,
				Scores:     sc,
				Latency: runner.ComputeLatencyStats([]time.Duration{
					10 * time.Millisecond,
					20 * time.Millisecond,
					50 * time.Millisecond,
				}),
			}
		}
	}
	return &runner.BenchmarkResult{
		Jobs: []*runner.JobResult{jr},
		Config: runner.Config{
			KValues:            []int{10},
			RelevanceThreshold: 1,
		},
	}
}

// ─── Generate ─────────────────────────────────────────────────────────────────

func TestGenerate_SchemaVersion(t *testing.T) {
	br := makeBenchmarkResult([]string{"eng-a"}, []string{"q1"}, nil)
	r := Generate(br, nil)
	if r.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", r.SchemaVersion)
	}
}

func TestGenerate_ProvenanceRunID(t *testing.T) {
	br := makeBenchmarkResult([]string{"eng-a"}, []string{"q1"}, nil)
	r := Generate(br, nil)
	if r.Provenance.RunID == "" {
		t.Error("Provenance.RunID should not be empty")
	}
}

func TestGenerate_JobsCount(t *testing.T) {
	br := makeBenchmarkResult([]string{"eng-a", "eng-b"}, []string{"q1", "q2"}, nil)
	r := Generate(br, nil)
	if got := len(r.Jobs); got != 1 {
		t.Errorf("len(Jobs) = %d, want 1", got)
	}
}

func TestGenerate_AggregatedMetrics(t *testing.T) {
	engines := []string{"pg", "es"}
	queries := []string{"q1", "q2", "q3"}
	scores := map[string]map[string]irmetrics.ScoreSet{
		"q1": {"pg": makeJudgedScores(0.8), "es": makeJudgedScores(0.6)},
		"q2": {"pg": makeJudgedScores(0.7), "es": makeJudgedScores(0.65)},
		"q3": {"pg": makeJudgedScores(0.9), "es": makeJudgedScores(0.7)},
	}
	br := makeBenchmarkResult(engines, queries, scores)
	r := Generate(br, nil)

	aggByEng := make(map[string]AggregatedEntry)
	for _, a := range r.Jobs[0].Aggregated {
		aggByEng[a.EngineName] = a
	}

	pgNDCG := aggByEng["pg"].NDCG[10]
	want := (0.8 + 0.7 + 0.9) / 3
	if diff := pgNDCG - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("pg NDCG@10 = %.6f, want %.6f", pgNDCG, want)
	}

	esNDCG := aggByEng["es"].NDCG[10]
	wantES := (0.6 + 0.65 + 0.7) / 3
	if diff := esNDCG - wantES; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("es NDCG@10 = %.6f, want %.6f", esNDCG, wantES)
	}

	if aggByEng["pg"].JudgedCount != 3 {
		t.Errorf("pg JudgedCount = %d, want 3", aggByEng["pg"].JudgedCount)
	}
}

func TestGenerate_LatencyPopulated(t *testing.T) {
	br := makeBenchmarkResult([]string{"eng"}, []string{"q1"}, nil)
	r := Generate(br, nil)
	agg := r.Jobs[0].Aggregated[0]
	if agg.Latency.SampleCount == 0 {
		t.Error("Latency.SampleCount should be > 0")
	}
	if agg.Latency.Mean == 0 {
		t.Error("Latency.Mean should be > 0")
	}
}

// ─── PerQuery entries ─────────────────────────────────────────────────────────

func TestGenerate_PerQueryEntries(t *testing.T) {
	engines := []string{"eng-a", "eng-b"}
	queries := []string{"q1", "q2"}
	br := makeBenchmarkResult(engines, queries, nil)
	r := Generate(br, nil)

	wantEntries := len(engines) * len(queries)
	if got := len(r.Jobs[0].PerQuery); got != wantEntries {
		t.Errorf("len(PerQuery) = %d, want %d", got, wantEntries)
	}
}

// ─── JSON round-trip ──────────────────────────────────────────────────────────

func TestWriteReadJSON_RoundTrip(t *testing.T) {
	br := makeBenchmarkResult([]string{"pg", "es"}, []string{"q1", "q2"}, map[string]map[string]irmetrics.ScoreSet{
		"q1": {"pg": makeJudgedScores(0.7), "es": makeJudgedScores(0.5)},
		"q2": {"pg": makeJudgedScores(0.6), "es": makeJudgedScores(0.55)},
	})
	orig := Generate(br, nil)
	orig.Provenance.SpecID = "round_trip_test"

	tmp := t.TempDir()
	path := filepath.Join(tmp, "report.json")

	if err := WriteJSON(orig, path); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	got, err := ReadJSON(path)
	if err != nil {
		t.Fatalf("ReadJSON: %v", err)
	}

	if got.SchemaVersion != orig.SchemaVersion {
		t.Errorf("SchemaVersion: got %d want %d", got.SchemaVersion, orig.SchemaVersion)
	}
	if got.Provenance.RunID != orig.Provenance.RunID {
		t.Errorf("RunID: got %q want %q", got.Provenance.RunID, orig.Provenance.RunID)
	}
	if got.Provenance.SpecID != "round_trip_test" {
		t.Errorf("SpecID: got %q want round_trip_test", got.Provenance.SpecID)
	}
	if len(got.Jobs) != len(orig.Jobs) {
		t.Fatalf("len(Jobs): got %d want %d", len(got.Jobs), len(orig.Jobs))
	}
}

func TestWriteJSON_IsValidJSON(t *testing.T) {
	br := makeBenchmarkResult([]string{"eng"}, []string{"q1"}, nil)
	r := Generate(br, nil)

	tmp := t.TempDir()
	path := filepath.Join(tmp, "r.json")
	if err := WriteJSON(r, path); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	data, _ := os.ReadFile(path)
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if _, ok := m["schema_version"]; !ok {
		t.Error("JSON missing 'schema_version' key")
	}
	if _, ok := m["provenance"]; !ok {
		t.Error("JSON missing 'provenance' key")
	}
}

func TestReadLatestReport(t *testing.T) {
	br := makeBenchmarkResult([]string{"eng"}, []string{"q1"}, nil)
	r := Generate(br, nil)
	r.Provenance.RunID = "2026-05-28T00-00-00-run-test01"

	tmp := t.TempDir()
	reportPath := filepath.Join(tmp, r.Provenance.RunID+".json")
	latestPath := filepath.Join(tmp, "latest.json")

	if err := WriteJSON(r, reportPath); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	latestData := []byte(`{"latest": "` + r.Provenance.RunID + `.json"}`)
	if err := os.WriteFile(latestPath, latestData, 0644); err != nil {
		t.Fatalf("write latest.json: %v", err)
	}

	got, err := ReadLatestReport(latestPath)
	if err != nil {
		t.Fatalf("ReadLatestReport: %v", err)
	}
	if got.Provenance.RunID != r.Provenance.RunID {
		t.Errorf("RunID = %q, want %q", got.Provenance.RunID, r.Provenance.RunID)
	}
}

func TestWriteTable_ContainsEngineNames(t *testing.T) {
	br := makeBenchmarkResult([]string{"postgres", "elastic"}, []string{"q1"}, map[string]map[string]irmetrics.ScoreSet{
		"q1": {"postgres": makeJudgedScores(0.9), "elastic": makeJudgedScores(0.7)},
	})
	r := Generate(br, nil)

	var buf bytes.Buffer
	WriteTable(r, &buf)

	out := buf.String()
	for _, eng := range []string{"postgres", "elastic"} {
		if !strings.Contains(out, eng) {
			t.Errorf("table output missing engine name %q", eng)
		}
	}
}

func TestWriteMarkdown_HasGFMTables(t *testing.T) {
	br := makeBenchmarkResult([]string{"eng-a"}, []string{"q1"}, map[string]map[string]irmetrics.ScoreSet{
		"q1": {"eng-a": makeJudgedScores(0.75)},
	})
	r := Generate(br, nil)
	r.Provenance.SpecID = "md_test"

	var buf bytes.Buffer
	WriteMarkdown(r, &buf)

	out := buf.String()
	if !strings.Contains(out, "| Engine |") {
		t.Error("WriteMarkdown output missing GFM table header '| Engine |'")
	}
	if !strings.Contains(out, "# md_test") {
		t.Error("WriteMarkdown output missing H1 title")
	}
}

// ─── Significance ────────────────────────────────────────────────────────────

func TestGenerate_SignificancePopulated_EnoughData(t *testing.T) {
	// Need ≥4 non-tied pairs for Wilcoxon to produce a result.
	engines := []string{"eng-a", "eng-b"}
	nQueries := 10
	queries := make([]string, nQueries)
	scores := make(map[string]map[string]irmetrics.ScoreSet, nQueries)
	for i := 0; i < nQueries; i++ {
		qid := "q" + string(rune('0'+i))
		queries[i] = qid
		scoreA := 0.5 + float64(i)*0.03
		scoreB := 0.4 + float64(i)*0.02
		scores[qid] = map[string]irmetrics.ScoreSet{
			"eng-a": makeJudgedScores(scoreA),
			"eng-b": makeJudgedScores(scoreB),
		}
	}
	br := makeBenchmarkResult(engines, queries, scores)
	r := Generate(br, nil)
	// With 10 distinct pairs we should have at least one significance entry.
	if len(r.Jobs[0].Significance) == 0 {
		t.Error("expected at least one significance entry with 10 judged query pairs")
	}
}

// ─── LatencyStats helpers ─────────────────────────────────────────────────────

func TestLatencyStats_Percentile_Helpers(t *testing.T) {
	ls := LatencyStats{
		Percentiles: map[int]time.Duration{
			50: 20 * time.Millisecond,
			75: 30 * time.Millisecond,
			90: 40 * time.Millisecond,
			95: 45 * time.Millisecond,
			99: 50 * time.Millisecond,
		},
	}
	if got := ls.P50(); got != 20*time.Millisecond {
		t.Errorf("P50 = %v, want 20ms", got)
	}
	if got := ls.P99(); got != 50*time.Millisecond {
		t.Errorf("P99 = %v, want 50ms", got)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "-"},
		{500 * time.Microsecond, "500.0µs"},
		{1500 * time.Microsecond, "1.50ms"},
		{2 * time.Second, "2.00s"},
	}
	for _, c := range cases {
		got := FormatDuration(c.d)
		if got != c.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// ─── connectionEndpoint ───────────────────────────────────────────────────────

func TestConnectionEndpoint_DropsCredentials(t *testing.T) {
	tests := []struct {
		name string
		conn string
		want string
	}{
		{"short postgres url with password", "postgresql://u:secret@db:5432/news", "postgresql://db:5432/news"},
		{"long postgres url with password", "postgresql://news_user:news_password@localhost:54320/news_db", "postgresql://localhost:54320/news_db"},
		{"password in query string", "postgres://localhost:54320/news_db?password=secret&sslmode=disable", "postgres://localhost:54320/news_db"},
		{"elasticsearch url with basic auth", "http://elastic:changeme@localhost:9200", "http://localhost:9200"},
		{"plain elasticsearch url", "http://localhost:9200", "http://localhost:9200"},
		{"key value dsn", "host=localhost port=54320 user=news_user password=secret dbname=news_db", "host=localhost port=54320 dbname=news_db"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := connectionEndpoint(tt.conn); got != tt.want {
				t.Errorf("connectionEndpoint(%q) = %q, want %q", tt.conn, got, tt.want)
			}
		})
	}
}

func TestWriters_JobWithoutJudgmentsShowsTimingOnly(t *testing.T) {
	r := &Report{
		Config: ReportConfig{KValues: []int{10}, RelevanceThreshold: 1},
		Jobs: []JobReport{{
			JobName:    "speed",
			PerQuery:   []Entry{{QueryID: "q1", EngineName: "pg"}},
			Aggregated: []AggregatedEntry{{EngineName: "pg", QueryCount: 1}},
		}},
	}
	var terminal, markdown bytes.Buffer
	WriteTable(r, &terminal)
	WriteMarkdown(r, &markdown)

	for name, out := range map[string]string{"table": terminal.String(), "markdown": markdown.String()} {
		if !strings.Contains(out, "Latency Statistics") {
			t.Errorf("%s has no latency table", name)
		}
		for _, qualityTable := range []string{"Aggregated Results", "Per-Query Results"} {
			if strings.Contains(out, qualityTable) {
				t.Errorf("%s shows %q for a job with no judgments", name, qualityTable)
			}
		}
	}
	if !strings.Contains(terminal.String(), "No relevance judgments found") {
		t.Error("table does not say the job has no judgments")
	}
}

func TestWriters_JobWithOneJudgedQueryShowsQuality(t *testing.T) {
	r := &Report{
		Config: ReportConfig{KValues: []int{10}, RelevanceThreshold: 1},
		Jobs: []JobReport{{
			JobName:    "quality",
			PerQuery:   []Entry{{QueryID: "q1", EngineName: "pg"}, {QueryID: "q2", EngineName: "pg", Judged: true}},
			Aggregated: []AggregatedEntry{{EngineName: "pg", QueryCount: 2}},
		}},
	}
	var terminal, markdown bytes.Buffer
	WriteTable(r, &terminal)
	WriteMarkdown(r, &markdown)

	for name, out := range map[string]string{"table": terminal.String(), "markdown": markdown.String()} {
		if !strings.Contains(out, "Aggregated Results") {
			t.Errorf("%s has no quality table", name)
		}
	}
}

func TestWriteTable_HighlightsTheBestScoreAndFastestMedian(t *testing.T) {
	colorless := color.NoColor
	color.NoColor = false
	t.Cleanup(func() { color.NoColor = colorless })
	r := &Report{
		Config: ReportConfig{KValues: []int{10}, RelevanceThreshold: 1},
		Jobs: []JobReport{{
			JobName:  "best",
			PerQuery: []Entry{{QueryID: "q1", EngineName: "pg", Judged: true}},
			Aggregated: []AggregatedEntry{
				{EngineName: "pg", JudgedCount: 1, QueryCount: 1, NDCG: map[int]float64{10: 0.8}, MAP: 0.4,
					Latency: LatencyStats{Percentiles: map[int]time.Duration{50: 30 * time.Millisecond}}},
				{EngineName: "es", JudgedCount: 1, QueryCount: 1, NDCG: map[int]float64{10: 0.6}, MAP: 0.4,
					Latency: LatencyStats{Percentiles: map[int]time.Duration{50: 20 * time.Millisecond}}},
			},
		}},
	}
	var out bytes.Buffer

	WriteTable(r, &out)

	best := color.New(color.FgGreen, color.Bold)
	for _, highlighted := range []string{"0.8000", "20.00ms"} {
		if !strings.Contains(out.String(), best.Sprint(highlighted)) {
			t.Errorf("%s is the best in its column but is not highlighted", highlighted)
		}
	}
	for _, plain := range []string{"0.6000", "30.00ms"} {
		if strings.Contains(out.String(), best.Sprint(plain)) {
			t.Errorf("%s is highlighted without being the best in its column", plain)
		}
	}
	if got := strings.Count(out.String(), best.Sprint("0.4000")); got != 2 {
		t.Errorf("tied MAP highlighted %d times, want both engines", got)
	}
}

// ─── Per-category aggregation ────────────────────────────────────────────────

func withCategories(br *runner.BenchmarkResult, byQuery map[string]string) *runner.BenchmarkResult {
	jr := br.Jobs[0]
	for qid, results := range jr.Results {
		for eng, qr := range results {
			qr.Category = byQuery[qid]
			results[eng] = qr
		}
	}
	return br
}

func TestGenerate_ByCategorySplitsQueries(t *testing.T) {
	engines := []string{"pg"}
	queries := []string{"kw1", "kw2", "ph1"}
	scores := map[string]map[string]irmetrics.ScoreSet{
		"kw1": {"pg": makeJudgedScores(0.9)},
		"kw2": {"pg": makeJudgedScores(0.7)},
		"ph1": {"pg": makeJudgedScores(0.2)},
	}
	br := withCategories(makeBenchmarkResult(engines, queries, scores), map[string]string{
		"kw1": "keyword", "kw2": "keyword", "ph1": "phrase",
	})
	r := Generate(br, nil)

	byCat := make(map[string]CategoryReport)
	for _, cr := range r.Jobs[0].ByCategory {
		byCat[cr.Category] = cr
	}
	if len(byCat) != 2 {
		t.Fatalf("got %d categories, want 2", len(byCat))
	}
	if got := byCat["keyword"].QueryCount; got != 2 {
		t.Errorf("keyword QueryCount = %d, want 2", got)
	}

	kw := byCat["keyword"].Aggregated[0].NDCG[10]
	if diff := kw - 0.8; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("keyword NDCG@10 = %.6f, want 0.8", kw)
	}
	ph := byCat["phrase"].Aggregated[0].NDCG[10]
	if diff := ph - 0.2; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("phrase NDCG@10 = %.6f, want 0.2", ph)
	}

	overall := r.Jobs[0].Aggregated[0].NDCG[10]
	want := (0.9 + 0.7 + 0.2) / 3
	if diff := overall - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("overall NDCG@10 = %.6f, want %.6f", overall, want)
	}
}

func TestGenerate_ByCategoryEmptyWhenUntagged(t *testing.T) {
	br := makeBenchmarkResult([]string{"pg"}, []string{"q1", "q2"}, nil)
	r := Generate(br, nil)
	if got := len(r.Jobs[0].ByCategory); got != 0 {
		t.Errorf("len(ByCategory) = %d, want 0 for an untagged suite", got)
	}
}

func TestGenerate_ByCategoryEmptyForSingleCategory(t *testing.T) {
	br := withCategories(makeBenchmarkResult([]string{"pg"}, []string{"q1", "q2"}, nil),
		map[string]string{"q1": "keyword", "q2": "keyword"})
	r := Generate(br, nil)
	if got := len(r.Jobs[0].ByCategory); got != 0 {
		t.Errorf("len(ByCategory) = %d, want 0 when every query shares one category", got)
	}
}

func TestWriteJSON_CreatesMissingReportsDirectory(t *testing.T) {
	br := makeBenchmarkResult([]string{"pg"}, []string{"q1"}, nil)
	path := filepath.Join(t.TempDir(), "reports", "2026-05-28T00-00-00-run-test01.json")

	if err := WriteJSON(Generate(br, nil), path); err != nil {
		t.Fatalf("WriteJSON into a track with no reports/ directory: %v", err)
	}
	if _, err := ReadJSON(path); err != nil {
		t.Fatalf("ReadJSON: %v", err)
	}
}

func TestReadJSON_RejectsReportWithoutSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, []byte(`{"provenance":{"spec_id":"x"}}`), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if _, err := ReadJSON(path); err == nil {
		t.Fatal("ReadJSON accepted a report with no schema_version")
	}
}
