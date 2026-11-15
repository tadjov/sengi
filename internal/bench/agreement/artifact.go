package agreement

import (
	"time"

	"github.com/tadjov/tusker/internal/bench/meta"
	"github.com/tadjov/tusker/internal/bench/version"
)

// Artifact is the JSON bench diff judgments writes to --output.
type Artifact struct {
	SchemaVersion      int            `json:"schema_version"`
	Meta               Meta           `json:"meta"`
	RelevanceThreshold int            `json:"relevance_threshold"`
	Seed               uint64         `json:"seed"`
	AllPairs           Summary        `json:"all_pairs"`
	Gate               *GateSummary   `json:"gate,omitempty"`
	Disagreements      []Disagreement `json:"disagreements,omitempty"`
	Rescoring          *Rescoring     `json:"rescoring,omitempty"`
}

// GateSummary is the agreement over the pairs the judges' gate admits.
type GateSummary struct {
	Refused int `json:"refused"`
	// Admitted is nil when the gate refused every pair.
	Admitted *Summary `json:"admitted,omitempty"`
}

// Meta is an Artifact's provenance block.
type Meta struct {
	RunID       string    `json:"run_id"`
	Tool        string    `json:"tool"`
	GeneratedAt time.Time `json:"generated_at"`
	Sources     Sources   `json:"sources"`
}

// Sources names the judgment files compared and the report rescored.
type Sources struct {
	Judgments []Source `json:"judgments"`
	// ReportRunID is empty unless a report was rescored.
	ReportRunID string `json:"report_run_id,omitempty"`
}

// Source is one judgment file under its label.
type Source struct {
	Label string `json:"label"`
	Path  string `json:"path"`
	RunID string `json:"run_id"`
}

// NewArtifact stamps an Artifact's schema version and provenance.
func NewArtifact(sets []Set, reportRunID string) Artifact {
	stamp := meta.New("agreement")
	sources := Sources{ReportRunID: reportRunID}
	for _, s := range sets {
		sources.Judgments = append(sources.Judgments, Source{Label: s.Label, Path: s.Path, RunID: s.File.Meta.RunID})
	}
	return Artifact{
		SchemaVersion: version.SchemaVersion,
		Meta:          Meta{RunID: stamp.RunID, Tool: stamp.Tool, GeneratedAt: stamp.GeneratedAt, Sources: sources},
	}
}
