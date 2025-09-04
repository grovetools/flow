package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// ComplexOrchestrationScenario tests multi-job plans with dependencies and dynamic job generation
func ComplexOrchestrationScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-complex-orchestration",
		Description: "Test complex job orchestration with dependencies and generate_jobs",
		Tags:        []string{"plan", "orchestration", "generate_jobs"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with plan configuration", func(ctx *harness.Context) error {
				// Setup git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)

				// Create directories
				planDir := filepath.Join(ctx.RootDir, "plans", "system-plan")
				fs.CreateDir(filepath.Dir(planDir))
				fs.CreateDir(planDir)

				// Write grove.yml with LLM config
				configContent := `name: test-project
flow:
  plans_directory: ./plans
orchestration:
  target_agent_container: test-container
llm:
  provider: openai
  model: test
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)

				// Setup mock LLM that returns generate_jobs JSON
				mockDir := filepath.Join(ctx.RootDir, "mocks")
				fs.CreateDir(mockDir)

				// Create mock response that generates job YAML directly
				generateJobsYAML := `Based on the system specification, I'll create a plan with the following jobs:

---
id: 01-setup-database
title: Setup Database
type: shell
---

# Setup Database

Create PostgreSQL database schema

echo 'Creating database schema...'

---
id: 02-generate-api  
title: Generate API
type: oneshot
depends_on:
  - 01-setup-database.md
---

# Generate API

Generate REST API endpoints based on the database schema.

---
id: 03-create-tests
title: Create Tests
type: oneshot
depends_on:
  - 02-generate-api.md
---

# Create Tests

Create unit tests for the API endpoints.
`
				fs.WriteString(filepath.Join(mockDir, "generate_response.txt"), generateJobsYAML)

				mockLLMScript := `#!/bin/bash
# Mock LLM that returns generate_jobs YAML for the first call
MOCK_DIR="$(dirname "$0")"
STATE_FILE="$MOCK_DIR/llm_called"

if [ ! -f "$STATE_FILE" ]; then
  # First call - return generate_jobs YAML
  cat "$MOCK_DIR/generate_response.txt"
  touch "$STATE_FILE"
else
  # Subsequent calls - return regular text
  echo "Task completed successfully."
fi
`
				mockPath := filepath.Join(mockDir, "llm")
				fs.WriteString(mockPath, mockLLMScript)
				os.Chmod(mockPath, 0755)

				// Store the mock directory for later use
				ctx.Set("test_bin_dir", mockDir)

				return nil
			}),

			harness.NewStep("Create initial spec and generator job", func(ctx *harness.Context) error {
				planDir := filepath.Join(ctx.RootDir, "plans", "system-plan")

				// Create spec file
				specContent := `# System Architecture Specification

We need to build a web application with:
1. PostgreSQL database
2. REST API
3. Unit tests
`
				fs.WriteString(filepath.Join(planDir, "spec.md"), specContent)

				// Create initial generator job without prompt_source (spec will be in the prompt directly)
				jobContent := `---
id: 00-high-level-plan
title: High-level Plan
type: oneshot
status: pending
output_type: generate_jobs
---

# High-level Plan

Create a plan for implementing the system described below:

## System Architecture Specification

We need to build a web application with:
1. PostgreSQL database
2. REST API
3. Unit tests
`
				fs.WriteString(filepath.Join(planDir, "00-high-level-plan.md"), jobContent)

				return nil
			}),

			harness.NewStep("Initialize and run the plan generator", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()

				// Set the plan as active first
				cmd := ctx.Command(flow, "plan", "set", "system-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return fmt.Errorf("plan set failed: %v", result.Error)
				}

				// Run the generator job
				cmd = ctx.Command(flow, "plan", "run", filepath.Join("plans", "system-plan", "00-high-level-plan.md"), "-y").Dir(ctx.RootDir)
				result = cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return fmt.Errorf("plan run failed: %v\nStdout: %s\nStderr: %s", result.Error, result.Stdout, result.Stderr)
				}

				return nil
			}),

			harness.NewStep("Verify generated jobs were created", func(ctx *harness.Context) error {
				planDir := filepath.Join(ctx.RootDir, "plans", "system-plan")

				// List files in the plan directory
				files, err := os.ReadDir(planDir)
				if err != nil {
					return fmt.Errorf("failed to read plan directory: %v", err)
				}

				fmt.Printf("Files in plan directory:\n")
				for _, f := range files {
					fmt.Printf("  - %s\n", f.Name())
				}

				// Check the content of the generator job to see if it has the output
				generatorJob := filepath.Join(planDir, "00-high-level-plan.md")
				content, _ := fs.ReadString(generatorJob)
				fmt.Printf("\nGenerator job content:\n%s\n", content)

				// Check that the expected job files were created
				expectedJobs := []string{
					"01-setup-database.md",
					"02-generate-api.md",
					"03-create-tests.md",
				}

				for _, jobFile := range expectedJobs {
					jobPath := filepath.Join(planDir, jobFile)
					if _, err := os.Stat(jobPath); os.IsNotExist(err) {
						return fmt.Errorf("expected job file %s was not created", jobFile)
					}

					// Verify job content
					content, err := fs.ReadString(jobPath)
					if err != nil {
						return err
					}

					// Check for expected content based on job
					switch jobFile {
					case "01-setup-database.md":
						if !strings.Contains(content, "Setup Database") {
							return fmt.Errorf("job %s missing expected title", jobFile)
						}
						if !strings.Contains(content, "type: shell") {
							return fmt.Errorf("job %s should be type shell", jobFile)
						}
					case "02-generate-api.md":
						if !strings.Contains(content, "depends_on:") && !strings.Contains(content, "01-setup-database.md") {
							return fmt.Errorf("job %s missing dependency on database setup", jobFile)
						}
					case "03-create-tests.md":
						if !strings.Contains(content, "depends_on:") && !strings.Contains(content, "02-generate-api.md") {
							return fmt.Errorf("job %s missing dependency on API generation", jobFile)
						}
					}
				}

				return nil
			}),

			harness.NewStep("Check plan status shows dependencies", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()

				cmd := ctx.Command(flow, "plan", "status", "system-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return fmt.Errorf("plan status failed: %v", result.Error)
				}

				// Verify status shows all jobs
				output := result.Stdout
				if !strings.Contains(output, "00-high-level-plan.md") {
					return fmt.Errorf("status should show generator job")
				}
				if !strings.Contains(output, "01-setup-database.md") {
					return fmt.Errorf("status should show database job")
				}
				if !strings.Contains(output, "02-generate-api.md") {
					return fmt.Errorf("status should show API job")
				}
				if !strings.Contains(output, "03-create-tests.md") {
					return fmt.Errorf("status should show tests job")
				}

				return nil
			}),

			harness.NewStep("Run jobs respecting dependencies", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()

				// Run the database setup job
				cmd := ctx.Command(flow, "plan", "run", filepath.Join("plans", "system-plan", "01-setup-database.md"), "-y").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return fmt.Errorf("database setup failed: %v", result.Error)
				}

				// Try to run the test job (should fail due to dependency)
				cmd = ctx.Command(flow, "plan", "run", filepath.Join("plans", "system-plan", "03-create-tests.md"), "-y").Dir(ctx.RootDir)
				result = cmd.Run()

				// This should fail because API job hasn't run yet
				if result.Error == nil {
					return fmt.Errorf("running tests job should fail when dependencies are not met")
				}

				// Now run the API job
				cmd = ctx.Command(flow, "plan", "run", filepath.Join("plans", "system-plan", "02-generate-api.md"), "-y").Dir(ctx.RootDir)
				result = cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return fmt.Errorf("API generation failed: %v", result.Error)
				}

				// Now the tests job should succeed
				cmd = ctx.Command(flow, "plan", "run", filepath.Join("plans", "system-plan", "03-create-tests.md"), "-y").Dir(ctx.RootDir)
				result = cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return fmt.Errorf("tests creation failed: %v", result.Error)
				}

				return nil
			}),

			harness.NewStep("Verify final plan status", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()

				cmd := ctx.Command(flow, "plan", "status", "system-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return fmt.Errorf("final plan status failed: %v", result.Error)
				}

				// All jobs should be completed
				output := result.Stdout
				completedCount := strings.Count(strings.ToLower(output), "completed")
				if completedCount < 4 {
					return fmt.Errorf("expected 4 completed jobs, status shows: %s", output)
				}

				return nil
			}),
		},
	}
}
