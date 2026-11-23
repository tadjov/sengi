package rungate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/bench/judgment"
	"github.com/tadjov/sengi/internal/bench/meta"
)

func TestLoadGrades_MissingFileExplicitErrors(t *testing.T) {
	_, _, err := LoadGrades(filepath.Join(t.TempDir(), "nope.yaml"), true)
	require.Error(t, err, "explicit --judgments path must error when file missing")
	assert.Contains(t, err.Error(), "--judgments file not found")
	assert.Contains(t, err.Error(), "keyword", "error should list valid strategies")
}

func TestLoadGrades_MissingFileImplicitErrors(t *testing.T) {
	_, _, err := LoadGrades(filepath.Join(t.TempDir(), "nope.yaml"), false)
	require.Error(t, err, "a missing spec-default judgments file must not yield an unscored report")
	assert.Contains(t, err.Error(), "spec.defaults.judgments")
}

func TestLoadGrades_FlattensAndFiltersUnjudged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ann.yaml")
	docOK := uuid.New()
	docSkip := uuid.New()

	jf := &judgment.File{
		Meta: meta.New("judge"),
		Queries: []judgment.Entry{
			{
				QueryID: "q1",
				Docs: []judgment.GradedDoc{
					{DocID: docOK, Grade: 2},
					{DocID: docSkip, Grade: -1},
				},
			},
		},
	}
	require.NoError(t, judgment.WriteFile(jf, path))

	m, jm, err := LoadGrades(path, true)
	require.NoError(t, err)
	require.NotNil(t, jm, "the grader identity must come back so the report can record it")
	assert.Equal(t, jf.Meta.RunID, jm.RunID)
	require.Contains(t, m, "q1")
	assert.Equal(t, 2, m["q1"][docOK.String()])
	_, present := m["q1"][docSkip.String()]
	assert.False(t, present, "unjudged (-1) entries must be filtered out")
}

func TestLoadGrades_RejectsMissingSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ann.yaml")
	// Write a syntactically valid YAML but with no schema_version — the
	// judgment loader should reject it.
	require.NoError(t, os.WriteFile(path, []byte(`strategy: keyword
queries: []
`), 0644))

	_, _, err := LoadGrades(path, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema_version")
}
