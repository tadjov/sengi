package runner

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tadjov/sengi/internal/bench/engine"
	"github.com/tadjov/sengi/internal/bench/metrics"
	"github.com/tadjov/sengi/internal/bench/spec"
	"github.com/tadjov/sengi/internal/bench/suite"
)

// cell is one query on one engine.
type cell struct {
	engine      string
	measurement measurement
	digest      string
	// unsent is why the request couldn't be built; the cell is never measured.
	unsent   error
	measured execResult
}

type queryCells struct {
	query *suite.Query
	cells []*cell
}

func (r *Runner) prepareCells(ctx context.Context, req JobRequest, engines []string, q *suite.Query) []*cell {
	queryVector := r.queryVector(ctx, req.Suite, q)
	cells := make([]*cell, 0, len(engines))
	for _, engName := range engines {
		exec, ok := req.Executors[engName]
		if !ok {
			continue
		}
		c := &cell{engine: engName}
		cells = append(cells, c)

		request, err := BuildRequest(RequestInput{
			Engine:      engName,
			Query:       q,
			Track:       req.Track,
			Suite:       req.Suite,
			Bindings:    req.Bindings,
			Dialects:    req.Dialects,
			QueryVector: queryVector,
		})
		if err != nil {
			c.unsent = err
			slog.Warn("resolve query failed", "query", q.ID, "engine", engName, "error", err)
			continue
		}
		if c.digest, err = requestDigest(request); err != nil {
			c.unsent = err
			slog.Warn("request digest failed", "query", q.ID, "engine", engName, "error", err)
			continue
		}
		c.measurement = measurement{
			queryID:  q.ID,
			engine:   engName,
			executor: exec,
			request:  request,
			timeout:  req.QueryTimeout,
		}
	}
	return cells
}

// measureQuery interleaves engines that run one at a time and gives the rest a goroutine each.
func (r *Runner) measureQuery(ctx context.Context, q queryCells, engineSem chan struct{}) {
	if r.config.EngineParallelism == EngineParallelismSerial {
		r.measureInterleaved(ctx, q, engineSem)
		return
	}
	var wg sync.WaitGroup
	for _, c := range q.cells {
		if c.unsent != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			engineSem <- struct{}{}
			defer func() { <-engineSem }()
			c.measured = r.measure(ctx, c.measurement)
		}()
	}
	wg.Wait()
}

// measureInterleaved runs each iteration across all engines in a seeded order, so host noise spreads over them.
func (r *Runner) measureInterleaved(ctx context.Context, q queryCells, engineSem chan struct{}) {
	var sendable []*cell
	for _, c := range q.cells {
		if c.unsent == nil {
			sendable = append(sendable, c)
		}
	}
	for warmup := 1; warmup <= r.config.WarmupRuns; warmup++ {
		for _, c := range engineOrder(sendable, orderStep{seed: r.config.Seed, queryID: q.query.ID, phase: warmupPhase, number: warmup}) {
			if _, err := c.measurement.executeAlone(ctx, engineSem); err != nil {
				slog.Warn("warmup failed", "query", q.query.ID, "engine", c.engine, "iteration", warmup, "error", err)
			}
		}
	}
	logs := make(map[*cell]*iterationLog, len(sendable))
	for _, c := range sendable {
		logs[c] = &iterationLog{iterations: r.config.Runs}
	}
	for iteration := 1; iteration <= r.config.Runs; iteration++ {
		for _, c := range engineOrder(sendable, orderStep{seed: r.config.Seed, queryID: q.query.ID, phase: iterationPhase, number: iteration}) {
			if logs[c].err != nil {
				continue
			}
			exec, err := c.measurement.executeAlone(ctx, engineSem)
			logs[c].record(iteration, exec, err)
		}
	}
	for _, c := range sendable {
		c.measured = logs[c].result(r.config.RankingDepth)
	}
}

type orderPhase string

const (
	warmupPhase    orderPhase = "warmup"
	iterationPhase orderPhase = "iteration"
)

type orderStep struct {
	seed    uint64
	queryID string
	phase   orderPhase
	number  int
}

// engineOrder is the same for the same seed and step.
func engineOrder(cells []*cell, step orderStep) []*cell {
	stream := fnv.New64a()
	fmt.Fprintf(stream, "%s\x00%s\x00%d", step.queryID, step.phase, step.number)
	rng := rand.New(rand.NewPCG(step.seed, stream.Sum64()))
	ordered := make([]*cell, len(cells))
	for i, j := range rng.Perm(len(cells)) {
		ordered[i] = cells[j]
	}
	return ordered
}

