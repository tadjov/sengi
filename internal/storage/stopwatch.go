package storage

import (
	"log/slog"
	"slices"
	"time"
)

// LoadStopwatch times a corpus load and its stages on the same monotonic clock.
type LoadStopwatch struct {
	started time.Time
	attrs   []any
	stages  []StageTiming
}

// StartLoad starts the load's clock; attrs label its stage log lines.
func StartLoad(attrs ...any) *LoadStopwatch {
	return &LoadStopwatch{started: time.Now(), attrs: attrs, stages: []StageTiming{}}
}

// Stage runs one step of the load and records its duration if it succeeds.
func (s *LoadStopwatch) Stage(name string, run func() error) error {
	attrs := append(slices.Clone(s.attrs), "stage", name)
	slog.Info("corpus load stage started", attrs...)
	began := time.Now()
	if err := run(); err != nil {
		return err
	}
	seconds := time.Since(began).Seconds()
	s.stages = append(s.stages, StageTiming{Name: name, Seconds: seconds})
	slog.Info("corpus load stage finished", append(attrs, "seconds", seconds)...)
	return nil
}

// Finish stamps the record's start and end and fills its stages and total.
func (s *LoadStopwatch) Finish(record *LoadRecord) {
	finished := time.Now()
	record.StartedAt = loadStamp(s.started)
	record.FinishedAt = loadStamp(finished)
	record.TotalSeconds = finished.Sub(s.started).Seconds()
	record.Stages = s.stages
}

// loadStamp keeps timestamptz's microseconds, so records round-trip through corpus_load.
func loadStamp(t time.Time) time.Time {
	return t.UTC().Truncate(time.Microsecond)
}
