package reader

import (
	"github.com/tadjov/sengi/internal/types/document"
)

type Mapper interface {
	Map(map[string]string) (document.Article, error)
}
