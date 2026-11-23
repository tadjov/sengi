package judgment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/tadjov/sengi/internal/bench/meta"
	"github.com/tadjov/sengi/internal/bench/pool"
	"github.com/tadjov/sengi/internal/progress"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/types/document"
)

const (
	defaultConcurrency = 4
	minConcurrency     = 1
	maxConcurrency     = 32
)

// QueryProgress reports a single query's outcome.
type QueryProgress struct {
	QueryID   string
	Graded    int
	Skipped   int // already-graded docs skipped via resume
	Unjudged  int // strategy returned no grade (failures)
	Histogram map[int]int
}

type RunnerConfig struct {
	Strategy    Strategy
	Reader      storage.Reader
	Concurrency int
	// Existing holds prior grades; a doc it grades is not graded again.
	Existing *File
	// Sink receives each query's entry as it finishes, so the caller can
	// persist incrementally.
	Sink func(Entry) error
	// OnQueryStart and OnQueryDone are optional per-query callbacks.
	OnQueryStart func(queryID string, docCount int, alreadyGraded int)
	OnQueryDone  func(QueryProgress)
	// Progress counts pooled docs as their grading finishes, graded or left
	// unjudged, against the pool's total; nil counts nothing.
	Progress progress.Counter
}

// Runner grades a pool query by query and skips the docs Existing already grades.
type Runner struct {
	cfg      RunnerConfig
	progress progress.Counter
}

func NewRunner(cfg RunnerConfig) *Runner {
	if cfg.Concurrency < minConcurrency {
		cfg.Concurrency = defaultConcurrency
	}
	if cfg.Concurrency > maxConcurrency {
		cfg.Concurrency = maxConcurrency
	}
	return &Runner{cfg: cfg, progress: progress.Or(cfg.Progress)}
}

// Run grades every pool entry. A query with no usable terms, or with a boolean
// text that does not parse, leaves its docs unjudged without stopping the
// others: Run then returns the file together with an error that joins a
// NoQueryTermsError or BooleanTextError per such query.
func (r *Runner) Run(ctx context.Context, pf *pool.PoolFile) (*File, error) {
	if err := requireQueryText(pf); err != nil {
		return nil, err
	}

	prior := indexExisting(r.cfg.Existing)

	judge := r.cfg.Strategy.Describe()
	jf := &File{
		Meta:    meta.Meta{Judge: &judge},
		Queries: make([]Entry, 0, len(pf.Queries)),
	}

	var pooled int64
	for _, entry := range pf.Queries {
		pooled += int64(len(entry.Docs))
	}
	r.progress.SetTotal(pooled)

	var unusable []error
	for _, entry := range pf.Queries {
		ge, prog, err := r.gradeQuery(ctx, entry, prior[entry.QueryID])
		if FailsWholeQuery(err) {
			unusable = append(unusable, err)
		} else if err != nil {
			return nil, fmt.Errorf("grade query %q: %w", entry.QueryID, err)
		}
		jf.Queries = append(jf.Queries, ge)

		if r.cfg.Sink != nil {
			if err := r.cfg.Sink(ge); err != nil {
				return nil, fmt.Errorf("sink query %q: %w", entry.QueryID, err)
			}
		}
		if r.cfg.OnQueryDone != nil {
			r.cfg.OnQueryDone(prog)
		}
	}
	return jf, errors.Join(unusable...)
}

