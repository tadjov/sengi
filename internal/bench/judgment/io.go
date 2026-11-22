package judgment

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tadjov/sengi/internal/bench/meta"
	"github.com/tadjov/sengi/internal/bench/pool"
	"github.com/tadjov/sengi/internal/bench/version"
	"gopkg.in/yaml.v3"
)

// WriteFile writes a JudgmentFile atomically: marshals to a tmp file in the
// same directory, fsyncs, then renames. Prevents readers from seeing a
// half-written YAML if the process is killed mid-write. Stamps schema_version
// and the grade counts on every write so reloads always see a current artifact.
func WriteFile(jf *File, path string) error {
	jf.SchemaVersion = version.SchemaVersion
	graded, unjudged := jf.GradeCounts()
	jf.Meta.GradedCount, jf.Meta.UnjudgedCount = &graded, &unjudged
	data, err := yaml.Marshal(jf)
	if err != nil {
		return fmt.Errorf("marshal judgment file: %w", err)
	}
	return writeAtomic(path, data)
}

func ReadFile(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read judgment file: %w", err)
	}
	var jf File
	if err := yaml.Unmarshal(data, &jf); err != nil {
		return nil, fmt.Errorf("parse judgment file: %w", err)
	}
	if err := version.CheckSchema(jf.SchemaVersion, "annotations"); err != nil {
		return nil, err
	}
	return &jf, nil
}

// ReadFileIfExists is like ReadFile but returns (nil, nil) when the file is
// missing — useful for resume flows where the prior output may not exist yet.
func ReadFileIfExists(path string) (*File, error) {
	jf, err := ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return jf, err
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".bench-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename temp: %w", err)
	}
	return nil
}

// IncrementalWriter persists a JudgmentFile after every query so the user can
// resume after a crash, Ctrl-C, or rate-limit interrupt. Pass Append on the
// Runner's Sink.
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
func (w *IncrementalWriter) Append(_ QueryProgress, entry Entry) error {
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
