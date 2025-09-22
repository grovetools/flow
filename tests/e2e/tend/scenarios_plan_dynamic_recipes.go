// File: tests/e2e/tend/scenarios_plan_dynamic_recipes.go
package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/command"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// PlanDynamicRecipesScenario tests the dynamic recipe loading from external commands.
func PlanDynamicRecipesScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-dynamic-recipes",
		Description: "Tests loading recipes dynamically from external commands via get_recipe_cmd.",
		Tags:        []string{"plan", "recipes", "dynamic"},
		Steps: []harness.Step{
			harness.NewStep("Setup git repository", func(ctx *harness.Context) error {
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")
				return nil
			}),
			harness.NewStep("Create a mock recipe provider script", func(ctx *harness.Context) error {
				// Create a simple script that outputs JSON with recipes
				scriptContent := `#!/bin/bash
cat << 'EOF'
{
  "dynamic-test": {
    "description": "A test recipe from dynamic provider",
    "jobs": {
      "01-test.md": "---\ntitle: Test Job\nstatus: pending\ntype: chat\n---\n\nThis is a test job from a dynamic provider."
    }
  },
  "dynamic-custom": {
    "description": "Custom documentation generator",
    "jobs": {
      "01-plan.md": "---\ntitle: Plan Documentation\nstatus: pending\ntype: chat\nmodel: {{.Vars.model}}\n---\n\nPlan the documentation for {{.PlanName}}.",
      "02-generate.md": "---\ntitle: Generate Docs\nstatus: pending\ntype: agent\nworktree: {{.PlanName}}\n---\n\nGenerate documentation based on the plan."
    }
  }
}
EOF
`
				scriptPath := filepath.Join(ctx.RootDir, "recipe-provider.sh")
				err := fs.WriteString(scriptPath, scriptContent)
				if err != nil {
					return err
				}
				// Make the script executable
				cmd := command.New("chmod", "+x", scriptPath).Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return result.Error
				}
				ctx.Set("recipe_provider_script", scriptPath)
				return nil
			}),
			harness.NewStep("Create grove.yml with get_recipe_cmd", func(ctx *harness.Context) error {
				scriptPath := ctx.GetString("recipe_provider_script")
				groveConfig := fmt.Sprintf(`name: test-project
flow:
  plans_directory: ./plans
  recipes:
    get_recipe_cmd: "%s"
`, scriptPath)
				return fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig)
			}),
			harness.NewStep("List recipes to verify dynamic loading", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "recipes", "list").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}

				// Check that dynamic recipes are listed with [Dynamic] source
				if !strings.Contains(result.Stdout, "dynamic-test") {
					return fmt.Errorf("expected dynamic-test recipe to be listed")
				}
				if !strings.Contains(result.Stdout, "[Dynamic]") {
					return fmt.Errorf("expected [Dynamic] source indicator")
				}
				if !strings.Contains(result.Stdout, "A test recipe from dynamic provider") {
					return fmt.Errorf("expected dynamic recipe description")
				}

				// Also verify built-in recipes still show
				if !strings.Contains(result.Stdout, "[Built-in]") {
					return fmt.Errorf("expected [Built-in] recipes to still be listed")
				}

				return nil
			}),
			harness.NewStep("Initialize plan with dynamic recipe", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "init", "test-dynamic", 
					"--recipe", "dynamic-test").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}

				// Verify the output shows the correct source
				if !strings.Contains(result.Stdout, "Using recipe: dynamic-test [Dynamic]") {
					return fmt.Errorf("expected output to show dynamic recipe source")
				}

				// Verify the job file was created correctly
				// First check if plan directory exists
				planDir1 := filepath.Join(ctx.RootDir, "test-dynamic")
				planDir2 := filepath.Join(ctx.RootDir, "plans", "test-dynamic")
				
				var jobPath string
				if fs.Exists(filepath.Join(planDir2, "01-test.md")) {
					jobPath = filepath.Join(planDir2, "01-test.md")
				} else if fs.Exists(filepath.Join(planDir1, "01-test.md")) {
					// Plan was created at root level
					jobPath = filepath.Join(planDir1, "01-test.md")
				} else {
					return fmt.Errorf("job file 01-test.md not found in expected locations")
				}
				
				content, err := fs.ReadString(jobPath)
				if err != nil {
					return fmt.Errorf("failed to read job file at %s: %w", jobPath, err)
				}

				if !strings.Contains(content, "This is a test job from a dynamic provider") {
					return fmt.Errorf("expected job content from dynamic provider")
				}

				return nil
			}),
			harness.NewStep("Test JSON output for recipes list", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "recipes", "list", "--json").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}

				// Parse JSON output
				var recipes []map[string]interface{}
				if err := json.Unmarshal([]byte(result.Stdout), &recipes); err != nil {
					return fmt.Errorf("failed to parse JSON output: %w", err)
				}

				// Find the dynamic recipe
				foundDynamic := false
				for _, recipe := range recipes {
					if name, ok := recipe["name"].(string); ok && name == "dynamic-test" {
						foundDynamic = true
						// Check the source field
						if source, ok := recipe["source"].(string); ok {
							if source != "[Dynamic]" {
								return fmt.Errorf("expected source to be [Dynamic], got %s", source)
							}
						} else {
							return fmt.Errorf("recipe missing source field")
						}
					}
				}

				if !foundDynamic {
					return fmt.Errorf("dynamic-test recipe not found in JSON output")
				}

				return nil
			}),
			// Note: Skipping user recipe precedence test as it requires modifying actual user home directory
			// which is not suitable for E2E tests. The precedence logic is tested in unit tests.
			harness.NewStep("Test with built-in recipe to verify dynamic doesn't break built-ins", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				// Initialize a plan with a built-in recipe to verify they still work
				cmd := command.New(flow, "plan", "init", "test-builtin", 
					"--recipe", "chat").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}

				// Check it says [Built-in]
				if !strings.Contains(result.Stdout, "Using recipe: chat [Built-in]") {
					return fmt.Errorf("expected built-in recipe to show [Built-in] source")
				}

				// Verify the chat job file was created
				jobPath1 := filepath.Join(ctx.RootDir, "test-builtin", "01-chat.md")
				jobPath2 := filepath.Join(ctx.RootDir, "plans", "test-builtin", "01-chat.md")
				
				if fs.Exists(jobPath2) {
					// Created under plans/
					return nil
				} else if fs.Exists(jobPath1) {
					// Created at root
					return nil
				} else {
					return fmt.Errorf("expected chat job file not found")
				}
			}),
			harness.NewStep("Test broken recipe provider command", func(ctx *harness.Context) error {
				// Update grove.yml with a broken command
				groveConfig := `name: test-project
flow:
  plans_directory: ./plans
  recipes:
    get_recipe_cmd: "/nonexistent/command"
`
				err := fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig)
				if err != nil {
					return err
				}

				// List recipes - should still work but show warning
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "recipes", "list").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				// Should not fail entirely
				if result.Error != nil {
					return fmt.Errorf("command should not fail when recipe provider is broken")
				}

				// Should show warning
				if !strings.Contains(result.Stderr, "Warning: recipe provider command failed") {
					return fmt.Errorf("expected warning about failed recipe provider")
				}

				// Should still show built-in recipes
				if !strings.Contains(result.Stdout, "[Built-in]") {
					return fmt.Errorf("should still list built-in recipes")
				}

				return nil
			}),
			harness.NewStep("Test recipe provider with invalid JSON", func(ctx *harness.Context) error {
				// Create a script that outputs invalid JSON
				scriptContent := `#!/bin/bash
echo "not valid json {{"
`
				scriptPath := filepath.Join(ctx.RootDir, "bad-provider.sh")
				err := fs.WriteString(scriptPath, scriptContent)
				if err != nil {
					return err
				}
				cmd := command.New("chmod", "+x", scriptPath).Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return result.Error
				}

				// Update grove.yml
				groveConfig := fmt.Sprintf(`name: test-project
flow:
  plans_directory: ./plans
  recipes:
    get_recipe_cmd: "%s"
`, scriptPath)
				err = fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig)
				if err != nil {
					return err
				}

				// List recipes - should show warning but not fail
				flow, _ := getFlowBinary()
				cmd = command.New(flow, "plan", "recipes", "list").Dir(ctx.RootDir)
				result = cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("command should not fail with invalid JSON from provider")
				}

				// Should show parse warning
				if !strings.Contains(result.Stderr, "Warning: failed to parse JSON from recipe provider") {
					return fmt.Errorf("expected warning about invalid JSON")
				}

				// Should still work with other recipes
				if !strings.Contains(result.Stdout, "[Built-in]") {
					return fmt.Errorf("should still list built-in recipes")
				}

				return nil
			}),
		},
	}
}