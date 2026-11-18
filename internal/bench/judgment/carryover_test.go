package judgment

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/tusker/internal/bench/meta"
	"github.com/tadjov/tusker/internal/bench/pool"
	"github.com/tadjov/tusker/internal/bench/suite"
)

var (
	kept    = uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	dropped = uuid.MustParse("00000000-0000-0000-0000-00000000000b")
	added   = uuid.MustParse("00000000-0000-0000-0000-00000000000c")
	failed  = uuid.MustParse("00000000-0000-0000-0000-00000000000d")
)

var sonnet = meta.Judge{Strategy: "llm", Provider: "claude-cli", Model: "claude-sonnet-5-5", PromptVersion: "v3"}

func carryPool(runID string, entries ...pool.PoolEntry) *pool.PoolFile {
	return &pool.PoolFile{Meta: meta.Meta{RunID: runID}, Queries: entries}
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
		name      string
		prior     *File
		priorPool string
		judge     meta.Judge
		wantErr   string
	}{
		{"judgments of another pool", oldJudgments(sonnet, "other-pool"), "old-pool", sonnet, "other-pool"},
		{"judgments without a pool", oldJudgments(sonnet, ""), "old-pool", sonnet, "no pool_run_id"},
		{"another judge", oldJudgments(sonnet, "old-pool"), "old-pool", opus, "another judge"},
		{"batched grades", oldJudgments(batched, "old-pool"), "old-pool", batched, "batch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CarryOver(CarryOverInput{
				Prior:     tt.prior,
				PriorPool: carryPool(tt.priorPool, pooled("q1", "flu vacine", kept)),
				Pool:      carryPool("new-pool", pooled("q1", "flu vacine", kept)),
				Judge:     tt.judge,
			})

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
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
