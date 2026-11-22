package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/bench/judgment"
	"github.com/tadjov/sengi/internal/bench/meta"
	"github.com/tadjov/sengi/internal/bench/pool"
	"github.com/tadjov/sengi/internal/bench/report"
	"github.com/tadjov/sengi/internal/bench/suite"
	"github.com/tadjov/sengi/internal/storage"
)

type diffRun struct {
	runID     string
	kValues   []int
	ndcg      map[int]float64
	threshold int
	judge     *meta.Judge
	judgments string
	spec      string
}

func diffReport(run diffRun) *report.Report {
	return &report.Report{
		SchemaVersion: 1,
		Provenance: meta.Meta{
			RunID:   run.runID,
			Judge:   run.judge,
			Sources: &meta.Sources{Judgments: run.judgments, Spec: run.spec},
		},
		Config: report.ReportConfig{KValues: run.kValues, RelevanceThreshold: run.threshold},
		Jobs: []report.JobReport{{
			JobName:    "fts",
			Aggregated: []report.AggregatedEntry{{EngineName: "pg", NDCG: run.ndcg, MAP: 0.5, JudgedCount: 1, QueryCount: 1}},
			PerQuery:   []report.Entry{{QueryID: "q1", EngineName: "pg", Judged: true, NDCG: run.ndcg}},
		}},
	}
}

func TestPrintDiff_ComparesOnlyKValuesBothRunsHave(t *testing.T) {
	before := diffReport(diffRun{runID: "a", kValues: []int{3, 5, 10}, ndcg: map[int]float64{3: 0.4, 5: 0.5, 10: 0.6}, threshold: 1})
	after := diffReport(diffRun{runID: "b", kValues: []int{3, 5, 20}, ndcg: map[int]float64{3: 0.4, 5: 0.55, 20: 0.7}, threshold: 1})

	var out bytes.Buffer
	printDiff(&out, before, after)
	got := out.String()

	assert.Contains(t, got, "NDCG@5")
	assert.Contains(t, got, "0.5000→0.5500")
	assert.NotContains(t, got, "NDCG@10")
	assert.NotContains(t, got, "NDCG@20")
	assert.NotContains(t, got, "0.6000→0.0000", "a K missing from one run must not read as a zero score")
}

func TestPrintDiff_SkipsNDCGWhenNoKValueIsShared(t *testing.T) {
	before := diffReport(diffRun{runID: "a", kValues: []int{10}, ndcg: map[int]float64{10: 0.6}, threshold: 1})
	after := diffReport(diffRun{runID: "b", kValues: []int{20}, ndcg: map[int]float64{20: 0.7}, threshold: 1})

	var out bytes.Buffer
	printDiff(&out, before, after)

	assert.NotContains(t, out.String(), "-100.0%")
	assert.NotContains(t, out.String(), "Per-Query NDCG")
}

func TestPrintDiff_WarnsWhenTheRunsWereScoredDifferently(t *testing.T) {
	keyword := &meta.Judge{Strategy: "keyword"}
	llm := &meta.Judge{Strategy: "llm-cli", Provider: "claude", Model: "claude-haiku-4-5"}
	base := diffRun{runID: "a", kValues: []int{10}, ndcg: map[int]float64{10: 0.6}, threshold: 1, judge: keyword, judgments: "trec/annotations.keyword.yaml"}

	tests := []struct {
		name    string
		after   func(diffRun) diffRun
		warning string
	}{
		{name: "judge", after: func(r diffRun) diffRun { r.judge = llm; return r }, warning: "judge"},
		{name: "judge prompt version", after: func(r diffRun) diffRun {
			r.judge = &meta.Judge{Strategy: "keyword", PromptVersion: "v2"}
			return r
		}, warning: "judge"},
		{name: "relevance threshold", after: func(r diffRun) diffRun { r.threshold = 2; return r }, warning: "relevance threshold"},
		{name: "judgments source", after: func(r diffRun) diffRun { r.judgments = "trec/annotations.llm-cli.yaml"; return r }, warning: "judgments"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			after := tc.after(base)
			after.runID = "b"

			var out bytes.Buffer
			printDiff(&out, diffReport(base), diffReport(after))
			got := out.String()

			warningAt := strings.Index(got, "WARNING")
			require.NotEqual(t, -1, warningAt, got)
			assert.Less(t, warningAt, strings.Index(got, "--- Job:"))
			assert.Contains(t, got[warningAt:strings.Index(got, "--- Job:")], tc.warning)
		})
	}
}

