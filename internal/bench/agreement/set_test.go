package agreement_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/bench/agreement"
	"github.com/tadjov/sengi/internal/bench/judgment"
	"github.com/tadjov/sengi/internal/bench/meta"
	"github.com/tadjov/sengi/internal/bench/trackctx"
)

func newTrack(t *testing.T) *trackctx.Track {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "trec"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "spec.yaml"), []byte("schema_version: 1\nid: t\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "suite.yaml"), []byte("schema_version: 1\nid: t\n"), 0o644))
	tr, err := trackctx.Resolve(trackctx.Inputs{TrackArg: root})
	require.NoError(t, err)
	return tr
}

func writeSet(t *testing.T, path string, judge meta.Judge, queries ...judgment.Entry) {
	t.Helper()
	require.NoError(t, judgment.WriteFile(&judgment.File{
		Meta:    meta.Meta{RunID: "judge-" + filepath.Base(path), Judge: &judge},
		Queries: queries,
	}, path))
}

func TestLoad_LabelsEachSetByItsModelElseItsStrategy(t *testing.T) {
	tr := newTrack(t)
	writeSet(t, tr.JudgmentsPath("claude-cli"), meta.Judge{Strategy: "llm", Provider: "claude-cli", Model: "claude-sonnet-5-5"})
	writeSet(t, tr.JudgmentsPath("bm25"), meta.Judge{Strategy: "bm25"})
	elsewhere := filepath.Join(t.TempDir(), "annotations.opus.yaml")
	writeSet(t, elsewhere, meta.Judge{Strategy: "llm", Model: "claude-opus-5-5"})

	sets, err := agreement.Load(tr, []string{"claude-cli", "bm25", "opus=" + elsewhere})
	require.NoError(t, err)

	var labels, paths []string
	for _, s := range sets {
		labels = append(labels, s.Label)
		paths = append(paths, s.Path)
	}
	assert.Equal(t, []string{"claude-sonnet-5-5", "bm25", "opus"}, labels)
	assert.Equal(t, []string{tr.JudgmentsPath("claude-cli"), tr.JudgmentsPath("bm25"), elsewhere}, paths)
}

func TestLoad_RefusesTwoSetsUnderOneLabel(t *testing.T) {
	tr := newTrack(t)
	writeSet(t, tr.JudgmentsPath("jev"), meta.Judge{Strategy: "jev", Model: "jev-1.14.0"})
	sample := filepath.Join(t.TempDir(), "annotations.jev.yaml")
	writeSet(t, sample, meta.Judge{Strategy: "jev", Model: "jev-1.14.0"})

	_, err := agreement.Load(tr, []string{"jev", sample})

	require.Error(t, err)
	assert.Contains(t, err.Error(), tr.JudgmentsPath("jev"))
	assert.Contains(t, err.Error(), sample)
	assert.Contains(t, err.Error(), "label=")

	sets, err := agreement.Load(tr, []string{"jev", "sample=" + sample})
	require.NoError(t, err)
	assert.Equal(t, "sample", sets[1].Label)
}

func TestLoad_NeedsTwoSets(t *testing.T) {
	tr := newTrack(t)
	writeSet(t, tr.JudgmentsPath("jev"), meta.Judge{Strategy: "jev", Model: "jev-1.14.0"})

	_, err := agreement.Load(tr, []string{"jev"})

	assert.ErrorContains(t, err, "two")
}

func TestLoad_NamesTheFileItCannotRead(t *testing.T) {
	tr := newTrack(t)
	writeSet(t, tr.JudgmentsPath("jev"), meta.Judge{Strategy: "jev", Model: "jev-1.14.0"})
	for name, content := range map[string]string{
		"malformed":         "schema_version: 1\nqueries: [oops\n",
		"no schema version": "meta:\n  run_id: x\nqueries: []\n",
	} {
		t.Run(name, func(t *testing.T) {
			bad := filepath.Join(t.TempDir(), "annotations.bad.yaml")
			require.NoError(t, os.WriteFile(bad, []byte(content), 0o644))

			_, err := agreement.Load(tr, []string{"jev", bad})

			assert.ErrorContains(t, err, bad)
		})
	}
}

func graded(queryID string, grades map[uuid.UUID]int) judgment.Entry {
	entry := judgment.Entry{QueryID: queryID}
	for doc, grade := range grades {
		entry.Docs = append(entry.Docs, judgment.GradedDoc{DocID: doc, Grade: grade})
	}
	return entry
}
