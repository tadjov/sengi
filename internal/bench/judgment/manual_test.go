package judgment

import (
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/bench/meta"
	"github.com/tadjov/sengi/internal/bench/pool"
)

func TestManualFile_KeepsHandGradesAndAddsPlaceholdersForNewPoolDocs(t *testing.T) {
	graded, placeholder, newInPool, leftThePool, newQueryDoc := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	pf := &pool.PoolFile{Queries: []pool.PoolEntry{
		{QueryID: "q1", Docs: []pool.PooledDoc{{DocID: graded}, {DocID: placeholder}, {DocID: newInPool}}},
		{QueryID: "q2", Docs: []pool.PooledDoc{{DocID: newQueryDoc}}},
	}}
	pf.Meta.RunID = "pool-run"
	existing := &File{Queries: []Entry{{QueryID: "q1", Docs: []GradedDoc{
		{DocID: graded, Grade: GradeRelevant},
		{DocID: placeholder, Grade: GradeUnjudged},
		{DocID: leftThePool, Grade: GradeNotRelev},
	}}}}
	path := filepath.Join(t.TempDir(), "annotations.manual.yaml")

	_, err := ManualFile{Pool: pf, PoolRef: "trec/pool.yaml", Path: path}.Write(existing)

	require.NoError(t, err)
	written, err := ReadFile(path)
	require.NoError(t, err)
	want := map[string]map[uuid.UUID]int{
		"q1": {
			graded:      GradeRelevant,
			placeholder: GradeUnjudged,
			newInPool:   GradeUnjudged,
			leftThePool: GradeNotRelev,
		},
		"q2": {newQueryDoc: GradeUnjudged},
	}
	assert.Equal(t, want, indexExisting(written))
	assert.Equal(t, ManualJudge(), written.Judge())
	assert.Equal(t, "pool-run", written.Meta.PoolRunID)
}

func TestManualFile_WithoutExistingGradesWritesPlaceholders(t *testing.T) {
	doc := uuid.New()
	pf := &pool.PoolFile{Queries: []pool.PoolEntry{{QueryID: "q1", Docs: []pool.PooledDoc{{DocID: doc}}}}}

	got, err := ManualFile{Pool: pf, Path: filepath.Join(t.TempDir(), "annotations.manual.yaml")}.Write(nil)

	require.NoError(t, err)
	assert.Equal(t, map[string]map[uuid.UUID]int{"q1": {doc: GradeUnjudged}}, indexExisting(got))
}

func TestManualFile_RecordsTheCutItsHandGradesAreReadAt(t *testing.T) {
	doc := uuid.New()
	pf := &pool.PoolFile{Queries: []pool.PoolEntry{{QueryID: "q1", Docs: []pool.PooledDoc{{DocID: doc}}}}}
	path := filepath.Join(t.TempDir(), "annotations.manual.yaml")

	_, err := ManualFile{Pool: pf, Path: path, ContentRunes: 400}.Write(nil)

	require.NoError(t, err)
	written, err := ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, 400, written.Judge().ContentRunes)
}

func TestManualFile_RefusesHandGradesReadAtAnotherCut(t *testing.T) {
	doc := uuid.New()
	pf := &pool.PoolFile{Queries: []pool.PoolEntry{{QueryID: "q1", Docs: []pool.PooledDoc{{DocID: doc}}}}}
	readAt := func(contentRunes, grade int) *File {
		judge := ManualJudge()
		judge.ContentRunes = contentRunes
		return &File{Meta: meta.Meta{Judge: &judge}, Queries: []Entry{{QueryID: "q1", Docs: []GradedDoc{{DocID: doc, Grade: grade}}}}}
	}

	t.Run("hand grades", func(t *testing.T) {
		_, err := ManualFile{Pool: pf, Path: filepath.Join(t.TempDir(), "m.yaml"), ContentRunes: 600}.Write(readAt(400, GradeRelevant))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "400")
		assert.Contains(t, err.Error(), "600")
	})

	t.Run("placeholders only", func(t *testing.T) {
		got, err := ManualFile{Pool: pf, Path: filepath.Join(t.TempDir(), "m.yaml"), ContentRunes: 600}.Write(readAt(400, GradeUnjudged))

		require.NoError(t, err)
		assert.Equal(t, 600, got.Judge().ContentRunes)
	})
}
