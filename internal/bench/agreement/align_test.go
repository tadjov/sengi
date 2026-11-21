package agreement_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/bench/agreement"
	"github.com/tadjov/sengi/internal/bench/judgment"
	"github.com/tadjov/sengi/internal/bench/meta"
)

func set(label string, queries ...judgment.Entry) agreement.Set {
	return agreement.Set{
		Label: label,
		Path:  "annotations." + label + ".yaml",
		File:  &judgment.File{Meta: meta.Meta{RunID: "judge-" + label}, Queries: queries},
	}
}

func TestAlign_KeepsOnlyPairsEverySetGraded(t *testing.T) {
	a, b, c, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	sets := []agreement.Set{
		set("full", graded("q1", map[uuid.UUID]int{a: 3, b: 0, c: 2}), graded("q2", map[uuid.UUID]int{d: 1})),
		set("sample", graded("q1", map[uuid.UUID]int{a: 2, c: 1}), graded("q2", map[uuid.UUID]int{d: 0})),
		set("hand", graded("q1", map[uuid.UUID]int{a: 3, b: 1, c: judgment.GradeUnjudged}), graded("q2", map[uuid.UUID]int{d: 1})),
	}

	pairs, err := agreement.Align(sets)
	require.NoError(t, err)

	assert.ElementsMatch(t, []agreement.Pair{
		{QueryID: "q1", DocID: a, Grades: []int{3, 2, 3}},
		{QueryID: "q2", DocID: d, Grades: []int{1, 0, 1}},
	}, pairs)
}

func TestAlign_RefusesSetsWithNoPairInCommon(t *testing.T) {
	sets := []agreement.Set{
		set("first", graded("q1", map[uuid.UUID]int{uuid.New(): 3})),
		set("second", graded("q1", map[uuid.UUID]int{uuid.New(): 3})),
	}

	_, err := agreement.Align(sets)

	assert.ErrorContains(t, err, "share no (query, doc) pair")
}
