package es

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/tadjov/sengi/internal/embedding"
	"github.com/tadjov/sengi/internal/testcontainer"
	dquery "github.com/tadjov/sengi/internal/types/query"
)

func newHybridTestEnv(t *testing.T, queryVec []float32) (ClientConfig, *HybridSearcher) {
	t.Helper()

	container := testcontainer.NewESContainer(context.Background(), t)
	cfg := ClientConfig{Addresses: []string{container.Address}, IndexName: "articles_hybrid_test"}

	qEmbedder := embedding.NewEmbedder(staticEmbedder{vec: queryVec})
	return cfg, NewHybridSearcher(newTestClient(t, cfg), qEmbedder, qEmbedder.Model())
}

func TestHybridSearcher_SearchHybrid_RanksBothSignalsFirst(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Docker (testcontainers ES)")
	}
	ctx := context.Background()

	near := vec(EmbeddingDims, 0.9)
	far := vec(EmbeddingDims, -0.9)
	// Not a scaled copy of near: cosine ignores magnitude, so the two would tie
	// and the kNN leg would order them arbitrarily. RRF margins here are ~1e-5.
	midway := vec(EmbeddingDims, 0.9)
	for i := range midway[:EmbeddingDims/4] {
		midway[i] = -0.9
	}

	cfg, searcher := newHybridTestEnv(t, near)

	// bothID: strong lexical match AND near query vector.
	// lexicalOnlyID: lexical match but far vector.
	// vectorOnlyID: near vector but no lexical match.
	articles := titledArticles("climate change policy", "climate change report", "unrelated cooking recipe")
	bothID, lexicalOnlyID, vectorOnlyID := articles[0].ID, articles[1].ID, articles[2].ID
	seedIndex(t, cfg, articles, []embedding.Vec{
		{ID: bothID, Model: storedTag, Embedding: near},
		{ID: lexicalOnlyID, Model: storedTag, Embedding: far},
		{ID: vectorOnlyID, Model: storedTag, Embedding: midway},
	})

	res, err := searcher.SearchHybrid(ctx,
		dquery.NewHybrid("climate change", dquery.HybridOptions{}),
		&dquery.BaseOptions{Size: 10},
	)
	if err != nil {
		t.Fatalf("SearchHybrid: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("expected at least one hit")
	}
	if res.Hits[0].Article.ID != bothID {
		t.Errorf("top hit = %s, want %s (strong on both signals)", res.Hits[0].Article.ID, bothID)
	}
	if res.HasMore {
		t.Error("hybrid must return a single page (HasMore=false)")
	}
	if res.NextCursor != nil {
		t.Error("hybrid must not return a cursor")
	}
	if res.TotalMatches != int64(len(res.Hits)) {
		t.Errorf("TotalMatches = %d, want %d", res.TotalMatches, len(res.Hits))
	}
	if res.MaxScore <= 0 {
		t.Errorf("MaxScore = %f, want > 0", res.MaxScore)
	}
}

func TestHybridSearcher_SearchHybrid_OnlySeesTheQueryModel(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Docker (testcontainers ES)")
	}
	ctx := context.Background()

	queryVec := vec(EmbeddingDims, 0.9)
	cfg, searcher := newHybridTestEnv(t, queryVec)

	// Titles share no term with the query, so only the kNN leg can return them.
	articles := titledArticles("same model", "other model")
	sameModel, otherModel := articles[0].ID, articles[1].ID
	seedIndex(t, cfg, articles, []embedding.Vec{
		{ID: sameModel, Model: storedTag, Embedding: queryVec},
		{ID: otherModel, Model: storedTag, Embedding: queryVec},
	})
	retagEmbedding(t, cfg, otherModel, embedding.DefaultModel)

	res, err := searcher.SearchHybrid(ctx, dquery.NewHybrid("climate change", dquery.HybridOptions{}), &dquery.BaseOptions{Size: 10})
	if err != nil {
		t.Fatalf("SearchHybrid: %v", err)
	}

	got := make([]uuid.UUID, 0, len(res.Hits))
	for _, hit := range res.Hits {
		got = append(got, hit.Article.ID)
	}
	if len(got) != 1 || got[0] != sameModel {
		t.Errorf("hits = %v, want only %s: a vector from another model must not be ranked", got, sameModel)
	}
}
