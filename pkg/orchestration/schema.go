package orchestration

import ()

// JobDefinition is the structure for a single job defined by the LLM.
type JobDefinition struct {
	Title      string   `json:"title"`
	Type       string   `json:"type"`
	DependsOn  []string `json:"depends_on,omitempty"`
	Prompt     string   `json:"prompt"`
	Worktree   string   `json:"worktree,omitempty"`
	OutputType string   `json:"output_type,omitempty"`
}

// JobGenerationSchema is the top-level schema for the LLM's output.
type JobGenerationSchema struct {
	Jobs []JobDefinition `json:"jobs"`
}

// GenerateJobCreationSchema produces the JSON Schema string for the llm tool.
func GenerateJobCreationSchema() (string, error) {
	schema := `{
    "$schema": "http://json-schema.org/draft-07/schema#",
    "title": "Grove Job Generation Plan",
    "description": "A list of jobs to be created for the orchestration plan.",
    "type": "object",
    "properties": {
        "jobs": {
            "type": "array",
            "description": "A list of jobs to be executed.",
            "items": {
                "type": "object",
                "properties": {
                    "title": {
                        "type": "string",
                        "description": "A descriptive title for the job."
                    },
                    "type": {
                        "type": "string",
                        "description": "The type of job.",
                        "enum": ["oneshot", "agent", "shell"]
                    },
                    "depends_on": {
                        "type": "array",
                        "description": "A list of job IDs that this job depends on. Use the 'id' from the job definitions you are creating.",
                        "items": { "type": "string" }
                    },
                    "prompt": {
                        "type": "string",
                        "description": "The detailed instructions or command for this job."
                    },
                    "worktree": {
                        "type": "string",
                        "description": "The git worktree to use for this job. Automatically set to the plan name if not specified."
                    },
                    "output_type": {
                        "type": "string",
                        "description": "The output type for the job.",
                        "enum": ["file", "commit", "none", "generate_jobs"]
                    }
                },
                "required": ["title", "type", "prompt"]
            }
        }
    },
    "required": ["jobs"]
}`
	// We use a raw string for simplicity, but this could be generated from structs too.
	// This approach is clear and avoids complex reflection.
	return schema, nil
}