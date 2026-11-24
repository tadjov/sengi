package judgment

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/storage"
	"github.com/tadjov/sengi/pkg/irmetrics"
)

// fakeVectorStore returns canned vectors so the scoring logic can be tested
// with no embedding backend or database.
type fakeVectorStore struct {
	query []float32
	docs  map[uuid.UUID][]float32
	model string
}

func (f fakeVectorStore) Model() string {
	return f.model
}

func (f fakeVectorStore) QueryVector(context.Context, string) ([]float32, error) {
	return f.query, nil
}

func (f fakeVectorStore) DocVectors(_ context.Context, ids []uuid.UUID) (map[uuid.UUID][]float32, error) {
	out := make(map[uuid.UUID][]float32)
	for _, id := range ids {
		if v, ok := f.docs[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

// angledDocs gives doc i the unit vector at i·5° from the query vector (1, 0),
// so its cosine falls as i grows. Ids are fixed so a tie breaks the same way
// on every run.
func angledDocs(n int) ([]GradingDoc, fakeVectorStore) {
	store := fakeVectorStore{query: []float32{1, 0}, docs: map[uuid.UUID][]float32{}}
	docs := make([]GradingDoc, n)
	for i := range n {
		id := uuid.MustParse(fmt.Sprintf("00000000-0000-4000-8000-%012d", i))
		angle := float64(i) * 5 * math.Pi / 180
		store.docs[id] = []float32{float32(math.Cos(angle)), float32(math.Sin(angle))}
		docs[i] = GradingDoc{ID: id}
	}
	return docs, store
}

func vectorGrades(t *testing.T, store fakeVectorStore, docs []GradingDoc) map[uuid.UUID]int {
	t.Helper()
	got, err := vectorStrategy(t, store, "fake-model").GradePool(context.Background(), GradingQuery{ID: "q", Text: "q"}, docs)
	if err != nil {
		t.Fatalf("GradePool: %v", err)
	}
	byID := map[uuid.UUID]int{}
	for _, g := range got {
		byID[g.DocID] = g.Grade
	}
	return byID
}

// The semantic track measures recall against exact search: the pool's docs
// nearest the query, as many as the track's largest K, are relevant, every
// other pooled doc is not.
func TestVectorGradePool_GradesTheDepthNearestDocsRelevantAndTheRestNot(t *testing.T) {
	docs, store := angledDocs(15)
	shuffled := slices.Clone(docs)
	rand.New(rand.NewPCG(1, 2)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

	got := vectorGrades(t, store, shuffled)

	require.Len(t, got, 15)
	for i, d := range docs {
		want := GradeNotRelev
		if i < testRelevantDepth {
			want = GradeHighly
		}
		assert.Equal(t, want, got[d.ID], "doc %d at %d°", i, i*5)
	}

	nearest := make([]uuid.UUID, testRelevantDepth)
	for i := range nearest {
		nearest[i] = docs[testRelevantDepth-1-i].ID
	}
	assert.Equal(t, 1.0, irmetrics.RecallAtK(nearest, got, testRelevantDepth, 1), "the exact top in reverse order")
	allButOneAndAFar := append(slices.Clone(nearest[:testRelevantDepth-1]), docs[14].ID)
	assert.InDelta(t, 0.75, irmetrics.RecallAtK(allButOneAndAFar, got, testRelevantDepth, 1), 1e-12)
}

func TestVectorGradePool_BreaksATieAtTheLastRelevantDocByID(t *testing.T) {
	docs, store := angledDocs(testRelevantDepth + 1)
	last := testRelevantDepth - 1
	store.docs[docs[last+1].ID] = store.docs[docs[last].ID]

	got := vectorGrades(t, store, docs)

	assert.Equal(t, GradeHighly, got[docs[last].ID], "the lower id of the tied pair")
	assert.Equal(t, GradeNotRelev, got[docs[last+1].ID])
}

func TestVectorGradePool_LeavesDocsWithoutAVectorUnjudged(t *testing.T) {
	docs, store := angledDocs(3)
	delete(store.docs, docs[1].ID)

	got := vectorGrades(t, store, docs)

	assert.NotContains(t, got, docs[1].ID)
	assert.Equal(t, GradeHighly, got[docs[0].ID])
	assert.Equal(t, GradeHighly, got[docs[2].ID])
}

func TestVectorDescribe(t *testing.T) {
	s := vectorStrategy(t, fakeVectorStore{}, "qwen3")

	got := s.Describe()
	if got.RelevantDepth != testRelevantDepth {
		t.Errorf("RelevantDepth = %d, want %d", got.RelevantDepth, testRelevantDepth)
	}
	if got.Strategy != string(StrategyVector) {
		t.Errorf("Strategy = %q, want %q", got.Strategy, StrategyVector)
	}
	if got.Model != "qwen3" {
		t.Errorf("Model = %q, want %q", got.Model, "qwen3")
	}
}

func TestCosine(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"identical", []float32{1, 0}, []float32{1, 0}, 1},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0},
		{"opposite", []float32{1, 0}, []float32{-1, 0}, -1},
		{"len mismatch", []float32{1, 0}, []float32{1}, 0},
		{"empty", nil, []float32{1}, 0},
		{"zero vector", []float32{0, 0}, []float32{1, 0}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cosine(c.a, c.b); got != c.want {
				t.Errorf("cosine = %v, want %v", got, c.want)
			}
		})
	}
}

// testRelevantDepth is not the spec loader's default largest K, so a judge that ignores it fails.
const testRelevantDepth = 4

func vectorStrategy(t *testing.T, store storage.VectorStore, model string) *VectorStrategy {
	t.Helper()
	s, err := NewVectorStrategy(StrategyOptions{VectorStore: store, EmbeddingModel: model, RelevantDepth: testRelevantDepth})
	require.NoError(t, err)
	return s
}

func hybridStrategy(t *testing.T, store storage.VectorStore, model string) *HybridStrategy {
	t.Helper()
	s, err := NewHybridStrategy(StrategyOptions{VectorStore: store, EmbeddingModel: model})
	require.NoError(t, err)
	return s
}
