package main

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/storage"
)

var (
	pooledCorpus      = storage.PackageRef{Name: "fixture-canonical", Version: "v1", ResourceHash: "sha256:pooled"}
	republishedCorpus = storage.PackageRef{Name: "fixture-canonical", Version: "v1", ResourceHash: "sha256:republished"}
)

// loadedTrack is a judged track whose engines a and b recorded a load of
// pooledCorpus when it was pooled; b is returned so a test can reload it.
func loadedTrack(t *testing.T) (lineageTrack, *fakeIndex) {
	t.Helper()
	relevant, other := uuid.New(), uuid.New()
	ranked := map[string][]uuid.UUID{"q1": {relevant, other}, "q2": {relevant}}
	b := &fakeIndex{count: 10, ranked: ranked, load: &storage.LoadRecord{Canonical: pooledCorpus}}
	cluster := fakeCluster(t, map[string]*fakeIndex{
		"a": {count: 10, ranked: ranked, load: &storage.LoadRecord{Canonical: pooledCorpus}},
		"b": b,
	})
	tr := newLineageTrack(t, cluster)
	pf := tr.pool(t)
	require.NotNil(t, pf.Meta.Canonical, "the pool records the canonical package its engines held")
	tr.judgeEveryPooledDoc(t, pf, 2)
	return tr, b
}

func TestRun_RefusesAnEngineHoldingAnotherCorpusThanThePool(t *testing.T) {
	tr, b := loadedTrack(t)
	b.load = &storage.LoadRecord{Canonical: republishedCorpus}
	before := seedLatest(t, tr)

	_, err := execute(t, newRunCmd(), tr.dir)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "sha256:pooled")
	assert.Contains(t, err.Error(), "b has fixture-canonical v1 (sha256:republished)")
	assert.Empty(t, tr.reports(t))
	assertLatestUnchanged(t, tr, before)
}

func TestRun_ScoresEnginesHoldingThePoolsCorpus(t *testing.T) {
	tr, _ := loadedTrack(t)

	out, err := execute(t, newRunCmd(), tr.dir)

	require.NoError(t, err, out)
	assert.NotContains(t, out, "corpus not checked")
	assert.Len(t, tr.reports(t), 1)
}

func TestRun_WarnsWhenTheCorpusCannotBeChecked(t *testing.T) {
	t.Run("the pool records no canonical package", func(t *testing.T) {
		tr := judgedTrack(t, nil)

		out, err := execute(t, newRunCmd(), tr.dir)

		require.NoError(t, err, out)
		assert.Contains(t, out, "corpus not checked")
		assert.Contains(t, out, "names no canonical package")
	})

	t.Run("an engine records no load", func(t *testing.T) {
		tr, b := loadedTrack(t)
		b.load = nil

		out, err := execute(t, newRunCmd(), tr.dir)

		require.NoError(t, err, out)
		assert.Contains(t, out, "corpus not checked")
		assert.Contains(t, out, "engines b record no corpus load")
	})
}

func TestRun_LatencyOnlyIsNotGatedOnTheCorpus(t *testing.T) {
	tr, b := loadedTrack(t)
	b.load = &storage.LoadRecord{Canonical: republishedCorpus}

	out, err := execute(t, newRunCmd(), tr.dir, "--latency-only")

	require.NoError(t, err, out)
	assert.Len(t, tr.reports(t), 1)
}