func TestPrintDiff_NoScoringWarningForIdenticallyScoredRuns(t *testing.T) {
	run := diffRun{runID: "a", kValues: []int{10}, ndcg: map[int]float64{10: 0.6}, threshold: 1, judge: &meta.Judge{Strategy: "keyword"}, judgments: "trec/annotations.keyword.yaml"}
	after := run
	after.runID = "b"

	var out bytes.Buffer
	printDiff(&out, diffReport(run), diffReport(after))

	assert.NotContains(t, out.String(), "WARNING")
}

func writeTrackReport(t *testing.T, dir, runID string, mutate func(*report.Report)) {
	t.Helper()
	r := diffReport(diffRun{runID: runID, kValues: []int{10}, ndcg: map[int]float64{10: 0.6}, threshold: 1})
	if mutate != nil {
		mutate(r)
	}
	require.NoError(t, report.WriteJSON(r, filepath.Join(dir, runID+".json")))
}

func TestExecuteDiff_DefaultsToLatestAndThePreviousFullyScoredRun(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	initCmd := newInitCmd()
	initCmd.SetOut(&bytes.Buffer{})
	initCmd.SetArgs([]string{"demo"})
	require.NoError(t, initCmd.Execute())
	reports := filepath.Join(cwd, "demo", "reports")

	latencyOnly := func(r *report.Report) {
		r.Jobs[0].Aggregated[0].JudgedCount = 0
		r.Jobs[0].PerQuery[0].Judged = false
	}
	errored := func(r *report.Report) {
		r.Jobs[0].Aggregated[0].ErrorCount = 1
		r.Jobs[0].PerQuery[0].Error = "timeout"
	}
	incomplete := func(r *report.Report) {
		r.Settings.Incomplete = []string{"--jobs selected a subset of the spec's jobs"}
	}
	writeTrackReport(t, reports, "2026-01-01T00-00-01-run-aaaaaa", nil)
	writeTrackReport(t, reports, "2026-01-01T00-00-02-run-bbbbbb", latencyOnly)
	writeTrackReport(t, reports, "2026-01-01T00-00-03-run-cccccc", errored)
	writeTrackReport(t, reports, "2026-01-01T00-00-03-run-ffffff", incomplete)
	writeTrackReport(t, reports, "2026-01-01T00-00-04-run-dddddd", nil)
	writeTrackReport(t, reports, "2026-01-01T00-00-05-run-eeeeee", errored)
	require.NoError(t, report.WriteLatestPointer(filepath.Join(reports, "latest.json"),
		filepath.Join(reports, "2026-01-01T00-00-04-run-dddddd.json")))

	var out bytes.Buffer
	require.NoError(t, executeDiff(&out, diffFlags{}, []string{"demo"}))

	assert.Regexp(t, `A \(before\):\s+2026-01-01T00-00-01-run-aaaaaa`, out.String())
	assert.Regexp(t, `B \(after\):\s+\S*2026-01-01T00-00-04-run-dddddd`, out.String())
}

func TestPrintDiff_PerQueryDeltasCountAFailedQueryAsZero(t *testing.T) {
	before := diffReport(diffRun{runID: "a", kValues: []int{10}, ndcg: map[int]float64{10: 0.6}, threshold: 1})
	after := diffReport(diffRun{runID: "b", kValues: []int{10}, ndcg: map[int]float64{10: 0.6}, threshold: 1})
	after.Jobs[0].PerQuery[0] = report.Entry{QueryID: "q1", EngineName: "pg", Judged: true, Error: "timeout"}

	var out bytes.Buffer
	printDiff(&out, before, after)

	assert.Regexp(t, `(?m)^q1\s+pg\s+0\.6000\s+0\.0000\s+\S*-0\.6000`, out.String())
}

func TestPrintDiff_PerQueryDeltasIgnoreAFailedQueryThatRunNeverGraded(t *testing.T) {
	before := diffReport(diffRun{runID: "a", kValues: []int{10}, ndcg: map[int]float64{10: 0.6}, threshold: 1})
	after := diffReport(diffRun{runID: "b", kValues: []int{10}, ndcg: map[int]float64{10: 0.6}, threshold: 1})
	after.Jobs[0].PerQuery[0] = report.Entry{QueryID: "q1", EngineName: "pg", Error: "timeout"}

	var out bytes.Buffer
	printDiff(&out, before, after)

	assert.NotContains(t, out.String(), "Per-Query NDCG")
}

