package runner

import (
	"fmt"

	"github.com/tadjov/sengi/internal/progress"
	"github.com/tadjov/sengi/internal/storage"
)

var DefaultKValues = []int{3, 5, 10}

const (
	DefaultRelevanceThreshold = 1
	DefaultWarmupRuns         = 1
	DefaultRuns               = 3
	DefaultEngineParallelism  = 4

	// Serial keeps a measured run's latency free of contention from other
	// queries or engines on the same host; unlimited is for pooling, where only
	// the returned doc IDs matter.
	QueryParallelismSerial     = 1
	QueryParallelismUnlimited  = 0
	EngineParallelismSerial    = 1
	EngineParallelismUnlimited = 0
)

type Config struct {
	KValues            []int
	RelevanceThreshold int
	WarmupRuns         int
	Runs               int
	// Seed draws the engine order of each warmup and iteration when engines run one at a time.
	Seed uint64
	// RankingDepth cuts every ranking before it is scored or stored, so scores
	// count only docs a pool that deep could have judged. Zero keeps it whole.
	RankingDepth     int
	QueryParallelism int
	// EngineParallelism bounds concurrent engine calls within one query.
	EngineParallelism int
	// Judgments[queryID][docID] is the grade; nil scores nothing.
	Judgments map[string]map[string]int
	// VectorStore supplies the vector of a query that takes one; nil for lexical tracks.
	VectorStore storage.VectorStore
	// Progress counts each job's queries as they finish; nil counts nothing.
	Progress progress.Counter
}

func DefaultConfig() Config {
	return Config{
		KValues:            DefaultKValues,
		RelevanceThreshold: DefaultRelevanceThreshold,
		WarmupRuns:         DefaultWarmupRuns,
		Runs:               DefaultRuns,
		QueryParallelism:   QueryParallelismSerial,
		EngineParallelism:  DefaultEngineParallelism,
	}
}

func (c Config) validate() error {
	if c.Runs < 1 {
		return fmt.Errorf("measured iterations must be at least 1, got %d", c.Runs)
	}
	if c.WarmupRuns < 0 {
		return fmt.Errorf("warmup iterations must not be negative, got %d", c.WarmupRuns)
	}
	return nil
}