func (r *Runner) queryResult(q *suite.Query, c *cell) QueryResult {
	if c.unsent != nil {
		return QueryResult{QueryID: q.ID, EngineName: c.engine, Error: c.unsent}
	}
	measured := c.measured
	if measured.err != nil {
		slog.Warn("query failed", "query", q.ID, "engine", c.engine, "error", measured.err)
	}
	var scores metrics.ScoreSet
	if judgments := r.judgmentsFor(q); measured.err == nil && len(judgments) > 0 {
		scores = metrics.ComputeAll(measured.rankedIDs, judgments, r.config.KValues, r.config.RelevanceThreshold)
	}
	return QueryResult{
		QueryID:            q.ID,
		Category:           string(q.Category),
		EngineName:         c.engine,
		RequestDigest:      c.digest,
		Scores:             scores,
		RankedDocIDs:       measured.rankedIDs,
		ReturnedCount:      measured.returnedCount,
		CorpusMatches:      measured.corpusMatches,
		Latency:            measured.latencyStats,
		ServerLatency:      measured.serverStats,
		UnstableIterations: measured.unstableIterations,
		Error:              measured.err,
	}
}

func (r *Runner) judgmentsFor(q *suite.Query) map[uuid.UUID]int {
	perQuery := r.config.Judgments[q.ID]
	grades := make(map[uuid.UUID]int, len(perQuery))
	for docID, grade := range perQuery {
		if id, err := uuid.Parse(docID); err == nil {
			grades[id] = grade
		}
	}
	return grades
}

type execResult struct {
	rankedIDs          []uuid.UUID
	returnedCount      int
	corpusMatches      *int64
	latencyStats       LatencyStats
	serverStats        *LatencyStats
	unstableIterations []int
	err                error
}

// measurement is one query on one engine, executed for warmup and then for the
// measured iterations.
type measurement struct {
	queryID  string
	engine   string
	executor engine.Executor
	request  engine.Request
	timeout  time.Duration
}

func (r *Runner) measure(ctx context.Context, m measurement) execResult {
	for i := 0; i < r.config.WarmupRuns; i++ {
		if _, err := m.execute(ctx); err != nil {
			slog.Warn("warmup failed", "query", m.queryID, "engine", m.engine, "iteration", i+1, "error", err)
		}
	}
	log := &iterationLog{iterations: r.config.Runs}
	for iteration := 1; iteration <= r.config.Runs && log.err == nil; iteration++ {
		exec, err := m.execute(ctx)
		log.record(iteration, exec, err)
	}
	return log.result(r.config.RankingDepth)
}

// iterationLog fails the cell at its first failed iteration, so a timeout never drops out of the latency tail.
type iterationLog struct {
	iterations  int
	latencies   []time.Duration
	serverTimes []time.Duration
	rankings    [][]uuid.UUID
	last        *engine.Execution
	err         error
}

func (l *iterationLog) record(iteration int, exec *engine.Execution, err error) {
	if err != nil {
		l.err = fmt.Errorf("measured iteration %d of %d: %w", iteration, l.iterations, err)
		return
	}
	l.last = exec
	l.rankings = append(l.rankings, exec.RankedDocIDs)
	l.latencies = append(l.latencies, exec.Latency)
	if exec.ServerTime != nil {
		l.serverTimes = append(l.serverTimes, *exec.ServerTime)
	}
}

// result scores the last iteration, cut at depth, and lists the iterations that rank differently from the first.
func (l *iterationLog) result(depth int) execResult {
	if l.err != nil {
		return execResult{err: l.err}
	}
	if l.last == nil {
		return execResult{err: errors.New("no measured iteration ran")}
	}
	measured := execResult{
		rankedIDs:     cutRanking(l.last.RankedDocIDs, depth),
		returnedCount: len(l.last.RankedDocIDs),
		corpusMatches: l.last.CorpusMatches,
		latencyStats:  ComputeLatencyStats(l.latencies),
	}
	if len(l.serverTimes) > 0 {
		serverStats := ComputeLatencyStats(l.serverTimes)
		measured.serverStats = &serverStats
	}
	first := cutRanking(l.rankings[0], depth)
	for i, ranking := range l.rankings[1:] {
		if !slices.Equal(cutRanking(ranking, depth), first) {
			measured.unstableIterations = append(measured.unstableIterations, i+2)
		}
	}
	return measured
}

func cutRanking(ranked []uuid.UUID, depth int) []uuid.UUID {
	if depth > 0 && len(ranked) > depth {
		return ranked[:depth]
	}
	return ranked
}

// executeAlone holds the engine semaphore, so nothing else runs while it is measured.
func (m measurement) executeAlone(ctx context.Context, engineSem chan struct{}) (*engine.Execution, error) {
	engineSem <- struct{}{}
	defer func() { <-engineSem }()
	return m.execute(ctx)
}

// execute runs once under the query deadline, which the runner owns so that no
// engine type is cut off sooner or later than another.
func (m measurement) execute(ctx context.Context) (*engine.Execution, error) {
	ctx, cancel := context.WithTimeout(ctx, timeoutOrDefault(m.timeout))
	defer cancel()
	return m.executor.Execute(ctx, m.request)
}

func timeoutOrDefault(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return spec.DefaultQueryTimeout
	}
	return timeout
}
