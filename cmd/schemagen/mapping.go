package main

import "github.com/tadjov/sengi/internal/ingest/datamapping"

const mappingSchemaFile = "datamapping-v1.json"

func mappingSchema() ([]byte, error) {
	return datamapping.Schema()
}