func scaffoldDiffTrack(t *testing.T) string {
	t.Helper()
	cwd := t.TempDir()
	t.Chdir(cwd)
	initCmd := newInitCmd()
	initCmd.SetOut(&bytes.Buffer{})
	initCmd.SetArgs([]string{"demo"})
	require.NoError(t, initCmd.Execute())
	return filepath.Join(cwd, "demo", "reports")
}

func TestExecuteDiff_BaselineSearchSkipsUnreadableReports(t *testing.T) {
	reports := scaffoldDiffTrack(t)
	writeTrackReport(t, reports, "2026-01-01T00-00-01-run-aaaaaa", nil)
	require.NoError(t, os.WriteFile(filepath.Join(reports, "2026-01-01T00-00-02-run-bbbbbb.json"), []byte("{not json"), 0o644))
	writeTrackReport(t, reports, "2026-01-01T00-00-03-run-cccccc", nil)
	require.NoError(t, report.WriteLatestPointer(filepath.Join(reports, "latest.json"),
		filepath.Join(reports, "2026-01-01T00-00-03-run-cccccc.json")))

	var out bytes.Buffer
	require.NoError(t, executeDiff(&out, diffFlags{}, []string{"demo"}))
	assert.Regexp(t, `A \(before\):\s+2026-01-01T00-00-01-run-aaaaaa`, out.String())

	err := executeDiff(&bytes.Buffer{}, diffFlags{pathA: "2026-01-01T00-00-02-run-bbbbbb"}, []string{"demo"})
	assert.Error(t, err, "an explicitly named unreadable report is still an error")
}

func TestExecuteDiff_FallsBackToTheNewestEarlierReportWhenNoneIsFullyScored(t *testing.T) {
	reports := scaffoldDiffTrack(t)
	latencyOnly := func(r *report.Report) {
		r.Jobs[0].Aggregated[0].JudgedCount = 0
		r.Jobs[0].PerQuery[0].Judged = false
	}
	writeTrackReport(t, reports, "2026-01-01T00-00-01-run-aaaaaa", latencyOnly)
	writeTrackReport(t, reports, "2026-01-01T00-00-02-run-bbbbbb", latencyOnly)
	writeTrackReport(t, reports, "2026-01-01T00-00-03-run-cccccc", latencyOnly)
	require.NoError(t, report.WriteLatestPointer(filepath.Join(reports, "latest.json"),
		filepath.Join(reports, "2026-01-01T00-00-03-run-cccccc.json")))

	var out bytes.Buffer
	require.NoError(t, executeDiff(&out, diffFlags{}, []string{"demo"}))

	assert.Regexp(t, `A \(before\):\s+2026-01-01T00-00-02-run-bbbbbb`, out.String())
}

func TestExecuteDiff_WithoutLatestPointerDiffsTheTwoNewestScoredReports(t *testing.T) {
	reports := scaffoldDiffTrack(t)
	writeTrackReport(t, reports, "2026-01-01T00-00-01-run-aaaaaa", nil)
	writeTrackReport(t, reports, "2026-01-01T00-00-02-run-bbbbbb", nil)
	require.NoFileExists(t, filepath.Join(reports, "latest.json"))

	var out bytes.Buffer
	require.NoError(t, executeDiff(&out, diffFlags{}, []string{"demo"}))

	assert.Regexp(t, `A \(before\):\s+2026-01-01T00-00-01-run-aaaaaa`, out.String())
	assert.Regexp(t, `B \(after\):\s+\S*2026-01-01T00-00-02-run-bbbbbb`, out.String())
}

func TestPrintDiff_NDCGHeadersNameTheGain(t *testing.T) {
	before := diffReport(diffRun{runID: "a", kValues: []int{10}, ndcg: map[int]float64{10: 0.6}, threshold: 1})
	after := diffReport(diffRun{runID: "b", kValues: []int{10}, ndcg: map[int]float64{10: 0.7}, threshold: 1})

	var out bytes.Buffer
	printDiff(&out, before, after)

	headers := regexp.MustCompile(`NDCG@\d+[^\t\n]*`).FindAllString(out.String(), -1)
	require.Len(t, headers, 2, "the aggregate column and the per-query title")
	for _, header := range headers {
		assert.Contains(t, header, "2^rel-1")
	}
}

