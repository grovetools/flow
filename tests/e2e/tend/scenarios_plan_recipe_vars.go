// File: tests/e2e/tend/scenarios_plan_recipe_vars.go
package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/assert"
	"github.com/mattsolo1/grove-tend/pkg/command"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// PlanRecipeVarsScenario tests the --recipe-vars flag functionality.
func PlanRecipeVarsScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-recipe-vars",
		Description: "Tests passing variables to recipe templates with --recipe-vars flag.",
		Tags:        []string{"plan", "recipes", "vars", "init"},
		Steps: []harness.Step{
			harness.NewStep("Setup git repository and config", func(ctx *harness.Context) error {
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				groveConfig := `name: test-project
flow:
  plans_directory: ./plans
`
				return fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig)
			}),
			harness.NewStep("Initialize plan with multiple recipe-vars", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "init", "docgen-test", 
					"--recipe", "docgen-customize",
					"--recipe-vars", "model=claude-3-5-sonnet-20241022",
					"--recipe-vars", "rules_file=custom.rules",
					"--recipe-vars", "output_dir=generated-docs").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}
				if !strings.Contains(result.Stdout, "Using recipe: docgen-customize") {
					return fmt.Errorf("output should confirm which recipe is being used")
				}
				// Store the plan path for verification in next steps
				ctx.Set("test_plan_path", filepath.Join(ctx.RootDir, "plans", "docgen-test"))
				return nil
			}),
			harness.NewStep("Verify ALL three variables were substituted in chat job", func(ctx *harness.Context) error {
				chatJobPath := filepath.Join(ctx.RootDir, "plans", "docgen-test", "01-customize-docs.md")
				content, err := fs.ReadString(chatJobPath)
				if err != nil {
					return fmt.Errorf("failed to read chat job file: %w", err)
				}

				// Variable 1: Check that model was set
				if !strings.Contains(content, `model: claude-3-5-sonnet-20241022`) {
					return fmt.Errorf("Variable 1 (model) failed: expected model to be set to claude-3-5-sonnet-20241022")
				}

				// Variable 2: Check that rules_file was substituted
				if !strings.Contains(content, "the rules in `custom.rules`") {
					return fmt.Errorf("Variable 2 (rules_file) failed: expected rules_file to be substituted to custom.rules")
				}

				// Variable 3: Check that output_dir was substituted
				if !strings.Contains(content, "`generated-docs` directory") {
					return fmt.Errorf("Variable 3 (output_dir) failed: expected output_dir to be substituted to generated-docs")
				}

				// Verify that template conditionals worked (model field should appear since it was provided)
				if !strings.Contains(content, "model:") {
					return fmt.Errorf("Template conditional failed: model field should appear when model variable is provided")
				}

				// All 3 variables successfully substituted in chat job
				return nil
			}),
			harness.NewStep("Verify both variables were substituted in agent job", func(ctx *harness.Context) error {
				agentJobPath := filepath.Join(ctx.RootDir, "plans", "docgen-test", "02-generate-docs.md")
				content, err := fs.ReadString(agentJobPath)
				if err != nil {
					return fmt.Errorf("failed to read agent job file: %w", err)
				}

				// Variable 2: Check that rules_file was substituted
				if !strings.Contains(content, "`custom.rules`") {
					return fmt.Errorf("Variable 2 (rules_file) failed in agent job: expected custom.rules to appear")
				}

				// Variable 3: Check that output_dir was substituted  
				if !strings.Contains(content, "the `generated-docs` directory") {
					return fmt.Errorf("Variable 3 (output_dir) failed in agent job: expected generated-docs to appear")
				}

				// Both variables (rules_file, output_dir) successfully substituted in agent job
				return nil
			}),
			harness.NewStep("Test with missing variable values (should use defaults)", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				// Create a plan with only some vars specified
				cmd := command.New(flow, "plan", "init", "docgen-defaults", 
					"--recipe", "docgen-customize",
					"--recipe-vars", "model=gemini-2.0-flash").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}

				// Check the chat job for defaults
				chatJobPath := filepath.Join(ctx.RootDir, "plans", "docgen-defaults", "01-customize-docs.md")
				content, err := fs.ReadString(chatJobPath)
				if err != nil {
					return fmt.Errorf("failed to read chat job file: %w", err)
				}

				// Model should be set
				if !strings.Contains(content, `model: gemini-2.0-flash`) {
					return fmt.Errorf("expected model to be set to gemini-2.0-flash")
				}

				// Should have default text for rules_file
				if !strings.Contains(content, "your documentation rules file (typically `docs.rules` or `docs/docs.rules`)") {
					return fmt.Errorf("expected default text for rules_file when not specified")
				}

				// Should have default text for output_dir
				if !strings.Contains(content, "your documentation directory (typically `docs/`)") {
					return fmt.Errorf("expected default text for output_dir when not specified")
				}

				return nil
			}),
			harness.NewStep("Test invalid recipe-vars format (warning)", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "init", "docgen-invalid", 
					"--recipe", "docgen-customize",
					"--recipe-vars", "model=gpt-4",
					"--recipe-vars", "invalid_no_equals").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				// Command should still succeed but with warning
				if result.Error != nil {
					return result.Error
				}

				// Should have warning about invalid format
				if !strings.Contains(result.Stdout, "Warning: invalid recipe-var format 'invalid_no_equals'") {
					return fmt.Errorf("expected warning about invalid recipe-var format")
				}

				return nil
			}),
			harness.NewStep("Verify recipe without vars support still works", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				// standard-feature recipe doesn't use Vars
				cmd := command.New(flow, "plan", "init", "standard-test", 
					"--recipe", "standard-feature",
					"--recipe-vars", "unused=value").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}

				// Verify plan was created successfully
				planDir := filepath.Join(ctx.RootDir, "plans", "standard-test")
				if !fs.Exists(planDir) {
					return fmt.Errorf("plan directory was not created")
				}

				// Check that standard recipe files exist
				expectedFiles := []string{"01-spec.md", "02-implement.md", "03-git-changes.md", "04-git-status.md", "05-review.md"}
				for _, file := range expectedFiles {
					path := filepath.Join(planDir, file)
					if !fs.Exists(path) {
						return fmt.Errorf("expected recipe file '%s' was not created", file)
					}
				}

				return nil
			}),
			harness.NewStep("Test comma-delimited format for multiple variables", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				// Use comma-delimited format instead of multiple flags
				cmd := command.New(flow, "plan", "init", "docgen-comma", 
					"--recipe", "docgen-customize",
					"--recipe-vars", "model=gemini-2.5-flash,rules_file=api.rules,output_dir=api-docs").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}

				// Verify all three variables from the comma-delimited string were applied
				chatJobPath := filepath.Join(ctx.RootDir, "plans", "docgen-comma", "01-customize-docs.md")
				content, err := fs.ReadString(chatJobPath)
				if err != nil {
					return fmt.Errorf("failed to read chat job file: %w", err)
				}

				// Check all three comma-separated variables
				if !strings.Contains(content, `model: gemini-2.5-flash`) {
					return fmt.Errorf("comma-delimited var 1 failed: expected model=gemini-2.5-flash")
				}
				if !strings.Contains(content, "the rules in `api.rules`") {
					return fmt.Errorf("comma-delimited var 2 failed: expected rules_file=api.rules")
				}
				if !strings.Contains(content, "`api-docs` directory") {
					return fmt.Errorf("comma-delimited var 3 failed: expected output_dir=api-docs")
				}

				// ✓ Comma-delimited format successfully parsed all 3 variables")
				return nil
			}),
			harness.NewStep("Test mixed format (comma-delimited + multiple flags)", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				// Mix comma-delimited with multiple flags
				cmd := command.New(flow, "plan", "init", "docgen-mixed", 
					"--recipe", "docgen-customize",
					"--recipe-vars", "model=claude-3-opus-20240229,rules_file=backend.rules",
					"--recipe-vars", "output_dir=backend-docs").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}

				// Verify the mixed format works
				chatJobPath := filepath.Join(ctx.RootDir, "plans", "docgen-mixed", "01-customize-docs.md")
				content, err := fs.ReadString(chatJobPath)
				if err != nil {
					return fmt.Errorf("failed to read chat job file: %w", err)
				}

				// Check that values from both the comma-delimited and separate flag work
				if !strings.Contains(content, `model: claude-3-opus-20240229`) {
					return fmt.Errorf("mixed format failed: model from comma-delimited string not found")
				}
				if !strings.Contains(content, "the rules in `backend.rules`") {
					return fmt.Errorf("mixed format failed: rules_file from comma-delimited string not found")
				}
				if !strings.Contains(content, "`backend-docs` directory") {
					return fmt.Errorf("mixed format failed: output_dir from separate flag not found")
				}

				// ✓ Mixed format (comma + separate flags) works correctly")
				return nil
			}),
			harness.NewStep("Setup grove.yml with recipe config", func(ctx *harness.Context) error {
				// Create a grove.yml with recipe defaults for multiple recipes
				groveConfig := `name: test-project
flow:
  plans_directory: ./plans
  recipes:
    docgen-customize:
      vars:
        model: "gpt-4o"
        rules_file: "config.rules"
        output_dir: "config-docs"
    another-recipe:
      vars:
        some_var: "some_value"
`
				err := fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig)
				if err != nil {
					return err
				}
				ctx.Set("grove_yml_path", filepath.Join(ctx.RootDir, "grove.yml"))
				return nil
			}),
			harness.NewStep("Test recipe vars loaded from grove.yml (no CLI vars)", func(ctx *harness.Context) error {
				// Initialize with just the recipe (no --recipe-vars)
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "init", "docgen-config", 
					"--recipe", "docgen-customize").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}

				// Should see message about loading defaults
				if !strings.Contains(result.Stdout, "Loaded default vars from grove.yml") {
					return fmt.Errorf("expected message about loading default vars from grove.yml")
				}

				// Verify ALL config defaults were applied
				chatJobPath := filepath.Join(ctx.RootDir, "plans", "docgen-config", "01-customize-docs.md")
				content, err := fs.ReadString(chatJobPath)
				if err != nil {
					return fmt.Errorf("failed to read chat job file: %w", err)
				}

				// Check all three variables from config
				if !strings.Contains(content, `model: gpt-4o`) {
					return fmt.Errorf("config default 1 failed: expected model=gpt-4o from grove.yml")
				}
				if !strings.Contains(content, "the rules in `config.rules`") {
					return fmt.Errorf("config default 2 failed: expected rules_file=config.rules from grove.yml")
				}
				if !strings.Contains(content, "`config-docs` directory") {
					return fmt.Errorf("config default 3 failed: expected output_dir=config-docs from grove.yml")
				}

				// Verify in agent job too
				agentJobPath := filepath.Join(ctx.RootDir, "plans", "docgen-config", "02-generate-docs.md")
				agentContent, err := fs.ReadString(agentJobPath)
				if err != nil {
					return fmt.Errorf("failed to read agent job file: %w", err)
				}
				if !strings.Contains(agentContent, "`config.rules`") {
					return fmt.Errorf("config default in agent job failed: expected config.rules")
				}
				if !strings.Contains(agentContent, "the `config-docs` directory") {
					return fmt.Errorf("config default in agent job failed: expected config-docs directory")
				}

				return nil
			}),
			harness.NewStep("Test partial CLI override of grove.yml defaults", func(ctx *harness.Context) error {
				// grove.yml still has the defaults from setup step
				// Override only some variables with command-line vars
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "init", "docgen-partial-override", 
					"--recipe", "docgen-customize",
					"--recipe-vars", "model=claude-3-5-sonnet-20241022",  // Override only model
					"--recipe-vars", "output_dir=override-docs").Dir(ctx.RootDir)  // Override only output_dir
				// Note: rules_file NOT specified, should use config default
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}

				// Should still see message about loading defaults
				if !strings.Contains(result.Stdout, "Loaded default vars from grove.yml") {
					return fmt.Errorf("expected message about loading defaults even when overriding some")
				}

				// Verify partial overrides work correctly
				chatJobPath := filepath.Join(ctx.RootDir, "plans", "docgen-partial-override", "01-customize-docs.md")
				content, err := fs.ReadString(chatJobPath)
				if err != nil {
					return fmt.Errorf("failed to read chat job file: %w", err)
				}

				// Model should be overridden
				if !strings.Contains(content, `model: claude-3-5-sonnet-20241022`) {
					return fmt.Errorf("partial override 1 failed: expected model to be overridden")
				}
				// rules_file should still be from config (NOT overridden)
				if !strings.Contains(content, "the rules in `config.rules`") {
					return fmt.Errorf("partial override 2 failed: expected rules_file to remain from grove.yml")
				}
				// output_dir should be overridden
				if !strings.Contains(content, "`override-docs` directory") {
					return fmt.Errorf("partial override 3 failed: expected output_dir to be overridden")
				}

				return nil
			}),
			harness.NewStep("Test recipe not configured in grove.yml", func(ctx *harness.Context) error {
				// Try to use standard-feature recipe which is NOT in our grove.yml config
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "init", "standard-no-config", 
					"--recipe", "standard-feature").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}

				// Should NOT see message about loading defaults
				if strings.Contains(result.Stdout, "Loaded default vars from grove.yml") {
					return fmt.Errorf("should not load vars for recipe not in config")
				}

				// Verify plan was created normally
				specPath := filepath.Join(ctx.RootDir, "plans", "standard-no-config", "01-spec.md")
				if !fs.Exists(specPath) {
					return fmt.Errorf("recipe without config should still work: spec.md not found")
				}

				return nil
			}),
			harness.NewStep("Test config + comma-delimited CLI vars", func(ctx *harness.Context) error {
				// Test that config defaults work with comma-delimited CLI overrides
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "init", "docgen-config-comma", 
					"--recipe", "docgen-customize",
					"--recipe-vars", "model=gemini-2.5-pro,output_dir=comma-docs").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}

				chatJobPath := filepath.Join(ctx.RootDir, "plans", "docgen-config-comma", "01-customize-docs.md")
				content, err := fs.ReadString(chatJobPath)
				if err != nil {
					return fmt.Errorf("failed to read chat job file: %w", err)
				}

				// Model from comma-delimited override
				if !strings.Contains(content, `model: gemini-2.5-pro`) {
					return fmt.Errorf("comma override failed: expected model=gemini-2.5-pro")
				}
				// rules_file from config (not in comma string)
				if !strings.Contains(content, "the rules in `config.rules`") {
					return fmt.Errorf("config default with comma failed: expected rules_file from config")
				}
				// output_dir from comma-delimited override
				if !strings.Contains(content, "`comma-docs` directory") {
					return fmt.Errorf("comma override failed: expected output_dir=comma-docs")
				}

				return nil
			}),
			harness.NewStep("Reset grove.yml to original state", func(ctx *harness.Context) error {
				// Reset grove.yml to original minimal state for remaining tests
				originalConfig := `name: test-project
flow:
  plans_directory: ./plans
`
				return fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), originalConfig)
			}),
			harness.NewStep("Test multiple values for same key (last wins)", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "init", "docgen-override", 
					"--recipe", "docgen-customize",
					"--recipe-vars", "model=gpt-4",
					"--recipe-vars", "model=claude-3-5-sonnet-20241022").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}

				// Check that the last value wins
				chatJobPath := filepath.Join(ctx.RootDir, "plans", "docgen-override", "01-customize-docs.md")
				content, err := fs.ReadString(chatJobPath)
				if err != nil {
					return fmt.Errorf("failed to read chat job file: %w", err)
				}

				// Should have the second (last) model value
				if !strings.Contains(content, `model: claude-3-5-sonnet-20241022`) {
					return fmt.Errorf("expected last model value to win")
				}

				return assert.NotContains(content, `model: gpt-4`, "should not contain the first model value")
			}),
		},
	}
}