package rungate

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/tadjov/sengi/internal/storage"
)

// CheckCorpus refuses a run whose engines hold another canonical package than
// the pool was graded on, compared by resource hash since a package can be
// republished under its version. It returns what it could not compare.
func (j *Judgments) CheckCorpus(loads map[string]*storage.LoadRecord) ([]string, error) {
	if j.Meta == nil {
		return nil, nil
	}
	graded := j.Pool.Meta.Canonical
	if graded == nil {
		return []string{"the pool names no canonical package"}, nil
	}
	var unrecorded, others []string
	for _, name := range slices.Sorted(maps.Keys(loads)) {
		switch load := loads[name]; {
		case load == nil:
			unrecorded = append(unrecorded, name)
		case load.Canonical.ResourceHash != graded.ResourceHash:
			others = append(others, fmt.Sprintf("%s has %s %s (%s)",
				name, load.Canonical.Name, load.Canonical.Version, load.Canonical.ResourceHash))
		}
	}
	if len(others) > 0 {
		return nil, fmt.Errorf("the pool was built on %s %s (%s), but %s; reload the engines from that package, or re-pool and re-judge the track",
			graded.Name, graded.Version, graded.ResourceHash, strings.Join(others, ", "))
	}
	if len(unrecorded) > 0 {
		return []string{"engines " + strings.Join(unrecorded, ", ") + " record no corpus load"}, nil
	}
	return nil, nil
}