func TestPrintDiff_SameJudgmentsFileInAMovedTrackIsNotAWarning(t *testing.T) {
	judge := &meta.Judge{Strategy: "keyword"}
	before := diffRun{
		runID: "a", kValues: []int{10}, ndcg: map[int]float64{10: 0.6}, threshold: 1, judge: judge,
		spec:      "/Users/me/projects/sengi/tracks/cc-news/fts/spec.yaml",
		judgments: "/Users/me/projects/sengi/tracks/cc-news/fts/trec/annotations.keyword.yaml",
	}
	moved := before
	moved.runID = "b"
	moved.spec = "/Users/me/projects/sengi-wt/fix-x/tracks/cc-news/fts/spec.yaml"
	moved.judgments = "/Users/me/projects/sengi-wt/fix-x/tracks/cc-news/fts/trec/annotations.keyword.yaml"
	swapped := moved
	swapped.judgments = "/Users/me/projects/sengi-wt/fix-x/tracks/cc-news/fts/trec/annotations.bm25.yaml"

	var same bytes.Buffer
	printDiff(&same, diffReport(before), diffReport(moved))
	assert.NotContains(t, same.String(), "WARNING")

	var different bytes.Buffer
	printDiff(&different, diffReport(before), diffReport(swapped))
	assert.Contains(t, different.String(), "judgments: trec/annotations.keyword.yaml → trec/annotations.bm25.yaml")
}

func TestPrintDiff_ChangeFromAZeroBaselineHasNoPercentage(t *testing.T) {
	before := diffReport(diffRun{runID: "a", kValues: []int{10}, ndcg: map[int]float64{10: 0}, threshold: 1})
	after := diffReport(diffRun{runID: "b", kValues: []int{10}, ndcg: map[int]float64{10: 0.4}, threshold: 1})
	after.Jobs[0].Aggregated[0].Latency = report.LatencyStats{Percentiles: map[int]time.Duration{50: 3 * time.Millisecond}}

	var out bytes.Buffer
	printDiff(&out, before, after)
	got := out.String()

	assert.Contains(t, got, "n/a")
	assert.NotContains(t, got, "+0.0%")
}

func TestDiff_WithoutAVerbNamesBoth(t *testing.T) {
	cmd := &cobra.Command{Use: "bench"}
	cmd.AddCommand(newDiffCmd())
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"diff", "fts"})

	err := cmd.Execute()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "bench diff runs")
	assert.Contains(t, err.Error(), "bench diff judgments")
}

func TestDiffRuns_ComparesTheLatestTwoRuns(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	initCmd := newInitCmd()
	initCmd.SetOut(&bytes.Buffer{})
	initCmd.SetArgs([]string{"demo"})
	require.NoError(t, initCmd.Execute())
	reports := filepath.Join(cwd, "demo", "reports")
	writeTrackReport(t, reports, "2026-01-01T00-00-01-run-aaaaaa", nil)
	writeTrackReport(t, reports, "2026-01-01T00-00-02-run-bbbbbb", nil)
	require.NoError(t, report.WriteLatestPointer(filepath.Join(reports, "latest.json"), filepath.Join(reports, "2026-01-01T00-00-02-run-bbbbbb.json")))

	cmd := newDiffCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"runs", "demo"})
	require.NoError(t, cmd.Execute())

	assert.Regexp(t, `A \(before\):\s+2026-01-01T00-00-01-run-aaaaaa`, out.String())
}

// judgmentsTrack: kw (k1-k3), phrase query ph (p1 has the phrase, p2 not); relevant is grade 3.
type judgmentsTrack struct {
	root     string
	articles articleStore
	k1, k2   uuid.UUID
	k3       uuid.UUID
	p1, p2   uuid.UUID
}

const judgmentsSpec = `schema_version: 1
id: agreement
kind: fts
engines:
  a: {type: postgres, connection: "postgres://localhost/a"}
  b: {type: postgres, connection: "postgres://localhost/b"}
jobs:
  - name: fts
    suite: suite.yaml
    engines: [a, b]
metrics:
  k_values: [2]
  relevance_threshold: 3
`

