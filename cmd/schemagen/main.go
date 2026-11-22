package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

func main() {
	outputDir := flag.String("output", "api/mapping-spec", "Output directory for the data mapping schema")
	dataPackageDir := flag.String("datapackage-output", "", "Output directory for the data package descriptor schema; written only when set")
	flag.Parse()

	if *dataPackageDir != "" {
		dataPackageFile, err := writeDataPackageSchema(*dataPackageDir)
		if err != nil {
			log.Fatalf("Failed to generate data package schema: %v", err)
		}
		fmt.Printf("Generated JSON schema: %s\n", dataPackageFile)
	}

	schemaJSON, err := mappingSchema()
	if err != nil {
		log.Fatalf("Failed to generate schema for DataMapping: %v", err)
	}
	if err := os.MkdirAll(*outputDir, 0o755); err != nil {
		log.Fatalf("Failed to create output directory %s: %v", *outputDir, err)
	}
	jsonFile := filepath.Join(*outputDir, mappingSchemaFile)
	if err := os.WriteFile(jsonFile, schemaJSON, 0o644); err != nil {
		log.Fatalf("Failed to write JSON schema to %s: %v", jsonFile, err)
	}
	fmt.Printf("Generated JSON schema: %s\n", jsonFile)
}
