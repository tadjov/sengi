// Package meta defines the provenance block every bench artifact carries.
package meta

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/tadjov/sengi/internal/bench/version"
	"github.com/tadjov/sengi/internal/storage"
)

// Meta is the provenance block of a pool, annotations or report file.
type Meta struct {
	RunID       string    `yaml:"run_id" json:"run_id"`
	Tool        string    `yaml:"tool" json:"tool"`
	GeneratedAt time.Time `yaml:"generated_at" json:"generated_at"`

	SpecID string `yaml:"spec_id,omitempty" json:"spec_id,omitempty"`

	// Pool-only fields.
	PoolDepth         int                `yaml:"pool_depth,omitempty" json:"pool_depth,omitempty"`
	Engines           []string           `yaml:"engines,omitempty" json:"engines,omitempty"`
	DroppedExecutions []DroppedExecution `yaml:"dropped_executions,omitempty" json:"dropped_executions,omitempty"`
	// Canonical is the canonical package the pooled engines had loaded.
	Canonical *storage.PackageRef `yaml:"canonical,omitempty" json:"canonical,omitempty"`

	// Judgment fields; a report also records its pool in PoolRunID.
	Judge          *Judge `yaml:"judge,omitempty" json:"judge,omitempty"`
	PoolRef        string `yaml:"pool_ref,omitempty" json:"pool_ref,omitempty"`
	PoolRunID      string `yaml:"pool_run_id,omitempty" json:"pool_run_id,omitempty"`
	GradedCount    *int   `yaml:"graded_count,omitempty" json:"graded_count,omitempty"`
	UnjudgedCount  *int   `yaml:"unjudged_count,omitempty" json:"unjudged_count,omitempty"`
	RelevanceScale []int  `yaml:"relevance_scale,omitempty" json:"relevance_scale,omitempty"`
	// CarriedOver is set when grades were reused from older judgments.
	CarriedOver *CarriedOver `yaml:"carried_over,omitempty" json:"carried_over,omitempty"`

	// Report-only fields.
	JudgmentsRunID string   `yaml:"judgments_run_id,omitempty" json:"judgments_run_id,omitempty"`
	Sources        *Sources `yaml:"sources,omitempty" json:"sources,omitempty"`
}

// CarriedOver names where reused grades came from.
type CarriedOver struct {
	JudgmentsRunID string `yaml:"judgments_run_id" json:"judgments_run_id"`
	PoolRunID      string `yaml:"pool_run_id" json:"pool_run_id"`
	Grades         int    `yaml:"grades" json:"grades"`
}

// DroppedExecution is a (query, engine) pair whose every execution failed in an
// --allow-partial pool.
type DroppedExecution struct {
	QueryID string `yaml:"query_id" json:"query_id"`
	Engine  string `yaml:"engine" json:"engine"`
	Error   string `yaml:"error" json:"error"`
}

// Judge records who graded an annotations file; the resume guard compares it field by field.
type Judge struct {
	Strategy      string `yaml:"strategy" json:"strategy"`
	Provider      string `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model         string `yaml:"model,omitempty" json:"model,omitempty"`
	PromptVersion string `yaml:"prompt_version,omitempty" json:"prompt_version,omitempty"`
	// Effort is the effort level an LLM judge's model graded at.
	Effort string `yaml:"effort,omitempty" json:"effort,omitempty"`
	// MaxOutputTokens is an LLM judge's output budget per request.
	MaxOutputTokens int `yaml:"max_output_tokens,omitempty" json:"max_output_tokens,omitempty"`
	// Invocation is how an LLM judge reached its model: a command line or API version.
	Invocation string `yaml:"invocation,omitempty" json:"invocation,omitempty"`
	// GateVersion names the phrase, boolean and filter gates behind a judge's zeros.
	GateVersion string `yaml:"gate_version,omitempty" json:"gate_version,omitempty"`
	// RubricVersion names the grading rules of a judge without a prompt.
	RubricVersion string `yaml:"rubric_version,omitempty" json:"rubric_version,omitempty"`
	// ContentRunes is how many runes of an article's content the judge read.
	ContentRunes int `yaml:"content_runes,omitempty" json:"content_runes,omitempty"`
}

// Equal reports whether two judges grade alike; the resume guard refuses a mismatch.
func (j Judge) Equal(other Judge) bool { return len(j.Differences(other)) == 0 }

// Differences lists each differing field with both values: `effort: was "low", now "high"`.
func (j Judge) Differences(other Judge) []string {
	was, now := reflect.ValueOf(j), reflect.ValueOf(other)
	var out []string
	for i := range was.NumField() {
		if reflect.DeepEqual(was.Field(i).Interface(), now.Field(i).Interface()) {
			continue
		}
		key, _, _ := strings.Cut(was.Type().Field(i).Tag.Get("yaml"), ",")
		out = append(out, fmt.Sprintf("%s: was %s, now %s", key, judgeValue(was.Field(i)), judgeValue(now.Field(i))))
	}
	return out
}

func judgeValue(v reflect.Value) string {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return "none"
		}
		v = v.Elem()
	}
	if v.Kind() == reflect.String {
		return strconv.Quote(v.String())
	}
	return fmt.Sprint(v.Interface())
}

// String renders the judge for CLI output.
func (j Judge) String() string {
	s := j.Strategy
	if j.Provider != "" {
		s += "/" + j.Provider
	}
	if j.Model != "" {
		s += " " + j.Model
	}
	return s
}

// Sources names the artifacts a report's metrics were computed from.
type Sources struct {
	Spec      string   `yaml:"spec,omitempty" json:"spec,omitempty"`
	Suites    []string `yaml:"suites,omitempty" json:"suites,omitempty"`
	Pool      string   `yaml:"pool,omitempty" json:"pool,omitempty"`
	Judgments string   `yaml:"judgments,omitempty" json:"judgments,omitempty"`
}

// New stamps a Meta with the tool, the time and a run id carrying kind.
func New(kind string) Meta {
	return Meta{
		RunID:       NewRunID(kind),
		Tool:        version.Tool(),
		GeneratedAt: time.Now().UTC(),
	}
}

// NewRunID returns an id like 2026-05-21T14-08-55-judge-7c91a3 that sorts by time.
func NewRunID(kind string) string {
	stamp := time.Now().UTC().Format("2006-01-02T15-04-05")
	return fmt.Sprintf("%s-%s-%s", stamp, kind, randHex(3))
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail in practice; keep the id well-formed if it does.
		return "000000"[:n*2]
	}
	return hex.EncodeToString(b)
}
