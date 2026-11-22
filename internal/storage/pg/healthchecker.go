package pg

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type HealthChecker struct {
	db *pgxpool.Pool
}

func NewHealthChecker(pool *ConnectionPool) *HealthChecker {
	return &HealthChecker{db: pool.Pool()}
}

func (hc *HealthChecker) Healthy(ctx context.Context) bool {
	return hc.db.Ping(ctx) == nil
}
