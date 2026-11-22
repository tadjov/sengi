package judgment

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tadjov/sengi/internal/bench/meta"
	"github.com/tadjov/sengi/internal/bench/suite"
	"github.com/tadjov/sengi/internal/storage"
)

// GradingQuery is the query a judge grades against.
type GradingQuery struct {
	ID       string
	Text     string
	Category suite.Category
	Filters  suite.Filters
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

// Strategy grades through DocStrategy or PoolStrategy.
// Describe names the judge every artifact records.
type Strategy interface {
	Describe() meta.Judge
}

// DocStrategy grades one (query, doc) pair at a time and must be safe for concurrent calls.
type DocStrategy interface {
	Strategy
	Grade(ctx context.Context, q GradingQuery, doc GradingDoc) (int, error)
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

// strategyKinds lists what --strategy accepts.
var strategyKinds = []StrategyKind{
	StrategyKeyword, StrategyBM25, StrategyVector, StrategyHybrid, StrategyLLM, StrategyJev, StrategyManual,
}

func ParseStrategyKind(s string) (StrategyKind, error) {
	if slices.Contains(strategyKinds, StrategyKind(s)) {
		return StrategyKind(s), nil
	}
	return "", fmt.Errorf("unknown strategy %q (known: %s)", s, strings.Join(strategyKindNames(), ", "))
}

func strategyKindNames() []string {
	names := make([]string, len(strategyKinds))
	for i, kind := range strategyKinds {
		names[i] = string(kind)
	}
	return names
}

// KnownStrategies lists every judgment-set name; LLM sets go by provider.
func KnownStrategies() []string {
	var names []string
	for _, kind := range strategyKinds {
		if kind == StrategyLLM {
			names = append(names, KnownProviders()...)
			continue
		}
		names = append(names, string(kind))
	}
	return names
}

type StrategyOptions struct {
	// Provider selects the endpoint behind StrategyLLM; empty means DefaultProvider.
	Provider Provider
	// Model is a full id or an alias such as "sonnet", which the provider expands.
	Model string
	// Effort is the effort an LLM judge's model grades at.
	Effort     Effort
	APIBaseURL string
	CLIBinary  string

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
	default:
		return nil, fmt.Errorf("unknown strategy %q (known: %s)", kind, strings.Join(strategyKindNames(), ", "))
	}
}
