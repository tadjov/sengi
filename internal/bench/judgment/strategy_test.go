package judgment

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewStrategy_VectorHybridRequireVectorStore(t *testing.T) {
	// vector/hybrid are implemented but need a vector store; without one they
	// must fail loudly rather than silently producing no grades.
	for _, kind := range []StrategyKind{StrategyVector, StrategyHybrid} {
		_, err := NewStrategy(kind, StrategyOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "vector store",
			"strategy %q should require a vector store", kind)
	}
}

func TestNewStrategy_VectorHybridWithStore(t *testing.T) {
	for _, kind := range []StrategyKind{StrategyVector, StrategyHybrid} {
		s, err := NewStrategy(kind, StrategyOptions{VectorStore: fakeVectorStore{}})
		require.NoError(t, err)
		assert.Equal(t, string(kind), s.Name())
	}
}

func TestNewStrategy_UnknownName(t *testing.T) {
	_, err := NewStrategy("bogus", StrategyOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown strategy")
}

// Heuristic judges have no prompt, so the rubric version is what tells two
// grade sets under the same strategy apart.
func TestHeuristicStrategies_NameTheirRubric(t *testing.T) {
	for _, kind := range []StrategyKind{StrategyKeyword, StrategyBM25, StrategyVector, StrategyHybrid} {
		s, err := NewStrategy(kind, StrategyOptions{VectorStore: fakeVectorStore{}})
		require.NoError(t, err)
		assert.NotEmpty(t, s.Describe().RubricVersion, "strategy %q", kind)
	}
}

// A gate change needs one bump, so every judge that gates names the same version.
func TestGatingStrategies_NameOneGateVersion(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", testJevKey)
	jev, err := NewStrategy(StrategyJev, StrategyOptions{Model: DefaultJevModel})
	require.NoError(t, err)
	llm := &LLMStrategy{provider: &fakeLLMProvider{}}

	for _, s := range []Strategy{NewKeywordStrategy(), NewBM25Strategy(), llm, jev} {
		assert.Equal(t, gateVersion, s.Describe().GateVersion, "strategy %q", s.Describe().Strategy)
	}
	assert.NotEmpty(t, gateVersion)
}
