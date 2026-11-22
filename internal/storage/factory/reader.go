package factory

import (
	"context"
	"fmt"

	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/internal/storage/pg"
)

func NewReader(ctx context.Context, cfg StorageConfig) (storage.Reader, error) {
	if cfg.Type != storage.PG {
		return nil, fmt.Errorf("reader not supported for storage type %s", cfg.Type)
	}

	pool, err := pg.NewConnectionPool(ctx, *cfg.Pg)
	if err != nil {
		return nil, fmt.Errorf("failed to create PostgreSQL connection pool: %w", err)
	}

	return pg.NewArticleReader(pool), nil
}
