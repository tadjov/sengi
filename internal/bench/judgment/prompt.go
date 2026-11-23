package judgment

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/tadjov/sengi/internal/bench/suite"
)

const (
	// contentCutRunes is the median cc-news article's length, so half the
	// corpus reaches the judge whole.
	contentCutRunes = 1744

	phraseSatisfied = `This is a phrase query, and the article contains the phrase. ` +
		`Grade how well it answers the query.`

	booleanSatisfied = `This is a boolean query, and the article satisfies the whole expression, negated terms included. ` +
		`Grade how well it answers the query.`
)

// systemPrompt is the rubric of every grading request.
const systemPrompt = `You are an information retrieval relevance judge. You score how well each candidate news article matches a user's search query.

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

func buildGradingPrompt(q GradingQuery, doc GradingDoc) string {
	var sb strings.Builder
	writeQuery(&sb, q)
	fmt.Fprintf(&sb, "Article (doc_id: %s):\n", doc.ID)
	writeArticle(&sb, doc)
	fmt.Fprintf(&sb, "\nRespond with ONLY this JSON: {\"doc_id\":\"%s\",\"grade\":<0|1|2|3>}", doc.ID)
	return sb.String()
}

func writeQuery(sb *strings.Builder, q GradingQuery) {
	if note := constraintNote(q.Category); note != "" {
		sb.WriteString(note)
		sb.WriteString("\n\n")
	}
	fmt.Fprintf(sb, "Query: %s\n\n", q.Text)
}

func writeArticle(sb *strings.Builder, doc GradingDoc) {
	fmt.Fprintf(sb, "Title: %s\n", doc.Title)
	if doc.Description != "" {
		fmt.Fprintf(sb, "Description: %s\n", doc.Description)
	}
	if doc.Content != "" {
		fmt.Fprintf(sb, "Content: %s\n", truncateRunes(doc.Content, contentCutRunes))
	}
}

// constraintNote tells the model the article already satisfies the query's
// category constraint, which the code checked before asking.
func constraintNote(category suite.Category) string {
	switch category {
	case suite.CategoryPhrase:
		return phraseSatisfied
	case suite.CategoryBoolean:
		return booleanSatisfied
	default:
		return ""
	}
}

// gradeResponse holds Grade as a pointer so a missing or null grade stays
// distinguishable from grade 0.
type gradeResponse struct {
	DocID string `json:"doc_id"`
	Grade *int   `json:"grade"`
}

// parseGradeJSON tolerates prose or code fences around the reply's JSON object.
func parseGradeJSON(raw string, expectedDocID string) (int, error) {
	body := isolateJSON(raw)

	var resp gradeResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return 0, fmt.Errorf("parse grade response: %w (body: %q)", err, body)
	}
	if resp.DocID != expectedDocID {
		return 0, fmt.Errorf("doc_id mismatch: got %q, expected %q", resp.DocID, expectedDocID)
	}
	if resp.Grade == nil {
		return 0, fmt.Errorf("doc %s has no grade (body: %q)", expectedDocID, body)
	}
	if *resp.Grade < 0 || *resp.Grade > 3 {
		return 0, fmt.Errorf("grade %d out of range [0,3]", *resp.Grade)
	}
	return *resp.Grade, nil
}

func isolateJSON(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return s
	}
	return s[start : end+1]
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "..."
}

// PromptVersion names the rubric in the judge block: bump it whenever the
// system prompt, the prompt builder or the grading scale could shift a grade.
const PromptVersion = "v3"
