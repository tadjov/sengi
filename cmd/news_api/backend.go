package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/tadjov/sengi/internal/api/server"
	"github.com/tadjov/sengi/internal/embedding"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/storage/es"
	"github.com/tadjov/sengi/internal/storage/pg"
	"github.com/tadjov/sengi/internal/storage/pg/native"
)

// searchBackend is everything the API serves from. semantic and hybrid are nil
// when embeddings are disabled; close releases the shared Postgres pool or
// Elasticsearch client.
type searchBackend struct {
	fts      storage.FullTextBackend
	semantic storage.SemanticSearcher
	hybrid   storage.HybridSearcher
	health   server.HealthChecker
	close    func()
}

func openSearchBackend(ctx context.Context, cfg *NewsSearchConfig) (*searchBackend, error) {
	switch cfg.StorageConfig.Type {
	case storage.PG:
		return openPostgresBackend(ctx, cfg)
	case storage.ES:
		return openElasticsearchBackend(ctx, cfg)
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

	embedder, err := openQueryEmbedder(ctx, cfg.EmbeddingConfig, func(ctx context.Context) (*storage.LoadRecord, error) {
		return pg.LatestLoad(ctx, pool)
	})
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

func openElasticsearchBackend(ctx context.Context, cfg *NewsSearchConfig) (*searchBackend, error) {
	client, err := es.NewClient(*cfg.StorageConfig.Es)
	if err != nil {
		return nil, fmt.Errorf("create Elasticsearch client: %w", err)
	}
	backend := &searchBackend{
		fts:    es.NewSearcher(client),
		health: es.NewHealthChecker(client),
		close:  client.Close,
	}

	embedder, err := openQueryEmbedder(ctx, cfg.EmbeddingConfig, client.LoadRecord)
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

// openQueryEmbedder returns nil when embeddings are disabled, else an embedder for the model the corpus was loaded with.
func openQueryEmbedder(ctx context.Context, cfg embedding.Config, readLoad func(context.Context) (*storage.LoadRecord, error)) (*embedding.Embedder, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	load, err := readLoad(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the corpus load record: %w", err)
	}
	if cfg, err = loadedModel(cfg, load); err != nil {
		return nil, err
	}
	client, err := embedding.NewOllamaClient(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("create embedding client: %w", err)
	}
	embedder := newQueryEmbedder(client, cfg)
	slog.Info("Embedding queries", "model", embedder.Model())
	return embedder, nil
}

// loadedModel takes the model and width from load, refusing an EMBEDDING_MODEL or EMBEDDING_DIM that names others.
func loadedModel(cfg embedding.Config, load *storage.LoadRecord) (embedding.Config, error) {
	if load == nil {
		slog.Warn("No load record names the corpus's embedding model, so queries are embedded as configured")
		return cfg, nil
	}
	if load.ModelTag == "" {
		return embedding.Config{}, errors.New("the corpus was loaded without embeddings; load them or set EMBEDDING_ENABLED=false")
	}
	model, dim, err := embedding.ParseTag(load.ModelTag)
	if err != nil {
		return embedding.Config{}, fmt.Errorf("corpus load record: %w", err)
	}
	if cfg.Model != "" && cfg.Model != model {
		return embedding.Config{}, fmt.Errorf("EMBEDDING_MODEL is %s, the corpus was loaded with %s", cfg.Model, model)
	}
	if cfg.Dim != 0 && cfg.Dim != dim {
		return embedding.Config{}, fmt.Errorf("EMBEDDING_DIM is %d, the corpus was loaded at %d", cfg.Dim, dim)
	}
	cfg.Model, cfg.Dim = model, dim
	return cfg, nil
}
