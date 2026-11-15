package agreement

import (
	"cmp"
	"slices"

	"github.com/google/uuid"
)

// Disagreement is a pair whose grades span at least the minimum gap.
type Disagreement struct {
	Pair
	Span int `json:"span"`
	// Title is empty unless the articles were read.
	Title string `json:"title,omitempty"`
}

// Disagreements returns the pairs whose grades span at least minGap, widest first.
func Disagreements(pairs []Pair, minGap int) []Disagreement {
	var out []Disagreement
	for _, p := range pairs {
		if span := slices.Max(p.Grades) - slices.Min(p.Grades); span >= minGap {
			out = append(out, Disagreement{Pair: p, Span: span})
		}
	}
	slices.SortFunc(out, func(a, b Disagreement) int {
		return cmp.Or(cmp.Compare(b.Span, a.Span), cmp.Compare(a.QueryID, b.QueryID), compareDocs(a.DocID, b.DocID))
	})
	return out
}

func compareDocs(a, b uuid.UUID) int { return cmp.Compare(a.String(), b.String()) }
