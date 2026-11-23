package es

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/tadjov/sengi/internal/embedding"
	"github.com/tadjov/sengi/internal/testcontainer"
	dquery "github.com/tadjov/sengi/internal/types/query"
)

const storedTag = "qwen3-embedding:0.6b@1024"

// staticEmbedder is an embedding.Client returning a fixed vector.
type staticEmbedder struct {
	vec []float32
}

func (s staticEmbedder) Generate(_ context.Context, _ embedding.Request) (*embedding.Response, error) {
	return &embedding.Response{Embedding: s.vec}, nil
}

func (s staticEmbedder) GenerateBatch(_ context.Context, _ embedding.BatchRequest) (*embedding.BatchResponse, error) {
	return &embedding.BatchResponse{Embeddings: [][]float32{s.vec}}, nil
}

func newSemanticTestEnv(t *testing.T, queryVec []float32) (ClientConfig, *SemanticSearcher) {
	t.Helper()

	container := testcontainer.NewESContainer(context.Background(), t)
	cfg := ClientConfig{Addresses: []string{container.Address}, IndexName: "articles_semantic_test"}

	qEmbedder := embedding.NewEmbedder(staticEmbedder{vec: queryVec})
	return cfg, NewSemanticSearcher(newTestClient(t, cfg), qEmbedder, qEmbedder.Model())
}

func TestSemanticSearcher_SearchSemantic_ReturnsNearest(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Docker (testcontainers ES)")
	}
	ctx := context.Background()

	near := vec(EmbeddingDims, 0.9)
	far := vec(EmbeddingDims, -0.9)

	cfg, searcher := newSemanticTestEnv(t, near)

	articles := titledArticles("near", "far")
	nearID, farID := articles[0].ID, articles[1].ID
	seedIndex(t, cfg, articles, []embedding.Vec{
		{ID: nearID, Model: storedTag, Embedding: near},
		{ID: farID, Model: storedTag, Embedding: far},
	})

	res, err := searcher.SearchSemantic(ctx, &dquery.Semantic{Query: "anything"}, &dquery.BaseOptions{Size: 1})
	if err != nil {
		t.Fatalf("SearchSemantic: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("expected at least one hit")
	}
	if res.Hits[0].ID != nearID {
		t.Errorf("nearest hit = %s, want %s", res.Hits[0].ID, nearID)
	}
}

func TestSemanticSearcher_SearchSemantic_OnlySeesTheQueryModel(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Docker (testcontainers ES)")
	}
	ctx := context.Background()

	queryVec := vec(EmbeddingDims, 0.9)
	nearQuery := vec(EmbeddingDims, 0.9)
	for i := len(nearQuery) / 2; i < len(nearQuery); i++ {
		nearQuery[i] = 0.5
	}

	cfg, searcher := newSemanticTestEnv(t, queryVec)

	articles := titledArticles("same model", "other model")
	sameModel, otherModel := articles[0].ID, articles[1].ID
	seedIndex(t, cfg, articles, []embedding.Vec{
		{ID: sameModel, Model: storedTag, Embedding: nearQuery},
		{ID: otherModel, Model: storedTag, Embedding: queryVec},
	})
	retagEmbedding(t, cfg, otherModel, embedding.DefaultModel)

	res, err := searcher.SearchSemantic(ctx, &dquery.Semantic{Query: "anything"}, &dquery.BaseOptions{Size: 1})
	if err != nil {
		t.Fatalf("SearchSemantic: %v", err)
	}

	got := make([]uuid.UUID, 0, len(res.Hits))
	for _, hit := range res.Hits {
		got = append(got, hit.ID)
	}
	if len(got) != 1 || got[0] != sameModel {
		t.Errorf("hits = %v, want only %s: a vector from another model must not be ranked, nor shrink k", got, sameModel)
	}
}
