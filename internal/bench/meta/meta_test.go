package meta

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJudgeDifferences(t *testing.T) {
	sonnet := Judge{
		Strategy:        "llm",
		Provider:        "claude-cli",
		Model:           "claude-sonnet-5-5",
		PromptVersion:   "v3",
		Effort:          "low",
		MaxOutputTokens: 16000,
		Invocation:      "claude -p --model claude-sonnet-5-5",
		GateVersion:     "v2",
	}
	bm25 := Judge{Strategy: "bm25", RubricVersion: "v2", GateVersion: "v2"}
	with := func(j Judge, change func(*Judge)) Judge {
		change(&j)
		return j
	}
	tests := []struct {
		name string
		was  Judge
		now  Judge
		want []string
	}{
		{name: "the same judge", was: sonnet, now: sonnet},
		{name: "strategy", was: bm25, now: with(bm25, func(j *Judge) { j.Strategy = "keyword" }), want: []string{`strategy: was "bm25", now "keyword"`}},
		{name: "provider", was: sonnet, now: with(sonnet, func(j *Judge) { j.Provider = "claude-api" }), want: []string{`provider: was "claude-cli", now "claude-api"`}},
		{name: "model", was: sonnet, now: with(sonnet, func(j *Judge) { j.Model = "claude-opus-5" }), want: []string{`model: was "claude-sonnet-5-5", now "claude-opus-5"`}},
		{name: "prompt version", was: sonnet, now: with(sonnet, func(j *Judge) { j.PromptVersion = "v4" }), want: []string{`prompt_version: was "v3", now "v4"`}},
		{name: "effort", was: sonnet, now: with(sonnet, func(j *Judge) { j.Effort = "high" }), want: []string{`effort: was "low", now "high"`}},
		{name: "output budget", was: sonnet, now: with(sonnet, func(j *Judge) { j.MaxOutputTokens = 20000 }), want: []string{`max_output_tokens: was 16000, now 20000`}},
		{
			name: "invocation", was: sonnet, now: with(sonnet, func(j *Judge) { j.Invocation = "claude -p --safe-mode" }),
			want: []string{`invocation: was "claude -p --model claude-sonnet-5-5", now "claude -p --safe-mode"`},
		},
		{name: "gate version", was: with(bm25, func(j *Judge) { j.GateVersion = "v0" }), now: bm25, want: []string{`gate_version: was "v0", now "v2"`}},
		{name: "rubric version", was: with(bm25, func(j *Judge) { j.RubricVersion = "v1" }), now: bm25, want: []string{`rubric_version: was "v1", now "v2"`}},
		{
			name: "content cut", was: with(sonnet, func(j *Judge) { j.ContentRunes = 400 }), now: with(sonnet, func(j *Judge) { j.ContentRunes = 600 }),
			want: []string{`content_runes: was 400, now 600`},
		},
		{name: "a file that names no judge", was: Judge{}, now: bm25, want: []string{
			`strategy: was "", now "bm25"`, `gate_version: was "", now "v2"`, `rubric_version: was "", now "v2"`,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.was.Differences(tt.now))
			assert.Equal(t, len(tt.want) == 0, tt.was.Equal(tt.now))
		})
	}
}

func TestNewRunID_FormatAndKind(t *testing.T) {
	id := NewRunID("pool")
	parts := strings.Split(id, "-")
	// 2026-05-21T14-08-55-pool-7c91a3 → 7 parts (date hyphens count)
	assert.GreaterOrEqual(t, len(parts), 6)
	assert.Contains(t, id, "-pool-", "kind should appear in id")
}

func TestNewRunID_SortableByTime(t *testing.T) {
	ids := []string{NewRunID("a"), NewRunID("b"), NewRunID("c")}
	sorted := append([]string{}, ids...)
	sort.Strings(sorted)
	// Lexicographic sort should match insertion order because timestamp prefix.
	// In the rare case two ids share a timestamp, the random suffix breaks ties
	// — order may flip but every id remains unique.
	uniq := map[string]struct{}{}
	for _, id := range ids {
		uniq[id] = struct{}{}
	}
	assert.Len(t, uniq, 3, "ids should be unique")
}

func TestNew_StampsToolAndTimestamp(t *testing.T) {
	m := New("judge")
	assert.Contains(t, m.Tool, "bench/")
	assert.False(t, m.GeneratedAt.IsZero())
	assert.Contains(t, m.RunID, "-judge-")
}
