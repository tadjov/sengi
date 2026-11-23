package preprocess_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/pkg/apis/mapping"
	"github.com/tadjov/sengi/pkg/apis/preprocess"
	"gopkg.in/yaml.v3"
)

const validSpec = `kind: Preprocess
version: v1
name: test
input: raw/datapackage.json
mapping: mapping.yaml
output:
  path: canonical
id:
  kind: urlHash
duplicates:
  keys:
    - {name: same-url, fields: [URL]}
    - {name: same-page, fields: [PublisherDomain, Title, Content]}
signals:
  - {name: headlines-sharing-body, distinct: Title, sharing: Content, within: PublisherDomain}
rules:
  - {name: shared-body, when: {signal: headlines-sharing-body, atLeast: 20}, action: clear, field: Content}
checks:
  dropped: {warn: 0.01, error: 0.02}
`

const validMapping = `kind: Mapping
version: v1
name: test
constants:
  Language: eng
fields:
  - {source: title, target: Title, required: true}
  - {source: url, type: url, target: URL, required: true}
  - {source: text, target: Content}
  - {source: summary, target: Description, required: true}
`

const repoRoot = "../../.."

func replaced(doc, old, replacement string) string {
	if !strings.Contains(doc, old) {
		panic("fixture does not contain " + old)
	}
	return strings.Replace(doc, old, replacement, 1)
}

func readSpec(doc string) (preprocess.Spec, error) {
	return preprocess.Read(strings.NewReader(doc))
}

var unknownKeySpecs = []struct {
	name    string
	doc     string
	wantKey string
}{
	{name: "top level", doc: validSpec + "rule: []\n", wantKey: "rule"},
	{name: "the retired identity block", doc: validSpec + "identity:\n  idKind: urlHash\n", wantKey: "identity"},
	{name: "inside a key", doc: replaced(validSpec, "{name: same-url, fields: [URL]}", "{name: same-url, field: [URL]}"), wantKey: "field"},
	{name: "inside a condition", doc: replaced(validSpec, "atLeast: 20", "atleast: 20"), wantKey: "atleast"},
	{name: "inside a budget", doc: replaced(validSpec, "{warn: 0.01, error: 0.02}", "{warn: 0.01, fail: 0.02}"), wantKey: "fail"},
	{name: "a flag action's options", doc: replaced(validSpec, "field: Content}", "field: Content, flag: true}"), wantKey: "flag"},
}

func TestRead_RejectsAnUnknownKeyByName(t *testing.T) {
	for _, tt := range unknownKeySpecs {
		t.Run(tt.name, func(t *testing.T) {
			_, err := readSpec(tt.doc)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantKey)
		})
	}
}

