package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/tadjov/sengi/internal/bench/engine"
	"github.com/tadjov/sengi/internal/bench/report"
	"github.com/tadjov/sengi/internal/bench/runner"
	"github.com/tadjov/sengi/internal/cli"
	"github.com/tadjov/sengi/internal/storage"
)

// indexDescribeTimeout bounds each engine's index read, so an engine that does
// not answer delays the command by this much rather than stalling it.
const indexDescribeTimeout = 15 * time.Second

// indexReading is what an engine told about its index, or why it could not.
type indexReading struct {
	description *engine.IndexDescription
	err         error
}

// readIndexes describes every engine that can describe its index; a failed read is warned about and kept.
func readIndexes(ctx context.Context, w io.Writer, executors map[string]engine.Executor) map[string]indexReading {
	readings := make(map[string]indexReading, len(executors))
	for name, exec := range executors {
		describer, ok := exec.(engine.IndexDescriber)
		if !ok {
			continue
		}
		description, err := describeIndex(ctx, describer)
		if err != nil {
			cli.Warn(w, fmt.Sprintf("index of %q not read: %v", name, err))
		}
		readings[name] = indexReading{description: description, err: err}
	}
	return readings
}

func describeIndex(ctx context.Context, describer engine.IndexDescriber) (*engine.IndexDescription, error) {
	ctx, cancel := context.WithTimeout(ctx, indexDescribeTimeout)
	defer cancel()
	return describer.DescribeIndex(ctx)
}

// loadRecords collects what each engine holding an index recorded of its
// corpus load; a fusion engine holds no index.
func loadRecords(readings map[string]indexReading) runner.EngineLoads {
	loads := runner.EngineLoads{Records: map[string]*storage.LoadRecord{}, Unread: map[string]error{}}
	for name, reading := range readings {
		switch {
		case reading.err != nil:
			loads.Unread[name] = reading.err
		case reading.description.Fusion == nil:
			loads.Records[name] = reading.description.Load()
		}
	}
	return loads
}

// indexProvenance records how every engine indexed the corpus, or why it could not be read.
func indexProvenance(executors map[string]engine.Executor, readings map[string]indexReading) map[string]report.IndexProvenance {
	provenance := make(map[string]report.IndexProvenance, len(executors))
	for name := range executors {
		reading, described := readings[name]
		switch {
		case !described:
			provenance[name] = report.IndexProvenance{Error: "engine cannot describe its index"}
		case reading.err != nil:
			provenance[name] = report.IndexProvenance{Error: reading.err.Error()}
		default:
			provenance[name] = report.IndexProvenance{IndexDescription: *reading.description}
		}
	}
	return provenance
}
