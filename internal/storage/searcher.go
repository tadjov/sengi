package storage

import (
	"context"

	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/internal/types/query"
)

// Hit is an article with its relevance score.
type Hit struct {
	document.Article
	Score float64
	// ScoreNormalized is Score over the result's best score, between 0 and 1.
	ScoreNormalized float64
}

type SearchResult struct {
	Hits         []Hit
	NextCursor   *query.Cursor
	HasMore      bool
	MaxScore     float64
	PageMaxScore float64
	TotalMatches int64
}

type VectorSearchResult struct {
	Hits       []document.Article
	NextCursor *query.Cursor
	HasMore    bool
}

// FullTextBackend is a full-text engine. It serves each paradigm whose
// searcher interface it also implements.
type FullTextBackend interface {
	// Capabilities reports which stated parameters the engine honours.
	Capabilities() query.Capabilities
}

// StringQuerySearcher serves free-text search over fields and weights the
// engine chooses.
type StringQuerySearcher interface {
	SearchStringQuery(ctx context.Context, query *query.String, baseOpts *query.BaseOptions) (*SearchResult, error)
}

type FieldSearcher interface {
	SearchField(ctx context.Context, query *query.Match, baseOpts *query.BaseOptions) (*SearchResult, error)
}

type MultiFieldSearcher interface {
	SearchFields(ctx context.Context, query *query.MultiMatch, baseOpts *query.BaseOptions) (*SearchResult, error)
}

// PhraseSearcher serves phrase search with slop: match_phrase on
// Elasticsearch, phraseto_tsquery or distance operators on PostgreSQL.
type PhraseSearcher interface {
	SearchPhrase(ctx context.Context, query *query.Phrase, baseOpts *query.BaseOptions) (*SearchResult, error)
}

type BooleanSearcher interface {
	SearchBoolean(ctx context.Context, query *query.Boolean, baseOpts *query.BaseOptions) (*SearchResult, error)
}

type SemanticSearcher interface {
	SearchSemantic(ctx context.Context, query *query.Semantic, baseOpts *query.BaseOptions) (*VectorSearchResult, error)
}

// HybridSearcher combines lexical FTS with vector similarity via RRF.
type HybridSearcher interface {
	SearchHybrid(ctx context.Context, query *query.Hybrid, baseOpts *query.BaseOptions) (*SearchResult, error)
}
