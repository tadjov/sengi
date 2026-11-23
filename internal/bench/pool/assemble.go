package pool

import (
	"cmp"
	"slices"
	"strings"

	"github.com/tadjov/sengi/internal/bench/engine"
	"github.com/tadjov/sengi/internal/bench/meta"
	"github.com/tadjov/sengi/internal/bench/runner"
	"github.com/tadjov/sengi/internal/bench/suite"
)

// FromResult pools each query once across the jobs that ran it: jobs sharing a
// suite repeat its queries, and the first job to answer for an engine supplies
// that engine's ranking.
func FromResult(result *runner.BenchmarkResult, queries map[string]suite.Query, depth int) *PoolFile {
	pf := &PoolFile{}

	var order []string
	byQuery := make(map[string]map[string]*engine.Execution)
	digests := make(map[string]map[string]string)
	jobSeen := make(map[string]struct{})
	var jobNames []string

	for _, jr := range result.Jobs {
		if _, ok := jobSeen[jr.JobName]; !ok {
			jobSeen[jr.JobName] = struct{}{}
			jobNames = append(jobNames, jr.JobName)
		}
		for _, qID := range jr.QueryOrder {
			execs, ok := byQuery[qID]
			if !ok {
				execs = make(map[string]*engine.Execution)
				byQuery[qID] = execs
				digests[qID] = make(map[string]string)
				order = append(order, qID)
			}
			engResults := jr.Results[qID]
			for _, engName := range jr.EngineNames {
				if _, exists := execs[engName]; exists {
					continue
				}
				qr, ok := engResults[engName]
				if !ok || qr.Error != nil {
					continue
				}
				execs[engName] = &engine.Execution{
					RankedDocIDs:  qr.RankedDocIDs,
					CorpusMatches: qr.CorpusMatches,
				}
				digests[qID][engName] = qr.RequestDigest
			}
		}
	}

	pf.SuiteName = strings.Join(jobNames, ",")
	for _, qID := range order {
		q := queries[qID]
		pf.Queries = append(pf.Queries, PoolEntry{
			QueryID:        qID,
			QueryText:      q.Text,
			QueryDesc:      q.Description,
			Category:       q.Category,
			Filters:        q.Filters,
			Docs:           PoolResults(byQuery[qID], depth),
			RequestDigests: digests[qID],
		})
	}
	return pf
}

// AnsweredEngines keeps the engines that answered at least one query, so an
// engine whose every execution failed is not claimed by the pool.
func AnsweredEngines(result *runner.BenchmarkResult, engines []string) []string {
	answered := make(map[string]bool)
	for cell := range answeredCells(result) {
		answered[cell.engine] = true
	}
	var out []string
	for _, name := range engines {
		if answered[name] {
			out = append(out, name)
		}
	}
	return out
}

type cell struct {
	queryID string
	engine  string
}

func answeredCells(result *runner.BenchmarkResult) map[cell]bool {
	answered := make(map[cell]bool)
	for _, jr := range result.Jobs {
		for queryID, byEngine := range jr.Results {
			for engineName, qr := range byEngine {
				if qr.Error == nil {
					answered[cell{queryID: queryID, engine: engineName}] = true
				}
			}
		}
	}
	return answered
}

// DroppedExecutions lists the (query, engine) pairs no job got an answer for,
// sorted by query then engine.
func DroppedExecutions(result *runner.BenchmarkResult) []meta.DroppedExecution {
	answered := answeredCells(result)
	failures := make(map[cell]string)
	for _, jr := range result.Jobs {
		for queryID, byEngine := range jr.Results {
			for engineName, qr := range byEngine {
				c := cell{queryID: queryID, engine: engineName}
				if qr.Error == nil || answered[c] {
					continue
				}
				if _, recorded := failures[c]; !recorded {
					failures[c] = qr.Error.Error()
				}
			}
		}
	}
	dropped := make([]meta.DroppedExecution, 0, len(failures))
	for c, msg := range failures {
		dropped = append(dropped, meta.DroppedExecution{QueryID: c.queryID, Engine: c.engine, Error: msg})
	}
	slices.SortFunc(dropped, func(a, b meta.DroppedExecution) int {
		return cmp.Or(cmp.Compare(a.QueryID, b.QueryID), cmp.Compare(a.Engine, b.Engine))
	})
	return dropped
}
