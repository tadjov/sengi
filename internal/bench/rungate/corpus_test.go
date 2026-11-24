package rungate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/bench/meta"
	"github.com/tadjov/sengi/internal/bench/pool"
	"github.com/tadjov/sengi/internal/storage"
)

var (
	graded      = storage.PackageRef{Name: "fixture-canonical", Version: "v1", ResourceHash: "sha256:graded"}
	republished = storage.PackageRef{Name: "fixture-canonical", Version: "v1", ResourceHash: "sha256:republished"}
)

func judgedOnPool(canonical *storage.PackageRef) *Judgments {
	return &Judgments{Meta: &meta.Meta{RunID: "judge"}, Pool: &pool.PoolFile{Meta: meta.Meta{RunID: "pool", Canonical: canonical}}}
}

func loaded(canonical storage.PackageRef) *storage.LoadRecord {
	return &storage.LoadRecord{Canonical: canonical}
}

func TestCheckCorpus_RefusesAnEngineHoldingAnotherCanonicalPackage(t *testing.T) {
	unchecked, err := judgedOnPool(&graded).CheckCorpus(map[string]*storage.LoadRecord{
		"pg":            loaded(graded),
		"elasticsearch": loaded(republished),
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "sha256:graded")
	assert.Contains(t, err.Error(), "elasticsearch has fixture-canonical v1 (sha256:republished)")
	assert.NotContains(t, err.Error(), "pg has")
	assert.Empty(t, unchecked)
}

func TestCheckCorpus_PassesEnginesHoldingThePoolsCanonicalPackage(t *testing.T) {
	unchecked, err := judgedOnPool(&graded).CheckCorpus(map[string]*storage.LoadRecord{
		"pg":            loaded(graded),
		"elasticsearch": loaded(graded),
	})

	require.NoError(t, err)
	assert.Empty(t, unchecked)
}

func TestCheckCorpus_GoesOnWhenASideRecordsNoCanonicalPackage(t *testing.T) {
	t.Run("the pool", func(t *testing.T) {
		unchecked, err := judgedOnPool(nil).CheckCorpus(map[string]*storage.LoadRecord{"pg": loaded(republished)})

		require.NoError(t, err)
		require.NotEmpty(t, unchecked)
		assert.Contains(t, unchecked[0], "pool")
	})

	t.Run("an engine", func(t *testing.T) {
		unchecked, err := judgedOnPool(&graded).CheckCorpus(map[string]*storage.LoadRecord{
			"pg":            loaded(graded),
			"elasticsearch": nil,
		})

		require.NoError(t, err)
		require.NotEmpty(t, unchecked)
		assert.Contains(t, unchecked[0], "elasticsearch")
	})

	t.Run("an engine, while another holds another package", func(t *testing.T) {
		_, err := judgedOnPool(&graded).CheckCorpus(map[string]*storage.LoadRecord{
			"pg":            loaded(republished),
			"elasticsearch": nil,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "pg has")
	})
}

func TestCheckCorpus_LeavesALatencyOnlyRunAlone(t *testing.T) {
	unchecked, err := LatencyOnly().CheckCorpus(map[string]*storage.LoadRecord{"pg": loaded(republished), "es": nil})

	require.NoError(t, err)
	assert.Empty(t, unchecked)
}
