package metrics

import (
	"fmt"
	"math"
)

const gradeLevels = GradeHighly + 1

// GradeConfusion counts two raters' grades on the same pairs: row is the first
// rater's grade, column the second's.
type GradeConfusion [gradeLevels][gradeLevels]int

// NewGradeConfusion tabulates paired grades; first[i] and second[i] grade the
// same pair, and every grade must be on the 0–3 scale.
func NewGradeConfusion(first, second []int) (GradeConfusion, error) {
	var c GradeConfusion
	if len(first) != len(second) {
		return c, fmt.Errorf("%d grades from the first rater against %d from the second", len(first), len(second))
	}
	for i := range first {
		f, s := first[i], second[i]
		if f < GradeNotRelevant || f > GradeHighly || s < GradeNotRelevant || s > GradeHighly {
			return c, fmt.Errorf("pair %d has grades %d and %d, outside 0–3", i, f, s)
		}
		c[f][s]++
	}
	return c, nil
}

func (c GradeConfusion) Pairs() int {
	n := 0
	for _, row := range c {
		for _, count := range row {
			n += count
		}
	}
	return n
}

// ExactAgreement is the share of pairs both raters graded the same; NaN
// without pairs.
func (c GradeConfusion) ExactAgreement() float64 {
	agreed := 0
	for g := range gradeLevels {
		agreed += c[g][g]
	}
	return float64(agreed) / float64(c.Pairs())
}

// QuadraticKappa is Cohen's κ weighted by the squared grade distance over the
// whole 0–3 scale, so a grade neither rater used still sets the distances. It
// is NaN when chance alone predicts no disagreement: no pairs, or both raters
// giving one and the same grade throughout.
func (c GradeConfusion) QuadraticKappa() float64 {
	n := float64(c.Pairs())
	var firstTotals, secondTotals [gradeLevels]float64
	for i := range gradeLevels {
		for j := range gradeLevels {
			firstTotals[i] += float64(c[i][j])
			secondTotals[j] += float64(c[i][j])
		}
	}
	var observed, expected float64
	for i := range gradeLevels {
		for j := range gradeLevels {
			weight := float64((i - j) * (i - j))
			observed += weight * float64(c[i][j])
			expected += weight * firstTotals[i] * secondTotals[j] / n
		}
	}
	if expected == 0 || math.IsNaN(expected) {
		return math.NaN()
	}
	return 1 - observed/expected
}

// WithinOneAgreement is the share of pairs whose grades differ by at most
// one; NaN without pairs.
func (c GradeConfusion) WithinOneAgreement() float64 {
	near := 0
	for i := range gradeLevels {
		for j := range gradeLevels {
			if i-j <= 1 && j-i <= 1 {
				near += c[i][j]
			}
		}
	}
	return float64(near) / float64(c.Pairs())
}

// RelevantAgreement is the share of pairs both raters put on the same side of
// threshold, a grade at or above it being relevant; NaN without pairs.
func (c GradeConfusion) RelevantAgreement(threshold int) float64 {
	b := c.relevance(threshold)
	return float64(b[0][0]+b[1][1]) / float64(c.Pairs())
}

// BinaryKappa is Cohen's κ on relevant, a grade at or above threshold,
// against not relevant. It is NaN when chance alone predicts no disagreement:
// no pairs, or both raters keeping every pair on one side.
func (c GradeConfusion) BinaryKappa(threshold int) float64 {
	b := c.relevance(threshold)
	n := float64(c.Pairs())
	observed := float64(b[0][0]+b[1][1]) / n
	firstRelevant := float64(b[1][0]+b[1][1]) / n
	secondRelevant := float64(b[0][1]+b[1][1]) / n
	expected := firstRelevant*secondRelevant + (1-firstRelevant)*(1-secondRelevant)
	return (observed - expected) / (1 - expected)
}

// relevance collapses the table to 2x2, index 1 being relevant.
func (c GradeConfusion) relevance(threshold int) [2][2]int {
	side := func(grade int) int {
		if grade >= threshold {
			return 1
		}
		return 0
	}
	var b [2][2]int
	for i := range gradeLevels {
		for j := range gradeLevels {
			b[side(i)][side(j)] += c[i][j]
		}
	}
	return b
}
