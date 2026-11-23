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
unique:
  keys:
    - {name: same-url, fields: [URL]}
    - {name: same-page, fields: [PublisherDomain, Title, Content]}
rules:
  - name: shared-body
    clear: Content
    when: {countDistinct: Title, groupBy: [PublisherDomain, Content], atLeast: 20}
checks:
  droppedOfRead: {warn: 1%, error: 2%}
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
	{name: "the retired duplicates block", doc: replaced(validSpec, "unique:\n", "duplicates:\n"), wantKey: "duplicates"},
	{name: "the retired signals block", doc: validSpec + "signals: []\n", wantKey: "signals"},
	{name: "inside a key", doc: replaced(validSpec, "{name: same-url, fields: [URL]}", "{name: same-url, field: [URL]}"), wantKey: "field"},
	{name: "inside a condition", doc: replaced(validSpec, "atLeast: 20", "atleast: 20"), wantKey: "atleast"},
	{name: "the retired action key", doc: replaced(validSpec, "    clear: Content\n", "    action: clear\n"), wantKey: "action"},
	{name: "inside a budget", doc: replaced(validSpec, "{warn: 1%, error: 2%}", "{warn: 1%, fail: 2%}"), wantKey: "fail"},
	{name: "the retired check names", doc: replaced(validSpec, "droppedOfRead:", "dropped:"), wantKey: "dropped"},
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
	{name: "a keep other than first", doc: replaced(validSpec, "unique:\n", "unique:\n  keep: last\n"), wantInErr: `"last"`},
	{name: "a key name that is not kebab-case", doc: replaced(validSpec, "name: same-page", "name: Same Page"), wantInErr: "Same Page"},
	{name: "a key without fields", doc: replaced(validSpec, "fields: [PublisherDomain, Title, Content]", "fields: []"), wantInErr: "fields"},
	{name: "a threshold under one", doc: replaced(validSpec, "atLeast: 20", "atLeast: 0"), wantInErr: "atLeast"},
	{name: "a rule without a condition", doc: replaced(validSpec, "    when: {countDistinct: Title, groupBy: [PublisherDomain, Content], atLeast: 20}\n", ""), wantInErr: "when"},
	{name: "an empty groupBy", doc: replaced(validSpec, "groupBy: [PublisherDomain, Content]", "groupBy: []"), wantInErr: "groupBy"},
	{name: "a percentage over 100", doc: replaced(validSpec, "error: 2%", "error: 120%"), wantInErr: "120%"},
	{name: "a fraction instead of a percentage", doc: replaced(validSpec, "error: 2%", "error: 0.02"), wantInErr: "0.02"},
	{name: "a negative count", doc: replaced(validSpec, "warn: 1%", "warn: -5"), wantInErr: "-5"},
	{name: "a url hash without a url key", doc: replaced(validSpec, "    - {name: same-url, fields: [URL]}\n", ""), wantInErr: "[URL]", crossField: true},
	{name: "a url hash whose url key compares more", doc: replaced(validSpec, "fields: [URL]}", "fields: [URL, Title]}"), wantInErr: "[URL]", crossField: true},
	{name: "a key field that is no text field", doc: replaced(validSpec, "fields: [PublisherDomain, Title, Content]", "fields: [Metadata.Authors]"), wantInErr: "Metadata.Authors", crossField: true},
	{name: "a key field named twice", doc: replaced(validSpec, "fields: [PublisherDomain, Title, Content]", "fields: [Title, Title]"), wantInErr: `"Title"`, crossField: true},
	{name: "two keys with one name", doc: replaced(validSpec, "name: same-page", "name: same-url"), wantInErr: `"same-url"`, crossField: true},
	{name: "a count of an unknown field", doc: replaced(validSpec, "countDistinct: Title", "countDistinct: Headline"), wantInErr: `"Headline"`, crossField: true},
	{name: "a group by an unknown field", doc: replaced(validSpec, "groupBy: [PublisherDomain, Content]", "groupBy: [Site, Content]"), wantInErr: `"Site"`, crossField: true},
	{name: "a group by a field named twice", doc: replaced(validSpec, "groupBy: [PublisherDomain, Content]", "groupBy: [Content, Content]"), wantInErr: "twice", crossField: true},
	{name: "a group by the counted field", doc: replaced(validSpec, "groupBy: [PublisherDomain, Content]", "groupBy: [Title, Content]"), wantInErr: "count 1", crossField: true},
	{name: "a rule that neither clears nor drops", doc: replaced(validSpec, "    clear: Content\n", ""), wantInErr: "clear", crossField: true},
	{name: "a rule that clears and drops", doc: replaced(validSpec, "    clear: Content\n", "    clear: Content\n    drop: true\n"), wantInErr: "either", crossField: true},
	{name: "a clear of an unknown field", doc: replaced(validSpec, "clear: Content", "clear: Body"), wantInErr: `"Body"`, crossField: true},
	{name: "a warn over its error", doc: replaced(validSpec, "{warn: 1%, error: 2%}", "{warn: 3%, error: 2%}"), wantInErr: "warn", crossField: true},
	{name: "a warn count over its error count", doc: replaced(validSpec, "{warn: 1%, error: 2%}", "{warn: 50, error: 20}"), wantInErr: "warn", crossField: true},
	{name: "a budget with no level", doc: replaced(validSpec, "{warn: 1%, error: 2%}", "{}"), wantInErr: "warn", crossField: true},
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
		{name: "a clear on a required field", doc: replaced(validSpec, "clear: Content", "clear: Title"), wantInErr: "required"},
		{name: "a clear on a field the mapping requires", doc: replaced(validSpec, "clear: Content", "clear: Description"), wantInErr: "required"},
		{name: "a key on an unmapped field", doc: replaced(validSpec, "fields: [PublisherDomain, Title, Content]", "fields: [Metadata.Section]"), wantInErr: "Metadata.Section"},
		{name: "a group by an unmapped field", doc: replaced(validSpec, "groupBy: [PublisherDomain, Content]", "groupBy: [Metadata.PublisherName, Content]"), wantInErr: "Metadata.PublisherName"},
		{name: "a count of an unmapped field", doc: replaced(validSpec, "countDistinct: Title", "countDistinct: Metadata.Section"), wantInErr: "Metadata.Section"},
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
		"unique:\n", "unique:\n  keep: first\n"))
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
