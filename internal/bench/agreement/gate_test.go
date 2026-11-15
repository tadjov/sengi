package agreement_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/tusker/internal/bench/agreement"
	"github.com/tadjov/tusker/internal/bench/pool"
	"github.com/tadjov/tusker/internal/bench/suite"
	"github.com/tadjov/tusker/internal/types/document"
)

type articles map[uuid.UUID]document.Article

func (a articles) GetByIDs(_ context.Context, ids []uuid.UUID) ([]document.Article, error) {
	var out []document.Article
	for _, id := range ids {
		if article, ok := a[id]; ok {
			out = append(out, article)
		}
	}
	return out, nil
}

func TestGate_SplitsOffThePairsTheJudgesGradeZeroUnasked(t *testing.T) {
	holds, lacks, keyword := uuid.New(), uuid.New(), uuid.New()
	store := articles{
		holds:   {ID: holds, Title: "climate change summit"},
		lacks:   {ID: lacks, Title: "a change in the climate"},
		keyword: {ID: keyword, Title: "weather"},
	}
	queries := &pool.PoolFile{Queries: []pool.PoolEntry{
		{QueryID: "phrase", QueryText: "climate change", Category: suite.CategoryPhrase},
		{QueryID: "keyword", QueryText: "climate", Category: suite.CategoryKeyword},
	}}
	common := []agreement.Pair{
		{QueryID: "keyword", DocID: keyword, Grades: []int{0, 1}},
		{QueryID: "phrase", DocID: holds, Grades: []int{3, 3}},
		{QueryID: "phrase", DocID: lacks, Grades: []int{0, 0}},
	}

	got, err := agreement.Gate(context.Background(), agreement.GateOptions{Pairs: common, Pool: queries, Reader: store})
	require.NoError(t, err)

	assert.Equal(t, []agreement.Pair{common[0], common[1]}, got.Admitted)
	assert.Equal(t, 1, got.Refused)
	assert.Equal(t, "a change in the climate", got.Titles[lacks])
}

func TestGate_FailsOnAnArticleTheStoreLacks(t *testing.T) {
	missing := uuid.New()
	queries := &pool.PoolFile{Queries: []pool.PoolEntry{{QueryID: "q1", QueryText: "climate"}}}

	_, err := agreement.Gate(context.Background(), agreement.GateOptions{
		Pairs:  []agreement.Pair{{QueryID: "q1", DocID: missing, Grades: []int{0, 0}}},
		Pool:   queries,
		Reader: articles{},
	})

	assert.ErrorContains(t, err, missing.String())
}

func TestGate_FailsOnAQueryThePoolLacks(t *testing.T) {
	doc := uuid.New()

	_, err := agreement.Gate(context.Background(), agreement.GateOptions{
		Pairs:  []agreement.Pair{{QueryID: "gone", DocID: doc, Grades: []int{0, 0}}},
		Pool:   &pool.PoolFile{},
		Reader: articles{doc: {ID: doc, Title: "climate"}},
	})

	assert.ErrorContains(t, err, `"gone"`)
}
