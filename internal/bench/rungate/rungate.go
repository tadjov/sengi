// Package rungate decides whether a finished run can stand as its track's
// result: every measured cell answered, ranked stably, and was scored against
// judgments graded on the pool the run's requests produced.
package rungate

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/tadjov/sengi/internal/bench/judgment"
	"github.com/tadjov/sengi/internal/bench/meta"
	"github.com/tadjov/sengi/internal/bench/pool"
	"github.com/tadjov/sengi/internal/bench/runner"
	"github.com/tadjov/sengi/internal/bench/suite"
)

// Judgments is what a run scores against. A latency-only run has none.
type Judgments struct {
	Path   string
	Grades map[string]map[string]int
	Meta   *meta.Meta
	// Pool is the pool the judgments were graded against; empty for latency-only.
	Pool *pool.PoolFile

	coverage judgmentCoverage
}

// LatencyOnly scores against nothing.
func LatencyOnly() *Judgments {
	return &Judgments{Pool: &pool.PoolFile{}}
}

// Source names the judgments file and whether --judgments chose it rather than
// spec.defaults.judgments, which only changes the advice when it is missing.
type Source struct {
	Path     string
	Explicit bool
	PoolPath string
}

// Load reads the judgments and refuses them unless they were graded against the
// track's current pool.
func Load(src Source) (*Judgments, error) {
	grades, judgmentsMeta, err := LoadGrades(src.Path, src.Explicit)
	if err != nil {
		return nil, err
	}
	currentPool, err := pool.ReadPoolFile(src.PoolPath)
	if err != nil {
		return nil, fmt.Errorf("read the track's pool to check what %s was graded against: %w", src.Path, err)
	}
	if err := currentPool.CheckJudgedAgainst(judgmentsMeta.PoolRunID); err != nil {
		return nil, fmt.Errorf("%s: %w", src.Path, err)
	}
	return &Judgments{
		Path:     src.Path,
		Grades:   grades,
		Meta:     judgmentsMeta,
		Pool:     currentPool,
		coverage: poolCoverage(currentPool, grades),
	}, nil
}

// UnjudgedCount is how many pooled (query, doc) pairs carry no grade.
func (j *Judgments) UnjudgedCount() int {
	return j.coverage.unjudged
}

func pooledRequestDigests(pf *pool.PoolFile) map[string]map[string]string {
	requests := make(map[string]map[string]string, len(pf.Queries))
	for _, entry := range pf.Queries {
		requests[entry.QueryID] = entry.RequestDigests
	}
	return requests
}

// judgmentCoverage counts the pool's (query, doc) pairs and how many of them
// the judgments leave without a grade, and names the queries and docs the pool holds.
type judgmentCoverage struct {
	pooled        int
	unjudged      int
	pooledQueries map[string]bool
	pooledDocs    map[queryDoc]bool
}

type queryDoc struct {
	queryID string
	docID   string
}

// poolCoverage checks the judgments against the pool itself rather than the
// judge's own count, which an interrupted run leaves stale and which cannot
// see queries the judge never reached.
func poolCoverage(pf *pool.PoolFile, grades map[string]map[string]int) judgmentCoverage {
	c := judgmentCoverage{pooledQueries: make(map[string]bool, len(pf.Queries)), pooledDocs: make(map[queryDoc]bool)}
	for _, entry := range pf.Queries {
		c.pooledQueries[entry.QueryID] = true
		for _, doc := range entry.Docs {
			c.pooled++
			c.pooledDocs[queryDoc{queryID: entry.QueryID, docID: doc.DocID.String()}] = true
			if _, graded := grades[entry.QueryID][doc.DocID.String()]; !graded {
				c.unjudged++
			}
		}
	}
	return c
}

