package reader

import (
	"github.com/tadjov/tusker/internal/types/document"
)

type Mapper interface {
	Map(map[string]string) (document.Article, error)
}
