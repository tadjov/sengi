package judgment

import (
	"strings"
	"testing"
)

func TestParseSelector(t *testing.T) {
	tests := []struct {
		sel      string
		kind     StrategyKind
		provider Provider
		wantErr  string
	}{
		{sel: "keyword", kind: StrategyKeyword},
		{sel: "bm25", kind: StrategyBM25},
		{sel: "vector", kind: StrategyVector},
		{sel: "hybrid", kind: StrategyHybrid},
		{sel: "manual", kind: StrategyManual},
		{sel: "jev", kind: StrategyJev},
		{sel: "claude-cli", kind: StrategyLLM, provider: ProviderClaudeCLI},
		{sel: "claude-api", kind: StrategyLLM, provider: ProviderClaudeAPI},

		// Every LLM set is filed under its provider.
		{sel: "llm", wantErr: "does not name a judgment set"},

		{sel: "codex-cli", wantErr: "unknown judgment set"},
		{sel: "bogus", wantErr: "unknown judgment set"},
	}

	for _, tc := range tests {
		t.Run(tc.sel, func(t *testing.T) {
			kind, provider, err := ParseSelector(tc.sel)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("ParseSelector(%q) = (%q, %q), want error", tc.sel, kind, provider)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSelector(%q): %v", tc.sel, err)
			}
			if kind != tc.kind || provider != tc.provider {
				t.Errorf("ParseSelector(%q) = (%q, %q), want (%q, %q)",
					tc.sel, kind, provider, tc.kind, tc.provider)
			}
		})
	}
}

// Providers are set names; the bare llm strategy is not.
func TestKnownStrategiesListsSetNames(t *testing.T) {
	known := KnownStrategies()
	joined := strings.Join(known, ",")

	for _, want := range []string{"keyword", "bm25", "vector", "hybrid", "claude-cli", "claude-api", "jev", "manual"} {
		if !strings.Contains(joined, want) {
			t.Errorf("KnownStrategies() = %v, missing %q", known, want)
		}
	}
	for _, k := range known {
		if k == string(StrategyLLM) {
			t.Errorf("KnownStrategies() lists %q, which is not a judgment-set name", k)
		}
	}
}

func TestJudgmentSetName(t *testing.T) {
	tests := []struct {
		kind     StrategyKind
		provider Provider
		want     string
	}{
		{kind: StrategyKeyword, want: "keyword"},
		{kind: StrategyManual, want: "manual"},
		{kind: StrategyJev, want: "jev"},
		{kind: StrategyLLM, provider: ProviderClaudeCLI, want: "claude-cli"},
		{kind: StrategyLLM, provider: ProviderClaudeAPI, want: "claude-api"},
		{kind: StrategyLLM, want: string(DefaultProvider)},
	}

	for _, tc := range tests {
		if got := JudgmentSetName(tc.kind, tc.provider); got != tc.want {
			t.Errorf("JudgmentSetName(%q, %q) = %q, want %q", tc.kind, tc.provider, got, tc.want)
		}
	}
}

func TestJudgmentSetNameRoundTrips(t *testing.T) {
	cases := []struct {
		kind     StrategyKind
		provider Provider
	}{
		{kind: StrategyKeyword},
		{kind: StrategyBM25},
		{kind: StrategyVector},
		{kind: StrategyHybrid},
		{kind: StrategyManual},
		{kind: StrategyJev},
		{kind: StrategyLLM, provider: ProviderClaudeCLI},
		{kind: StrategyLLM, provider: ProviderClaudeAPI},
	}

	for _, tc := range cases {
		name := JudgmentSetName(tc.kind, tc.provider)
		kind, provider, err := ParseSelector(name)
		if err != nil {
			t.Errorf("ParseSelector(%q): %v", name, err)
			continue
		}
		if kind != tc.kind || provider != tc.provider {
			t.Errorf("round trip of (%q, %q) via %q = (%q, %q)", tc.kind, tc.provider, name, kind, provider)
		}
	}
}

