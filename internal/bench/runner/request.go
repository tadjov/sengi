package runner

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/tadjov/sengi/internal/bench/dialect"
	"github.com/tadjov/sengi/internal/bench/engine"
	"github.com/tadjov/sengi/internal/bench/spec"
	"github.com/tadjov/sengi/internal/bench/suite"
)

// RequestInput is one suite query to turn into the request one engine receives.
type RequestInput struct {
	Engine      string
	Query       *suite.Query
	Track       string
	Suite       *suite.LoadedSuite
	Bindings    map[string]spec.QueryBinding
	Dialects    map[string]dialect.Dialect
	QueryVector []float32
}

// BuildRequest resolves the engine's block for the query in the engine's
// dialect. A fusion engine gets one leg request per leg, each built as that
// leg would build it on its own.
func BuildRequest(in RequestInput) (engine.Request, error) {
	// An engine with no declared binding reads its own block; an empty
	// QuerySource would instead match nothing and drop the query silently.
	binding, declared := in.Bindings[in.Engine]
	if !declared {
		binding = spec.QueryBinding{QuerySource: in.Engine}
	}
	if fusion := binding.Fusion; fusion != nil {
		legs := make([]engine.LegRequest, 0, len(fusion.Legs))
		for _, leg := range fusion.Legs {
			legInput := in
			legInput.Engine = leg
			request, err := BuildRequest(legInput)
			if err != nil {
				return engine.Request{}, fmt.Errorf("fusion leg %q: %w", leg, err)
			}
			legs = append(legs, engine.LegRequest{Engine: leg, Request: request})
		}
		return engine.Request{Legs: legs, Fusion: fusion}, nil
	}
	resolved, err := in.Query.ResolveEngineQuery(suite.ResolveOptions{
		Engine:      binding.QuerySource,
		Registry:    in.Suite.Registry,
		SuiteDir:    in.Suite.Dir,
		Defaults:    binding.Params,
		QueryVector: in.QueryVector,
	})
	if err != nil {
		return engine.Request{}, fmt.Errorf("resolve query: %w", err)
	}
	d, ok := in.Dialects[in.Engine]
	if !ok {
		return engine.Request{}, fmt.Errorf("engine %q has no query dialect", in.Engine)
	}
	return d.Request(in.Track, resolved), nil
}

// queryVector looks the query's text up once when any of its blocks takes the
// query vector. Without a store or a text, or when the lookup fails, it returns
// nil and those blocks fail to resolve, recorded per engine.
func (r *Runner) queryVector(ctx context.Context, ls *suite.LoadedSuite, q *suite.Query) []float32 {
	if r.config.VectorStore == nil || !ls.NeedsQueryVector(q) {
		return nil
	}
	if q.Text == "" {
		slog.Warn("query has no text to look its vector up by; vector queries for this query will not resolve", "query", q.ID)
		return nil
	}
	vec, err := r.config.VectorStore.QueryVector(ctx, q.Text)
	if err != nil {
		slog.Warn("query embedding failed; vector queries for this query will not resolve",
			"query", q.ID, "error", err)
		return nil
	}
	return vec
}
