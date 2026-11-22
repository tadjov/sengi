// Package artifact reads and writes the YAML files a track keeps on disk.
package artifact

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tadjov/sengi/internal/bench/version"
	"gopkg.in/yaml.v3"
)

// ReadYAML decodes the artifact at path and refuses any schema version but the
// current one; kind names the artifact in errors.
func ReadYAML[T any](path, kind string, schemaVersion func(T) int) (*T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s file: %w", kind, err)
	}
	var file T
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse %s file: %w", kind, err)
	}
	if err := version.CheckSchema(schemaVersion(file), kind); err != nil {
		return nil, err
	}
	return &file, nil
}

// WriteAtomic replaces path through a synced temp file in its directory, so a
// killed process never leaves a half-written artifact.
func WriteAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".bench-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync temp: %w", err)
	}
	// CreateTemp makes 0600 files.
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rename temp: %w", err)
	}
	return nil
}
