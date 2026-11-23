package storage

import (
	"time"

	"github.com/tadjov/sengi/internal/progress"
)

// LoadStopwatch times a corpus load and its stages on the same monotonic clock, naming each stage on its counter.
type LoadStopwatch struct {
	started time.Time
	stages  []StageTiming
	counter progress.Counter
}

// StartLoad starts the load's clock; a nil counter counts nothing.
func StartLoad(counter progress.Counter) *LoadStopwatch {
	return &LoadStopwatch{started: time.Now(), stages: []StageTiming{}, counter: progress.Or(counter)}
}

func (s *LoadStopwatch) Counter() progress.Counter { return s.counter }

// Stage runs one step of the load and records its duration if it succeeds.
func (s *LoadStopwatch) Stage(name string, run func() error) error {
	end := s.counter.Stage(name)
	began := time.Now()
	if err := run(); err != nil {
		return err
	}
	s.stages = append(s.stages, StageTiming{Name: name, Seconds: time.Since(began).Seconds()})
	end()
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
