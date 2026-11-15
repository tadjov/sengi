package metrics

import (
	"math"
	"testing"
)

// Expected values are sklearn.metrics.cohen_kappa_score(first, second,
// weights="quadratic", labels=[0, 1, 2, 3]). The labels fix the 0–3 scale;
// without them sklearn renumbers the grades seen, which differs when one is unused.
func TestGradeConfusion_QuadraticKappaMatchesSklearn(t *testing.T) {
	tests := []struct {
		name          string
		first, second []int
		want          float64
	}{
		{
			name:   "perfect agreement",
			first:  []int{0, 1, 2, 3, 3, 2, 1, 0},
			second: []int{0, 1, 2, 3, 3, 2, 1, 0},
			want:   1.0,
		},
		{
			name:   "grade 2 used by neither rater still counts on the scale",
			first:  []int{0, 0, 1, 1, 3, 3, 0, 1},
			second: []int{0, 1, 1, 0, 3, 1, 0, 3},
			want:   0.5402298850574713,
		},
		{
			name:   "partial agreement over every grade",
			first:  []int{0, 1, 2, 3, 2, 1, 0, 3, 2, 2},
			second: []int{0, 2, 2, 3, 1, 1, 1, 2, 3, 0},
			want:   0.5714285714285714,
		},
		{
			name:   "reversed scale",
			first:  []int{0, 1, 2, 3},
			second: []int{3, 2, 1, 0},
			want:   -1.0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewGradeConfusion(tt.first, tt.second)
			if err != nil {
				t.Fatal(err)
			}
			if got := c.QuadraticKappa(); math.Abs(got-tt.want) > 1e-12 {
				t.Errorf("QuadraticKappa() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGradeConfusion_KappaIsUndefinedWithoutExpectedDisagreement(t *testing.T) {
	for name, grades := range map[string][2][]int{
		"no pairs":                  {nil, nil},
		"both raters use one grade": {{2, 2, 2}, {2, 2, 2}},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := NewGradeConfusion(grades[0], grades[1])
			if err != nil {
				t.Fatal(err)
			}
			if got := c.QuadraticKappa(); !math.IsNaN(got) {
				t.Errorf("QuadraticKappa() = %v, want NaN", got)
			}
		})
	}
}

func TestGradeConfusion_ExactAgreement(t *testing.T) {
	c, err := NewGradeConfusion([]int{2, 1, 0, 3, 3, 1}, []int{2, 2, 0, 3, 1, 1})
	if err != nil {
		t.Fatal(err)
	}
	if c.Pairs() != 6 {
		t.Errorf("Pairs() = %d, want 6", c.Pairs())
	}
	if got := c.ExactAgreement(); math.Abs(got-4.0/6.0) > 1e-12 {
		t.Errorf("ExactAgreement() = %v, want %v", got, 4.0/6.0)
	}
	if c[3][1] != 1 {
		t.Errorf("first rater 3, second rater 1 counted %d times, want 1", c[3][1])
	}
}

func TestNewGradeConfusion_RejectsBadInput(t *testing.T) {
	for name, grades := range map[string][2][]int{
		"different lengths": {{0, 1}, {0}},
		"grade above 3":     {{4}, {0}},
		"unjudged grade":    {{0}, {-1}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewGradeConfusion(grades[0], grades[1]); err == nil {
				t.Error("want an error")
			}
		})
	}
}

// Worked by hand: 18 of the 20 pairs sit on or next to the diagonal, and the
// weighted disagreement is 14 observed against 992/20 expected by chance.
func TestGradeConfusion_WithinOneAndQuadraticKappaOnAWorkedTable(t *testing.T) {
	c := GradeConfusion{
		{4, 1, 1, 0},
		{1, 3, 1, 0},
		{0, 1, 2, 1},
		{0, 1, 1, 3},
	}
	if got := c.WithinOneAgreement(); math.Abs(got-0.9) > 1e-12 {
		t.Errorf("WithinOneAgreement() = %v, want 0.9", got)
	}
	if got, want := c.QuadraticKappa(), 1-14/49.6; math.Abs(got-want) > 1e-12 {
		t.Errorf("QuadraticKappa() = %v, want %v", got, want)
	}
}

// Cohen's worked example: the 2x2 [[20, 5], [10, 15]] has κ = 0.4. The grades
// below collapse onto it at threshold 2.
func TestGradeConfusion_BinaryKappaCollapsesAtTheThreshold(t *testing.T) {
	c := GradeConfusion{
		{8, 4, 0, 0},
		{3, 5, 5, 0},
		{0, 0, 6, 4},
		{10, 0, 0, 5},
	}
	if got := c.BinaryKappa(2); math.Abs(got-0.4) > 1e-12 {
		t.Errorf("BinaryKappa(2) = %v, want 0.4", got)
	}
	if got := c.RelevantAgreement(2); math.Abs(got-0.7) > 1e-12 {
		t.Errorf("RelevantAgreement(2) = %v, want 0.7", got)
	}
}

func TestGradeConfusion_BinaryKappaIsUndefinedWhenBothRatersStayOnOneSide(t *testing.T) {
	c := GradeConfusion{{3, 2, 0, 0}, {1, 4, 0, 0}}
	if got := c.BinaryKappa(2); !math.IsNaN(got) {
		t.Errorf("BinaryKappa(2) = %v, want NaN", got)
	}
}

func confusionOf(t *testing.T, first, second []int) GradeConfusion {
	t.Helper()
	c, err := NewGradeConfusion(first, second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBootstrapKappa_IdenticalRatersGiveAnIntervalOfOne(t *testing.T) {
	byQuery := []GradeConfusion{
		confusionOf(t, []int{0, 0, 3}, []int{0, 0, 3}),
		confusionOf(t, []int{1, 2, 2}, []int{1, 2, 2}),
		confusionOf(t, []int{0, 3, 3}, []int{0, 3, 3}),
	}
	for name, kappa := range map[string]func(GradeConfusion) float64{
		"quadratic": GradeConfusion.QuadraticKappa,
		"binary":    func(c GradeConfusion) float64 { return c.BinaryKappa(2) },
	} {
		t.Run(name, func(t *testing.T) {
			got := BootstrapKappa(KappaBootstrap{ByQuery: byQuery, Kappa: kappa, Seed: 1})
			if got != (KappaInterval{Kappa: 1, Low: 1, High: 1}) {
				t.Errorf("BootstrapKappa() = %+v, want κ 1 in [1, 1]", got)
			}
		})
	}
}

func TestBootstrapKappa_IntervalHoldsTheEstimateAndRepeatsForASeed(t *testing.T) {
	byQuery := []GradeConfusion{
		confusionOf(t, []int{0, 1, 2, 3}, []int{0, 2, 2, 3}),
		confusionOf(t, []int{3, 3, 2, 0}, []int{2, 3, 1, 0}),
		confusionOf(t, []int{1, 1, 0, 0}, []int{0, 1, 0, 1}),
		confusionOf(t, []int{2, 3, 3, 1}, []int{3, 3, 2, 1}),
		confusionOf(t, []int{0, 0, 2, 3}, []int{0, 1, 3, 3}),
		confusionOf(t, []int{2, 1, 0, 3}, []int{1, 1, 0, 2}),
	}
	var all GradeConfusion
	for _, c := range byQuery {
		for i := range c {
			for j := range c[i] {
				all[i][j] += c[i][j]
			}
		}
	}

	got := BootstrapKappa(KappaBootstrap{ByQuery: byQuery, Kappa: GradeConfusion.QuadraticKappa, Seed: 7})

	if math.Abs(got.Kappa-all.QuadraticKappa()) > 1e-12 {
		t.Errorf("κ = %v, want the κ over every pair, %v", got.Kappa, all.QuadraticKappa())
	}
	if got.Low > got.Kappa || got.Kappa > got.High || got.Low == got.High {
		t.Errorf("interval [%v, %v] does not hold κ %v with any width", got.Low, got.High, got.Kappa)
	}
	if again := BootstrapKappa(KappaBootstrap{ByQuery: byQuery, Kappa: GradeConfusion.QuadraticKappa, Seed: 7}); again != got {
		t.Errorf("seed 7 gave %+v, then %+v", got, again)
	}
}
