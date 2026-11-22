// Package datapackage reads and writes Frictionless Data Package v2 descriptors.
package datapackage

import (
	"github.com/tadjov/sengi/internal/ingest/datamapping"
	"github.com/tadjov/sengi/internal/types/language"
)

const (
	Profile        = "https://datapackage.org/profiles/2.0/datapackage.json"
	DescriptorName = "datapackage.json"
	ArticleSchema  = "article/v2"
)

// Resource names fixed by the contract.
const (
	ArticlesResource     = "articles"
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
	Identity      *Identity     `json:"identity,omitempty"`
	ProducedBy    *ProducedBy   `json:"producedBy,omitempty"`
	Report        *Report       `json:"report,omitempty"`
	Embedding     *Embedding    `json:"embedding,omitempty"`
	Canonical     *Ref          `json:"canonical,omitempty"`
	DerivedFrom   *DerivedFrom  `json:"derivedFrom,omitempty"`
	Raw           *RawRef       `json:"raw,omitempty"`
	Suite         *Suite        `json:"suite,omitempty"`
	Sources       []Source      `json:"sources,omitempty"`
	Resources     []Resource    `json:"resources" schema:"required,minItems=1"`
}

func (p Package) Resource(name string) (Resource, bool) {
	for _, r := range p.Resources {
		if r.Name == name {
			return r, true
		}
	}
	return Resource{}, false
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

// Identity is the mapping's identity block, plus how its ids were minted;
// Namespace is set only for urlHash ids.
type Identity struct {
	Unique    []datamapping.UniqueRule `json:"unique,omitempty"`
	IDKind    datamapping.IDKind       `json:"idKind" schema:"enum=urlHash|random"`
	Algorithm string                   `json:"algorithm" schema:"enum=uuidv5|uuidv7"`
	Namespace string                   `json:"namespace,omitempty"`
}

type ProducedBy struct {
	Tool string `json:"tool"`
}

// Report counts rows through the producing stage; Dropped is keyed by reason,
// Deduped by the identity rule that matched.
type Report struct {
	Read    int64            `json:"read"`
	Written int64            `json:"written"`
	Dropped map[string]int64 `json:"dropped,omitempty"`
	Deduped map[string]int64 `json:"deduped,omitempty"`
}

func (r Report) DroppedTotal() int64 { return sum(r.Dropped) }

func (r Report) DedupedTotal() int64 { return sum(r.Deduped) }

func sum(counts map[string]int64) int64 {
	var total int64
	for _, n := range counts {
		total += n
	}
	return total
}

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
