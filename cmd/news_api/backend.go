package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/tadjov/sengi/internal/embedding"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/storage/es"
	"github.com/tadjov/sengi/internal/storage/pg"
	"github.com/tadjov/sengi/internal/storage/pg/native"
	pkgserver "github.com/tadjov/sengi/pkg/server"
)

// searchBackend is everything the API serves from. semantic and hybrid are nil
// when embeddings are disabled; close releases the shared Postgres pool or
// Elasticsearch client.
type searchBackend struct {
	fts      storage.FullTextBackend
	semantic storage.SemanticSearcher
	hybrid   storage.HybridSearcher
	health   pkgserver.HealthChecker
	close    func()
}

func openSearchBackend(ctx context.Context, cfg *NewsSearchConfig) (*searchBackend, error) {
	switch cfg.StorageConfig.Type {
	case storage.PG:
		return openPostgresBackend(ctx, cfg)
	case storage.ES:
		return openElasticsearchBackend(cfg)
	default:
		return nil, fmt.Errorf("no search backend for storage type %q", cfg.StorageConfig.Type)
	}
}

func openPostgresBackend(ctx context.Context, cfg *NewsSearchConfig) (*searchBackend, error) {
	poolCfg := *cfg.StorageConfig.Pg
	poolCfg.RegisterVecTypes = cfg.EmbeddingConfig.Enabled
	pool, err := pg.NewConnectionPool(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL connection pool: %w", err)
	}
	backend := &searchBackend{
		fts:    native.NewSearcher(pool),
		health: pg.NewHealthChecker(pool),
		close:  pool.Close,
	}

	embedder, err := openQueryEmbedder(cfg.EmbeddingConfig)
	if err != nil {
		pool.Close()
		return nil, err
	}
	if embedder != nil {
		backend.semantic = pg.NewSemanticSearcher(embedder, pool)
		backend.hybrid = native.NewHybridSearcher(embedder, pool)
	}
	return backend, nil
}

func openElasticsearchBackend(cfg *NewsSearchConfig) (*searchBackend, error) {
	client, err := es.NewClient(*cfg.StorageConfig.Es)
	if err != nil {
		return nil, fmt.Errorf("create Elasticsearch client: %w", err)
	}
	backend := &searchBackend{
		fts:    es.NewSearcher(client),
		health: es.NewHealthChecker(client),
		close:  client.Close,
	}

	embedder, err := openQueryEmbedder(cfg.EmbeddingConfig)
	if err != nil {
		client.Close()
		return nil, err
	}
	if embedder != nil {
		backend.semantic = es.NewSemanticSearcher(client, embedder, embedder.Model())
		backend.hybrid = es.NewHybridSearcher(client, embedder, embedder.Model())
	}
	return backend, nil
}

// openQueryEmbedder returns nil when embeddings are disabled.
func openQueryEmbedder(cfg embedding.Config) (*embedding.Embedder, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	client, err := embedding.NewOllamaClient(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("create embedding client: %w", err)
	}
	embedder := newQueryEmbedder(client, cfg)
	slog.Info("Embedding queries", "model", embedder.Model())
	return embedder, nil
}
