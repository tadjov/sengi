package judgment

import (
	"errors"
	"fmt"
	"os"

	"github.com/tadjov/sengi/internal/bench/artifact"
	"github.com/tadjov/sengi/internal/bench/meta"
	"github.com/tadjov/sengi/internal/bench/pool"
	"github.com/tadjov/sengi/internal/bench/version"
	"gopkg.in/yaml.v3"
)

// WriteFile stamps schema_version and the grade counts and replaces path atomically.
func WriteFile(jf *File, path string) error {
	jf.SchemaVersion = version.SchemaVersion
	graded, unjudged := jf.GradeCounts()
	jf.Meta.GradedCount, jf.Meta.UnjudgedCount = &graded, &unjudged
	data, err := yaml.Marshal(jf)
	if err != nil {
		return fmt.Errorf("marshal judgment file: %w", err)
	}
	return artifact.WriteAtomic(path, data)
}

func ReadFile(path string) (*File, error) {
	return artifact.ReadYAML(path, "annotations", func(jf File) int { return jf.SchemaVersion })
}

// ReadFileIfExists returns (nil, nil) when the file is missing.
func ReadFileIfExists(path string) (*File, error) {
	jf, err := ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return jf, err
}

// IncrementalWriter rewrites the file after every query, so an interrupted run
// can be resumed. Pass Append as the Runner's Sink.
type IncrementalWriter struct {
	Path    string
	stamp   meta.Meta
	current File
}

// NewIncrementalWriter writes stamp as the meta block of every flush, so a run
// interrupted mid-grade still leaves an artifact that names its grader and
// whatever else the caller set, which is what the resume guard reads back.
func NewIncrementalWriter(path string, stamp meta.Meta) *IncrementalWriter {
	return &IncrementalWriter{
		Path:    path,
		stamp:   stamp,
		current: File{Queries: []Entry{}},
	}
}

// Seed starts the writer from prior's queries still in pf, so every flush keeps them until the run reaches each.
// It drops prior's meta; a nil prior seeds nothing.
func (w *IncrementalWriter) Seed(prior *File, pf *pool.PoolFile) {
	w.current = File{Queries: []Entry{}}
	if prior == nil {
		return
	}
	byQuery := make(map[string]Entry, len(prior.Queries))
	for _, qe := range prior.Queries {
		byQuery[qe.QueryID] = qe
	}
	for _, entry := range pf.Queries {
		if qe, ok := byQuery[entry.QueryID]; ok {
			w.current.Queries = append(w.current.Queries, qe)
		}
	}
}

// Append adds (or updates) a query's judgments and flushes to disk atomically.
func (w *IncrementalWriter) Append(entry Entry) error {
	for i, qe := range w.current.Queries {
		if qe.QueryID == entry.QueryID {
			w.current.Queries[i] = entry
			return w.flush()
		}
	}
	w.current.Queries = append(w.current.Queries, entry)
	return w.flush()
}

func (w *IncrementalWriter) flush() error {
	w.current.Meta = w.stamp
	return WriteFile(&w.current, w.Path)
}
