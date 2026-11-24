package judgment

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/google/uuid"
	"github.com/tadjov/sengi/internal/bench/meta"
	"github.com/tadjov/sengi/internal/storage"
)

// VectorStrategy grades the pool docs nearest the query by cosine as exact
// search's top N, N the track's largest K, so R@N against its grades is
// recall@N against exact search, provided the pool holds exact search's own top N.
type VectorStrategy struct {
	store         storage.VectorStore
	model         string
	relevantDepth int
}

func NewVectorStrategy(opts StrategyOptions) (*VectorStrategy, error) {
	if opts.VectorStore == nil {
		return nil, errNoVectorStore
	}
	return &VectorStrategy{store: opts.VectorStore, model: opts.EmbeddingModel, relevantDepth: opts.RelevantDepth}, nil
}

func (s VectorStrategy) Describe() meta.Judge {
	return meta.Judge{
		Strategy:      string(StrategyVector),
		Model:         s.model,
		RubricVersion: vectorRubricVersion,
		RelevantDepth: s.relevantDepth,
	}
}

const vectorRubricVersion = "v2"

// GradePool grades the relevantDepth docs nearest the query 3, ties broken by
// the lower id, and every other doc 0. A doc without a stored vector is left
// out, so the runner records it unjudged.
func (s VectorStrategy) GradePool(ctx context.Context, q GradingQuery, docs []GradingDoc) ([]GradedDoc, error) {
	if len(docs) == 0 {
		return nil, nil
	}
	qVec, vecs, err := s.vectors(ctx, q, docs)
	if err != nil {
		return nil, err
	}
	type nearness struct {
		id     uuid.UUID
		cosine float64
	}
	ranked := make([]nearness, 0, len(docs))
	for _, d := range docs {
		if dv, ok := vecs[d.ID]; ok {
			ranked = append(ranked, nearness{id: d.ID, cosine: cosine(qVec, dv)})
		}
	}
	slices.SortFunc(ranked, func(a, b nearness) int {
		return cmp.Or(cmp.Compare(b.cosine, a.cosine), bytes.Compare(a.id[:], b.id[:]))
	})
	out := make([]GradedDoc, len(ranked))
	for i, r := range ranked {
		grade := GradeNotRelev
		if i < s.relevantDepth {
			grade = GradeHighly
		}
		out[i] = GradedDoc{DocID: r.id, Grade: grade}
	}
	return out, nil
}

func (s VectorStrategy) vectors(ctx context.Context, q GradingQuery, docs []GradingDoc) ([]float32, map[uuid.UUID][]float32, error) {
	qVec, err := s.store.QueryVector(ctx, q.Text)
	if err != nil {
		return nil, nil, fmt.Errorf("embed query %q: %w", q.ID, err)
	}
	ids := make([]uuid.UUID, len(docs))
	for i, d := range docs {
		ids[i] = d.ID
	}
	vecs, err := s.store.DocVectors(ctx, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch doc vectors: %w", err)
	}
	if err := checkVectorWidths(q.ID, qVec, docs, vecs); err != nil {
		return nil, nil, err
	}
	return qVec, vecs, nil
}

// VectorWidthError reports a query vector and a stored document vector of
// different widths. The widths come from the models, so every document fails
// the same way and the judge runner stops on it.
type VectorWidthError struct {
	QueryID    string
	DocID      uuid.UUID
	QueryWidth int
	DocWidth   int
}

func (e *VectorWidthError) Error() string {
	if e.QueryWidth == 0 {
		return fmt.Sprintf("query %q embedded to an empty vector: the embedding endpoint returned no dimensions", e.QueryID)
	}
	return fmt.Sprintf(
		"query %q embeds to %d dimensions but document %s is stored with %d: the query embedder and the stored vectors are different models",
		e.QueryID, e.QueryWidth, e.DocID, e.DocWidth)
}

// checkVectorWidths walks the documents in pool order so the error names the
// same document on every run.
func checkVectorWidths(queryID string, qVec []float32, docs []GradingDoc, vecs map[uuid.UUID][]float32) error {
	if len(qVec) == 0 {
		return &VectorWidthError{QueryID: queryID}
	}
	for _, d := range docs {
		dv, ok := vecs[d.ID]
		if !ok {
			continue
		}
		if len(dv) != len(qVec) {
			return &VectorWidthError{QueryID: queryID, DocID: d.ID, QueryWidth: len(qVec), DocWidth: len(dv)}
		}
	}
	return nil
}

var errNoVectorStore = errors.New(
	"vector and hybrid strategies require a vector store: set --pg/PG_CONNECTION_STRING and an embedding endpoint (--embedding-base / EMBEDDING_BASE_URL)")

func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
