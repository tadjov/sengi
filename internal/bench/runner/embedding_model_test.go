package runner

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/bench/spec"
	"github.com/tadjov/sengi/internal/storage"
)

// fixedModelStore embeds nothing — only the model it reports is under test.
type fixedModelStore struct{ model string }

func (s fixedModelStore) QueryVector(context.Context, string) ([]float32, error) { return nil, nil }
func (s fixedModelStore) DocVectors(context.Context, []uuid.UUID) (map[uuid.UUID][]float32, error) {
	return nil, nil
}
func (s fixedModelStore) Model() string { return s.model }

func engineDeclaring(model string) spec.Engine {
	eng := spec.Engine{Type: "postgres", Connection: "postgresql://localhost/news_db"}
	if model != "" {
		eng.Params = map[string]any{"embedding_model": model}
	}
	return eng
}

func TestVerifyEmbeddingModel(t *testing.T) {
	const loaded = "qwen3-embedding:0.6b"
	loadedWith := func(tag string) *storage.LoadRecord { return &storage.LoadRecord{ModelTag: tag} }

	tests := []struct {
		name    string
		engines map[string]spec.Engine
		models  EmbeddingModels
		// wantErr lists what the refusal must name; nil means no refusal.
		wantErr []string
	}{
		{
			name:    "declaration agrees with the query embedder",
			engines: map[string]spec.Engine{"pgvector-cosine": engineDeclaring(loaded)},
			models:  EmbeddingModels{Queries: fixedModelStore{model: loaded}},
		},
		{
			name:    "declaration disagrees with the query embedder",
			engines: map[string]spec.Engine{"pgvector-cosine": engineDeclaring("nomic-embed-text")},
			models:  EmbeddingModels{Queries: fixedModelStore{model: loaded}},
			wantErr: []string{"pgvector-cosine", "nomic-embed-text", loaded},
		},
		{
			name: "one arm of several disagrees",
			engines: map[string]spec.Engine{
				"pgvector-cosine": engineDeclaring(loaded),
				"pgvector-exact":  engineDeclaring("nomic-embed-text"),
				"elasticsearch":   engineDeclaring(loaded),
			},
			models:  EmbeddingModels{Queries: fixedModelStore{model: loaded}},
			wantErr: []string{"pgvector-exact", "nomic-embed-text", loaded},
		},
		{
			name:    "engine declares no model",
			engines: map[string]spec.Engine{"pg-gin": engineDeclaring("")},
			models:  EmbeddingModels{Queries: fixedModelStore{model: loaded}, Loads: EngineLoads{Records: map[string]*storage.LoadRecord{"pg-gin": loadedWith("nomic-embed-text")}}},
		},
		{
			name:    "no store, so nothing embeds a query",
			engines: map[string]spec.Engine{"pg-gin": engineDeclaring("nomic-embed-text")},
		},
		{
			name:    "declaration agrees with the engine's load",
			engines: map[string]spec.Engine{"pg-hnsw": engineDeclaring(loaded)},
			models:  EmbeddingModels{Queries: fixedModelStore{model: loaded}, Loads: EngineLoads{Records: map[string]*storage.LoadRecord{"pg-hnsw": loadedWith(loaded)}}},
		},
		{
			name:    "declaration disagrees with the engine's load",
			engines: map[string]spec.Engine{"pg-hnsw": engineDeclaring(loaded)},
			models:  EmbeddingModels{Queries: fixedModelStore{model: loaded}, Loads: EngineLoads{Records: map[string]*storage.LoadRecord{"pg-hnsw": loadedWith("nomic-embed-text")}}},
			wantErr: []string{"pg-hnsw", loaded, "nomic-embed-text"},
		},
		{
			name:    "the engine's load disagrees though nothing embeds a query",
			engines: map[string]spec.Engine{"pg-hnsw": engineDeclaring(loaded)},
			models:  EmbeddingModels{Loads: EngineLoads{Records: map[string]*storage.LoadRecord{"pg-hnsw": loadedWith("nomic-embed-text")}}},
			wantErr: []string{"pg-hnsw", loaded, "nomic-embed-text"},
		},
		{
			name:    "engine records no load",
			engines: map[string]spec.Engine{"es-knn": engineDeclaring(loaded)},
			models:  EmbeddingModels{Queries: fixedModelStore{model: loaded}, Loads: EngineLoads{Records: map[string]*storage.LoadRecord{"es-knn": nil}}},
			wantErr: []string{"es-knn", loaded, "no corpus load"},
		},
		{
			name:    "engine's index could not be read",
			engines: map[string]spec.Engine{"es-knn": engineDeclaring(loaded)},
			models:  EmbeddingModels{Queries: fixedModelStore{model: loaded}, Loads: EngineLoads{Unread: map[string]error{"es-knn": errors.New("context deadline exceeded")}}},
			wantErr: []string{"es-knn", loaded, "could not be read", "context deadline exceeded"},
		},
		{
			name:    "engine cannot describe its index",
			engines: map[string]spec.Engine{"api": engineDeclaring(loaded)},
			models:  EmbeddingModels{Queries: fixedModelStore{model: loaded}, Loads: EngineLoads{Records: map[string]*storage.LoadRecord{}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyEmbeddingModel(tt.engines, tt.models)
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, want := range tt.wantErr {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// A mismatch must stop the run before any engine is touched: the numbers such a
// run produces are valid arithmetic over two vector spaces and read as ordinary
// results.
func TestRunAllRejectsMismatchedEmbeddingModel(t *testing.T) {
	bs := &spec.BenchSpec{
		Kind:    spec.KindSemantic,
		Engines: map[string]spec.Engine{"pgvector-cosine": engineDeclaring("nomic-embed-text")},
		Jobs: []spec.Job{{
			Name:    "semantic",
			Suite:   "/nonexistent/suite.yaml",
			Engines: []string{"pgvector-cosine"},
		}},
	}
	r := New(Config{Runs: DefaultRuns, VectorStore: fixedModelStore{model: "qwen3-embedding:0.6b"}})

	result, err := r.RunAll(context.Background(), bs, nil)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "pgvector-cosine")
}

func TestRunAllRejectsAnEngineLoadedWithAnotherModel(t *testing.T) {
	bs := &spec.BenchSpec{
		Kind:    spec.KindSemantic,
		Engines: map[string]spec.Engine{"pgvector-cosine": engineDeclaring("qwen3-embedding:0.6b")},
		Jobs: []spec.Job{{
			Name:    "semantic",
			Suite:   "/nonexistent/suite.yaml",
			Engines: []string{"pgvector-cosine"},
		}},
	}
	r := New(Config{
		Runs:        DefaultRuns,
		VectorStore: fixedModelStore{model: "qwen3-embedding:0.6b"},
		Loads:       EngineLoads{Records: map[string]*storage.LoadRecord{"pgvector-cosine": {ModelTag: "nomic-embed-text"}}},
	})

	result, err := r.RunAll(context.Background(), bs, nil)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "nomic-embed-text")
}
