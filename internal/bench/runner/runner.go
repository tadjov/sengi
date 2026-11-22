package runner

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/tadjov/sengi/internal/bench/dialect"
	"github.com/tadjov/sengi/internal/bench/engine"
	"github.com/tadjov/sengi/internal/bench/spec"
	"github.com/tadjov/sengi/internal/bench/suite"
	"github.com/tadjov/sengi/internal/progress"
)

type Runner struct {
	config Config
}

func New(cfg Config) *Runner {
	return &Runner{config: cfg}
}

func (r *Runner) RunAll(
	ctx context.Context,
	bs *spec.BenchSpec,
	executors map[string]engine.Executor,
) (*BenchmarkResult, error) {
	if err := r.config.validate(); err != nil {
		return nil, err
	}
	if err := VerifyEmbeddingModel(bs.Engines, r.config.VectorStore); err != nil {
		return nil, err
	}

	br := &BenchmarkResult{Config: r.config}

	suites, err := LoadSuites(bs)
	if err != nil {
		return nil, err
	}
	if err := RegisterSearchTemplates(ctx, TemplateRegistration{Spec: bs, Suites: suites, Executors: executors}); err != nil {
		return nil, err
	}
	dialects, err := Dialects(bs)
	if err != nil {
		return nil, err
	}
	bindings := QueryBindings(bs)

	var queries int64
	for _, job := range bs.Jobs {
		queries += int64(len(suites[job.Suite].Suite.Queries))
	}
	progress.Or(r.config.Progress).SetTotal(queries)

	for _, job := range bs.Jobs {
		jr, err := r.RunJob(ctx, JobRequest{
			Track:        bs.ID,
			Job:          job,
			Suite:        suites[job.Suite],
			Executors:    executors,
			Bindings:     bindings,
			Dialects:     dialects,
			QueryTimeout: bs.Runs.QueryTimeout,
		})
		if err != nil {
			return nil, fmt.Errorf("run job %q: %w", job.Name, err)
		}
		br.Jobs = append(br.Jobs, jr)
	}

	return br, nil
}

// JobRequest is everything one job needs to run: its declaration, the loaded
// suite, the executors to drive, and how each engine reaches its queries. The
// suite must come from LoadSuites, which guarantees every engine a block for
// every query.
type JobRequest struct {
	// Track is the spec id, which namespaces the job's stored search templates.
	Track     string
	Job       spec.Job
	Suite     *suite.LoadedSuite
	Executors map[string]engine.Executor
	Bindings  map[string]spec.QueryBinding
	Dialects  map[string]dialect.Dialect
	// QueryTimeout bounds each execution; zero means spec.DefaultQueryTimeout.
	QueryTimeout time.Duration
}

func (r *Runner) RunJob(ctx context.Context, req JobRequest) (*JobResult, error) {
	jobExecutors := make(map[string]engine.Executor)
	for _, engName := range req.Job.Engines {
		exec, ok := req.Executors[engName]
		if !ok {
			return nil, fmt.Errorf("executor %q not found", engName)
		}
		jobExecutors[engName] = exec
	}

	jr := &JobResult{
		JobName:     req.Job.Name,
		Results:     make(map[string]map[string]QueryResult),
		EngineNames: req.Job.Engines,
	}

	jobReq := req
	jobReq.Executors = jobExecutors
	r.runQueries(ctx, jr, jobReq)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return jr, nil
}

func (r *Runner) runQueries(ctx context.Context, jr *JobResult, req JobRequest) {
	queries := req.Suite.Suite.Queries
	// Pre-populate order and result maps sequentially before launching any
	// goroutines. Goroutines only READ the outer jr.Results map (to get their
	// inner map pointer) and write only to their own inner map — no races.
	for i := range queries {
		jr.QueryOrder = append(jr.QueryOrder, queries[i].ID)
		jr.Results[queries[i].ID] = make(map[string]QueryResult)
	}

	// inFlight bounds the executions of every query together; nil measures.
	var inFlight chan struct{}
	if r.config.concurrent() {
		inFlight = make(chan struct{}, r.config.Concurrency)
	}
	counter := progress.Or(r.config.Progress)
	runQuery := func(q *suite.Query) {
		if ctx.Err() != nil {
			return
		}
		cells := r.prepareCells(ctx, req, jr.EngineNames, q)
		r.measureQuery(ctx, queryCells{query: q, cells: cells}, inFlight)
		for _, c := range cells {
			jr.Results[q.ID][c.engine] = r.queryResult(q, c)
		}
		counter.Add(1)
	}
	if !r.config.concurrent() {
		for i := range queries {
			runQuery(&queries[i])
		}
		return
	}
	var wg sync.WaitGroup
	for i := range queries {
		wg.Go(func() { runQuery(&queries[i]) })
	}
	wg.Wait()
}
