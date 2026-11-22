package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/tadjov/sengi/internal/bench/dialect"
	"github.com/tadjov/sengi/internal/bench/engine"
	"github.com/tadjov/sengi/internal/bench/spec"
	"github.com/tadjov/sengi/internal/bench/suite"
)

type templateInSuite struct {
	suite    string
	template string
}

type engineDialect struct {
	engine  string
	dialect dialect.Dialect
}

// TemplateRegistration is a loaded track whose search templates are to be
// stored on the engines that render them.
type TemplateRegistration struct {
	Spec      *spec.BenchSpec
	Suites    map[string]*suite.LoadedSuite
	Executors map[string]engine.Executor
}

type templateOnEngine struct {
	engine   string
	template engine.SearchTemplate
}

// RegisterSearchTemplates stores templates on engines that render them, failing before storing any on a conflict.
func RegisterSearchTemplates(ctx context.Context, reg TemplateRegistration) error {
	dialects, err := Dialects(reg.Spec)
	if err != nil {
		return err
	}
	var pending []templateOnEngine
	sourceByID := map[string]string{}
	queued := map[templateOnEngine]bool{}
	for _, job := range reg.Spec.Jobs {
		ls := reg.Suites[job.Suite]
		for _, engName := range reg.Spec.EnginesRead(job) {
			if d := dialects[engName]; d == nil || !d.StoresTemplates() {
				continue
			}
			exec, ok := reg.Executors[engName]
			if !ok {
				continue
			}
			if _, stores := exec.(engine.SearchTemplateRegistrar); !stores {
				return fmt.Errorf("engine %q reads %s templates but its executor cannot store them", engName, dialects[engName].Name())
			}
			source := reg.Spec.QueryBinding(engName).QuerySource
			for _, q := range ls.Suite.Queries {
				block, ok := q.Engines[source]
				if !ok || block.Template == "" {
					continue
				}
				tmpl, ok := ls.Registry.Get(block.Template)
				if !ok {
					return fmt.Errorf("query %q engine %q: template %q not found", q.ID, engName, block.Template)
				}
				stored := templateOnEngine{
					engine:   engName,
					template: engine.SearchTemplate{ID: engine.SearchTemplateID(reg.Spec.ID, tmpl.ID), Source: tmpl.Query},
				}
				if prev, seen := sourceByID[stored.template.ID]; seen && prev != tmpl.Query {
					return fmt.Errorf("two different templates would be stored as %q: rename one", stored.template.ID)
				}
				sourceByID[stored.template.ID] = tmpl.Query
				if !queued[stored] {
					queued[stored] = true
					pending = append(pending, stored)
				}
			}
		}
	}
	for _, p := range pending {
		if err := p.register(ctx, reg.Executors[p.engine], reg.Spec.Runs.QueryTimeout); err != nil {
			return fmt.Errorf("engine %q: %w", p.engine, err)
		}
	}
	return nil
}

func (p templateOnEngine) register(ctx context.Context, exec engine.Executor, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeoutOrDefault(timeout))
	defer cancel()
	return exec.(engine.SearchTemplateRegistrar).RegisterSearchTemplate(ctx, p.template)
}
