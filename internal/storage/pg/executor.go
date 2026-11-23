package pg

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tadjov/sengi/internal/storage"
)

type RawExecutor struct {
	db *pgxpool.Pool
}

func NewRawExecutor(pool *ConnectionPool) *RawExecutor {
	return &RawExecutor{db: pool.Pool()}
}

func (e *RawExecutor) Exec(ctx context.Context, sql string, args []any) (*storage.ExecuteResult, error) {
	rows, err := e.db.Query(ctx, sql, args...)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []map[string]interface{}

	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, err
		}

		rowMap := make(map[string]interface{})
		fieldDescriptions := rows.FieldDescriptions()
		for i, fd := range fieldDescriptions {
			rowMap[fd.Name] = values[i]
		}
		results = append(results, rowMap)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &storage.ExecuteResult{Hits: results}, nil
}

var _ storage.RawExecutor = (*RawExecutor)(nil)