func newJudgmentsTrack(t *testing.T) *judgmentsTrack {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	tr := &judgmentsTrack{root: root, k1: uuid.New(), k2: uuid.New(), k3: uuid.New(), p1: uuid.New(), p2: uuid.New()}
	require.NoError(t, os.MkdirAll(filepath.Join(tr.root, "trec"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(tr.root, "reports"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tr.root, "spec.yaml"), []byte(judgmentsSpec), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tr.root, "suite.yaml"), []byte("schema_version: 1\nid: agreement\n"), 0o644))
	tr.articles = articleStore{
		tr.k1: {ID: tr.k1, Title: "climate one"},
		tr.k2: {ID: tr.k2, Title: "climate two"},
		tr.k3: {ID: tr.k3, Title: "climate three"},
		tr.p1: {ID: tr.p1, Title: "climate change summit"},
		tr.p2: {ID: tr.p2, Title: "a change in the climate"},
	}
	require.NoError(t, pool.WritePoolFile(&pool.PoolFile{Meta: meta.New("pool"), Queries: []pool.PoolEntry{
		{QueryID: "kw", QueryText: "climate", Category: suite.CategoryKeyword, Docs: []pool.PooledDoc{{DocID: tr.k1}, {DocID: tr.k2}, {DocID: tr.k3}}},
		{QueryID: "ph", QueryText: "climate change", Category: suite.CategoryPhrase, Docs: []pool.PooledDoc{{DocID: tr.p1}, {DocID: tr.p2}}},
	}}, filepath.Join(tr.root, "trec", "pool.yaml")))
	tr.writeSet(t, "first", meta.Judge{Strategy: "llm", Model: "model-a"}, [5]int{3, 2, 0, 3, 0})
	tr.writeSet(t, "second", meta.Judge{Strategy: "llm", Model: "model-b"}, [5]int{2, 2, 1, 3, 0})
	tr.writeSet(t, "bm25", meta.Judge{Strategy: "bm25"}, [5]int{3, 0, 0, 1, 0})
	return tr
}

// writeSet grades k1, k2, k3, p1 and p2 in that order.
func (tr *judgmentsTrack) writeSet(t *testing.T, name string, judge meta.Judge, grades [5]int) {
	t.Helper()
	require.NoError(t, judgment.WriteFile(&judgment.File{
		Meta: meta.Meta{RunID: "judge-" + name, Judge: &judge},
		Queries: []judgment.Entry{
			{QueryID: "kw", Docs: []judgment.GradedDoc{{DocID: tr.k1, Grade: grades[0]}, {DocID: tr.k2, Grade: grades[1]}, {DocID: tr.k3, Grade: grades[2]}}},
			{QueryID: "ph", Docs: []judgment.GradedDoc{{DocID: tr.p1, Grade: grades[3]}, {DocID: tr.p2, Grade: grades[4]}}},
		},
	}, filepath.Join(tr.root, "trec", "annotations."+name+".yaml")))
}

func (tr *judgmentsTrack) diff(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newDiffCmdWithReader(func(*cobra.Command, string) (storage.Reader, error) { return tr.articles, nil })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(append([]string{"judgments", tr.root}, args...))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

// pairwiseRow returns a pairwise row's numbers: n, exact, within one, relevant, then each kappa.
func pairwiseRow(t *testing.T, output, pair string) []string {
	t.Helper()
	line := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(pair) + `\s+(\d+)\s+(\S+)\s+(\S+)\s+(\S+)\s+(\S+)`).FindStringSubmatch(output)
	require.NotNil(t, line, "no %q row in:\n%s", pair, output)
	return line[1:]
}

func TestDiffJudgments_RelevantMeansTheSpecsThreshold(t *testing.T) {
	tr := newJudgmentsTrack(t)

	out, err := tr.diff(t, "--judgments", "first,second")
	require.NoError(t, err)

	// At the spec's threshold 3, k1 (3 against 2) splits the sets; at 2 all five pairs would agree.
	assert.Equal(t, []string{"5", "0.6000", "1.0000", "0.8000"}, pairwiseRow(t, out, "model-a vs model-b")[:4])
}

func TestDiffJudgments_WithoutPGSaysGatedZerosAreIncluded(t *testing.T) {
	tr := newJudgmentsTrack(t)

	out, err := tr.diff(t, "--judgments", "first,second")
	require.NoError(t, err)

	assert.Contains(t, out, "gated zeros are included")
}

func TestDiffJudgments_WithPGMeasuresThePairsTheGateAdmits(t *testing.T) {
	tr := newJudgmentsTrack(t)

	out, err := tr.diff(t, "--judgments", "first,second", "--pg", "postgres://stub")
	require.NoError(t, err)

	assert.Contains(t, out, "gate refused 1 of 5")
	admitted := out[strings.Index(out, "4 pairs the gate admits"):]
	assert.Equal(t, []string{"4", "0.5000"}, pairwiseRow(t, admitted, "model-a vs model-b")[:2])
}

