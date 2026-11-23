package storage

import (
	"context"
)

type ExecuteResult struct {
	Hits []map[string]any
}

// RawExecutor runs SQL as written; args follow the order of its placeholders.
type RawExecutor interface {
	Exec(ctx context.Context, sql string, args []any) (*ExecuteResult, error)
}
