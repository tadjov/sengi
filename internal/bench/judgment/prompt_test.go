package judgment

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tadjov/sengi/internal/bench/suite"
)

// The code has already checked the constraint, so the note tells the model it
// holds rather than asking the model to check it.
func TestConstraintNote(t *testing.T) {
	if got := constraintNote("phrase"); !strings.Contains(got, "contains the phrase") || strings.Contains(got, "Grade 0") {
		t.Errorf("phrase note = %q, want it to say the article contains the phrase", got)
	}
	if got := constraintNote("boolean"); !strings.Contains(got, "satisfies the whole expression") || strings.Contains(got, "Grade 0") {
		t.Errorf("boolean note = %q, want it to say the article satisfies the expression", got)
	}
	for _, c := range []suite.Category{"", suite.CategoryKeyword, suite.CategoryMultiField, suite.CategorySingleField} {
		if got := constraintNote(c); got != "" {
			t.Errorf("constraintNote(%q) = %q, want empty", c, got)
		}
	}
}

func TestBuildGradingPrompt_IncludesCategoryConstraint(t *testing.T) {
	doc := GradingDoc{ID: uuid.New(), Title: "Markets slide"}

	phrase := buildGradingPrompt(GradingQuery{ID: "q", Text: "stock market crash", Category: "phrase"}, doc)
	if !strings.Contains(phrase, "phrase query") {
		t.Error("phrase query prompt should carry the phrase note")
	}

	keyword := buildGradingPrompt(GradingQuery{ID: "q", Text: "stock market crash", Category: "keyword"}, doc)
	if strings.Contains(keyword, "phrase query") {
		t.Error("keyword query prompt should grade on topic alone")
	}
}