func TestDiffJudgments_PairsListsTheWidestDisagreementsFirst(t *testing.T) {
	tr := newJudgmentsTrack(t)

	rows := func(t *testing.T, args ...string) []string {
		t.Helper()
		out, err := tr.diff(t, append([]string{"--judgments", "first,second,bm25", "--pairs"}, args...)...)
		require.NoError(t, err)
		var docs []string
		for _, line := range strings.Split(out, "\n") {
			for _, doc := range []uuid.UUID{tr.k1, tr.k2, tr.k3, tr.p1, tr.p2} {
				if strings.Contains(line, doc.String()) {
					docs = append(docs, doc.String())
				}
			}
		}
		return docs
	}

	assert.Equal(t, []string{tr.k2.String(), tr.p1.String()}, rows(t))
	wider := rows(t, "--min-gap", "1")
	assert.Equal(t, []string{tr.k2.String(), tr.p1.String()}, wider[:2])
	assert.ElementsMatch(t, []string{tr.k1.String(), tr.k3.String()}, wider[2:])
}

func TestDiffJudgments_ReferenceCountsBothKindsOfMiss(t *testing.T) {
	tr := newJudgmentsTrack(t)

	out, err := tr.diff(t, "--judgments", "first,second", "--reference", "model-a")
	require.NoError(t, err)

	// model-a's relevant pairs at threshold 3 are k1 and p1, and model-b grades k1 a 2.
	assert.Contains(t, out, "model-b graded 0 of model-a's 3 irrelevant pairs relevant and 1 of its 2 relevant pairs irrelevant")
	assert.Regexp(t, `(?m)^keyword\s+1\s+model-a vs model-b\s+3\s+0\.3333`, out)
	assert.Regexp(t, `(?m)^phrase\s+1\s+model-a vs model-b\s+2\s+1\.0000`, out)
}

func TestDiffJudgments_SameInputsPrintTheSameBytes(t *testing.T) {
	tr := newJudgmentsTrack(t)
	tr.addQueries(t, 8)
	args := []string{"--judgments", "first,second,bm25", "--reference", "bm25", "--pairs"}

	first, err := tr.diff(t, args...)
	require.NoError(t, err)
	second, err := tr.diff(t, args...)
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

// addQueries adds n graded keyword queries, so the bootstrap has intervals a seed can move.
func (tr *judgmentsTrack) addQueries(t *testing.T, n int) {
	t.Helper()
	pf, err := pool.ReadPoolFile(filepath.Join(tr.root, "trec", "pool.yaml"))
	require.NoError(t, err)
	sets := map[string]*judgment.File{}
	for _, name := range []string{"first", "second", "bm25"} {
		sets[name], err = judgment.ReadFile(filepath.Join(tr.root, "trec", "annotations."+name+".yaml"))
		require.NoError(t, err)
	}
	for q := range n {
		entry := pool.PoolEntry{QueryID: fmt.Sprintf("extra-%d", q), QueryText: "climate", Category: suite.CategoryKeyword}
		graded := map[string]judgment.Entry{}
		for d := range 4 {
			doc := uuid.New()
			entry.Docs = append(entry.Docs, pool.PooledDoc{DocID: doc})
			for i, name := range []string{"first", "second", "bm25"} {
				e := graded[name]
				e.QueryID = entry.QueryID
				e.Docs = append(e.Docs, judgment.GradedDoc{DocID: doc, Grade: (q*(i+1) + d*(i+2)) % 4})
				graded[name] = e
			}
		}
		pf.Queries = append(pf.Queries, entry)
		for name, e := range graded {
			sets[name].Queries = append(sets[name].Queries, e)
		}
	}
	require.NoError(t, pool.WritePoolFile(pf, filepath.Join(tr.root, "trec", "pool.yaml")))
	for name, jf := range sets {
		require.NoError(t, judgment.WriteFile(jf, filepath.Join(tr.root, "trec", "annotations."+name+".yaml")))
	}
}

func TestDiffJudgments_UndefinedKappaPrintsNAAndWritesNull(t *testing.T) {
	tr := newJudgmentsTrack(t)
	tr.writeSet(t, "zeros", meta.Judge{Strategy: "keyword"}, [5]int{})
	tr.writeSet(t, "nil", meta.Judge{Strategy: "manual"}, [5]int{})
	output := filepath.Join(t.TempDir(), "agreement.json")

	out, err := tr.diff(t, "--judgments", "zeros,nil", "--output", output)
	require.NoError(t, err)

	assert.Equal(t, []string{"5", "1.0000", "1.0000", "1.0000", "n/a"}, pairwiseRow(t, out, "keyword vs manual"))
	var written struct {
		AllPairs struct {
			Comparisons []struct {
				QuadraticKappa struct {
					Value *float64 `json:"value"`
				} `json:"quadratic_kappa"`
			} `json:"comparisons"`
		} `json:"all_pairs"`
	}
	data, err := os.ReadFile(output)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &written))
	assert.Nil(t, written.AllPairs.Comparisons[0].QuadraticKappa.Value)
}