func requireQueryText(pf *pool.PoolFile) error {
	var missing []string
	for _, entry := range pf.Queries {
		if entry.QueryText == "" {
			missing = append(missing, strconv.Quote(entry.QueryID))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("pool queries %s have no text to grade: give each suite query its text and re-pool", strings.Join(missing, ", "))
	}
	return nil
}

// gradeQuery returns a whole-query failure together with the entry, its
// ungraded docs unjudged.
func (r *Runner) gradeQuery(ctx context.Context, entry pool.PoolEntry, prior map[uuid.UUID]int) (Entry, QueryProgress, error) {
	ge := Entry{QueryID: entry.QueryID, Docs: make([]GradedDoc, 0, len(entry.Docs))}
	prog := QueryProgress{QueryID: entry.QueryID, Histogram: map[int]int{}}

	if len(entry.Docs) == 0 {
		return ge, prog, nil
	}

	var todo, kept []pool.PooledDoc
	for _, pd := range entry.Docs {
		if g, ok := prior[pd.DocID]; ok && g >= 0 {
			kept = append(kept, pd)
		} else {
			todo = append(todo, pd)
		}
	}
	// A pool grade is relative to the pool the prior run could read, so a
	// query with anything left to grade is regraded whole.
	if _, wholePool := r.cfg.Strategy.(PoolStrategy); wholePool && len(todo) > 0 {
		todo, kept = entry.Docs, nil
	}
	for _, pd := range kept {
		g := prior[pd.DocID]
		ge.Docs = append(ge.Docs, GradedDoc{DocID: pd.DocID, Grade: g})
		prog.Skipped++
		prog.Histogram[g]++
	}
	r.progress.Add(int64(len(kept)))
	if r.cfg.OnQueryStart != nil {
		r.cfg.OnQueryStart(entry.QueryID, len(entry.Docs), prog.Skipped)
	}
	if len(todo) == 0 {
		return ge, prog, nil
	}

	docs, err := r.readableDocs(ctx, entry.QueryID, todo)
	if err != nil {
		return ge, prog, err
	}
	r.progress.Add(int64(len(todo) - len(docs)))

	grades, err := r.grade(ctx, NewGradingQuery(entry), docs)
	if err != nil && !FailsWholeQuery(err) {
		return ge, prog, err
	}

	for _, pd := range todo {
		grade, ok := grades[pd.DocID]
		if !ok {
			grade = GradeUnjudged
			prog.Unjudged++
		} else {
			prog.Graded++
			prog.Histogram[grade]++
		}
		ge.Docs = append(ge.Docs, GradedDoc{DocID: pd.DocID, Grade: grade})
	}
	return ge, prog, err
}

// readableDocs returns the pooled docs the store holds, in pool order; a doc
// it cannot read is logged and left out.
func (r *Runner) readableDocs(ctx context.Context, queryID string, pooled []pool.PooledDoc) ([]GradingDoc, error) {
	ids := make([]uuid.UUID, len(pooled))
	for i, pd := range pooled {
		ids[i] = pd.DocID
	}
	articles, err := r.cfg.Reader.GetByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("fetch docs: %w", err)
	}
	stored := make(map[uuid.UUID]document.Article, len(articles))
	for _, a := range articles {
		stored[a.ID] = a
	}
	docs := make([]GradingDoc, 0, len(pooled))
	for _, id := range ids {
		a, ok := stored[id]
		if !ok {
			slog.Warn("article not found during enrichment; leaving unjudged",
				"query", queryID, "doc_id", id)
			continue
		}
		docs = append(docs, NewGradingDoc(a))
	}
	return docs, nil
}

// grade returns the grades by doc id; a doc without one stays unjudged.
func (r *Runner) grade(ctx context.Context, q GradingQuery, docs []GradingDoc) (map[uuid.UUID]int, error) {
	if ps, ok := r.cfg.Strategy.(PoolStrategy); ok {
		return r.gradePool(ctx, ps, q, docs)
	}
	return r.gradeDocs(ctx, r.cfg.Strategy.(DocStrategy), q, docs)
}

// gradePool has no per-doc fallback, since a doc graded alone is normalised
// against itself: when the strategy fails, the query's docs stay unjudged.
func (r *Runner) gradePool(ctx context.Context, ps PoolStrategy, q GradingQuery, docs []GradingDoc) (map[uuid.UUID]int, error) {
	graded, err := ps.GradePool(ctx, q, docs)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	r.progress.Add(int64(len(docs)))
	var width *VectorWidthError
	if errors.As(err, &width) || FailsWholeQuery(err) {
		return nil, err
	}
	if err != nil {
		slog.Warn("pool grading failed; leaving the query unjudged", "query", q.ID, "error", err)
		return nil, nil
	}
	grades := make(map[uuid.UUID]int, len(graded))
	for _, gd := range graded {
		grades[gd.DocID] = gd.Grade
	}
	return grades, nil
}

// gradeDocs leaves a doc unjudged when its grade fails, except for an error
// that fails every doc of the query, which is returned.
func (r *Runner) gradeDocs(ctx context.Context, s DocStrategy, q GradingQuery, docs []GradingDoc) (map[uuid.UUID]int, error) {
	grades := make(map[uuid.UUID]int, len(docs))
	sem := make(chan struct{}, r.cfg.Concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var queryErr error

	for _, d := range docs {
		wg.Add(1)
		go func(gd GradingDoc) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			defer r.progress.Add(1)

			grade, err := s.Grade(ctx, q, gd)
			if FailsWholeQuery(err) {
				mu.Lock()
				queryErr = err
				mu.Unlock()
				return
			}
			if err != nil {
				slog.Warn("grade failed; leaving unjudged",
					"query", q.ID, "doc_id", gd.ID, "error", err)
				return
			}
			mu.Lock()
			grades[gd.ID] = grade
			mu.Unlock()
		}(d)
	}
	wg.Wait()
	if queryErr != nil {
		return nil, queryErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return grades, nil
}

func indexExisting(jf *File) map[string]map[uuid.UUID]int {
	if jf == nil {
		return map[string]map[uuid.UUID]int{}
	}
	out := make(map[string]map[uuid.UUID]int, len(jf.Queries))
	for _, qe := range jf.Queries {
		m := make(map[uuid.UUID]int, len(qe.Docs))
		for _, d := range qe.Docs {
			m[d.DocID] = d.Grade
		}
		out[qe.QueryID] = m
	}
	return out
}
