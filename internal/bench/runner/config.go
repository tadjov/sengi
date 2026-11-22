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
	RankingDepth int
	// Concurrency is how many executions are in flight at once. 1, the
	// default, measures: queries run in suite order and each iteration visits
	// the engines one at a time in a seeded order, so nothing else loads the
	// host while a latency is taken. Above 1 only collects rankings, as pooling
	// does.
	Concurrency int
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
		Concurrency:        1,
	}
}

func (c Config) validate() error {
	if c.Runs < 1 {
		return fmt.Errorf("measured iterations must be at least 1, got %d", c.Runs)
	}
	if c.Concurrency < 0 {
		return fmt.Errorf("concurrency must not be negative, got %d", c.Concurrency)
	}
	if c.WarmupRuns < 0 {
		return fmt.Errorf("warmup iterations must not be negative, got %d", c.WarmupRuns)
	}
	return nil
}

func (c Config) concurrent() bool {
	return c.Concurrency > 1
}
