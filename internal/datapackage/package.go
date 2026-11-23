// Package datapackage reads and writes Frictionless Data Package v2 descriptors.
package datapackage

import (
	"github.com/tadjov/sengi/internal/types/language"
	"github.com/tadjov/sengi/pkg/apis/preprocess"
)

const (
	Profile        = "https://datapackage.org/profiles/2.0/datapackage.json"
	DescriptorName = "datapackage.json"
	ArticleSchema  = "article/v2"
)

// Resource names fixed by the contract.
const (
	ArticlesResource     = "articles"
	RejectsResource      = "rejects"
	ChangesResource      = "changes"
	EmbeddingsResource   = "embeddings"
	QueryVectorsResource = "query-vectors"
)

// Package is a data package descriptor.
type Package struct {
	Schema        string        `json:"$schema"`
	Name          string        `json:"name"`
	Version       string        `json:"version,omitempty"`
	ArticleSchema string        `json:"articleSchema,omitempty"`
	Language      language.Code `json:"language,omitempty"`
	ProducedBy    *ProducedBy   `json:"producedBy,omitempty"`
	Preprocess    *Preprocess   `json:"preprocess,omitempty"`
	Embedding     *Embedding    `json:"embedding,omitempty"`
	Canonical     *Ref          `json:"canonical,omitempty"`
	DerivedFrom   *DerivedFrom  `json:"derivedFrom,omitempty"`
	Raw           *RawRef       `json:"raw,omitempty"`
	Suite         *Suite        `json:"suite,omitempty"`
	Sources       []Source      `json:"sources,omitempty"`
	Resources     []Resource    `json:"resources" schema:"required,minItems=1"`
}

// Resource is one file of a package; Path is relative to the descriptor.
type Resource struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Format    string `json:"format"`
	MediaType string `json:"mediatype,omitempty"`
	Bytes     int64  `json:"bytes"`
	Hash      string `json:"hash"`
}

type Source struct {
	Title   string `json:"title,omitempty"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
}

type ProducedBy struct {
	Tool string `json:"tool"`
}

// Preprocess records how a canonical package was made from its raw package.
type Preprocess struct {
	ID      ArticleIDs             `json:"id"`
	Lock    Lock                   `json:"lock"`
	Counts  Counts                 `json:"counts"`
	Signals map[string]SignalStats `json:"signals,omitempty"`
	Checks  map[string]CheckResult `json:"checks,omitempty"`
}

// ArticleIDs says how ids were minted; Namespace is set only for urlHash ids.
type ArticleIDs struct {
	Kind      preprocess.IDKind `json:"kind" schema:"enum=urlHash|random"`
	Algorithm string            `json:"algorithm" schema:"enum=uuidv5|uuidv7"`
	Namespace string            `json:"namespace,omitempty"`
}

// Lock pins what a package was made from: the specs and raw package by hash, the tool by version.
type Lock struct {
	Spec    string `json:"spec"`
	Mapping string `json:"mapping"`
	Input   string `json:"input"`
	Tool    string `json:"tool"`
}

// SameInputs reports whether two locks hash the same specs and input, whatever tool made them.
func (l Lock) SameInputs(other Lock) bool {
	return l.Spec == other.Spec && l.Mapping == other.Mapping && l.Input == other.Input
}

// Counts reconcile: Read is Written plus every Dropped and Duplicates count.
// Dropped is keyed <stage>/<what>, Duplicates by key, Cleared by rule.
type Counts struct {
	Read       int64            `json:"read"`
	Dropped    map[string]int64 `json:"dropped,omitempty"`
	Duplicates map[string]int64 `json:"duplicates,omitempty"`
	Cleared    map[string]int64 `json:"cleared,omitempty"`
	Written    int64            `json:"written"`
}

// SignalStats is the distribution of one signal over the kept articles it gives a value.
type SignalStats struct {
	Distinct string `json:"distinct"`
	Sharing  string `json:"sharing"`
	Within   string `json:"within,omitempty"`
	Articles int64  `json:"articles"`
	Median   int64  `json:"median"`
	P90      int64  `json:"p90"`
	P99      int64  `json:"p99"`
	Max      int64  `json:"max"`
	// AtLeast counts the articles whose value is at least each key, for choosing a threshold.
	AtLeast map[string]int64 `json:"atLeast,omitempty"`
	Top     []SignalGroup    `json:"top,omitempty"`
}

// SignalGroup is one group of articles sharing a value, with the shared value cut short.
type SignalGroup struct {
	Within   string `json:"within,omitempty"`
	Sharing  string `json:"sharing"`
	Value    int64  `json:"value"`
	Articles int64  `json:"articles"`
}

type CheckResult struct {
	Observed float64     `json:"observed"`
	Warn     *float64    `json:"warn,omitempty"`
	Error    *float64    `json:"error,omitempty"`
	Result   CheckStatus `json:"result" schema:"enum=pass|warn|error"`
}

type CheckStatus string

const (
	CheckPass  CheckStatus = "pass"
	CheckWarn  CheckStatus = "warn"
	CheckError CheckStatus = "error"
)

// Embedding describes how vectors were made; Limit is set only on a --limit trial.
type Embedding struct {
	Model            string `json:"model"`
	HuggingFaceModel string `json:"hfModel"`
	Revision         string `json:"revision"`
	Tag              string `json:"tag"`
	Dim              int    `json:"dim"`
	Recipe           string `json:"recipe"`
	Pooling          string `json:"pooling"`
	Normalized       bool   `json:"normalized"`
	MaxTokens        int    `json:"maxTokens"`
	BatchSize        int    `json:"batchSize"`
	DType            string `json:"dtype"`
	Limit            *int   `json:"limit,omitempty"`
}

// Ref names the canonical package an embeddings package was built from.
type Ref struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	ResourceHash string `json:"resourceHash"`
}

// DerivedFrom names the package a derived package was cut from.
type DerivedFrom struct {
	Name         string `json:"name"`
	Version      string `json:"version,omitempty"`
	Tag          string `json:"tag"`
	ResourceHash string `json:"resourceHash"`
}

// RawRef names the raw package a canonical package was built from.
type RawRef struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// Suite names the bench suite query vectors were embedded from.
type Suite struct {
	ID string `json:"id"`
}
