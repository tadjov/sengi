package judgment

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/bench/pool"
	"github.com/tadjov/sengi/internal/types/document"

	"github.com/tadjov/sengi/internal/bench/meta"
)

type stubStorageReader struct {
	articles map[uuid.UUID]document.Article
	err      error
}

func (s *stubStorageReader) GetByIDs(_ context.Context, ids []uuid.UUID) ([]document.Article, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([]document.Article, 0, len(ids))
	for _, id := range ids {
		if a, ok := s.articles[id]; ok {
			out = append(out, a)
		}
	}
	return out, nil
}

type fakeStrategy struct {
	name  string
	grade int
	err   error
	calls atomic.Int64
}

func (f *fakeStrategy) Describe() meta.Judge {
	return meta.Judge{Strategy: f.name}
}
func (f *fakeStrategy) Grade(_ context.Context, _ GradingQuery, _ GradingDoc) (int, error) {
	f.calls.Add(1)
	return f.grade, f.err
}

func buildPool(t *testing.T, n int) (*pool.PoolFile, map[uuid.UUID]document.Article) {
	t.Helper()
	articles := make(map[uuid.UUID]document.Article, n)
	pe := pool.PoolEntry{QueryID: "q1", QueryText: "test"}
	for i := 0; i < n; i++ {
		id := uuid.New()
		articles[id] = document.Article{ID: id, Title: "title"}
		pe.Docs = append(pe.Docs, pool.PooledDoc{DocID: id, Sources: []string{"pg"}})
	}
	return &pool.PoolFile{SuiteName: "t", Queries: []pool.PoolEntry{pe}}, articles
}

func TestRunner_ResumeSkipsAlreadyGraded(t *testing.T) {
	pf, articles := buildPool(t, 5)
	strategy := &fakeStrategy{name: "fake", grade: 2}

	// Pre-grade the first 3 docs
	prior := &File{
		Meta: meta.Meta{Judge: &meta.Judge{Strategy: "fake"}},
		Queries: []Entry{
			{
				QueryID: "q1",
				Docs: []GradedDoc{
					{DocID: pf.Queries[0].Docs[0].DocID, Grade: 3},
					{DocID: pf.Queries[0].Docs[1].DocID, Grade: 3},
					{DocID: pf.Queries[0].Docs[2].DocID, Grade: 3},
				},
			},
		},
	}

	r := NewRunner(RunnerConfig{
		Strategy: strategy,
		Reader:   &stubStorageReader{articles: articles},
		Existing: prior,
	})
	jf, err := r.Run(context.Background(), pf)
	require.NoError(t, err)
	require.Len(t, jf.Queries[0].Docs, 5)

	// The 3 prior grades survive, the new 2 come from the strategy (grade=2)
	assert.Equal(t, 3, jf.Queries[0].Docs[0].Grade)
	assert.Equal(t, 3, jf.Queries[0].Docs[1].Grade)
	assert.Equal(t, 3, jf.Queries[0].Docs[2].Grade)
	assert.Equal(t, 2, jf.Queries[0].Docs[3].Grade)
	assert.Equal(t, 2, jf.Queries[0].Docs[4].Grade)
	assert.Equal(t, int64(2), strategy.calls.Load(), "only the 2 remaining docs are graded")
}

func TestRunner_PerDocPathStillWorks(t *testing.T) {
	pf, articles := buildPool(t, 3)
	r := NewRunner(RunnerConfig{
		Strategy: &fakeStrategy{name: "fake", grade: 1},
		Reader:   &stubStorageReader{articles: articles},
	})
	jf, err := r.Run(context.Background(), pf)
	require.NoError(t, err)
	for _, d := range jf.Queries[0].Docs {
		assert.Equal(t, 1, d.Grade)
	}
}

func TestRunner_MissingArticleStaysUnjudged(t *testing.T) {
	pf, articles := buildPool(t, 3)
	// Remove one article from the reader's view
	missingID := pf.Queries[0].Docs[1].DocID
	delete(articles, missingID)

	r := NewRunner(RunnerConfig{
		Strategy: &fakeStrategy{name: "fake", grade: 2},
		Reader:   &stubStorageReader{articles: articles},
	})
	jf, err := r.Run(context.Background(), pf)
	require.NoError(t, err)

	for _, d := range jf.Queries[0].Docs {
		if d.DocID == missingID {
			assert.Equal(t, GradeUnjudged, d.Grade)
		}
	}
}

