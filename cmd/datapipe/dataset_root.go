package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// datasetRoot maps datasets/<ds>/canonical/<version>/ to datasets/<ds>/.
func datasetRoot(canonical string) (string, error) {
	if strings.Contains(canonical, "://") {
		return "", fmt.Errorf("canonical package %s is remote", canonical)
	}
	dir := canonical
	if filepath.Ext(dir) == ".json" {
		dir = filepath.Dir(dir)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", canonical, err)
	}
	if filepath.Base(filepath.Dir(dir)) != "canonical" {
		return "", fmt.Errorf("canonical package %s is not under datasets/<ds>/canonical/<version>/", canonical)
	}
	return filepath.Dir(filepath.Dir(dir)), nil
}
