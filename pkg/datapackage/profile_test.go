package datapackage_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dlclark/regexp2"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/tadjov/sengi/pkg/apis/preprocess"
	"github.com/tadjov/sengi/pkg/datapackage"
)

// The profile's path patterns use lookaheads, which Go's RE2 cannot compile.
type ecmaRegexp struct{ re *regexp2.Regexp }

func (r ecmaRegexp) MatchString(s string) bool {
	ok, err := r.re.MatchString(s)
	return err == nil && ok
}

func (r ecmaRegexp) String() string { return r.re.String() }

func compileSchema(t *testing.T, path, id string) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.UseRegexpEngine(func(expr string) (jsonschema.Regexp, error) {
		re, err := regexp2.Compile(expr, regexp2.ECMAScript)
		if err != nil {
			return nil, err
		}
		return ecmaRegexp{re}, nil
	})
	if err := c.AddResource(id, doc); err != nil {
		t.Fatal(err)
	}
	schema, err := c.Compile(id)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func compileProfile(t *testing.T) *jsonschema.Schema {
	t.Helper()
	return compileSchema(t, filepath.Join("testdata", "datapackage-profile-2.0.json"), datapackage.Profile)
}

func TestWrittenDescriptorConformsToDataPackageV2Profile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "articles.parquet", "not really parquet")
	writeFile(t, dir, "embeddings.parquet", "not really vectors")

	_, err := datapackage.Write(context.Background(), dir, fullPackage())
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, datapackage.DescriptorName))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := compileProfile(t).Validate(doc); err != nil {
		t.Errorf("descriptor violates the v2 profile: %v\n%s", err, raw)
	}
}

func fullPackage() datapackage.Package {
	return datapackage.Package{
		Name:          "cc-news-canonical",
		Version:       "v1",
		ArticleSchema: datapackage.ArticleSchema,
		Language:      "eng",
		ProducedBy:    &datapackage.ProducedBy{Tool: "datapipe preprocess"},
		Preprocess:    fullPreprocess(),
		Embedding: &datapackage.Embedding{
			Model: "qwen3-embedding:0.6b", HuggingFaceModel: "Qwen/Qwen3-Embedding-0.6B", Revision: "abc123",
			Tag: "qwen3-embedding:0.6b@1024", Dim: 1024, Recipe: "lead-v1", Pooling: "last_token",
			Normalized: true, MaxTokens: 512, BatchSize: 64, DType: "float16",
		},
		Canonical: &datapackage.Ref{Name: "cc-news-canonical", Version: "v1", ResourceHash: "sha256:" + zeros64},
		DerivedFrom: &datapackage.DerivedFrom{
			Name: "cc-news-embeddings", Version: "v1", Tag: "qwen3-embedding:0.6b@2048", ResourceHash: "sha256:" + zeros64,
		},
		Sources: []datapackage.Source{{Title: "huggingface vblagoje/cc_news", Path: "https://huggingface.co/datasets/vblagoje/cc_news", Version: "81eb2ce0d2a9dad6ad16b68ef750ec290880fa36"}},
		Resources: []datapackage.Resource{
			{Name: datapackage.ArticlesResource, Path: "articles.parquet", Format: "parquet"},
			{Name: datapackage.EmbeddingsResource, Path: "embeddings.parquet", Format: "parquet"},
		},
	}
}

func fullPreprocess() *datapackage.Preprocess {
	warn, fail := preprocess.MustParseThreshold("8%"), preprocess.MustParseThreshold("2")
	return &datapackage.Preprocess{
		ID:   datapackage.ArticleIDs{Kind: preprocess.IDKindURLHash, Algorithm: "uuidv5", Namespace: "6ba7b811-9dad-11d1-80b4-00c04fd430c8"},
		Lock: datapackage.Lock{Spec: "sha256:" + zeros64, Mapping: "sha256:" + zeros64, Input: "sha256:" + zeros64, Tool: "datapipe/0.0.0"},
		Counts: datapackage.Counts{
			Read: 10, Written: 6,
			Dropped:    map[string]int64{"required/Title": 2},
			Duplicates: map[string]int64{"same-url": 1, "same-page": 1},
			Cleared:    map[string]int64{"shared-body": 1},
		},
		Rules: map[string]datapackage.RuleStats{"shared-body": {
			CountDistinct: "Title", GroupBy: []string{"PublisherDomain", "Content"},
			Articles: 6, Median: 1, P90: 3, P99: 3, Max: 3,
			AtLeast: map[string]int64{"2": 3},
			Top:     []datapackage.Group{{Values: []string{"example.com", "Subscribe to read"}, CountDistinct: 3, Articles: 3}},
		}},
		Checks: map[string]datapackage.CheckResult{"duplicatesOfRead": {Count: 2, Share: 0.2, Warn: &warn, Error: &fail, Result: datapackage.CheckError}},
	}
}

