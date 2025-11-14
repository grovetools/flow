// File: tests/e2e/tend/scenarios_generate_recipe.go
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

// GenerateRecipeScenario tests the generate-recipe job type functionality.
func GenerateRecipeScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-generate-recipe",
		Description: "Tests generate-recipe job type: creating reusable recipes from existing plans",
		Tags:        []string{"plan", "recipes", "generate"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with source plan", func(ctx *harness.Context) error {
				// 1. Setup git repo in the isolated test directory (ctx.RootDir)
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)

				// 2. Create the test-specific grove.yml inside the test directory
				groveConfig := `name: test-project
flow:
  plans_directory: ./plans
  oneshot_model: mock-summarizer
`
				if err := fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig); err != nil {
					return fmt.Errorf("failed to write grove.yml: %w", err)
				}
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Setup empty global config in sandboxed environment
				if err := setupEmptyGlobalConfig(ctx); err != nil {
					return err
				}

				// 3. Use the flow binary to create the source plan
				flow, err := getFlowBinary()
				if err != nil {
					return err
				}
				
				cmd := ctx.Command(flow, "plan", "init", "api-migration-example").Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to init plan: %v\nOutput: %s\nError: %s", result.Error, result.Stdout, result.Stderr)
				}
				
				cmd = ctx.Command(flow, "plan", "add", "api-migration-example",
					"--title", "Spec for user-profile-api",
					"--type", "oneshot",
					"-p", "Define the spec.").Dir(ctx.RootDir)
				result = cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to add spec job: %v", result.Error)
				}
				
				cmd = ctx.Command(flow, "plan", "add", "api-migration-example",
					"--title", "Implement user-profile-api migration",
					"--type", "interactive_agent",
					"--depends-on", "01-spec-for-user-profile-api.md",
					"--worktree", "user-profile-api",
					"-p", "Implement the migration.").Dir(ctx.RootDir)
				result = cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to add impl job: %v", result.Error)
				}

				return nil
			}),
			// 4. Set up mock LLM response file
			harness.NewStep("Setup mock LLM response", func(ctx *harness.Context) error {
				// Write the mock response to a file
				mockResponse := `--- [01-spec.md] ---
---
id: spec
title: "Specification for {{ .PlanName }}"
type: oneshot
status: pending
---
Define the detailed specification for the "{{ .PlanName }}" feature.

--- [02-implement.md] ---
---
id: implement
title: "Implement {{ .PlanName }}"
type: interactive_agent
depends_on:
  - 01-spec.md
worktree: "{{ .PlanName }}"
status: pending
---
Implement the "{{ .PlanName }}" feature based on the specification.
`
				mockFile := filepath.Join(ctx.RootDir, "mock-response.txt")
				if err := fs.WriteString(mockFile, mockResponse); err != nil {
					return fmt.Errorf("failed to write mock response file: %w", err)
				}
				
				// Set environment variable
				os.Setenv("GROVE_MOCK_LLM_RESPONSE_FILE", mockFile)
				
				return nil
			}),
			setupTestEnvironment(map[string]interface{}{}),
			harness.NewStep("Create and run the generate-recipe job", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()

				// Create the generate-recipe job
				jobContent := `---
id: generate-api-migration-recipe
title: Generate API Migration Recipe
status: pending
type: generate-recipe
source_plan: plans/api-migration-example
recipe_name: api-migration
model: mock-summarizer
---
Generalize this plan for API endpoint migrations. Replace 'user-profile-api' with '{{ .PlanName }}'.
`
				fs.CreateDir(filepath.Join(ctx.RootDir, "plans", "recipe-generation-plan"))
				jobPath := filepath.Join(ctx.RootDir, "plans", "recipe-generation-plan", "01-generate-recipe.md")
				fs.WriteString(jobPath, jobContent)
				
				// Run the job - use --next to run the next available job in the plan
				cmd := ctx.Command(flow, "plan", "run", "--next", "--yes", "plans/recipe-generation-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				return result.Error
			}),
			harness.NewStep("Verify recipe was created in user config", func(ctx *harness.Context) error {
				homeDir, _ := os.UserHomeDir()
				recipePath := filepath.Join(homeDir, ".config", "grove", "recipes", "api-migration")
				
				if !fs.Exists(recipePath) {
					return fmt.Errorf("recipe directory was not created at %s", recipePath)
				}

				specFile := filepath.Join(recipePath, "01-spec.md")
				if !fs.Exists(specFile) {
					return fmt.Errorf("expected spec file not found in recipe")
				}
				
				content, _ := fs.ReadString(specFile)
				if !strings.Contains(content, "{{ .PlanName }}") {
					return fmt.Errorf("recipe file was not correctly parameterized")
				}
				
				// Cleanup
				os.RemoveAll(recipePath)
				os.Unsetenv("GROVE_MOCK_LLM_RESPONSE_FILE")
				return nil
			}),
			harness.NewStep("Initialize a new plan from the generated recipe", func(ctx *harness.Context) error {
				// First, recreate the recipe since we cleaned it up
				homeDir, _ := os.UserHomeDir()
				recipePath := filepath.Join(homeDir, ".config", "grove", "recipes", "api-migration")
				fs.CreateDir(recipePath)
				fs.WriteString(filepath.Join(recipePath, "01-spec.md"), "title: Specification for {{ .PlanName }}")
				
				flow, _ := getFlowBinary()
				cmd := ctx.Command(flow, "plan", "init", "new-api-plan", "--recipe", "api-migration").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}

				// Verify the new plan was created and parameterized
				newPlanSpec := filepath.Join(ctx.RootDir, "plans", "new-api-plan", "01-spec.md")
				if !fs.Exists(newPlanSpec) {
					return fmt.Errorf("new plan was not created from recipe")
				}
				
				content, _ := fs.ReadString(newPlanSpec)
				if !strings.Contains(content, "title: Specification for new-api-plan") {
					return fmt.Errorf("new plan from recipe was not parameterized correctly")
				}

				// Cleanup
				os.RemoveAll(recipePath)
				return nil
			}),
		},
	}
}

