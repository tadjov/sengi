package judgment

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/bench/meta"
	"github.com/tadjov/sengi/internal/bench/pool"
	"github.com/tadjov/sengi/internal/bench/suite"
	"github.com/tadjov/sengi/internal/storage"
)

var (
	kept    = uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	dropped = uuid.MustParse("00000000-0000-0000-0000-00000000000b")
	added   = uuid.MustParse("00000000-0000-0000-0000-00000000000c")
	failed  = uuid.MustParse("00000000-0000-0000-0000-00000000000d")
)

var sonnet = meta.Judge{Strategy: "llm", Provider: "claude-cli", Model: "claude-sonnet-5-5", PromptVersion: "v3"}

var (
	canonicalV1 = &storage.PackageRef{Name: "cc-news-canonical", Version: "v1", ResourceHash: "sha256:aaaa"}
	canonicalV2 = &storage.PackageRef{Name: "cc-news-canonical", Version: "v2", ResourceHash: "sha256:bbbb"}
)

func carryPool(runID string, entries ...pool.PoolEntry) *pool.PoolFile {
	return &pool.PoolFile{Meta: meta.Meta{RunID: runID}, Queries: entries}
}

func onCanonical(pf *pool.PoolFile, canonical *storage.PackageRef) *pool.PoolFile {
	pf.Meta.Canonical = canonical
	return pf
}

func pooled(id string, text string, docs ...uuid.UUID) pool.PoolEntry {
	entry := pool.PoolEntry{QueryID: id, QueryText: text, Category: suite.CategoryKeyword}
	for _, doc := range docs {
		entry.Docs = append(entry.Docs, pool.PooledDoc{DocID: doc})
	}
	return entry
}

func oldJudgments(judge meta.Judge, poolRunID string) *File {
	return &File{
		Meta: meta.Meta{RunID: "old-judge", PoolRunID: poolRunID, Judge: &judge},
		Queries: []Entry{{QueryID: "q1", Docs: []GradedDoc{
			{DocID: kept, Grade: 3}, {DocID: dropped, Grade: 1}, {DocID: failed, Grade: GradeUnjudged},
		}}},
	}
}

func TestCarryOver_KeepsGradesOfDocsStillPooled(t *testing.T) {
	carried, err := CarryOver(CarryOverInput{
		Prior:     oldJudgments(sonnet, "old-pool"),
		PriorPool: carryPool("old-pool", pooled("q1", "flu vacine", kept, dropped, failed)),
		Pool:      carryPool("new-pool", pooled("q1", "flu vacine", kept, added, failed)),
		Judge:     sonnet,
	})

	require.NoError(t, err)
	assert.Equal(t, map[string]map[uuid.UUID]int{"q1": {kept: 3}}, indexExisting(carried.Grades))
	assert.Equal(t, 1, carried.Count)
	assert.Empty(t, carried.ChangedQueries)
}

func TestCarryOver_GradesAChangedQueryAgain(t *testing.T) {
	tests := []struct {
		name   string
		change func(*pool.PoolEntry)
	}{
		{"text", func(e *pool.PoolEntry) { e.QueryText = "flu vaccine" }},
		{"category", func(e *pool.PoolEntry) { e.Category = suite.CategoryTypo }},
		{"filters", func(e *pool.PoolEntry) { e.Filters = suite.Filters{PublisherDomain: "bbc.co.uk"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := pooled("q1", "flu vacine", kept)
			tt.change(&now)

			carried, err := CarryOver(CarryOverInput{
				Prior:     oldJudgments(sonnet, "old-pool"),
				PriorPool: carryPool("old-pool", pooled("q1", "flu vacine", kept)),
				Pool:      carryPool("new-pool", now),
				Judge:     sonnet,
			})

			require.NoError(t, err)
			assert.Zero(t, carried.Count)
			assert.Equal(t, []string{"q1"}, carried.ChangedQueries)
		})
	}
}

func TestCarryOver_Refuses(t *testing.T) {
	opus := sonnet
	opus.Model = "claude-opus-5-5"
	batched := sonnet
	batched.Batching = &meta.Batching{Size: 20, Seed: 1}
	tests := []struct {
		name           string
		prior          *File
		priorPool      string
		priorCanonical *storage.PackageRef
		canonical      *storage.PackageRef
		judge          meta.Judge
		wantErr        string
	}{
		{name: "judgments of another pool", prior: oldJudgments(sonnet, "other-pool"), priorPool: "old-pool", judge: sonnet, wantErr: "other-pool"},
		{name: "judgments without a pool", prior: oldJudgments(sonnet, ""), priorPool: "old-pool", judge: sonnet, wantErr: "no pool_run_id"},
		{name: "another judge", prior: oldJudgments(sonnet, "old-pool"), priorPool: "old-pool", judge: opus, wantErr: "another judge"},
		{name: "batched grades", prior: oldJudgments(batched, "old-pool"), priorPool: "old-pool", judge: batched, wantErr: "batch"},
		{
			name: "another canonical package", prior: oldJudgments(sonnet, "old-pool"), priorPool: "old-pool",
			priorCanonical: canonicalV1, canonical: canonicalV2, judge: sonnet, wantErr: "sha256:bbbb",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CarryOver(CarryOverInput{
				Prior:     tt.prior,
				PriorPool: onCanonical(carryPool(tt.priorPool, pooled("q1", "flu vacine", kept)), tt.priorCanonical),
				Pool:      onCanonical(carryPool("new-pool", pooled("q1", "flu vacine", kept)), tt.canonical),
				Judge:     tt.judge,
			})

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestCarryOver_ChecksTheCorpusOnlyWhenBothPoolsNameIt(t *testing.T) {
	tests := []struct {
		name           string
		priorCanonical *storage.PackageRef
		canonical      *storage.PackageRef
		wantChecked    bool
	}{
		{"same canonical package", canonicalV1, canonicalV1, true},
		{"old pool names none", nil, canonicalV1, false},
		{"new pool names none", canonicalV1, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			carried, err := CarryOver(CarryOverInput{
				Prior:     oldJudgments(sonnet, "old-pool"),
				PriorPool: onCanonical(carryPool("old-pool", pooled("q1", "flu vacine", kept)), tt.priorCanonical),
				Pool:      onCanonical(carryPool("new-pool", pooled("q1", "flu vacine", kept)), tt.canonical),
				Judge:     sonnet,
			})

			require.NoError(t, err)
			assert.Equal(t, map[string]map[uuid.UUID]int{"q1": {kept: 3}}, indexExisting(carried.Grades))
			assert.Equal(t, tt.wantChecked, carried.CorpusChecked)
		})
	}
}

func TestMergeGrades_LaterWinsButUnjudgedKeepsEarlier(t *testing.T) {
	earlier := &File{Queries: []Entry{{QueryID: "q1", Docs: []GradedDoc{{DocID: kept, Grade: 3}, {DocID: dropped, Grade: 2}}}}}
	later := &File{Queries: []Entry{
		{QueryID: "q1", Docs: []GradedDoc{{DocID: kept, Grade: GradeUnjudged}, {DocID: dropped, Grade: 0}}},
		{QueryID: "q2", Docs: []GradedDoc{{DocID: added, Grade: 1}}},
	}}

	merged := MergeGrades(earlier, later)

	assert.Equal(t, map[string]map[uuid.UUID]int{"q1": {kept: 3, dropped: 0}, "q2": {added: 1}}, indexExisting(merged))
}
