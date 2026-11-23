package runner

import (
	"fmt"
	"maps"
	"slices"

	"github.com/tadjov/sengi/internal/bench/dialect"
	"github.com/tadjov/sengi/internal/bench/spec"
	"github.com/tadjov/sengi/internal/bench/suite"
	"github.com/tadjov/sengi/internal/storage"
)

// VerifyEmbeddingModel rejects an engine that declares a different embedding
// model from the one the store embeds queries with. A vector template filters
// document vectors by the declared model while the query is embedded by the
// store, so a disagreement means every cosine distance is computed across two
// vector spaces — arithmetic that succeeds and ranks nothing. Engines that
// declare no model, and tracks with no store at all, are left alone.
func VerifyEmbeddingModel(engines map[string]spec.Engine, store storage.VectorStore) error {
	if store == nil {
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(engines)) {
		declared, _ := engines[name].Params[suite.EmbeddingModelParam].(string)
		if declared == "" || declared == store.Model() {
			continue
		}
		return fmt.Errorf(
			"engine %q declares %s %q but queries are embedded with %q: set EMBEDDING_MODEL to the declared model without its @<dim> and EMBEDDING_DIM to that width, or fix the spec",
			name, suite.EmbeddingModelParam, declared, store.Model())
	}
	return nil
}

// QueryBindings resolves every engine's query block and declared params once,
// so the per-query fan-out never reaches back into the spec.
func QueryBindings(bs *spec.BenchSpec) map[string]spec.QueryBinding {
	bindings := make(map[string]spec.QueryBinding, len(bs.Engines))
	for name := range bs.Engines {
		bindings[name] = bs.QueryBinding(name)
	}
	return bindings
}

// Dialects maps each declared engine to the syntax its type reads params in.
// A fusion engine has none: it sends each leg's request in the leg's dialect.
func Dialects(bs *spec.BenchSpec) (map[string]dialect.Dialect, error) {
	dialects := make(map[string]dialect.Dialect, len(bs.Engines))
	for name, eng := range bs.Engines {
		if eng.Type == spec.EngineFusion {
			continue
		}
		d, err := dialect.For(eng.Type)
		if err != nil {
			return nil, fmt.Errorf("engine %q: %w", name, err)
		}
		dialects[name] = d
	}
	return dialects, nil
}

// LoadSuites loads every job's suite and checks its blocks so a broken one fails before any query runs.
func LoadSuites(bs *spec.BenchSpec) (map[string]*suite.LoadedSuite, error) {
	dialects, err := Dialects(bs)
	if err != nil {
		return nil, err
	}
	suites := map[string]*suite.LoadedSuite{}
	templateReaders := map[templateInSuite]engineDialect{}
	for _, job := range bs.Jobs {
		loaded, ok := suites[job.Suite]
		if !ok {
			ls, err := suite.LoadFromFile(job.Suite)
			if err != nil {
				return nil, fmt.Errorf("load suite for job %q: %w", job.Name, err)
			}
			suites[job.Suite] = ls
			loaded = ls
		}
		for _, engName := range bs.EnginesRead(job) {
			binding := bs.QueryBinding(engName)
			if err := loaded.CheckArgsSupplied(binding.QuerySource, binding.Params); err != nil {
				return nil, fmt.Errorf("job %q engine %q: %w", job.Name, engName, err)
			}
			reader := engineDialect{engine: engName, dialect: dialects[engName]}
			if reader.dialect == nil {
				return nil, fmt.Errorf("job %q engine %q is not declared", job.Name, engName)
			}
			if err := checkEveryQueryHasABlock(loaded, boundEngine{name: engName, binding: binding}); err != nil {
				return nil, fmt.Errorf("job %q: %w", job.Name, err)
			}
			blocks, err := loaded.Blocks(binding.QuerySource, binding.Params)
			if err != nil {
				return nil, fmt.Errorf("job %q engine %q: %w", job.Name, engName, err)
			}
			for _, block := range blocks {
				if err := reader.dialect.Check(block); err != nil {
					return nil, fmt.Errorf("job %q engine %q query %q: %w", job.Name, engName, block.QueryID, err)
				}
				if block.Template == "" {
					continue
				}
				key := templateInSuite{suite: job.Suite, template: block.Template}
				if prev, seen := templateReaders[key]; seen && prev.dialect.Name() != reader.dialect.Name() {
					return nil, fmt.Errorf("template %q is read by engine %q as %s and by engine %q as %s",
						block.Template, prev.engine, prev.dialect.Name(), engName, reader.dialect.Name())
				}
				templateReaders[key] = reader
			}
		}
	}
	return suites, nil
}

// checkEveryQueryHasABlock rejects an engine whose block for one of the suite's
// queries is absent or empty. RunJob resolves every block without checking.
func checkEveryQueryHasABlock(ls *suite.LoadedSuite, eng boundEngine) error {
	source := eng.binding.QuerySource
	for _, q := range ls.Suite.Queries {
		if block, ok := q.Engines[source]; ok && hasStatement(block) {
			continue
		}
		if source != eng.name {
			return fmt.Errorf("query %q has no %q block for engine %q", q.ID, source, eng.name)
		}
		return fmt.Errorf("query %q has no block for engine %q", q.ID, eng.name)
	}
	return nil
}

// boundEngine is a job's engine and the binding it reads its queries through.
type boundEngine struct {
	name    string
	binding spec.QueryBinding
}

func hasStatement(block suite.EngineQuery) bool {
	return block.Query != "" || block.File != "" || block.Template != ""
}
