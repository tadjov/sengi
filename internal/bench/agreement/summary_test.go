package agreement_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/tusker/internal/bench/agreement"
	"github.com/tadjov/tusker/internal/bench/metrics"
	"github.com/tadjov/tusker/internal/bench/suite"
)

func pairs(queryID string, grades ...[]int) []agreement.Pair {
	var out []agreement.Pair
	for doc := range grades[0] {
		pair := agreement.Pair{QueryID: queryID, DocID: uuid.New()}
		for _, set := range grades {
			pair.Grades = append(pair.Grades, set[doc])
		}
		out = append(out, pair)
	}
	return out
}

// Worked by hand over the six pairs (3,3) (2,1) (0,0) (1,3) (2,2) (0,1).
func TestSummarize_ComparesEveryPairOfSets(t *testing.T) {
	common := append(pairs("q1", []int{3, 2, 0, 1}, []int{3, 1, 0, 3}), pairs("q2", []int{2, 0}, []int{2, 1})...)

	got, err := agreement.Summarize(common, agreement.SummaryOptions{Labels: []string{"a", "b"}, Threshold: 2, Seed: 1})
	require.NoError(t, err)

	assert.Equal(t, 6, got.Pairs)
	assert.Equal(t, []agreement.Distribution{{Label: "a", Grades: [4]int{2, 1, 2, 1}}, {Label: "b", Grades: [4]int{1, 2, 1, 2}}}, got.Distributions)
	require.Len(t, got.Comparisons, 1)
	c := got.Comparisons[0]
	assert.Equal(t, "a", c.First)
	assert.Equal(t, "b", c.Second)
	assert.Equal(t, 6, c.Pairs)
	assert.InDelta(t, 0.5, c.Exact, 1e-12)
	assert.InDelta(t, 5.0/6.0, c.WithinOne, 1e-12)
	assert.InDelta(t, 4.0/6.0, c.Relevant, 1e-12)
	assert.InDelta(t, 14.0/23.0, float64(c.QuadraticKappa.Value), 1e-12)
	assert.InDelta(t, 1.0/3.0, float64(c.BinaryKappa.Value), 1e-12)
}

func TestSummarize_RelevantMeansAtOrAboveTheThreshold(t *testing.T) {
	common := append(pairs("q1", []int{3, 2, 0, 1}, []int{3, 1, 0, 3}), pairs("q2", []int{2, 0}, []int{2, 1})...)

	got, err := agreement.Summarize(common, agreement.SummaryOptions{Labels: []string{"a", "b"}, Threshold: 3, Seed: 1})
	require.NoError(t, err)

	assert.InDelta(t, 5.0/6.0, got.Comparisons[0].Relevant, 1e-12)
	assert.InDelta(t, 4.0/7.0, float64(got.Comparisons[0].BinaryKappa.Value), 1e-12)
}

func TestSummarize_ListsSetPairsInEntryOrder(t *testing.T) {
	common := pairs("q1", []int{0, 3}, []int{0, 3}, []int{1, 3})

	got, err := agreement.Summarize(common, agreement.SummaryOptions{Labels: []string{"a", "b", "c"}, Threshold: 2, Seed: 1})
	require.NoError(t, err)

	var names [][2]string
	for _, c := range got.Comparisons {
		names = append(names, [2]string{c.First, c.Second})
	}
	assert.Equal(t, [][2]string{{"a", "b"}, {"a", "c"}, {"b", "c"}}, names)
}

func TestSummarize_AgainstAReference(t *testing.T) {
	common := append(pairs("q1", []int{0, 0, 2, 3}, []int{2, 0, 1, 3}), pairs("q2", []int{1, 3}, []int{1, 3})...)

	got, err := agreement.Summarize(common, agreement.SummaryOptions{
		Labels:     []string{"ref", "other"},
		Threshold:  2,
		Seed:       1,
		Reference:  "ref",
		Categories: map[string]suite.Category{"q1": suite.CategoryPhrase, "q2": suite.CategoryKeyword},
	})
	require.NoError(t, err)

	require.NotNil(t, got.Reference)
	require.Len(t, got.Reference.Against, 1)
	against := got.Reference.Against[0]
	assert.Equal(t, "other", against.Label)
	assert.Equal(t, metrics.GradeConfusion{{1, 0, 1, 0}, {0, 1, 0, 0}, {0, 1, 0, 0}, {0, 0, 0, 2}}, against.Table)
	assert.Equal(t, 3, against.ReferenceIrrelevant)
	assert.Equal(t, 1, against.GradedRelevant)
	assert.Equal(t, 3, against.ReferenceRelevant)
	assert.Equal(t, 1, against.GradedIrrelevant)

	exactByCategory := map[suite.Category]float64{}
	for _, category := range got.Reference.ByCategory {
		require.Len(t, category.Comparisons, 1)
		exactByCategory[category.Category] = category.Comparisons[0].Exact
	}
	assert.Equal(t, map[suite.Category]float64{suite.CategoryPhrase: 0.5, suite.CategoryKeyword: 1}, exactByCategory)
}

func TestSummarize_RefusesAnUnknownReference(t *testing.T) {
	_, err := agreement.Summarize(pairs("q1", []int{0}, []int{0}), agreement.SummaryOptions{Labels: []string{"a", "b"}, Reference: "opus"})

	assert.ErrorContains(t, err, `"opus"`)
}
