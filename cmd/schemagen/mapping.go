package main

import (
	"fmt"

	"github.com/tadjov/sengi/internal/ingest/datamapping"
	"github.com/tadjov/sengi/internal/schema"
)

const mappingSchemaFile = "datamapping-v1.json"

// mappingSchema is closed because the loader rejects unknown keys too.
func mappingSchema() ([]byte, error) {
	generator := schema.NewGenerator(schema.GeneratorConfig{ClosedObjects: true})
	generated, err := generator.GenerateJSONSchema(datamapping.DataMapper{})
	if err != nil {
		return nil, fmt.Errorf("generate data mapping schema: %w", err)
	}
	return []byte(generated), nil
}