const zeros64 = "0000000000000000000000000000000000000000000000000000000000000000"

// Python producers don't use this package, so names are checked against the contract.
func TestWrittenDescriptorUsesContractPropertyNames(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "articles.parquet", "not really parquet")
	writeFile(t, dir, "embeddings.parquet", "not really vectors")
	if _, err := datapackage.Write(context.Background(), dir, fullPackage()); err != nil {
		t.Fatalf("Write: %v", err)
	}
	doc := readDescriptorJSON(t, dir)

	want := map[string]any{
		"$schema":                                   "https://datapackage.org/profiles/2.0/datapackage.json",
		"name":                                      "cc-news-canonical",
		"version":                                   "v1",
		"articleSchema":                             "article/v2",
		"language":                                  "eng",
		"preprocess/id/kind":                        "urlHash",
		"preprocess/id/algorithm":                   "uuidv5",
		"preprocess/lock/tool":                      "datapipe/0.0.0",
		"producedBy/tool":                           "datapipe preprocess",
		"preprocess/counts/read":                    10.0,
		"preprocess/counts/written":                 6.0,
		"preprocess/counts/duplicates/same-page":    1.0,
		"preprocess/counts/cleared/shared-body":     1.0,
		"preprocess/checks/duplicatesOfRead/result": "error",
		"preprocess/checks/duplicatesOfRead/warn":   "8%",
		"preprocess/checks/duplicatesOfRead/error":  2.0,
		"preprocess/rules/shared-body/atLeast/2":    3.0,
		"embedding/model":                           "qwen3-embedding:0.6b",
		"embedding/hfModel":                         "Qwen/Qwen3-Embedding-0.6B",
		"embedding/revision":                        "abc123",
		"embedding/tag":                             "qwen3-embedding:0.6b@1024",
		"embedding/dim":                             1024.0,
		"embedding/recipe":                          "lead-v1",
		"embedding/pooling":                         "last_token",
		"embedding/normalized":                      true,
		"embedding/maxTokens":                       512.0,
		"embedding/batchSize":                       64.0,
		"embedding/dtype":                           "float16",
		"canonical/name":                            "cc-news-canonical",
		"canonical/version":                         "v1",
		"canonical/resourceHash":                    "sha256:" + zeros64,
		"derivedFrom/name":                          "cc-news-embeddings",
		"derivedFrom/version":                       "v1",
		"derivedFrom/tag":                           "qwen3-embedding:0.6b@2048",
		"derivedFrom/resourceHash":                  "sha256:" + zeros64,
	}
	for pointer, value := range want {
		if got := lookup(doc, pointer); got != value {
			t.Errorf("%s = %v, want %v", pointer, got, value)
		}
	}
	if _, ok := doc["embedding"].(map[string]any)["limit"]; ok {
		t.Errorf("embedding.limit is written for a package that is not a trial: %v", doc["embedding"])
	}
	resources := doc["resources"].([]any)
	for i, name := range []string{"articles", "embeddings"} {
		resource := resources[i].(map[string]any)
		if resource["name"] != name {
			t.Errorf("resource %d is named %v, want %s", i, resource["name"], name)
		}
		for _, key := range []string{"name", "path", "format", "bytes", "hash"} {
			if _, ok := resource[key]; !ok {
				t.Errorf("resource lacks %q: %v", key, resource)
			}
		}
	}
}

func lookup(doc map[string]any, pointer string) any {
	var node any = doc
	for _, key := range strings.Split(pointer, "/") {
		object, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		node = object[key]
	}
	return node
}
