package mapping_test

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
	"github.com/tadjov/sengi/internal/types/language"
	"github.com/tadjov/sengi/pkg/apis/mapping"
	"gopkg.in/yaml.v3"
)

const validMapping = `kind: Mapping
version: v1
name: test
constants:
  Language: eng
fields:
  - source: title
    target: Title
    required: true
  - source: link
    type: url
    target: URL
    required: true
`

const repoRoot = "../../.."

func loadMapping(doc string) (mapping.Spec, error) {
	return mapping.Read(strings.NewReader(doc))
}

func withFields(doc string, fields ...string) string {
	return doc + strings.Join(fields, "")
}

func replaced(doc, old, replacement string) string {
	if !strings.Contains(doc, old) {
		panic("fixture does not contain " + old)
	}
	return strings.Replace(doc, old, replacement, 1)
}

func TestReadMapping_AcceptsEveryTargetKind(t *testing.T) {
	doc := withFields(validMapping,
		"  - {source: body, target: Content}\n",
		"  - {source: summary, target: Description}\n",
		"  - {source: date, type: datetime, format: '2006-01-02 15:04:05', target: PublishedAt}\n",
		"  - {source: outlet, target: Metadata.PublisherName}\n",
		"  - {source: byline, target: Metadata.Authors, separator: ';'}\n",
		"  - {source: tags, target: Metadata.Keywords, separator: ','}\n",
		"  - {source: section, target: Metadata.Section}\n",
		"  - {source: image_url, type: url, target: Metadata.Image}\n",
		"  - {source: modified, type: date, target: Metadata.DateModified}\n",
		"  - {source: words, type: integer, target: Metadata.Extra.wordCount}\n",
	)

	spec, err := loadMapping(doc)
	require.NoError(t, err)

	lang, err := spec.Constants.ResolvedLanguage()
	require.NoError(t, err)
	assert.Equal(t, language.English, lang)
}

func TestMapping_CountsOnlyTheListedValuesAsMissing(t *testing.T) {
	spec, err := loadMapping(replaced(validMapping, "name: test\n", "name: test\nmissingValues: ['', 'N/A']\n"))
	require.NoError(t, err)

	for raw, missing := range map[string]bool{"": true, "  ": true, "N/A": true, " N/A ": true, "n/a": false, "0": false} {
		assert.Equal(t, missing, spec.IsMissing(raw), "%q", raw)
	}
}

func TestMapping_CountsOnlyAnEmptyValueAsMissingByDefault(t *testing.T) {
	spec, err := loadMapping(validMapping)
	require.NoError(t, err)

	assert.True(t, spec.IsMissing(" \t"))
	assert.False(t, spec.IsMissing("N/A"))
}

var unknownKeyMappings = []struct {
	name    string
	doc     string
	wantKey string
}{
	{name: "top level", doc: validMapping + "constans:\n  Language: eng\n", wantKey: "constans"},
	{name: "the retired fieldMappings", doc: validMapping + "fieldMappings: []\n", wantKey: "fieldMappings"},
	{name: "the retired identity", doc: validMapping + "identity:\n  idKind: urlHash\n", wantKey: "identity"},
	{name: "the retired dateFormat", doc: validMapping + "dateFormat: '2006-01-02'\n", wantKey: "dateFormat"},
	{name: "the retired output", doc: validMapping + "output:\n  format: parquet\n", wantKey: "output"},
	{name: "inside a field", doc: replaced(validMapping, "    target: Title\n", "    target: Title\n    requried: true\n"), wantKey: "requried"},
	{name: "the retired sourceType", doc: replaced(validMapping, "    type: url\n", "    sourceType: url\n"), wantKey: "sourceType"},
}

func TestReadMapping_RejectsAnUnknownKeyByName(t *testing.T) {
	for _, tt := range unknownKeyMappings {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadMapping(tt.doc)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantKey)
		})
	}
}

