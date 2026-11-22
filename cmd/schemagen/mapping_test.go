package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCommittedSpecSchemasMatchTheGenerator(t *testing.T) {
	for _, tt := range []struct {
		dir, file string
		generate  func(bool) ([]byte, error)
	}{
		{dir: "mapping-spec", file: mappingSchemaFile, generate: mappingSchema},
		{dir: "preprocess-spec", file: preprocessSchemaFile, generate: preprocessSchema},
	} {
		t.Run(tt.file, func(t *testing.T) {
			committed, err := os.ReadFile(filepath.Join("..", "..", "api", tt.dir, tt.file))
			if err != nil {
				t.Fatalf("read committed schema: %v", err)
			}
			generated, err := tt.generate(true)
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			if !bytes.Equal(committed, generated) {
				t.Errorf("api/%s/%s is stale; regenerate it with `make schema-gen` and commit the result", tt.dir, tt.file)
			}
		})
	}
}