// crossField marks rules the JSON schema cannot express, so only Go rejects them.
var rejectedSpecs = []struct {
	name       string
	doc        string
	wantInErr  string
	crossField bool
}{
	{name: "the wrong kind", doc: replaced(validSpec, "kind: Preprocess", "kind: Mapping"), wantInErr: "Mapping"},
	{name: "no input", doc: replaced(validSpec, "input: raw/datapackage.json\n", ""), wantInErr: "input"},
	{name: "no mapping", doc: replaced(validSpec, "mapping: mapping.yaml\n", ""), wantInErr: "mapping"},
	{name: "no output path", doc: replaced(validSpec, "  path: canonical\n", "  format: parquet\n"), wantInErr: "path"},
	{name: "an unknown output format", doc: replaced(validSpec, "  path: canonical\n", "  path: canonical\n  format: csv\n"), wantInErr: `"csv"`},
	{name: "no id", doc: replaced(validSpec, "id:\n  kind: urlHash\n", ""), wantInErr: "id"},
	{name: "an unknown id kind", doc: replaced(validSpec, "kind: urlHash", "kind: uuidv5"), wantInErr: `"uuidv5"`},
	{name: "a keep other than first", doc: replaced(validSpec, "duplicates:\n", "duplicates:\n  keep: last\n"), wantInErr: `"last"`},
	{name: "a key name that is not kebab-case", doc: replaced(validSpec, "name: same-page", "name: Same Page"), wantInErr: "Same Page"},
	{name: "a key without fields", doc: replaced(validSpec, "fields: [PublisherDomain, Title, Content]", "fields: []"), wantInErr: "fields"},
	{name: "an unknown action", doc: replaced(validSpec, "action: clear", "action: flag"), wantInErr: `"flag"`},
	{name: "a threshold under one", doc: replaced(validSpec, "atLeast: 20", "atLeast: 0"), wantInErr: "atLeast"},
	{name: "a share over one", doc: replaced(validSpec, "error: 0.02", "error: 2"), wantInErr: "2"},
	{name: "a negative share", doc: replaced(validSpec, "warn: 0.01", "warn: -0.01"), wantInErr: "-0.01"},
	{name: "a url hash without a url key", doc: replaced(validSpec, "    - {name: same-url, fields: [URL]}\n", ""), wantInErr: "[URL]", crossField: true},
	{name: "a url hash whose url key compares more", doc: replaced(validSpec, "fields: [URL]}", "fields: [URL, Title]}"), wantInErr: "[URL]", crossField: true},
	{name: "a key field that is no text field", doc: replaced(validSpec, "fields: [PublisherDomain, Title, Content]", "fields: [Metadata.Authors]"), wantInErr: "Metadata.Authors", crossField: true},
	{name: "a key field named twice", doc: replaced(validSpec, "fields: [PublisherDomain, Title, Content]", "fields: [Title, Title]"), wantInErr: `"Title"`, crossField: true},
	{name: "two keys with one name", doc: replaced(validSpec, "name: same-page", "name: same-url"), wantInErr: `"same-url"`, crossField: true},
	{name: "a signal on an unknown field", doc: replaced(validSpec, "distinct: Title", "distinct: Headline"), wantInErr: `"Headline"`, crossField: true},
	{name: "a signal counting the field it groups by", doc: replaced(validSpec, "distinct: Title", "distinct: Content"), wantInErr: "different fields", crossField: true},
	{name: "a rule on an undefined signal", doc: replaced(validSpec, "{signal: headlines-sharing-body,", "{signal: shared-titles,"), wantInErr: `"shared-titles"`, crossField: true},
	{name: "a clear without a field", doc: replaced(validSpec, ", field: Content}", "}"), wantInErr: "field", crossField: true},
	{name: "a drop with a field", doc: replaced(validSpec, "action: clear", "action: drop"), wantInErr: "no field", crossField: true},
	{name: "a warn over its error", doc: replaced(validSpec, "{warn: 0.01, error: 0.02}", "{warn: 0.03, error: 0.02}"), wantInErr: "warn", crossField: true},
	{name: "a budget with no level", doc: replaced(validSpec, "{warn: 0.01, error: 0.02}", "{}"), wantInErr: "warn", crossField: true},
}

func TestRead_RejectsAnInvalidSpecPrecisely(t *testing.T) {
	for _, tt := range rejectedSpecs {
		t.Run(tt.name, func(t *testing.T) {
			_, err := readSpec(tt.doc)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantInErr)
		})
	}
}

func TestValidateWith_RefusesFieldsTheMappingCannotFill(t *testing.T) {
	m, err := mapping.Read(strings.NewReader(validMapping))
	require.NoError(t, err)
	tests := []struct {
		name      string
		doc       string
		wantInErr string
	}{
		{name: "a clear on a required field", doc: replaced(validSpec, "field: Content}", "field: Title}"), wantInErr: "required"},
		{name: "a clear on a field the mapping requires", doc: replaced(validSpec, "field: Content}", "field: Description}"), wantInErr: "required"},
		{name: "a key on an unmapped field", doc: replaced(validSpec, "fields: [PublisherDomain, Title, Content]", "fields: [Metadata.Section]"), wantInErr: "Metadata.Section"},
		{name: "a signal on an unmapped field", doc: replaced(validSpec, "within: PublisherDomain", "within: Metadata.PublisherName"), wantInErr: "Metadata.PublisherName"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := readSpec(tt.doc)
			require.NoError(t, err)

			err = spec.ValidateWith(m)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantInErr)
		})
	}
	spec, err := readSpec(validSpec)
	require.NoError(t, err)
	assert.NoError(t, spec.ValidateWith(m), "the derived PublisherDomain needs no field")
}

