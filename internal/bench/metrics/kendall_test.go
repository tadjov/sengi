package metrics

import (
	"math"
	"testing"
)

// Expected values worked by hand: (concordant − discordant) / √((n − x ties)(n − y ties)).
func TestKendallTauB(t *testing.T) {
	tests := []struct {
		name string
		x, y []float64
		want float64
	}{
		{name: "one swapped pair of six", x: []float64{1, 2, 3, 4}, y: []float64{1, 3, 2, 4}, want: 4.0 / 6.0},
		{name: "reversed order", x: []float64{1, 2, 3}, y: []float64{3, 2, 1}, want: -1},
		{name: "a tie in each scoring", x: []float64{1, 2, 2, 3}, y: []float64{1, 2, 3, 3}, want: 4.0 / 5.0},
		{name: "a pair tied in both and three discordant", x: []float64{1, 1, 2, 3}, y: []float64{2, 2, 3, 1}, want: -1.0 / 5.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := KendallTauB(tt.x, tt.y); math.Abs(got-tt.want) > 1e-12 {
				t.Errorf("KendallTauB() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestKendallTauB_IsUndefinedWhenOneScoringTiesEverything(t *testing.T) {
	if got := KendallTauB([]float64{0.5, 0.5}, []float64{0.4, 0.6}); !math.IsNaN(got) {
		t.Errorf("KendallTauB() = %v, want NaN", got)
	}
}
