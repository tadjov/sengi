package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/bench/judgment"
	"github.com/tadjov/sengi/internal/bench/meta"
	"github.com/tadjov/sengi/internal/storage"
)

func keywordJudgedTrack(t *testing.T) lineageTrack {
	t.Helper()
	relevant, other := uuid.New(), uuid.New()
	cluster := fakeCluster(t, map[string]*fakeIndex{
		"a": {count: 10, ranked: map[string][]uuid.UUID{"q1": {relevant, other}, "q2": {relevant}}},
		"b": {count: 10, ranked: map[string][]uuid.UUID{"q1": {other, relevant}, "q2": {other}}},
	})
	tr := newLineageTrack(t, cluster)
	editFile(t, filepath.Join(tr.dir, "spec.yaml"), strconv.Quote(tr.judgments), "keyword")
	tr.pool(t)
	articles := articleStore{
		relevant: {ID: relevant, Title: "first query second query"},
		other:    {ID: other, Title: "unrelated"},
	}
	judge := newJudgeCmdWithReader(func(*cobra.Command, string) (storage.Reader, error) { return articles, nil })
	_, err := execute(t, judge, tr.dir, "--strategy", "keyword")
	require.NoError(t, err)
	return tr
}

// machinePaths names dir as given and with symlinks resolved, since macOS's /var is /private/var.
func machinePaths(t *testing.T, dir string) []string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return []string{dir, resolved}
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(to, rel), 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), data, 0o644)
	}))
}

func TestBench_ArtifactsRecordNoPathOfTheMachineTheyWereMadeOn(t *testing.T) {
	tr := keywordJudgedTrack(t)

	_, err := execute(t, newRunCmd(), tr.dir)
	require.NoError(t, err)

	require.NoError(t, filepath.WalkDir(tr.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		for _, prefix := range machinePaths(t, tr.dir) {
			assert.NotContains(t, string(data), prefix, "%s", path)
		}
		return nil
	}))
}

