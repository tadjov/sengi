package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

func main() {
	var (
		outputDir       = flag.String("output", "api/mapping-spec", "Output directory for the mapping schema")
		preprocessDir   = flag.String("preprocess-output", "", "Output directory for the preprocess schema; written only when set")
		strictMode      = flag.Bool("strict", true, "Enable strict validation mode for the spec schemas")
		includeExamples = flag.Bool("examples", true, "Include an example of each spec")
		dataPackageDir  = flag.String("datapackage-output", "", "Output directory for the data package descriptor schema; written only when set")
	)
	flag.Parse()

	if *dataPackageDir != "" {
		dataPackageFile, err := writeDataPackageSchema(*dataPackageDir)
		if err != nil {
			log.Fatalf("Failed to generate data package schema: %v", err)
		}
		fmt.Printf("✅ Generated JSON schema: %s\n", dataPackageFile)
	}

	specs := []struct {
		dir, file, exampleFile, example string
		generate                        func(bool) ([]byte, error)
	}{
		{dir: *outputDir, file: mappingSchemaFile, exampleFile: "mapping-example.yaml", example: mappingExample, generate: mappingSchema},
		{dir: *preprocessDir, file: preprocessSchemaFile, exampleFile: "preprocess-example.yaml", example: preprocessExample, generate: preprocessSchema},
	}
	for _, spec := range specs {
		if spec.dir == "" {
			continue
		}
		generated, err := spec.generate(*strictMode)
		if err != nil {
			log.Fatalf("Failed to generate %s: %v", spec.file, err)
		}
		written, err := writeFile(spec.dir, spec.file, generated)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("✅ Generated JSON schema: %s\n", written)
		if !*includeExamples {
			continue
		}
		written, err = writeFile(filepath.Join(spec.dir, "example"), spec.exampleFile, []byte(spec.example))
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("✅ Generated YAML example: %s\n", written)
	}
}

func writeFile(dir, name string, content []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

const mappingExample = `# Mapping example: the columns of a raw news dataset, mapped onto articles.

kind: Mapping
version: v1
name: example
description: "Field mapping for a raw news dataset"
missingValues: ["", "N/A"]
constants:
  Language: eng
fields:
  - source: url
    type: url
    target: URL
    required: true
  - source: title
    target: Title
    required: true
  - source: summary
    target: Description
  - source: content
    target: Content
  - source: published_date
    type: datetime
    format: "2006-01-02 15:04:05"
    target: PublishedAt
  - source: publication
    target: Metadata.PublisherName
  - source: authors
    target: Metadata.Authors
    separator: ";"
  - source: section
    target: Metadata.Section
  - source: word_count
    type: integer
    target: Metadata.Extra.wordCount
`

const preprocessExample = `# Preprocess example: how the example mapping's records become a canonical package.

kind: Preprocess
version: v1
name: example
input: raw/datapackage.json
mapping: ../../mapping-spec/example/mapping-example.yaml
output:
  path: canonical
  format: parquet

id:
  kind: urlHash

duplicates:
  keep: first
  keys:
    - name: same-url
      fields: [URL]
    - name: same-page
      fields: [PublisherDomain, Title, Content]

signals:
  - name: headlines-sharing-body
    distinct: Title
    sharing: Content
    within: PublisherDomain

rules:
  - name: shared-body
    when: {signal: headlines-sharing-body, atLeast: 20}
    action: clear
    field: Content

checks:
  dropped: {warn: 0.01, error: 0.02}
  duplicates: {warn: 0.08, error: 0.10}
  cleared: {warn: 0.08, error: 0.10}
`
