package native

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/tadjov/sengi/internal/embedding"
	"github.com/tadjov/sengi/internal/storage/pg"
	"github.com/tadjov/sengi/internal/storage/pg/pgtest"
	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/internal/types/language"
	dquery "github.com/tadjov/sengi/internal/types/query"
)

type staticEmbedder struct {
	vec []float32
}

func (s staticEmbedder) Generate(_ context.Context, _ embedding.Request) (*embedding.Response, error) {
	return &embedding.Response{Embedding: s.vec}, nil
}

func (s staticEmbedder) GenerateBatch(_ context.Context, _ embedding.BatchRequest) (*embedding.BatchResponse, error) {
	return &embedding.BatchResponse{Embeddings: [][]float32{s.vec}}, nil
}

func TestHybridSearcher_LexicalLegReadsTheQueryAsFreeText(t *testing.T) {
	if testPool == nil {
		t.Skip("requires Docker (testcontainers Postgres)")
	}
	bothTerms, budgetOnly := uuid.New(), uuid.New()
	articles := []document.Article{
		{ID: bothTerms, Title: "budget tariff", URL: "https://example.com/1", Language: language.English},
		{ID: budgetOnly, Title: "budget", URL: "https://example.com/2", Language: language.English},
	}
	pool, err := pg.NewConnectionPool(testCtx, pg.PoolConfig{
		ConnStr:          pgtest.Load(t, testServer, pgtest.Corpus{Articles: articles}),
		RegisterVecTypes: true,
	})
	if err != nil {
		t.Fatalf("NewConnectionPool: %v", err)
	}
	t.Cleanup(pool.Close)
	// No article has an embedding, so the vector leg is empty and the hits are
	// exactly the lexical leg.
	queryVector := make([]float32, 1024)
	for i := range queryVector {
		queryVector[i] = 0.9
	}
	searcher := NewHybridSearcher(embedding.NewEmbedder(staticEmbedder{vec: queryVector}), pool)

	tests := []struct {
		name string
		text string
	}{
		{name: "a leading minus is not negation", text: "budget -tariff"},
		{name: "or is a stopword, not an operator", text: "budget or tariff"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := searcher.SearchHybrid(testCtx, dquery.NewHybrid(tt.text, dquery.HybridOptions{}), &dquery.BaseOptions{Size: 10})
			if err != nil {
				t.Fatalf("SearchHybrid: %v", err)
			}
			got := make([]uuid.UUID, 0, len(res.Hits))
			for _, hit := range res.Hits {
				got = append(got, hit.Article.ID)
			}
			if !slices.Equal(got, []uuid.UUID{bothTerms}) {
				t.Errorf("hits = %v, want only %s, the article holding every term", got, bothTerms)
			}
		})
	}
}
