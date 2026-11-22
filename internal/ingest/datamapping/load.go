package datamapping

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/tadjov/sengi/internal/schema"
	"gopkg.in/yaml.v3"
)

// Schema is the JSON schema of a mapping file, generated from DataMapper's
// struct tags. It is closed: a key it does not define is refused.
func Schema() ([]byte, error) {
	generator := schema.NewGenerator(schema.GeneratorConfig{ClosedObjects: true})
	generated, err := generator.GenerateJSONSchema(DataMapper{})
	if err != nil {
		return nil, fmt.Errorf("generate data mapping schema: %w", err)
	}
	return []byte(generated), nil
}

const schemaURL = "datamapping-v1.json"

var compiledSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	raw, err := Schema()
	if err != nil {
		return nil, err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("read data mapping schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(schemaURL, doc); err != nil {
		return nil, fmt.Errorf("read data mapping schema: %w", err)
	}
	return compiler.Compile(schemaURL)
})

// Load reads a mapping file. The schema decides its shape: keys, required
// values, enums, patterns and lengths. Validate decides what a schema cannot
// say, the rules that span fields.
func Load(r io.Reader) (*DataMapper, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read mapping: %w", err)
	}
	if err := checkShape(raw); err != nil {
		return nil, err
	}
	var mapping DataMapper
	if err := yaml.Unmarshal(raw, &mapping); err != nil {
		return nil, fmt.Errorf("decode mapping: %w", err)
	}
	if err := mapping.Validate(); err != nil {
		return nil, err
	}
	return &mapping, nil
}

func checkShape(raw []byte) error {
	var decoded any
	if err := yaml.Unmarshal(raw, &decoded); err != nil {
		return fmt.Errorf("decode mapping: %w", err)
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return fmt.Errorf("decode mapping: %w", err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("decode mapping: %w", err)
	}
	mappingSchema, err := compiledSchema()
	if err != nil {
		return err
	}
	if err := mappingSchema.Validate(instance); err != nil {
		return fmt.Errorf("mapping does not match its schema: %s", violations(err))
	}
	return nil
}

// violations lists each broken rule with the path of the value that broke it,
// without the validator's heading line that names the schema's location.
func violations(err error) string {
	_, list, found := strings.Cut(err.Error(), "\n")
	if !found {
		return err.Error()
	}
	lines := strings.Split(list, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
	}
	return strings.Join(lines, "; ")
}