func TestPolicy_IgnoresWhereTheFilesLieAndWhichDefaultsAreSpelledOut(t *testing.T) {
	implicit, err := readSpec(validSpec)
	require.NoError(t, err)
	explicit, err := readSpec(replaced(replaced(replaced(validSpec,
		"input: raw/datapackage.json", "input: /data/news/raw"),
		"  path: canonical\n", "  path: /tmp/trial\n  format: parquet\n"),
		"duplicates:\n", "duplicates:\n  keep: first\n"))
	require.NoError(t, err)

	assert.Equal(t, implicit.Policy(), explicit.Policy())
}

func TestLoad_ResolvesPathsAgainstTheSpecsFolder(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mapping.yaml"), []byte(validMapping), 0o644))
	spec := filepath.Join(dir, "preprocess.yaml")
	require.NoError(t, os.WriteFile(spec, []byte(replaced(validSpec, "  path: canonical\n", "  path: /abs/canonical\n")), 0o644))

	resolved, err := preprocess.Load(spec)

	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "raw", "datapackage.json"), resolved.Input)
	assert.Equal(t, "/abs/canonical", resolved.OutputRoot)
	assert.Equal(t, "test", resolved.Mapping.Name)
}

func TestLoad_KeepsARemoteInputAsIs(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mapping.yaml"), []byte(validMapping), 0o644))
	spec := filepath.Join(dir, "preprocess.yaml")
	require.NoError(t, os.WriteFile(spec, []byte(replaced(validSpec, "input: raw/datapackage.json", "input: s3://bucket/news/raw")), 0o644))

	resolved, err := preprocess.Load(spec)

	require.NoError(t, err)
	assert.Equal(t, "s3://bucket/news/raw", resolved.Input)
}

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, "api/preprocess-spec/preprocess-v1.json"))
	require.NoError(t, err)
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	require.NoError(t, err)
	compiler := jsonschema.NewCompiler()
	require.NoError(t, compiler.AddResource("preprocess-v1.json", doc))
	schema, err := compiler.Compile("preprocess-v1.json")
	require.NoError(t, err)
	return schema
}

func validateAgainstSchema(t *testing.T, schema *jsonschema.Schema, doc string) error {
	t.Helper()
	var decoded any
	require.NoError(t, yaml.Unmarshal([]byte(doc), &decoded))
	encoded, err := json.Marshal(decoded)
	require.NoError(t, err)
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	require.NoError(t, err)
	return schema.Validate(instance)
}

func TestSchema_AgreesWithTheReader(t *testing.T) {
	schema := compileSchema(t)
	t.Run("accepts a valid spec", func(t *testing.T) {
		_, err := readSpec(validSpec)
		require.NoError(t, err)
		require.NoError(t, validateAgainstSchema(t, schema, validSpec))
	})
	for _, tt := range rejectedSpecs {
		if tt.crossField {
			continue
		}
		t.Run("rejects "+tt.name, func(t *testing.T) {
			assert.Error(t, validateAgainstSchema(t, schema, tt.doc))
		})
	}
	for _, tt := range unknownKeySpecs {
		t.Run("rejects an unknown key "+tt.name, func(t *testing.T) {
			assert.Error(t, validateAgainstSchema(t, schema, tt.doc))
		})
	}
}

func TestCommittedSpecs_LoadAndMatchTheSchema(t *testing.T) {
	schema := compileSchema(t)
	paths, err := filepath.Glob(filepath.Join(repoRoot, "datasets/*/preprocess.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	paths = append(paths, filepath.Join(repoRoot, "api/preprocess-spec/example/preprocess-example.yaml"))

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			_, err = preprocess.Load(path)
			require.NoError(t, err)
			require.NoError(t, validateAgainstSchema(t, schema, string(raw)))
		})
	}
}
