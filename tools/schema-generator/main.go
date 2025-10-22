package main

import (
	"encoding/json"
	"log"
	"os"

	"github.com/invopop/jsonschema"
	"github.com/mattsolo1/grove-flow/cmd"
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
}