func TestDiffJudgments_OutputNamesEverySourceItRead(t *testing.T) {
	tr := newJudgmentsTrack(t)
	tr.writeReport(t)
	output := filepath.Join(t.TempDir(), "agreement.json")

	_, err := tr.diff(t, "--judgments", "first,second", "--report", "latest", "--output", output)
	require.NoError(t, err)

	var written struct {
		SchemaVersion int `json:"schema_version"`
		Meta          struct {
			Sources struct {
				Judgments []struct {
					Label string `json:"label"`
					Path  string `json:"path"`
					RunID string `json:"run_id"`
				} `json:"judgments"`
				ReportRunID string `json:"report_run_id"`
			} `json:"sources"`
		} `json:"meta"`
	}
	data, err := os.ReadFile(output)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &written))
	assert.Equal(t, 1, written.SchemaVersion)
	require.Len(t, written.Meta.Sources.Judgments, 2)
	assert.Equal(t, "model-a", written.Meta.Sources.Judgments[0].Label)
	assert.Equal(t, filepath.Join(tr.root, "trec", "annotations.first.yaml"), written.Meta.Sources.Judgments[0].Path)
	assert.Equal(t, "judge-first", written.Meta.Sources.Judgments[0].RunID)
	assert.Equal(t, "judge-second", written.Meta.Sources.Judgments[1].RunID)
	assert.Equal(t, "run-agreement", written.Meta.Sources.ReportRunID)
}

// writeReport records engines a (k1, k2) and b (k3, k1) on kw, both P@2 = 1/2 under "first".
func (tr *judgmentsTrack) writeReport(t *testing.T) {
	t.Helper()
	cell := func(engine string, docs ...uuid.UUID) report.Entry {
		e := report.Entry{QueryID: "kw", EngineName: engine, Judged: true, Precision: map[int]float64{2: 0.5}, ReturnedCount: len(docs)}
		for i, doc := range docs {
			e.Ranking = append(e.Ranking, report.RankedDoc{DocID: doc, Score: float64(len(docs) - i)})
		}
		return e
	}
	rpt := &report.Report{
		SchemaVersion: 1,
		Provenance:    meta.Meta{RunID: "run-agreement", JudgmentsRunID: "judge-first"},
		Jobs: []report.JobReport{{
			JobName:    "fts",
			Aggregated: []report.AggregatedEntry{{EngineName: "a"}, {EngineName: "b"}},
			PerQuery:   []report.Entry{cell("a", tr.k1, tr.k2), cell("b", tr.k3, tr.k1)},
		}},
	}
	path := filepath.Join(tr.root, "reports", "run-agreement.json")
	require.NoError(t, report.WriteJSON(rpt, path))
	require.NoError(t, report.WriteLatestPointer(filepath.Join(tr.root, "reports", "latest.json"), path))
}

func TestDiffJudgments_ReportRanksTheEnginesUnderEachSet(t *testing.T) {
	tr := newJudgmentsTrack(t)
	tr.writeReport(t)
	tr.writeSet(t, "sample", meta.Judge{Strategy: "llm", Model: "model-c"}, [5]int{3, 2, 0, 3, 0})
	sample := filepath.Join(tr.root, "trec", "annotations.sample.yaml")
	jf, err := judgment.ReadFile(sample)
	require.NoError(t, err)
	jf.Queries[0].Docs = jf.Queries[0].Docs[:2]
	require.NoError(t, judgment.WriteFile(jf, sample))

	out, err := tr.diff(t, "--judgments", "first,second,sample", "--report", "latest")
	require.NoError(t, err)

	assert.Contains(t, out, "model-c: leaves 1 of the rankings' top-2 docs ungraded")
	// Under model-b (k1 2, k3 1, nothing relevant at 3), both engines score 0 and tie.
	assert.Regexp(t, `(?m)^P@2\s+model-a\s+0\.5000\s+0\.5000\s+a = b`, out)
	assert.Regexp(t, `(?m)^\s+model-b\s+0\.0000\s+0\.0000\s+a = b\s+n/a`, out)
}