func TestBench_ArtifactsRecordTheirSourcesRelativeToTheTrack(t *testing.T) {
	tr := keywordJudgedTrack(t)

	_, err := execute(t, newRunCmd(), tr.dir)
	require.NoError(t, err)

	jf, err := judgment.ReadFile(filepath.Join(tr.dir, "trec", "annotations.keyword.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "trec/pool.yaml", jf.Meta.PoolRef)
	provenance := tr.latestReport(t).Provenance
	assert.Equal(t, "trec/pool.yaml", provenance.PoolRef)
	assert.Equal(t, &meta.Sources{
		Spec:      "spec.yaml",
		Suites:    []string{"suite.yaml"},
		Pool:      "trec/pool.yaml",
		Judgments: "trec/annotations.keyword.yaml",
	}, provenance.Sources)
}

func TestJudge_ManualFileNamesItsPoolRelativeToTheTrack(t *testing.T) {
	fx := newJudgeFixture(t, "climate change", "climate change summit")

	require.NoError(t, fx.judge(t, "--strategy", "manual"))

	assert.Equal(t, "trec/pool.yaml", fx.annotations(t, "manual").Meta.PoolRef)
}

func TestRun_ReportNamesTheTracksOwnPoolWhateverPathTheJudgmentsRecorded(t *testing.T) {
	tests := []struct {
		name    string
		poolRef func(tr lineageTrack) string
	}{
		{name: "another machine's checkout", poolRef: func(lineageTrack) string {
			return "/Users/someone/projects/sengi/tracks/x/trec/pool.yaml"
		}},
		{name: "this machine, absolute", poolRef: func(tr lineageTrack) string { return tr.poolPath() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := judgedTrack(t, nil)
			jf, err := judgment.ReadFile(tr.judgments)
			require.NoError(t, err)
			jf.Meta.PoolRef = tt.poolRef(tr)
			require.NoError(t, judgment.WriteFile(jf, tr.judgments))

			_, err = execute(t, newRunCmd(), tr.dir)
			require.NoError(t, err)

			assert.Equal(t, "trec/pool.yaml", tr.latestReport(t).Provenance.PoolRef)
		})
	}
}

func TestRun_ATrackCopiedElsewhereStillRuns(t *testing.T) {
	tr := keywordJudgedTrack(t)
	_, err := execute(t, newRunCmd(), tr.dir)
	require.NoError(t, err)
	moved := lineageTrack{dir: filepath.Join(t.TempDir(), "moved")}
	copyTree(t, tr.dir, moved.dir)
	require.NoError(t, os.RemoveAll(tr.dir))

	_, err = execute(t, newRunCmd(), moved.dir)

	require.NoError(t, err)
	assert.Len(t, moved.reports(t), 2, "the copied report and the new one")
}

func TestRun_ASuiteOutsideTheTrackIsRecordedRelativeToIt(t *testing.T) {
	tr := judgedTrack(t, nil)
	shared := filepath.Join(t.TempDir(), "shared", "suite.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(shared), 0o755))
	copyFile(t, filepath.Join(tr.dir, "suite.yaml"), shared)
	editFile(t, filepath.Join(tr.dir, "spec.yaml"), "  - name: a-only\n    suite: suite.yaml", "  - name: a-only\n    suite: "+strconv.Quote(shared))

	_, err := execute(t, newRunCmd(), tr.dir)
	require.NoError(t, err)

	suites := tr.latestReport(t).Provenance.Sources.Suites
	require.Len(t, suites, 2)
	assert.Equal(t, "suite.yaml", suites[0])
	assert.False(t, filepath.IsAbs(suites[1]), suites[1])
	recorded, err := filepath.EvalSymlinks(filepath.Join(tr.dir, suites[1]))
	require.NoError(t, err, "the recorded path resolves from the track")
	want, err := filepath.EvalSymlinks(shared)
	require.NoError(t, err)
	assert.Equal(t, want, recorded)
}

func TestPrintDiff_AnAbsoluteAndATrackRelativeReportOfOneTrackScoreAlike(t *testing.T) {
	judge := &meta.Judge{Strategy: "keyword"}
	older := diffRun{
		runID: "a", kValues: []int{10}, ndcg: map[int]float64{10: 0.6}, threshold: 1, judge: judge,
		spec:      "/Users/me/projects/sengi/tracks/cc-news/fts/spec.yaml",
		judgments: "/Users/me/projects/sengi/tracks/cc-news/fts/trec/annotations.keyword.yaml",
	}
	relative := older
	relative.runID = "b"
	relative.spec = "spec.yaml"
	relative.judgments = "trec/annotations.keyword.yaml"

	var out bytes.Buffer
	printDiff(&out, diffReport(older), diffReport(relative))

	assert.NotContains(t, out.String(), "WARNING")
}

func TestPrintDiff_OneJudgmentsFileReachedThroughASymlinkIsNotAWarning(t *testing.T) {
	real := filepath.Join(t.TempDir(), "fts")
	require.NoError(t, os.MkdirAll(filepath.Join(real, "trec"), 0o755))
	linked := filepath.Join(t.TempDir(), "fts")
	require.NoError(t, os.Symlink(real, linked))
	judge := &meta.Judge{Strategy: "keyword"}
	throughLink := diffRun{
		runID: "a", kValues: []int{10}, ndcg: map[int]float64{10: 0.6}, threshold: 1, judge: judge,
		spec:      filepath.Join(linked, "spec.yaml"),
		judgments: filepath.Join(real, "trec", "annotations.keyword.yaml"),
	}
	direct := throughLink
	direct.runID = "b"
	direct.spec = filepath.Join(real, "spec.yaml")

	var out bytes.Buffer
	printDiff(&out, diffReport(throughLink), diffReport(direct))

	assert.NotContains(t, out.String(), "WARNING")
}
