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
	// ServerLatency is nil when the engine reports no server time.
	ServerLatency *LatencyStats
	// UnstableIterations are the iterations, from 1, that rank differently from the first.
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
