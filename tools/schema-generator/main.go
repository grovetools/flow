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

	// Post-process via JSON injection to ensure x-status fields appear.
	// The jsonschema library's custom marshaler ignores manual Extras modifications,
	// so we marshal to JSON first, then inject fields directly into the JSON structure.
	jsonBytes, err := json.Marshal(schema)
	if err != nil {
		log.Fatalf("Error marshaling schema: %v", err)
	}

	var rawSchema map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &rawSchema); err != nil {
		log.Fatalf("Error unmarshaling schema: %v", err)
	}

	// Inject deprecation status for chat_directory and plans_directory
	if props, ok := rawSchema["properties"].(map[string]interface{}); ok {
		if chatDir, ok := props["chat_directory"].(map[string]interface{}); ok {
			chatDir["x-status"] = "deprecated"
			chatDir["x-status-message"] = "Chats are now stored in notebook workspaces"
			chatDir["x-status-since"] = "v0.6.0"
			chatDir["x-status-target"] = "v1.0"
			chatDir["x-status-replaced-by"] = "notebook.root_dir"
		}
		if plansDir, ok := props["plans_directory"].(map[string]interface{}); ok {
			plansDir["x-status"] = "deprecated"
			plansDir["x-status-message"] = "Plans are now stored in notebook workspaces"
			plansDir["x-status-since"] = "v0.6.0"
			plansDir["x-status-target"] = "v1.0"
			plansDir["x-status-replaced-by"] = "notebook.root_dir"
		}
	}

	data, err := json.MarshalIndent(rawSchema, "", "  ")
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
