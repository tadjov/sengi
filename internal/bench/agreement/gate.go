package agreement

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/tadjov/tusker/internal/bench/judgment"
	"github.com/tadjov/tusker/internal/bench/pool"
	"github.com/tadjov/tusker/internal/storage"
	"github.com/tadjov/tusker/internal/types/document"
)

// GateOptions configures Gate.
type GateOptions struct {
	Pairs []Pair
	// Pool holds each query's text, category and filters as the judges graded them.
	Pool   *pool.PoolFile
	Reader storage.Reader
}

// Gated is pairs split by the judges' phrase, boolean and filter gate.
type Gated struct {
	Admitted []Pair
	Refused  int
	Titles   map[uuid.UUID]string
}

// Gate reads each pair's article and keeps the pairs the gate admits, in the
// order given.
func Gate(ctx context.Context, opts GateOptions) (Gated, error) {
	queries := make(map[string]pool.PoolEntry, len(opts.Pool.Queries))
	for _, entry := range opts.Pool.Queries {
		queries[entry.QueryID] = entry
	}
	articles, err := opts.readArticles(ctx)
	if err != nil {
		return Gated{}, err
	}
	gated := Gated{Titles: make(map[uuid.UUID]string, len(articles))}
	for _, p := range opts.Pairs {
		entry, ok := queries[p.QueryID]
		if !ok {
			return Gated{}, fmt.Errorf("query %q is not in the pool, so its gate is unknown", p.QueryID)
		}
		article, ok := articles[p.DocID]
		if !ok {
			return Gated{}, fmt.Errorf("article %s of query %q is not in the store", p.DocID, p.QueryID)
		}
		gated.Titles[p.DocID] = article.Title
		admitted, err := judgment.GateAdmits(gradingQuery(entry), gradingDoc(article))
		if err != nil {
			return Gated{}, fmt.Errorf("gate query %q: %w", p.QueryID, err)
		}
		if admitted {
			gated.Admitted = append(gated.Admitted, p)
		} else {
			gated.Refused++
		}
	}
	return gated, nil
}

func (o GateOptions) readArticles(ctx context.Context) (map[uuid.UUID]document.Article, error) {
	seen := make(map[uuid.UUID]bool, len(o.Pairs))
	var ids []uuid.UUID
	for _, p := range o.Pairs {
		if !seen[p.DocID] {
			seen[p.DocID] = true
			ids = append(ids, p.DocID)
		}
	}
	found, err := o.Reader.GetByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("read %d articles: %w", len(ids), err)
	}
	articles := make(map[uuid.UUID]document.Article, len(found))
	for _, a := range found {
		articles[a.ID] = a
	}
	return articles, nil
}

func gradingQuery(entry pool.PoolEntry) judgment.GradingQuery {
	return judgment.GradingQuery{
		ID:          entry.QueryID,
		Text:        entry.QueryText,
		Description: entry.QueryDesc,
		Category:    entry.Category,
		Filters:     entry.Filters,
	}
}

func gradingDoc(a document.Article) judgment.GradingDoc {
	return judgment.GradingDoc{
		ID:              a.ID,
		Title:           a.Title,
		Description:     a.Description,
		Content:         a.Content,
		PublishedAt:     a.PublishedAt,
		PublisherDomain: a.PublisherDomain,
	}
}
