package agreement_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/tadjov/tusker/internal/bench/agreement"
)

func TestDisagreements_WidestSpanFirstThenQueryThenDoc(t *testing.T) {
	low, high := uuid.MustParse("00000000-0000-0000-0000-000000000001"), uuid.MustParse("ffffffff-0000-0000-0000-000000000000")
	common := []agreement.Pair{
		{QueryID: "q1", DocID: high, Grades: []int{0, 2, 1}},
		{QueryID: "q2", DocID: low, Grades: []int{3, 0, 3}},
		{QueryID: "q1", DocID: low, Grades: []int{1, 3, 2}},
		{QueryID: "q0", DocID: high, Grades: []int{1, 3, 2}},
		{QueryID: "q0", DocID: low, Grades: []int{3, 3, 3}},
	}

	got := agreement.Disagreements(common, 2)

	assert.Equal(t, []agreement.Disagreement{
		{Pair: common[1], Span: 3},
		{Pair: common[3], Span: 2},
		{Pair: common[2], Span: 2},
		{Pair: common[0], Span: 2},
	}, got)
}

func TestDisagreements_MinimumGapIsInclusive(t *testing.T) {
	common := []agreement.Pair{
		{QueryID: "q1", DocID: uuid.New(), Grades: []int{0, 1}},
		{QueryID: "q1", DocID: uuid.New(), Grades: []int{2, 2}},
	}

	assert.Len(t, agreement.Disagreements(common, 1), 1)
	assert.Len(t, agreement.Disagreements(common, 0), 2)
	assert.Empty(t, agreement.Disagreements(common, 2))
}
