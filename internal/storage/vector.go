package storage

import (
	"context"

	"github.com/google/uuid"
)

// VectorStore embeds queries with the model its stored document vectors were produced with.
type VectorStore interface {
	QueryVector(ctx context.Context, text string) ([]float32, error)

	// DocVectors leaves out ids that have no stored vector.
	DocVectors(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]float32, error)

	// Model lets a caller that reads document vectors by another route check its
	// own model: vectors of two models compare as valid arithmetic and meaningless ranking.
	Model() string
}

// HNSW build parameters, declared so both engines index at the same point
// rather than at their own defaults (pgvector 64, Elasticsearch 100). The
// CREATE INDEX in db/*migrations repeats them; the container tests check both
// engines against these. Changing either is a new run.
const (
	HNSWM              = 16
	HNSWEfConstruction = 64
)