// Failures lists why a finished run cannot stand as the track's result.
func Failures(result *runner.BenchmarkResult, judgments *Judgments, suiteQueries map[string]suite.Query) []string {
	var failures []string
	if cells := measuredDroppedCells(result, judgments.Pool.Meta.DroppedExecutions); len(cells) > 0 {
		failures = append(failures, fmt.Sprintf("the pool holds no documents for %d measured (query, engine) executions, so their hits score as non-relevant: %s; re-pool without --allow-partial",
			len(cells), strings.Join(cells, ", ")))
	}
	if cells := erroredCells(result); len(cells) > 0 {
		failures = append(failures, fmt.Sprintf("%d (query, engine) executions errored: %s", len(cells), strings.Join(cells, ", ")))
	}
	if cells := unstableRankings(result); len(cells) > 0 {
		failures = append(failures, fmt.Sprintf("%d (query, engine) cells ranked differently in a later iteration than in the first: %s; an unstable tie or a corpus that changed under the run",
			len(cells), strings.Join(cells, ", ")))
	}
	if judgments.Meta != nil {
		if engines := unpooledEngines(result, judgments); len(engines) > 0 {
			failures = append(failures, fmt.Sprintf("engines %s were never pooled, so none of their results are judged; re-pool and re-judge the track",
				strings.Join(engines, ", ")))
		}
		if cells := changedRequests(result, judgments); len(cells) > 0 {
			failures = append(failures, fmt.Sprintf("%d (query, engine) requests are not the ones the pool sent, so their judgments grade another query: %s; re-pool and re-judge the track",
				len(cells), strings.Join(cells, ", ")))
		}
		if poolPredatesQueryText(judgments.Pool) {
			failures = append(failures, "the pool predates query text, so its judgments graded no query's text; re-pool and re-judge the track")
		} else if queries := rewrittenQueries(result, judgments.Pool, suiteQueries); len(queries) > 0 {
			failures = append(failures, fmt.Sprintf("%d queries have another text, category or filters than the pool holds, so their judgments grade another query: %s; re-pool and re-judge the track",
				len(queries), strings.Join(queries, ", ")))
		}
	}
	if missing := unpooledQueries(result, judgments.coverage.pooledQueries); len(missing) > 0 {
		failures = append(failures, fmt.Sprintf("%d suite queries were never pooled, so none of their results are judged: %s; re-pool and re-judge the track",
			len(missing), strings.Join(missing, ", ")))
	}
	if judgments.Meta != nil {
		if unjudged := unjudgedRetrieved(result, judgments); unjudged.docs > 0 {
			failures = append(failures, fmt.Sprintf("%d retrieved docs within the pool depth have no grade, in %s, so they score as non-relevant; the engines no longer return what was pooled, re-pool and re-judge the track",
				unjudged.docs, strings.Join(unjudged.cells, ", ")))
		}
	}
	if judgments.coverage.unjudged > 0 {
		failures = append(failures, fmt.Sprintf("the judgments leave %d of %d pooled docs unjudged; finish judging (bench judge --resume)",
			judgments.coverage.unjudged, judgments.coverage.pooled))
	}
	return failures
}

// changedRequests lists, sorted, the measured cells of pooled queries and
// engines whose request differs from the pool's, or that the pool holds none
// for. Dropped cells are reported on their own.
func changedRequests(result *runner.BenchmarkResult, judgments *Judgments) []string {
	accounted := accountedEngines(judgments)
	dropped := droppedCells(judgments.Pool.Meta.DroppedExecutions)
	pooledDigests := pooledRequestDigests(judgments.Pool)
	changed := make(map[string]bool)
	for _, jr := range result.Jobs {
		for queryID, byEngine := range jr.Results {
			pooled, ok := pooledDigests[queryID]
			if !ok {
				continue
			}
			for engineName, qr := range byEngine {
				if qr.RequestDigest == "" || !accounted[engineName] || dropped[queryID+"/"+engineName] {
					continue
				}
				if pooled[engineName] != qr.RequestDigest {
					changed[queryID+"/"+engineName] = true
				}
			}
		}
	}
	return slices.Sorted(maps.Keys(changed))
}

// poolPredatesQueryText reports a pool written before entries carried the query
// text, which bench pool now refuses to write without.
func poolPredatesQueryText(pf *pool.PoolFile) bool {
	return slices.ContainsFunc(pf.Queries, func(entry pool.PoolEntry) bool { return entry.QueryText == "" })
}

