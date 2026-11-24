package judgment

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/bench/suite"
)

const goldenSystemPrompt = `You are an information retrieval relevance judge. You score how well each candidate news article matches a user's search query.

Grading scale:
- 3 = Highly relevant: article is centrally about the topic
- 2 = Relevant: article clearly covers the topic
- 1 = Marginal: topic is mentioned but not the focus
- 0 = Not relevant: article is about something else

Rules:
- Grade what the article IS ABOUT, not whether the query terms literally appear.
- When in doubt between two grades, pick the lower one.
- A passing mention does NOT warrant grade 2 — that is grade 1 at most.
- Be consistent: the same article quality relative to the same query should always get the same grade.

Output ONLY the JSON the request asks for. Never wrap it in markdown, never add prose.`

const goldenArticle = `Query: stock market crash

Article (doc_id: 493384af-c5fc-4677-ad11-2081e73f0588):
Title: Markets slide
Description: Shares fell.
Content: Stocks dropped on Monday.

Respond with ONLY this JSON: {"doc_id":"493384af-c5fc-4677-ad11-2081e73f0588","grade":<0|1|2|3>}`

// A grade depends on every byte of the prompt, so changing one means a new
// PromptVersion and new goldens here.
func TestGradingPrompt_IsThePromptOfItsVersion(t *testing.T) {
	require.Equal(t, "v3", PromptVersion)
	doc := GradingDoc{
		ID:          uuid.MustParse("493384af-c5fc-4677-ad11-2081e73f0588"),
		Title:       "Markets slide",
		Description: "Shares fell.",
		Content:     "Stocks dropped on Monday.",
	}
	tests := []struct {
		category suite.Category
		want     string
	}{
		{category: suite.CategoryKeyword, want: goldenArticle},
		{
			category: suite.CategoryPhrase,
			want: "This is a phrase query, and the article contains the phrase. Grade how well it answers the query.\n\n" +
				goldenArticle,
		},
		{
			category: suite.CategoryBoolean,
			want: "This is a boolean query, and the article satisfies the whole expression, negated terms included. " +
				"Grade how well it answers the query.\n\n" + goldenArticle,
		},
	}
	for _, tt := range tests {
		t.Run(string(tt.category), func(t *testing.T) {
			got := buildGradingPrompt(GradingQuery{ID: "q1", Text: "stock market crash", Category: tt.category}, doc, testContentRunes)

			assert.Equal(t, tt.want, got)
		})
	}
	assert.Equal(t, goldenSystemPrompt, systemPrompt)
}
