package rungate

import (
	"fmt"
	"slices"
)

// CheckRelevantDepth refuses scoring deeper than the vector judge graded: past
// its depth, recall no longer measures recall of exact search's top K.
func (j *Judgments) CheckRelevantDepth(kValues []int) error {
	if j.Meta == nil || j.Meta.Judge == nil {
		return nil
	}
	depth, deepest := j.Meta.Judge.RelevantDepth, slices.Max(kValues)
	if depth == 0 || deepest <= depth {
		return nil
	}
	return fmt.Errorf("the vector judge graded the %d docs nearest each query relevant, so recall at K %d is not recall of exact search's top %d; re-judge with --strategy vector, or score at K up to %d",
		depth, deepest, deepest, depth)
}