func TestResolveClaudeModelAliases(t *testing.T) {
	tests := []struct{ in, want string }{
		{in: "haiku", want: "claude-haiku-5-5"},
		{in: "HAIKU", want: "claude-haiku-5-5"},
		{in: "sonnet", want: "claude-sonnet-5-5"},
		{in: "opus", want: "claude-opus-5-5"},
		{in: "fable", want: "claude-fable-5-2"},
		{in: "claude-sonnet-5", want: "claude-sonnet-5"},
	}

	for _, tc := range tests {
		if got := resolveClaudeModel(tc.in); got != tc.want {
			t.Errorf("resolveClaudeModel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLLMRefusesAnEmptyModel(t *testing.T) {
	for _, provider := range []Provider{ProviderClaudeCLI, ProviderClaudeAPI} {
		t.Run(string(provider), func(t *testing.T) {
			_, err := NewLLMStrategy(StrategyOptions{Provider: provider, APIKey: "test-key", Effort: EffortLow})

			if err == nil || !strings.Contains(err.Error(), "--model") {
				t.Errorf("NewLLMStrategy with an empty model: err = %v, want one naming --model", err)
			}
		})
	}
}

func TestLLMRequiresAnEffort(t *testing.T) {
	for _, provider := range []Provider{ProviderClaudeCLI, ProviderClaudeAPI} {
		t.Run(string(provider), func(t *testing.T) {
			_, err := NewLLMStrategy(StrategyOptions{Provider: provider, APIKey: "test-key", Model: "claude-sonnet-5-5"})

			if err == nil || !strings.Contains(err.Error(), "--effort") {
				t.Errorf("NewLLMStrategy without an effort: err = %v, want one naming --effort", err)
			}
		})
	}
}

func TestParseEffort(t *testing.T) {
	for _, level := range []string{"low", "medium", "high", "xhigh", "max"} {
		if got, err := ParseEffort(level); err != nil || string(got) != level {
			t.Errorf("ParseEffort(%q) = (%q, %v), want %q", level, got, err, level)
		}
	}
	for _, bad := range []string{"", "LOW", "none", "minimal"} {
		if _, err := ParseEffort(bad); err == nil {
			t.Errorf("ParseEffort(%q) succeeded, want an error", bad)
		}
	}
}

func TestLLMDescribeNamesTheModelItWasGiven(t *testing.T) {
	for _, provider := range []Provider{ProviderClaudeCLI, ProviderClaudeAPI} {
		t.Run(string(provider), func(t *testing.T) {
			s, err := NewLLMStrategy(StrategyOptions{Provider: provider, APIKey: "test-key", CLIBinary: versionOnlyClaudeCLI(t), Model: "claude-haiku-5-5", Effort: EffortLow})
			if err != nil {
				t.Fatalf("NewLLMStrategy: %v", err)
			}

			got := s.Describe()
			if got.Strategy != string(StrategyLLM) {
				t.Errorf("Strategy = %q, want %q", got.Strategy, StrategyLLM)
			}
			if got.Provider != string(provider) {
				t.Errorf("Provider = %q, want %q", got.Provider, provider)
			}
			if got.Model != "claude-haiku-5-5" {
				t.Errorf("Model = %q, want %q", got.Model, "claude-haiku-5-5")
			}
			if got.PromptVersion != PromptVersion {
				t.Errorf("PromptVersion = %q, want %q", got.PromptVersion, PromptVersion)
			}
			if got.Effort != "low" {
				t.Errorf("Effort = %q, want %q", got.Effort, "low")
			}
		})
	}
}

func TestLLMDefaultProvider(t *testing.T) {
	s, err := NewLLMStrategy(StrategyOptions{CLIBinary: versionOnlyClaudeCLI(t), Model: "claude-haiku-5-5", Effort: EffortLow})
	if err != nil {
		t.Fatalf("NewLLMStrategy: %v", err)
	}

	if got := s.Describe().Provider; got != string(DefaultProvider) {
		t.Errorf("Provider = %q, want %q", got, DefaultProvider)
	}
}

func TestLLMModelAliasRecordedAsFullID(t *testing.T) {
	s, err := NewLLMStrategy(StrategyOptions{Provider: ProviderClaudeCLI, CLIBinary: versionOnlyClaudeCLI(t), Model: "sonnet", Effort: EffortLow})
	if err != nil {
		t.Fatalf("NewLLMStrategy: %v", err)
	}

	if got := s.Describe().Model; got != "claude-sonnet-5-5" {
		t.Errorf("Model = %q, want %q", got, "claude-sonnet-5-5")
	}
}

func TestLLMStrategyUnknownProvider(t *testing.T) {
	if _, err := NewLLMStrategy(StrategyOptions{Provider: "codex-cli"}); err == nil {
		t.Error("NewLLMStrategy with unregistered provider should fail")
	}
}
