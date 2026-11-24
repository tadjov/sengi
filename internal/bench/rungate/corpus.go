package rungate

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/tadjov/sengi/internal/bench/runner"
)

// CheckCorpus refuses a run whose engines hold another canonical package than
// the pool was graded on, compared by resource hash since a package can be
// republished under its version. It returns what it could not compare.
func (j *Judgments) CheckCorpus(loads runner.EngineLoads) ([]string, error) {
	if j.Meta == nil {
		return nil, nil
	}
	graded := j.Pool.Meta.Canonical
	if graded == nil {
		return []string{"the pool names no canonical package"}, nil
	}
	var unrecorded, others []string
	for _, name := range slices.Sorted(maps.Keys(loads.Records)) {
		switch load := loads.Records[name]; {
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
	var unchecked []string
	if len(unrecorded) > 0 {
		unchecked = append(unchecked, "no corpus load recorded by "+strings.Join(unrecorded, ", "))
	}
	if len(loads.Unread) > 0 {
		unchecked = append(unchecked, "index not read for "+strings.Join(slices.Sorted(maps.Keys(loads.Unread)), ", "))
	}
	return unchecked, nil
}
