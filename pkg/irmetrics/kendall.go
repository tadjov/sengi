package irmetrics

import (
	"cmp"
	"math"
)

// KendallTauB is Kendall's τ-b between equal-length scorings x and y; NaN when either is all ties.
func KendallTauB(x, y []float64) float64 {
	var concordant, discordant, tiedX, tiedY, pairs int
	for i := range x {
		for j := i + 1; j < len(x); j++ {
			pairs++
			dx, dy := cmp.Compare(x[i], x[j]), cmp.Compare(y[i], y[j])
			switch {
			case dx == 0 || dy == 0:
				if dx == 0 {
					tiedX++
				}
				if dy == 0 {
					tiedY++
				}
			case dx == dy:
				concordant++
			default:
				discordant++
			}
		}
	}
	return float64(concordant-discordant) / math.Sqrt(float64((pairs-tiedX)*(pairs-tiedY)))
}
