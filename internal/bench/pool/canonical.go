package pool

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/tadjov/sengi/internal/storage"
)

// SharedCanonical is the canonical package every engine loaded; nil when none records one.
func SharedCanonical(loads map[string]storage.PackageRef) (*storage.PackageRef, error) {
	engines := slices.Sorted(maps.Keys(loads))
	if len(engines) == 0 {
		return nil, nil
	}
	shared := loads[engines[0]]
	for _, name := range engines[1:] {
		if loads[name].ResourceHash != shared.ResourceHash {
			return nil, fmt.Errorf("engines loaded different canonical packages: %s", describeLoads(loads, engines))
		}
	}
	return &shared, nil
}

func describeLoads(loads map[string]storage.PackageRef, engines []string) string {
	parts := make([]string, len(engines))
	for i, name := range engines {
		ref := loads[name]
		parts[i] = fmt.Sprintf("%s has %s %s (%s)", name, ref.Name, ref.Version, ref.ResourceHash)
	}
	return strings.Join(parts, ", ")
}
