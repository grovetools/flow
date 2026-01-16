package main

import (
	"encoding/json"
	"log"
	"os"

	"github.com/invopop/jsonschema"
	"github.com/grovetools/flow/cmd"
	"github.com/grovetools/flow/pkg/orchestration"
)

func main() {
	r := &jsonschema.Reflector{
		AllowAdditionalProperties: true,
		ExpandedStruct:            true,
		FieldNameTag:              "yaml",
	}

	schema := r.Reflect(&cmd.FlowConfig{})
	schema.Title = "Grove Flow Configuration"
	schema.Description = "Schema for the 'flow' extension in grove.yml."

	// Make all fields optional - Grove configs should not require any fields
	schema.Required = nil

	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		log.Fatalf("Error marshaling schema: %v", err)
	}

	// Write to the package root
	if err := os.WriteFile("flow.schema.json", data, 0644); err != nil {
		log.Fatalf("Error writing schema file: %v", err)
	}

	log.Printf("Successfully generated flow schema at flow.schema.json")

	// Generate schema for Job frontmatter
	jobSchema := r.Reflect(&orchestration.Job{})
	jobSchema.Title = "Grove Flow Job"
	jobSchema.Description = "Schema for Grove Flow job frontmatter in markdown files."

	// Make all fields optional - Job frontmatter should not require all fields
	jobSchema.Required = nil

	jobData, err := json.MarshalIndent(jobSchema, "", "  ")
	if err != nil {
		log.Fatalf("Error marshaling job schema: %v", err)
	}

	// Write to the package root
	if err := os.WriteFile("flow-job.schema.json", jobData, 0644); err != nil {
		log.Fatalf("Error writing job schema file: %v", err)
	}

	log.Printf("Successfully generated job schema at flow-job.schema.json")
}
