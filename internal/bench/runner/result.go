package runner

import (
	"github.com/google/uuid"
	"github.com/tadjov/sengi/internal/bench/metrics"
)

type QueryResult struct {
	QueryID       string
	Category      string
	EngineName    string
	RequestDigest string
	Scores        metrics.ScoreSet
	RankedDocIDs  []uuid.UUID
	ReturnedCount int
	CorpusMatches *int64
	Latency       LatencyStats
	// ServerLatency is the server time the engine reported for each measured
	// iteration; nil when it reports none.
	ServerLatency *LatencyStats
	// UnstableIterations lists the measured iterations, counted from 1, whose
	// ranking differs from the first iteration's.
	UnstableIterations []int
	Error              error
}

type JobResult struct {
	JobName     string
	Results     map[string]map[string]QueryResult // [queryID][engineName]
	QueryOrder  []string
	EngineNames []string
}

type BenchmarkResult struct {
	Jobs   []*JobResult
	Config Config
}

func (br *BenchmarkResult) AllEngineNames() []string {
	seen := make(map[string]bool)
	var names []string
	for _, jr := range br.Jobs {
		for _, name := range jr.EngineNames {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	return names
}