// crossField marks rules the JSON schema cannot express, so only Go rejects them.
var rejectedMappings = []struct {
	name       string
	doc        string
	wantInErr  string
	crossField bool
}{
	{name: "a retired target", doc: withFields(validMapping, "  - {source: author, target: Author}\n"), wantInErr: `"Author"`},
	{name: "a mapped publisher domain", doc: withFields(validMapping, "  - {source: domain, target: PublisherDomain}\n"), wantInErr: "PublisherDomain"},
	{name: "language as a mapped target", doc: withFields(validMapping, "  - {source: lang, target: Language}\n"), wantInErr: `"Language"`},
	{name: "an extra key with no name", doc: withFields(validMapping, "  - {source: x, target: Metadata.Extra.}\n"), wantInErr: "Metadata.Extra."},
	{name: "an extra key with a dot", doc: withFields(validMapping, "  - {source: x, target: Metadata.Extra.a.b}\n"), wantInErr: "Metadata.Extra.a.b"},
	{name: "an unknown type", doc: withFields(validMapping, "  - {source: body, type: text, target: Content}\n"), wantInErr: `"text"`},
	{name: "the retired int type", doc: withFields(validMapping, "  - {source: n, type: int, target: Metadata.Extra.n}\n"), wantInErr: `"int"`},
	{name: "an unsupported language", doc: replaced(validMapping, "Language: eng", "Language: en"), wantInErr: `"en"`},
	{name: "no constants", doc: replaced(validMapping, "constants:\n  Language: eng\n", ""), wantInErr: "Language"},
	{name: "the retired kind", doc: replaced(validMapping, "kind: Mapping", "kind: DataMapper"), wantInErr: "DataMapper"},
	{name: "a name that is not kebab-case", doc: replaced(validMapping, "name: test", "name: cc_news"), wantInErr: "cc_news"},
	{name: "no name", doc: replaced(validMapping, "name: test\n", ""), wantInErr: "name"},
	{name: "a mapped article id", doc: withFields(validMapping, "  - {source: uid, target: ID}\n"), wantInErr: `"ID"`},
	{name: "a 501-character Cyrillic description", doc: replaced(validMapping, "name: test", "name: test\ndescription: "+strings.Repeat("ж", 501)), wantInErr: "description"},
	{name: "a 101-character Cyrillic source", doc: replaced(validMapping, "source: title", "source: "+strings.Repeat("ж", 101)), wantInErr: "source"},
	{
		name:       "two sources on one target",
		doc:        withFields(validMapping, "  - {source: full_content, target: Content}\n", "  - {source: content, target: Content}\n"),
		wantInErr:  `"Content"`,
		crossField: true,
	},
	{
		name:       "two sources on one extra key",
		doc:        withFields(validMapping, "  - {source: a, target: Metadata.Extra.k}\n", "  - {source: b, target: Metadata.Extra.k}\n"),
		wantInErr:  "Metadata.Extra.k",
		crossField: true,
	},
	{name: "no url", doc: replaced(validMapping, "  - source: link\n    type: url\n    target: URL\n    required: true\n", ""), wantInErr: "URL", crossField: true},
	{name: "an optional title", doc: replaced(validMapping, "    target: Title\n    required: true\n", "    target: Title\n"), wantInErr: "Title", crossField: true},
	{name: "a url read as a plain string", doc: replaced(validMapping, "type: url\n    target: URL", "type: string\n    target: URL"), wantInErr: "URL", crossField: true},
	{name: "a date read into a text target", doc: withFields(validMapping, "  - {source: body, type: datetime, target: Content}\n"), wantInErr: "datetime", crossField: true},
	{name: "a format on a text value", doc: withFields(validMapping, "  - {source: body, format: '2006', target: Content}\n"), wantInErr: "format", crossField: true},
	{name: "a separator on a single-valued target", doc: withFields(validMapping, "  - {source: body, target: Content, separator: ';'}\n"), wantInErr: "separator", crossField: true},
	{name: "an external id read as a number", doc: withFields(validMapping, "  - {source: uid, type: integer, target: Metadata.ExternalID}\n"), wantInErr: "integer", crossField: true},
}

func TestReadMapping_RejectsAnInvalidMappingPrecisely(t *testing.T) {
	for _, tt := range rejectedMappings {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadMapping(tt.doc)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantInErr)
		})
	}
}

func compileMappingSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, "api/mapping-spec/mapping-v1.json"))
	require.NoError(t, err)
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	require.NoError(t, err)
	compiler := jsonschema.NewCompiler()
	require.NoError(t, compiler.AddResource("mapping-v1.json", doc))
	schema, err := compiler.Compile("mapping-v1.json")
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

func TestMappingSchema_AgreesWithTheReader(t *testing.T) {
	schema := compileMappingSchema(t)

	accepted := []struct {
		name string
		doc  string
	}{
		{name: "a valid mapping", doc: validMapping},
		{name: "missing values", doc: replaced(validMapping, "name: test\n", "name: test\nmissingValues: ['', 'null']\n")},
		{name: "a 500-character Cyrillic description", doc: replaced(validMapping, "name: test", "name: test\ndescription: "+strings.Repeat("ж", 500))},
		{name: "a 100-character Cyrillic source", doc: replaced(validMapping, "source: title", "source: "+strings.Repeat("ж", 100))},
	}
	for _, tt := range accepted {
		t.Run("accepts "+tt.name, func(t *testing.T) {
			_, err := loadMapping(tt.doc)
			require.NoError(t, err)
			require.NoError(t, validateAgainstSchema(t, schema, tt.doc))
		})
	}
	for _, tt := range rejectedMappings {
		if tt.crossField {
			continue
		}
		t.Run("rejects "+tt.name, func(t *testing.T) {
			assert.Error(t, validateAgainstSchema(t, schema, tt.doc))
		})
	}
	for _, tt := range unknownKeyMappings {
		t.Run("rejects an unknown key "+tt.name, func(t *testing.T) {
			assert.Error(t, validateAgainstSchema(t, schema, tt.doc))
		})
	}
}

func TestCommittedMappings_ReadAndMatchTheSchema(t *testing.T) {
	schema := compileMappingSchema(t)
	paths, err := filepath.Glob(filepath.Join(repoRoot, "datasets/*/mapping.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	paths = append(paths, filepath.Join(repoRoot, "api/mapping-spec/example/mapping-example.yaml"))

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			_, err = loadMapping(string(raw))
			require.NoError(t, err)
			require.NoError(t, validateAgainstSchema(t, schema, string(raw)))
		})
	}
}