// rewrittenQueries lists, sorted, the measured pooled queries whose text,
// category or filters the suite no longer reads as the pool entry the judges
// graded.
func rewrittenQueries(result *runner.BenchmarkResult, pf *pool.PoolFile, suiteQueries map[string]suite.Query) []string {
	pooled := make(map[string]pool.PoolEntry, len(pf.Queries))
	for _, entry := range pf.Queries {
		pooled[entry.QueryID] = entry
	}
	rewritten := make(map[string]bool)
	for _, jr := range result.Jobs {
		for _, queryID := range jr.QueryOrder {
			entry, wasPooled := pooled[queryID]
			q, inSuite := suiteQueries[queryID]
			if wasPooled && inSuite && (entry.QueryText != q.Text || entry.Category != q.Category || !entry.Filters.Equal(q.Filters)) {
				rewritten[queryID] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(rewritten))
}

// retrievedWithoutGrade counts the unpooled (query, doc) pairs ranked within the
// pool depth, which no judge could have graded, and names the cells that ranked
// them. The runner has already cut every ranking at that depth.
type retrievedWithoutGrade struct {
	docs  int
	cells []string
}

// unjudgedRetrieved checks the rankings of pooled queries and engines; queries
// and engines the pool lacks, dropped cells and pooled docs left ungraded are
// reported on their own.
func unjudgedRetrieved(result *runner.BenchmarkResult, judgments *Judgments) retrievedWithoutGrade {
	accounted := accountedEngines(judgments)
	dropped := droppedCells(judgments.Pool.Meta.DroppedExecutions)
	docs := make(map[queryDoc]bool)
	cells := make(map[string]bool)
	for _, jr := range result.Jobs {
		for queryID, byEngine := range jr.Results {
			if !judgments.coverage.pooledQueries[queryID] {
				continue
			}
			for engineName, qr := range byEngine {
				if qr.Error != nil || !accounted[engineName] || dropped[queryID+"/"+engineName] {
					continue
				}
				for _, docID := range qr.RankedDocIDs {
					retrieved := queryDoc{queryID: queryID, docID: docID.String()}
					if _, graded := judgments.Grades[queryID][retrieved.docID]; graded || judgments.coverage.pooledDocs[retrieved] {
						continue
					}
					docs[retrieved] = true
					cells[queryID+"/"+engineName] = true
				}
			}
		}
	}
	return retrievedWithoutGrade{docs: len(docs), cells: slices.Sorted(maps.Keys(cells))}
}

func droppedCells(dropped []meta.DroppedExecution) map[string]bool {
	cells := make(map[string]bool, len(dropped))
	for _, d := range dropped {
		cells[d.QueryID+"/"+d.Engine] = true
	}
	return cells
}

// accountedEngines are the engines the pool holds results from or records as dropped.
func accountedEngines(judgments *Judgments) map[string]bool {
	accounted := make(map[string]bool)
	for _, name := range judgments.Pool.Meta.Engines {
		accounted[name] = true
	}
	for _, d := range judgments.Pool.Meta.DroppedExecutions {
		accounted[d.Engine] = true
	}
	return accounted
}

// unpooledEngines lists, sorted, the engines this run measured that the pool
// neither holds results from nor records as dropped.
func unpooledEngines(result *runner.BenchmarkResult, judgments *Judgments) []string {
	accounted := accountedEngines(judgments)
	missing := make(map[string]bool)
	for _, jr := range result.Jobs {
		for _, byEngine := range jr.Results {
			for name := range byEngine {
				if !accounted[name] {
					missing[name] = true
				}
			}
		}
	}
	return slices.Sorted(maps.Keys(missing))
}

// unpooledQueries lists the queries this run executed that have no pool entry.
// A latency-only run has no pool to check against.
func unpooledQueries(result *runner.BenchmarkResult, pooled map[string]bool) []string {
	if pooled == nil {
		return nil
	}
	seen := make(map[string]bool)
	var missing []string
	for _, jr := range result.Jobs {
		for _, queryID := range jr.QueryOrder {
			if !pooled[queryID] && !seen[queryID] {
				seen[queryID] = true
				missing = append(missing, queryID)
			}
		}
	}
	return missing
}

// measuredDroppedCells lists the dropped pool cells this run executed.
func measuredDroppedCells(result *runner.BenchmarkResult, dropped []meta.DroppedExecution) []string {
	var cells []string
	for _, d := range dropped {
		for _, jr := range result.Jobs {
			if _, ran := jr.Results[d.QueryID][d.Engine]; ran {
				cells = append(cells, d.QueryID+"/"+d.Engine)
				break
			}
		}
	}
	return cells
}

func erroredCells(result *runner.BenchmarkResult) []string {
	var cells []string
	for _, jr := range result.Jobs {
		for _, queryID := range jr.QueryOrder {
			for _, engineName := range jr.EngineNames {
				if qr, ok := jr.Results[queryID][engineName]; ok && qr.Error != nil {
					cells = append(cells, fmt.Sprintf("%s:%s/%s", jr.JobName, queryID, engineName))
				}
			}
		}
	}
	return cells
}

func unstableRankings(result *runner.BenchmarkResult) []string {
	var cells []string
	for _, jr := range result.Jobs {
		for _, queryID := range jr.QueryOrder {
			for _, engineName := range jr.EngineNames {
				qr, ok := jr.Results[queryID][engineName]
				if !ok || len(qr.UnstableIterations) == 0 {
					continue
				}
				iterations := make([]string, len(qr.UnstableIterations))
				for i, iteration := range qr.UnstableIterations {
					iterations[i] = strconv.Itoa(iteration)
				}
				cells = append(cells, fmt.Sprintf("%s:%s/%s (iteration %s)", jr.JobName, queryID, engineName, strings.Join(iterations, ", ")))
			}
		}
	}
	return cells
}

// LoadGrades reads the graded pairs of a judgments file as grades[queryID][docID].
func LoadGrades(path string, explicit bool) (map[string]map[string]int, *meta.Meta, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if explicit {
			return nil, nil, fmt.Errorf("--judgments file not found: %s\n"+
				"  • check the strategy name (keyword, claude-cli, claude-api, manual)\n"+
				"  • or pass an explicit path to an existing YAML\n"+
				"  • or pass --latency-only to measure latency without scoring", path)
		}
		return nil, nil, fmt.Errorf("spec.defaults.judgments file not found: %s\n"+
			"  • judge the track first (bench judge)\n"+
			"  • or pass --latency-only to measure latency without scoring", path)
	}
	jf, err := judgment.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("load judgments %s: %w", path, err)
	}
	out := make(map[string]map[string]int, len(jf.Queries))
	for _, qe := range jf.Queries {
		inner := make(map[string]int, len(qe.Docs))
		for _, d := range qe.Docs {
			if d.Grade < 0 {
				continue
			}
			inner[d.DocID.String()] = d.Grade
		}
		out[qe.QueryID] = inner
	}
	return out, &jf.Meta, nil
}
