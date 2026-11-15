package judgment

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tadjov/tusker/internal/bench/meta"
	"github.com/tadjov/tusker/internal/bench/suite"
	"github.com/tadjov/tusker/internal/storage"
)

// GradingQuery is the query a judge grades against.
type GradingQuery struct {
	ID string
	// Text is the query as typed; the deterministic judges grade against it, not Description.
	Text        string
	Description string
	Category    suite.Category
	Filters     suite.Filters
}

// GradingDoc is a single article candidate to grade.
type GradingDoc struct {
	ID              uuid.UUID
	Title           string
	Description     string
	Content         string
	PublishedAt     time.Time
	PublisherDomain string
}

// Strategy grades through DocStrategy, BatchStrategy or PoolStrategy.
// Describe names the judge every artifact records.
type Strategy interface {
	Name() string
	Describe() meta.Judge
}

// DocStrategy grades one (query, doc) pair at a time and must be safe for concurrent calls.
type DocStrategy interface {
	Strategy
	Grade(ctx context.Context, q GradingQuery, doc GradingDoc) (int, error)
}

// BatchStrategy grades several docs in one LLM call when RunnerConfig.BatchSize asks for it.
// On a partial reply it returns what it parsed, and the runner grades the rest one by one.
type BatchStrategy interface {
	DocStrategy
	// BatchMaxOutputTokens is the output budget of one batch request.
	BatchMaxOutputTokens() int
	GradeBatch(ctx context.Context, q GradingQuery, docs []GradingDoc) ([]GradedDoc, error)
}

// PoolStrategy grades a query's whole pool at once, since its grades are relative to the pool.
type PoolStrategy interface {
	Strategy
	GradePool(ctx context.Context, q GradingQuery, docs []GradingDoc) ([]GradedDoc, error)
}

type StrategyKind string

const (
	StrategyKeyword StrategyKind = "keyword" // token-overlap baseline
	StrategyBM25    StrategyKind = "bm25"    // pool-local Okapi BM25
	StrategyVector  StrategyKind = "vector"  // embedding cosine similarity
	StrategyHybrid  StrategyKind = "hybrid"  // BM25 + vector fusion

	// StrategyLLM grades with a model behind a Provider, picked with --provider.
	StrategyLLM StrategyKind = "llm"

	// StrategyJev grades with TypeSafe's Jev, a non-Anthropic cross-judge.
	StrategyJev StrategyKind = "jev"

	StrategyManual StrategyKind = "manual"
)

type StrategyOptions struct {
	// Provider selects the endpoint behind StrategyLLM; empty means DefaultProvider.
	Provider Provider
	// Model is a full id or an alias such as "sonnet", which the provider expands.
	Model string
	// Effort is the effort an LLM judge's model grades at.
	Effort      Effort
	APIBaseURL  string
	CLIBinary   string
	Concurrency int

	// VectorStore holds the doc vectors and embeds the query for vector and hybrid.
	VectorStore storage.VectorStore
	// EmbeddingModel labels the judge block's model for vector/hybrid.
	EmbeddingModel string
}

func NewStrategy(kind StrategyKind, opts StrategyOptions) (Strategy, error) {
	switch kind {
	case StrategyKeyword:
		return NewKeywordStrategy(), nil
	case StrategyBM25:
		return NewBM25Strategy(), nil
	case StrategyVector:
		return NewVectorStrategy(opts)
	case StrategyHybrid:
		return NewHybridStrategy(opts)
	case StrategyLLM:
		return NewLLMStrategy(opts)
	case StrategyJev:
		return NewJevStrategy(opts)
	case StrategyManual:
		return NewManualStrategy(), nil
	default:
		return nil, fmt.Errorf("unknown strategy %q (known: %s)", kind, strings.Join(KnownStrategies(), ", "))
	}
}
