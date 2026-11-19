package engine

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/tusker/internal/bench/spec"
)

var (
	docA = uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000000")
	docB = uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000000")
	docC = uuid.MustParse("cccccccc-0000-0000-0000-000000000000")
	docD = uuid.MustParse("dddddddd-0000-0000-0000-000000000000")
)

func TestFuseRRF(t *testing.T) {
	tests := []struct {
		name     string
		rankings [][]uuid.UUID
		window   int
		want     []uuid.UUID
	}{
		{
			// c: 1/63 + 1/61 beats a: 1/61; b and d tie at 1/62 and order by id.
			name:     "a doc in both rankings outranks the top of one",
			rankings: [][]uuid.UUID{{docA, docB, docC}, {docC, docD}},
			window:   100,
			want:     []uuid.UUID{docC, docA, docB, docD},
		},
		{
			// a and d tie at 1/61, b and c at 1/62, and the fused list keeps two.
			// Counting c's third place would give it 1/62 + 1/63 and the top.
			name:     "ranks past the window do not count",
			rankings: [][]uuid.UUID{{docA, docB, docC}, {docD, docC}},
			window:   2,
			want:     []uuid.UUID{docA, docD},
		},
		{
			name:     "an equal score orders by id whichever ranking holds the doc",
			rankings: [][]uuid.UUID{{docD}, {docA}},
			window:   100,
			want:     []uuid.UUID{docA, docD},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FuseRRF(tt.rankings, 60, tt.window))
		})
	}
}

// Ranks count from 1, as ROW_NUMBER() does in the SQL fusions: top of one
// ranking scores 1/61, exactly what rank 62 of both scores, so the tie goes to
// the smaller id. Counting from 0 would give 1/60 against 2/121 instead.
func TestFuseRRF_RanksCountFromOne(t *testing.T) {
	top, bothAt62 := docD, docA
	first, second := []uuid.UUID{top}, []uuid.UUID{}
	for len(first) < 61 {
		first = append(first, uuid.UUID{1, byte(len(first))})
	}
	for len(second) < 61 {
		second = append(second, uuid.UUID{2, byte(len(second))})
	}
	first, second = append(first, bothAt62), append(second, bothAt62)

	fused := FuseRRF([][]uuid.UUID{first, second}, 60, 100)

	assert.Less(t, slices.Index(fused, bothAt62), slices.Index(fused, top))
}

type fixedExecutor struct {
	ranking  []uuid.UUID
	err      error
	received []Request
}

func (e *fixedExecutor) Execute(_ context.Context, req Request) (*Execution, error) {
	e.received = append(e.received, req)
	if e.err != nil {
		return nil, e.err
	}
	return &Execution{RankedDocIDs: e.ranking}, nil
}

func (e *fixedExecutor) Name() string { return "fixed" }
func (e *fixedExecutor) Close() error { return nil }

func (e *fixedExecutor) Validate(_ context.Context, req Request) error {
	e.received = append(e.received, req)
	return e.err
}

var lexicalAndVector = spec.Fusion{Method: spec.FusionRRF, RankConstant: 60, Window: 100, Legs: []string{"lexical", "vector"}}

func legRequests(lexical, vector string) Request {
	return Request{Legs: []LegRequest{
		{Engine: "lexical", Request: Request{Query: lexical}},
		{Engine: "vector", Request: Request{Query: vector}},
	}}
}

func TestFusionExecutor_RunsEachLegsOwnRequestAndFusesTheirRankings(t *testing.T) {
	lexical := &fixedExecutor{ranking: []uuid.UUID{docA, docB, docC}}
	vector := &fixedExecutor{ranking: []uuid.UUID{docC, docD}}
	fusion := NewFusionExecutor("hybrid", lexicalAndVector, map[string]Executor{"lexical": lexical, "vector": vector})

	exec, err := fusion.Execute(context.Background(), legRequests("SELECT lexical", "SELECT vector"))

	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{docC, docA, docB, docD}, exec.RankedDocIDs)
	assert.Equal(t, []Request{{Query: "SELECT lexical"}}, lexical.received)
	assert.Equal(t, []Request{{Query: "SELECT vector"}}, vector.received)
	assert.Nil(t, exec.CorpusMatches)
}

func TestFusionExecutor_RefusesRequestsThatAreNotItsLegsInOrder(t *testing.T) {
	executors := map[string]Executor{"lexical": &fixedExecutor{}, "vector": &fixedExecutor{}}
	fusion := NewFusionExecutor("hybrid", lexicalAndVector, executors)
	swapped := Request{Legs: []LegRequest{{Engine: "vector"}, {Engine: "lexical"}}}

	_, err := fusion.Execute(context.Background(), swapped)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "hybrid")
}

func TestFusionExecutor_AFailedLegFailsTheQueryNamingTheLeg(t *testing.T) {
	executors := map[string]Executor{
		"lexical": &fixedExecutor{ranking: []uuid.UUID{docA}},
		"vector":  &fixedExecutor{err: errors.New("knn timed out")},
	}
	fusion := NewFusionExecutor("hybrid", lexicalAndVector, executors)

	_, err := fusion.Execute(context.Background(), legRequests("a", "b"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), `"vector"`)
	assert.Contains(t, err.Error(), "knn timed out")
}

func TestFusionExecutor_ValidatesEveryLegOnItsOwnExecutor(t *testing.T) {
	lexical := &fixedExecutor{}
	vector := &fixedExecutor{err: errors.New("bad knn clause")}
	fusion := NewFusionExecutor("hybrid", lexicalAndVector, map[string]Executor{"lexical": lexical, "vector": vector})

	err := fusion.Validate(context.Background(), legRequests("SELECT lexical", "SELECT vector"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad knn clause")
	assert.Equal(t, []Request{{Query: "SELECT lexical"}}, lexical.received)
}