func TestDiffJudgments_PairsWithPGShowEachArticlesTitle(t *testing.T) {
	tr := newJudgmentsTrack(t)

	out, err := tr.diff(t, "--judgments", "first,second", "--pairs", "--min-gap", "1", "--pg", "postgres://stub")
	require.NoError(t, err)

	assert.Regexp(t, `(?m)^kw\s+`+tr.k1.String()+`\s+1\s+3\s+2\s+climate one$`, out)
}

// writeGrades files grades[i] for docs[i] under one query, "hand".
func (tr *judgmentsTrack) writeGrades(t *testing.T, name string, judge meta.Judge, docs []uuid.UUID, grades []int) {
	t.Helper()
	entry := judgment.Entry{QueryID: "hand"}
	for i, doc := range docs {
		entry.Docs = append(entry.Docs, judgment.GradedDoc{DocID: doc, Grade: grades[i]})
	}
	require.NoError(t, judgment.WriteFile(&judgment.File{Meta: meta.Meta{RunID: "judge-" + name, Judge: &judge}, Queries: []judgment.Entry{entry}},
		filepath.Join(tr.root, "trec", "annotations."+name+".yaml")))
}

// Expected κ is sklearn's quadratic cohen_kappa_score over the six shared pairs, 8/13.
func TestDiffJudgments_MeasuresHandGradesAgainstAJudgeWithoutTouchingThem(t *testing.T) {
	tr := newJudgmentsTrack(t)
	hand := []int{2, 1, 0, 3, 3, 1, 3, judgment.GradeUnjudged}
	judge := []int{2, 2, 0, 3, 1, 1, judgment.GradeUnjudged, 2}
	docs := make([]uuid.UUID, len(hand))
	for i := range docs {
		docs[i] = uuid.New()
	}
	tr.writeGrades(t, "manual", judgment.ManualJudge(), docs, hand)
	tr.writeGrades(t, "bm25", judgment.NewBM25Strategy().Describe(), docs, judge)
	manual := filepath.Join(tr.root, "trec", "annotations.manual.yaml")
	before, err := os.ReadFile(manual)
	require.NoError(t, err)

	out, err := tr.diff(t, "--judgments", "manual,bm25", "--reference", "manual")
	require.NoError(t, err)

	row := pairwiseRow(t, out, "manual vs bm25")
	assert.Equal(t, []string{"6", "0.6667"}, row[:2])
	assert.Equal(t, "0.6154", row[4])
	table := out[strings.Index(out, "bm25 against manual"):]
	assert.Regexp(t, `(?m)^3\s+0\s+1\s+0\s+1$`, table)
	after, err := os.ReadFile(manual)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestPrintDiff_PerQueryDeltasThatTieGoByQueryThenEngine(t *testing.T) {
	run := func(runID string, ndcg float64) *report.Report {
		cell := func(query, engine string) report.Entry {
			return report.Entry{QueryID: query, EngineName: engine, Judged: true, NDCG: map[int]float64{10: ndcg}}
		}
		return &report.Report{
			SchemaVersion: 1,
			Provenance:    meta.Meta{RunID: runID},
			Config:        report.ReportConfig{KValues: []int{10}, RelevanceThreshold: 1},
			Jobs: []report.JobReport{{
				JobName:    "fts",
				Aggregated: []report.AggregatedEntry{{EngineName: "pg", JudgedCount: 2}, {EngineName: "es", JudgedCount: 2}},
				PerQuery:   []report.Entry{cell("q2", "pg"), cell("q1", "pg"), cell("q1", "es")},
			}},
		}
	}

	var out bytes.Buffer
	printDiff(&out, run("a", 0.5), run("b", 0.7))

	rows := regexp.MustCompile(`(?m)^(q\d)\s+(pg|es)\s+0\.5000`).FindAllStringSubmatch(out.String(), -1)
	var order []string
	for _, row := range rows {
		order = append(order, row[1]+"/"+row[2])
	}
	assert.Equal(t, []string{"q1/es", "q1/pg", "q2/pg"}, order)
}
