// File: tests/e2e/tend/scenarios_plan_recipes.go
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

// PlanRecipesScenario tests the `flow plan recipes` and `flow plan init --recipe` commands.
func PlanRecipesScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-recipes",
		Description: "Tests the creation of plans from recipes and listing available recipes.",
		Tags:        []string{"plan", "recipes", "init"},
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
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig)
				return nil
			}),
			harness.NewStep("List available recipes", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "recipes", "list").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}
				if !strings.Contains(result.Stdout, "standard-feature") {
					return fmt.Errorf("list output should contain the 'standard-feature' recipe")
				}
				if !strings.Contains(result.Stdout, "A standard workflow: spec -> implement -> review.") {
					return fmt.Errorf("list output should contain the recipe description")
				}
				return nil
			}),
			harness.NewStep("List recipes as JSON", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "recipes", "list", "--json").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}
				var recipes []map[string]interface{}
				if err := json.Unmarshal([]byte(result.Stdout), &recipes); err != nil {
					return fmt.Errorf("failed to parse JSON output: %w", err)
				}
				if len(recipes) == 0 {
					return fmt.Errorf("expected at least one recipe in JSON output")
				}
				// Check that standard-feature recipe exists in the list
				found := false
				for _, recipe := range recipes {
					if recipe["name"] == "standard-feature" {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("expected 'standard-feature' recipe to be in the list")
				}
				return nil
			}),
			harness.NewStep("Initialize plan from recipe", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "init", "my-feature-plan", "--recipe", "standard-feature").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}
				if !strings.Contains(result.Stdout, "Using recipe: standard-feature") {
					return fmt.Errorf("output should confirm which recipe is being used")
				}
				return nil
			}),
			harness.NewStep("Verify generated files and their content", func(ctx *harness.Context) error {
				planDir := filepath.Join(ctx.RootDir, "plans", "my-feature-plan")
				expectedFiles := []string{"01-spec.md", "02-implement.md", "03-git-changes.md", "04-git-status.md", "05-review.md"}

				for _, file := range expectedFiles {
					path := filepath.Join(planDir, file)
					if !fs.Exists(path) {
						return fmt.Errorf("expected recipe file '%s' was not created", file)
					}
					content, err := fs.ReadString(path)
					if err != nil {
						return err
					}
					if !strings.Contains(content, "my-feature-plan") {
						return fmt.Errorf("file '%s' did not have its PlanName template variable replaced", file)
					}
				}

				// Verify implement job
				implementContent, _ := fs.ReadString(filepath.Join(planDir, "02-implement.md"))
				if !strings.Contains(implementContent, "worktree: my-feature-plan") {
					return fmt.Errorf("implement job did not have its worktree templated correctly")
				}

				// Verify shell jobs
				gitChangesContent, _ := fs.ReadString(filepath.Join(planDir, "03-git-changes.md"))
				if !strings.Contains(gitChangesContent, "type: shell") {
					return fmt.Errorf("git-changes job should be a shell type")
				}
				if !strings.Contains(gitChangesContent, "git diff --name-status main...HEAD") {
					return fmt.Errorf("git-changes job should contain git diff command with main...HEAD")
				}

				gitStatusContent, _ := fs.ReadString(filepath.Join(planDir, "04-git-status.md"))
				if !strings.Contains(gitStatusContent, "type: shell") {
					return fmt.Errorf("git-status job should be a shell type")
				}
				if !strings.Contains(gitStatusContent, "git status --porcelain") {
					return fmt.Errorf("git-status job should contain git status command")
				}

				// Verify review job dependencies
				reviewContent, _ := fs.ReadString(filepath.Join(planDir, "05-review.md"))
				if !strings.Contains(reviewContent, "03-git-changes.md") {
					return fmt.Errorf("review job should depend on git-changes job")
				}
				if !strings.Contains(reviewContent, "04-git-status.md") {
					return fmt.Errorf("review job should depend on git-status job")
				}
				
				return nil
			}),
			harness.NewStep("Verify .grove-plan.yml was created correctly", func(ctx *harness.Context) error {
				planConfigPath := filepath.Join(ctx.RootDir, "plans", "my-feature-plan", ".grove-plan.yml")
				if !fs.Exists(planConfigPath) {
					return fmt.Errorf(".grove-plan.yml was not created")
				}
				content, err := fs.ReadString(planConfigPath)
				if err != nil {
					return err
				}
				if !strings.Contains(content, "worktree: my-feature-plan") {
					return fmt.Errorf("plan config should have worktree auto-detected from the recipe")
				}
				return nil
			}),
			harness.NewStep("Verify status of the new plan", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "status", "my-feature-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}
				if !strings.Contains(result.Stdout, "Jobs: 5 total") {
					return fmt.Errorf("status should show 5 total jobs, got: %s", result.Stdout)
				}
				if !strings.Contains(result.Stdout, "Pending: 5") {
					return fmt.Errorf("all 5 jobs should be pending")
				}
				return nil
			}),
		},
	}
}