package storage

import (
	"context"

	"github.com/google/uuid"
	"github.com/tadjov/sengi/internal/types/document"
)

type Reader interface {
	GetByIDs(ctx context.Context, ids []uuid.UUID) ([]document.Article, error)
}
