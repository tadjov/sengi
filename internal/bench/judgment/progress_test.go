package judgment

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/bench/meta"
	"github.com/tadjov/sengi/internal/bench/pool"
	"github.com/tadjov/sengi/internal/progress/progresstest"
	"github.com/tadjov/sengi/internal/types/document"
)

func TestRunner_CountsEveryPooledDocAgainstThePoolTotal(t *testing.T) {
	tests := []struct {
		name     string
		strategy Strategy
		// stored is how many of the pool's docs the store can read.
		stored int
		// prior is how many docs an earlier run already graded.
		prior int
	}{
		{name: "one doc per request", strategy: &fakeStrategy{name: "doc", grade: 2}},
		{name: "failing grades left unjudged", strategy: &fakeStrategy{name: "doc", err: errors.New("rate limited")}},
		{name: "resumed", strategy: &fakeStrategy{name: "doc", grade: 2}, prior: 9},
		{name: "articles missing from the store", strategy: &fakeStrategy{name: "doc", grade: 2}, stored: 15},
		{name: "whole pool", strategy: NewBM25Strategy()},
		{name: "whole pool resumed", strategy: NewBM25Strategy(), prior: 9},
		{name: "whole pool with articles missing from the store", strategy: NewBM25Strategy(), stored: 15},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pf, articles := twoQueryPool(t, 25, 12)
			if tt.stored > 0 {
				articles = keepFirst(pf, articles, tt.stored)
			}
			counter := &progresstest.Recorder{}

			_, err := NewRunner(RunnerConfig{
				Strategy: tt.strategy,
				Reader:   &stubStorageReader{articles: articles},
				Existing: priorGrades(pf, tt.prior),
				Progress: counter,
			}).Run(context.Background(), pf)

			require.NoError(t, err)
			assert.Equal(t, int64(37), counter.Total())
			assert.Equal(t, counter.Total(), counter.Count())
		})
	}
}

func TestRunner_CountsAQueryWithNoUsableTermsInFull(t *testing.T) {
	pf, articles := twoQueryPool(t, 4, 3)
	pf.Queries[0].QueryText = "the of and"
	counter := &progresstest.Recorder{}

	_, err := NewRunner(RunnerConfig{
		Strategy: NewKeywordStrategy(),
		Reader:   &stubStorageReader{articles: articles},
		Progress: counter,
	}).Run(context.Background(), pf)

	require.True(t, FailsWholeQuery(err), "want a whole-query failure, got %v", err)
	assert.Equal(t, int64(7), counter.Count())
}

func twoQueryPool(t *testing.T, first, second int) (*pool.PoolFile, map[uuid.UUID]document.Article) {
	t.Helper()
	articles := map[uuid.UUID]document.Article{}
	entry := func(id string, n int) pool.PoolEntry {
		e := pool.PoolEntry{QueryID: id, QueryText: "climate policy"}
		for range n {
			docID := uuid.New()
			articles[docID] = document.Article{ID: docID, Title: "climate policy"}
			e.Docs = append(e.Docs, pool.PooledDoc{DocID: docID, Sources: []string{"pg"}})
		}
		return e
	}
	pf := &pool.PoolFile{SuiteName: "t", Queries: []pool.PoolEntry{entry("q1", first)}}
	if second > 0 {
		pf.Queries = append(pf.Queries, entry("q2", second))
	}
	return pf, articles
}

func keepFirst(pf *pool.PoolFile, articles map[uuid.UUID]document.Article, n int) map[uuid.UUID]document.Article {
	kept := map[uuid.UUID]document.Article{}
	for _, entry := range pf.Queries {
		for _, doc := range entry.Docs {
			if len(kept) < n {
				kept[doc.DocID] = articles[doc.DocID]
			}
		}
	}
	return kept
}

func priorGrades(pf *pool.PoolFile, n int) *File {
	if n == 0 {
		return nil
	}
	entry := Entry{QueryID: pf.Queries[0].QueryID}
	for _, doc := range pf.Queries[0].Docs[:n] {
		entry.Docs = append(entry.Docs, GradedDoc{DocID: doc.DocID, Grade: 2})
	}
	return &File{Meta: meta.Meta{Judge: &meta.Judge{Strategy: "doc"}}, Queries: []Entry{entry}}
}
