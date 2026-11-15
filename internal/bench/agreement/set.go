// Package agreement measures how alike judgment sets grade pairs and order a run's engines.
package agreement

import (
	"errors"
	"fmt"
	"strings"

	"github.com/tadjov/tusker/internal/bench/judgment"
	"github.com/tadjov/tusker/internal/bench/trackctx"
)

// Set is one judgment set under the label its output carries.
type Set struct {
	Label string
	Path  string
	File  *judgment.File
}

// Load reads `[label=]<set or path>` entries; an unlabelled set is named after its judge's model.
func Load(track *trackctx.Track, entries []string) ([]Set, error) {
	if len(entries) < 2 {
		return nil, fmt.Errorf("--judgments needs at least two sets to compare, got %d", len(entries))
	}
	sets := make([]Set, 0, len(entries))
	byLabel := make(map[string]string, len(entries))
	for _, entry := range entries {
		label, value, labelled := strings.Cut(entry, "=")
		if !labelled {
			value, label = entry, ""
		}
		path := track.JudgmentsPath(value)
		file, err := judgment.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read judgments %s: %w", path, err)
		}
		if label == "" {
			if label, err = defaultLabel(file); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
		}
		if other, taken := byLabel[label]; taken {
			return nil, fmt.Errorf("%s and %s both go by %q: name one with label=<set>", other, path, label)
		}
		byLabel[label] = path
		sets = append(sets, Set{Label: label, Path: path, File: file})
	}
	return sets, nil
}

func defaultLabel(file *judgment.File) (string, error) {
	judge := file.Judge()
	switch {
	case judge.Model != "":
		return judge.Model, nil
	case judge.Strategy != "":
		return judge.Strategy, nil
	default:
		return "", errors.New("names no judge to label it by: name it with label=<set>")
	}
}
