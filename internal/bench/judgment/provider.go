package judgment

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Provider names one LLM endpoint behind StrategyLLM: a vendor plus its transport.
type Provider string

const (
	ProviderClaudeCLI Provider = "claude-cli"
	ProviderClaudeAPI Provider = "claude-api"
)

// Effort is how much a judge model may think before it grades.
type Effort string

const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXHigh  Effort = "xhigh"
	EffortMax    Effort = "max"
)

var efforts = []Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}

func ParseEffort(s string) (Effort, error) {
	if slices.Contains(efforts, Effort(s)) {
		return Effort(s), nil
	}
	return "", fmt.Errorf("unknown effort %q (known: %s)", s, strings.Join(KnownEfforts(), ", "))
}

// KnownEfforts lists the effort levels, lowest first.
func KnownEfforts() []string {
	names := make([]string, len(efforts))
	for i, e := range efforts {
		names[i] = string(e)
	}
	return names
}

// CompletionRequest is one grading call: a rubric and the query with its candidates.
type CompletionRequest struct {
	System    string
	User      string
	MaxTokens int
}

// LLMProvider executes grading prompts against one vendor endpoint.
type LLMProvider interface {
	Name() Provider
	// Model is the resolved model id, never an alias.
	Model() string
	Effort() Effort
	// Invocation is the command line or API version a call runs with.
	Invocation() string
	// Complete sends one prompt and returns the assistant's raw text.
	Complete(ctx context.Context, req CompletionRequest) (string, error)
}

// providers is the LLM endpoint registry; each name is also a judgment set.
var providers = map[Provider]func(StrategyOptions) (LLMProvider, error){
	ProviderClaudeCLI: newClaudeCLIProvider,
	ProviderClaudeAPI: newClaudeAPIProvider,
}

// DefaultProvider bills the Claude subscription, not API credit.
const DefaultProvider = ProviderClaudeCLI

func newProvider(name Provider, opts StrategyOptions) (LLMProvider, error) {
	if name == "" {
		name = DefaultProvider
	}
	factory, ok := providers[name]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (known: %s)", name, strings.Join(KnownProviders(), ", "))
	}
	return factory(opts)
}

// KnownProviders returns every registered provider name, sorted.
func KnownProviders() []string {
	out := make([]string, 0, len(providers))
	for name := range providers {
		out = append(out, string(name))
	}
	sort.Strings(out)
	return out
}

// JudgmentSetName names a judgment set on disk and in --judgments. LLM sets go by
// provider, so two vendors can grade one track.
func JudgmentSetName(kind StrategyKind, provider Provider) string {
	if kind != StrategyLLM {
		return string(kind)
	}
	if provider == "" {
		provider = DefaultProvider
	}
	return string(provider)
}

// KnownJudgeStrategies lists what --strategy accepts, unlike the set names in KnownStrategies.
func KnownJudgeStrategies() []string {
	return []string{
		string(StrategyKeyword),
		string(StrategyBM25),
		string(StrategyVector),
		string(StrategyHybrid),
		string(StrategyLLM),
		string(StrategyJev),
		string(StrategyManual),
	}
}

// KnownStrategies lists every judgment-set name, registered providers included.
func KnownStrategies() []string {
	out := []string{
		string(StrategyKeyword),
		string(StrategyBM25),
		string(StrategyVector),
		string(StrategyHybrid),
	}
	out = append(out, KnownProviders()...)
	return append(out, string(StrategyJev), string(StrategyManual))
}
