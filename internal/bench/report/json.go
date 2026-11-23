package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tadjov/sengi/internal/bench/version"
)

func WriteJSON(r *Report, path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

func ReadJSON(path string) (*Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read report %s: %w", path, err)
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse report %s: %w", path, err)
	}
	if err := version.CheckSchema(r.SchemaVersion, "report"); err != nil {
		return nil, fmt.Errorf("read report %s: %w", path, err)
	}
	return &r, nil
}

type latestPointer struct {
	Latest string `json:"latest"`
}

// WriteLatestPointer points latestPath at reportPath, relative to the pointer
// so the track folder can move.
func WriteLatestPointer(latestPath, reportPath string) error {
	rel, err := filepath.Rel(filepath.Dir(latestPath), reportPath)
	if err != nil {
		rel = reportPath
	}
	data, err := json.Marshal(latestPointer{Latest: rel})
	if err != nil {
		return fmt.Errorf("marshal latest pointer: %w", err)
	}
	return os.WriteFile(latestPath, append(data, '\n'), 0644)
}

// LatestTarget is the report file latestPath points at.
func LatestTarget(latestPath string) (string, error) {
	data, err := os.ReadFile(latestPath)
	if err != nil {
		return "", fmt.Errorf("read latest pointer %s: %w", latestPath, err)
	}
	var pointer latestPointer
	if err := json.Unmarshal(data, &pointer); err != nil {
		return "", fmt.Errorf("parse latest pointer %s: %w", latestPath, err)
	}
	if pointer.Latest == "" {
		return "", fmt.Errorf("latest pointer %s has no 'latest' field", latestPath)
	}
	if filepath.IsAbs(pointer.Latest) {
		return filepath.Clean(pointer.Latest), nil
	}
	return filepath.Join(filepath.Dir(latestPath), pointer.Latest), nil
}

func ReadLatestReport(latestPath string) (*Report, error) {
	reportPath, err := LatestTarget(latestPath)
	if err != nil {
		return nil, err
	}
	return ReadJSON(reportPath)
}
