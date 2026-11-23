package es

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/tadjov/sengi/internal/embedding"
	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/internal/types/language"
)

// seedIndex loads the articles, and their vectors when given, the way datapipe load does.
func seedIndex(t *testing.T, cfg ClientConfig, articles []document.Article, vecs []embedding.Vec) {
	t.Helper()
	if _, err := loadCorpus(t, cfg.Addresses[0], cfg.IndexName, corpusOf(articles, vecs), len(articles)); err != nil {
		t.Fatalf("load corpus into %s: %v", cfg.IndexName, err)
	}
}

// createEmptyIndex leaves the index as a load does before its first document.
func createEmptyIndex(t *testing.T, cfg ClientConfig) {
	t.Helper()
	if err := NewCorpusLoader(newTestClient(t, cfg)).createIndex(context.Background(), language.English); err != nil {
		t.Fatalf("create index %s: %v", cfg.IndexName, err)
	}
}

func refreshIndex(t *testing.T, cfg ClientConfig) {
	t.Helper()
	if _, err := newTestClient(t, cfg).typed.Indices.Refresh().Index(cfg.IndexName).Do(context.Background()); err != nil {
		t.Fatalf("refresh %s: %v", cfg.IndexName, err)
	}
}

// retagEmbedding relabels a stored vector, which a load only ever tags with the corpus model.
func retagEmbedding(t *testing.T, cfg ClientConfig, id uuid.UUID, model string) {
	t.Helper()
	_, err := newTestClient(t, cfg).typed.Update(cfg.IndexName, id.String()).
		Doc(map[string]string{"embedding_model": model}).Do(context.Background())
	if err != nil {
		t.Fatalf("retag embedding of %s: %v", id, err)
	}
	refreshIndex(t, cfg)
}

func titledArticles(titles ...string) []document.Article {
	articles := make([]document.Article, len(titles))
	for i, title := range titles {
		articles[i] = document.Article{ID: uuid.New(), Title: title, Language: language.English}
	}
	return articles
}

func vec(dim int, fill float32) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = fill
	}
	return v
}