// GenerateRecipeWithVariablesScenario tests recipe generation with multiple template variables.
func GenerateRecipeWithVariablesScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-generate-recipe-variables",
		Description: "Tests generate-recipe with multiple template variables",
		Tags:        []string{"plan", "recipes", "generate"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with source plan", func(ctx *harness.Context) error {
				// Setup git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)

				// Create grove.yml
				groveConfig := `name: test-project
flow:
  plans_directory: ./plans
  oneshot_model: mock-summarizer
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig)
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Setup empty global config in sandboxed environment
				if err := setupEmptyGlobalConfig(ctx); err != nil {
					return err
				}

				// Use flow binary to create source plan
				flow, _ := getFlowBinary()

				// Initialize the source plan
				cmd := ctx.Command(flow, "plan", "init", "microservice-setup").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to init source plan: %w", err)
				}

				// Add jobs with multiple variable parts
				cmd = ctx.Command(flow, "plan", "add", "microservice-setup",
					"--title", "Design user-service API",
					"--type", "oneshot",
					"-p", "Design the REST API for user-service using OpenAPI 3.0 specification.").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to add design job: %w", err)
				}

				cmd = ctx.Command(flow, "plan", "add", "microservice-setup",
					"--title", "Implement user-service in Go",
					"--type", "interactive_agent",
					"--worktree", "user-service",
					"-p", "Implement the user-service microservice in Go based on the API design.").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to add implementation job: %w", err)
				}

				return nil
			}),
			harness.NewStep("Setup mock LLM response", func(ctx *harness.Context) error {
				// Write the mock response to a file
				mockResponse := `--- [01-design-api.md] ---
---
id: design-api
title: "Design {{ .ServiceName }} API"
type: oneshot
status: pending
---

Design the REST API for {{ .ServiceName }} using OpenAPI 3.0 specification.

--- [02-implement-service.md] ---
---
id: implement-service
title: "Implement {{ .ServiceName }} in {{ .Language }}"
type: interactive_agent
depends_on:
  - 01-design-api.md
worktree: "{{ .ServiceName }}"
status: pending
---

Implement the {{ .ServiceName }} microservice in {{ .Language }} based on the API design.
`
				mockFile := filepath.Join(ctx.RootDir, "mock-response.txt")
				if err := fs.WriteString(mockFile, mockResponse); err != nil {
					return fmt.Errorf("failed to write mock response file: %w", err)
				}
				
				// Set environment variable
				os.Setenv("GROVE_MOCK_LLM_RESPONSE_FILE", mockFile)
				
				return nil
			}),
			setupTestEnvironment(map[string]interface{}{}),
			harness.NewStep("Create and run generate-recipe job with multiple variables", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()

				// Create generate-recipe job
				jobContent := `---
id: generate-microservice-recipe
title: Generate Microservice Setup Recipe
status: pending
type: generate-recipe
source_plan: plans/microservice-setup
recipe_name: microservice-template
model: mock-summarizer
---

Generalize this plan for creating any microservice. The variable parts are:
- Service name: 'user-service' should become '{{ .ServiceName }}'
- Programming language: 'Go' should become '{{ .Language }}'

Make sure to replace these consistently throughout the recipe.
`
				fs.CreateDir(filepath.Join(ctx.RootDir, "plans", "recipe-gen-multi"))
				jobPath := filepath.Join(ctx.RootDir, "plans", "recipe-gen-multi", "01-generate-microservice-recipe.md")
				if err := fs.WriteString(jobPath, jobContent); err != nil {
					return fmt.Errorf("failed to write generate-recipe job: %w", err)
				}

				// Run the job - use --next to run the next available job in the plan
				cmd := ctx.Command(flow, "plan", "run", "--next", "--yes", "plans/recipe-gen-multi").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				return result.Error
			}),

			harness.NewStep("Verify recipe contains multiple variables", func(ctx *harness.Context) error {
				// Verify recipe contains both variables
				homeDir, _ := os.UserHomeDir()
				recipePath := filepath.Join(homeDir, ".config", "grove", "recipes", "microservice-template")
				
				for _, file := range []string{"01-design-api.md", "02-implement-service.md"} {
					content, err := os.ReadFile(filepath.Join(recipePath, file))
					if err != nil {
						return fmt.Errorf("failed to read recipe file %s: %w", file, err)
					}

					contentStr := string(content)
					if !strings.Contains(contentStr, "{{ .ServiceName }}") {
						return fmt.Errorf("recipe file %s missing {{ .ServiceName }} variable", file)
					}
					if strings.Contains(contentStr, "02-implement") && !strings.Contains(contentStr, "{{ .Language }}") {
						return fmt.Errorf("implementation file missing {{ .Language }} variable")
					}
				}

				// Clean up
				os.RemoveAll(recipePath)
				os.Unsetenv("GROVE_MOCK_LLM_RESPONSE_FILE")
				return nil
			}),
		},
	}
}