package main

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/tadjov/sengi/pkg/apis/mapping"
	"github.com/tadjov/sengi/pkg/apis/preprocess"
	"github.com/tadjov/sengi/pkg/schema"
)

const (
	mappingSchemaFile    = "mapping-v1.json"
	preprocessSchemaFile = "preprocess-v1.json"
)

// mappingSchema is closed because the reader rejects unknown keys too.
func mappingSchema(strict bool) ([]byte, error) {
	return specSchema(reflect.TypeFor[mapping.Spec](), "mapping-v1", "Mapping", strict)
}

// preprocessSchema is closed because the reader rejects unknown keys too.
func preprocessSchema(strict bool) ([]byte, error) {
	return specSchema(reflect.TypeFor[preprocess.Spec](), "preprocess-v1", "Preprocess", strict)
}

func specSchema(t reflect.Type, id, title string, strict bool) ([]byte, error) {
	generator := schema.NewGeneratorWithConfig(schema.GeneratorConfig{
		StrictValidation: strict,
		ClosedObjects:    true,
	})
	generated, err := generator.GenerateSchema(t)
	if err != nil {
		return nil, fmt.Errorf("generate %s schema: %w", title, err)
	}
	generated.ID = "https://schemas.sengi.sh/" + id
	generated.Title = title
	encoded, err := json.MarshalIndent(generated, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode %s schema: %w", title, err)
	}
	return encoded, nil
}