func TestRunner_SinkCalledPerQuery(t *testing.T) {
	pf, articles := buildPool(t, 2)
	calls := 0
	r := NewRunner(RunnerConfig{
		Strategy: &fakeStrategy{name: "fake", grade: 2},
		Reader:   &stubStorageReader{articles: articles},
		Sink: func(e Entry) error {
			assert.Equal(t, "q1", e.QueryID)
			calls++
			return nil
		},
	})
	_, err := r.Run(context.Background(), pf)
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

// A query with no usable terms fails the run, but only after every other query
// is graded, so the file still lands and names what is missing.
func TestRunner_QueryWithoutTermsFailsTheRunAfterGradingTheRest(t *testing.T) {
	for _, strategy := range []Strategy{NewKeywordStrategy(), NewBM25Strategy()} {
		t.Run(strategy.Describe().Strategy, func(t *testing.T) {
			pf, articles := buildPool(t, 3)
			pf.Queries[0].QueryText = "the of a"
			usable := pool.PoolEntry{QueryID: "q2", QueryText: "title"}
			for id := range articles {
				usable.Docs = append(usable.Docs, pool.PooledDoc{DocID: id})
			}
			pf.Queries = append(pf.Queries, usable)

			sunk := map[string]bool{}
			r := NewRunner(RunnerConfig{
				Strategy: strategy,
				Reader:   &stubStorageReader{articles: articles},
				Sink: func(e Entry) error {
					sunk[e.QueryID] = true
					return nil
				},
			})
			jf, err := r.Run(context.Background(), pf)

			var noTerms *NoQueryTermsError
			require.ErrorAs(t, err, &noTerms)
			assert.Equal(t, "q1", noTerms.QueryID)
			require.NotNil(t, jf)
			grades := map[string][]int{}
			for _, qe := range jf.Queries {
				for _, d := range qe.Docs {
					grades[qe.QueryID] = append(grades[qe.QueryID], d.Grade)
				}
			}
			assert.Equal(t, []int{GradeUnjudged, GradeUnjudged, GradeUnjudged}, grades["q1"])
			assert.NotContains(t, grades["q2"], GradeUnjudged)
			assert.Len(t, grades["q2"], 3)
			assert.True(t, sunk["q1"] && sunk["q2"], "both queries reach the sink")
		})
	}
}

// Every judge bench judge runs through the runner grades the query's text, so
// a pool entry without one fails the run before anything is graded.
func TestRunner_PoolQueryWithoutTextFailsBeforeGrading(t *testing.T) {
	pf, articles := buildPool(t, 3)
	graded := pool.PoolEntry{QueryID: "q0", QueryText: "title", Docs: pf.Queries[0].Docs}
	pf.Queries[0].QueryText = ""
	pf.Queries[0].QueryDesc = "A query described but never written"
	pf.Queries = append([]pool.PoolEntry{graded}, pf.Queries...)
	sunk := false

	_, err := NewRunner(RunnerConfig{
		Strategy: &fakeStrategy{name: "fake", grade: 2},
		Reader:   &stubStorageReader{articles: articles},
		Sink:     func(Entry) error { sunk = true; return nil },
	}).Run(context.Background(), pf)

	require.Error(t, err)
	assert.Contains(t, err.Error(), `"q1"`)
	assert.False(t, sunk, "no query may be graded once one has no text")
}

// A boolean text that does not parse is a property of its query, like one
// without terms: that query stays unjudged and the others are graded.
func TestRunner_UnparsableBooleanTextFailsTheRunAfterGradingTheRest(t *testing.T) {
	pf, articles := buildPool(t, 3)
	pf.Queries[0].QueryText = "title AND (unclosed"
	pf.Queries[0].Category = "boolean"
	usable := pool.PoolEntry{QueryID: "q2", QueryText: "title"}
	for id := range articles {
		usable.Docs = append(usable.Docs, pool.PooledDoc{DocID: id})
	}
	pf.Queries = append(pf.Queries, usable)

	jf, err := NewRunner(RunnerConfig{Strategy: NewBM25Strategy(), Reader: &stubStorageReader{articles: articles}}).Run(context.Background(), pf)

	var unparsed *BooleanTextError
	require.ErrorAs(t, err, &unparsed)
	assert.Equal(t, "q1", unparsed.QueryID)
	require.NotNil(t, jf)
	for _, qe := range jf.Queries {
		for _, d := range qe.Docs {
			if qe.QueryID == "q1" {
				assert.Equal(t, GradeUnjudged, d.Grade)
			} else {
				assert.NotEqual(t, GradeUnjudged, d.Grade)
			}
		}
	}
}
