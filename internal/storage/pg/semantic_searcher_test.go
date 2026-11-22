package pg_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/tadjov/sengi/internal/embedding"
	"github.com/tadjov/sengi/internal/storage/pg"
	"github.com/tadjov/sengi/internal/storage/pg/pgtest"
	"github.com/tadjov/sengi/internal/testcontainer"
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

func vec(dim int, fill float32) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = fill
	}
	return v
}

// loadedPool connects to a database of its own, loaded the way datapipe load does.
func loadedPool(t *testing.T, c pgtest.Corpus) *pg.ConnectionPool {
	t.Helper()
	ctx := context.Background()
	container := testcontainer.NewPGContainerWithCleanup(ctx, t)
	pool, err := pg.NewConnectionPool(ctx, pg.PoolConfig{
		ConnStr:          pgtest.Load(t, container.ConnString, c),
		RegisterVecTypes: true,
	})
	if err != nil {
		t.Fatalf("NewConnectionPool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestSemanticSearcher_SearchSemantic_OnlySeesTheQueryModel(t *testing.T) {
	ctx := context.Background()
	const storedTag = "qwen3-embedding:0.6b@1024"

	queryVec := vec(1024, 0.9)
	nearQuery := vec(1024, 0.9)
	for i := len(nearQuery) / 2; i < len(nearQuery); i++ {
		nearQuery[i] = 0.5
	}

	sameModel, otherModel := uuid.New(), uuid.New()
	var articles []document.Article
	for _, id := range []uuid.UUID{sameModel, otherModel} {
		articles = append(articles, document.Article{ID: id, Title: id.String(), URL: "http://test.com/" + id.String(), Language: language.English})
	}
	pool := loadedPool(t, pgtest.Corpus{
		Articles: articles,
		Vectors:  []embedding.Vec{{ID: sameModel, Embedding: nearQuery}, {ID: otherModel, Embedding: queryVec}},
		ModelTag: storedTag,
	})
	// A load tags every vector with the corpus model.
	_, err := pool.Pool().Exec(ctx, "UPDATE article_embeddings SET model_name = $1 WHERE article_id = $2", embedding.DefaultModel, otherModel)
	if err != nil {
		t.Fatalf("retag embedding: %v", err)
	}

	searcher := pg.NewSemanticSearcher(embedding.NewEmbedder(staticEmbedder{vec: queryVec}), pool)
	res, err := searcher.SearchSemantic(ctx, &dquery.Semantic{Query: "anything"}, &dquery.BaseOptions{Size: 1})
	if err != nil {
		t.Fatalf("SearchSemantic: %v", err)
	}

	got := make([]uuid.UUID, 0, len(res.Hits))
	for _, hit := range res.Hits {
		got = append(got, hit.ID)
	}
	if len(got) != 1 || got[0] != sameModel {
		t.Errorf("hits = %v, want only %s: a vector from another model must not be ranked, nor take a slot of the limit", got, sameModel)
	}
}
