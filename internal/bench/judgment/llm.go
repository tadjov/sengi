package judgment

import (
	"context"
	"fmt"

	"github.com/tadjov/sengi/internal/bench/meta"
)

// The output budget leaves room for adaptive thinking at any effort before the
// JSON grade, and stays under the ~21,333 max_tokens the CLI's SDK sends
// without streaming.
const llmMaxOutputTokens = 16000

// LLMStrategy is the LLM-as-judge strategy. It owns the rubric and response
// parsing; reaching a vendor is delegated to an LLMProvider, so the
// same grading behaviour runs over a local CLI subprocess or a vendor HTTP API
// and stays comparable across both.
type LLMStrategy struct {
	provider LLMProvider
}

func NewLLMStrategy(opts StrategyOptions) (*LLMStrategy, error) {
	p, err := newProvider(opts.Provider, opts)
	if err != nil {
		return nil, err
	}
	return &LLMStrategy{provider: p}, nil
}

// Describe reports the grader for the artifact's judge block. The model is the
// provider's resolved id — aliases are expanded at construction — so the qrels
// name a concrete model and the resume guard can catch a grader swap.
func (s *LLMStrategy) Describe() meta.Judge {
	return meta.Judge{
		Strategy:        string(StrategyLLM),
		Provider:        string(s.provider.Name()),
		Model:           s.provider.Model(),
		PromptVersion:   PromptVersion,
		GateVersion:     gateVersion,
		Effort:          string(s.provider.Effort()),
		MaxOutputTokens: llmMaxOutputTokens,
		Invocation:      s.provider.Invocation(),
	}
}

// Grade gives 0 without a request to a doc the query's filters or category
// gate refuse; the model grades only how well the rest answer the query.
func (s *LLMStrategy) Grade(ctx context.Context, q GradingQuery, doc GradingDoc) (int, error) {
	admitted, err := GateAdmits(q, doc)
	if err != nil {
		return GradeUnjudged, err
	}
	if !admitted {
		return GradeNotRelev, nil
	}
	text, err := s.provider.Complete(ctx, CompletionRequest{
		System:    systemPrompt,
		User:      buildGradingPrompt(q, doc),
		MaxTokens: llmMaxOutputTokens,
	})
	if err != nil {
		return GradeUnjudged, err
	}
	grade, err := parseGradeJSON(text, doc.ID.String())
	if err != nil {
		return GradeUnjudged, fmt.Errorf("doc %s: %w", doc.ID, err)
	}
	return grade, nil
}
