package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/command"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// PlanInitImprovementsScenario tests the new features for plan init:
// 1. Empty plans with .grove-plan.yml show in list
// 2. --worktree flag with and without values
func PlanInitImprovementsScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-init-improvements",
		Description: "Tests plan init improvements: empty plan listing and --worktree flag variations",
		Tags:        []string{"plan", "init"},
		Steps: []harness.Step{
			harness.NewStep("Setup git repository and config", func(ctx *harness.Context) error {
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")
				
				// Create grove.yml with oneshot_model
				groveConfig := `name: test-project
flow:
  plans_directory: ./plans
  oneshot_model: gemini-2.5-pro
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig)
				return nil
			}),
			
			harness.NewStep("Test plan init with --worktree flag (no value, auto mode)", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return fmt.Errorf("failed to get flow binary: %w", err)
				}
				
				// Initialize plan with --worktree (no value)
				cmd := command.New(flow, "plan", "init", "test-auto-worktree", "--worktree").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return fmt.Errorf("flow plan init failed: %w", result.Error)
				}
				
				// Verify output shows plan was set as active
				if !strings.Contains(result.Stdout, "Set active plan to: test-auto-worktree") {
					return fmt.Errorf("expected init to set active plan, output:\n%s", result.Stdout)
				}
				
				// Verify .grove-plan.yml was created with correct content
				planConfigPath := filepath.Join(ctx.RootDir, "plans", "test-auto-worktree", ".grove-plan.yml")
				content, err := os.ReadFile(planConfigPath)
				if err != nil {
					return fmt.Errorf("failed to read .grove-plan.yml: %w", err)
				}
				
				// Check that worktree matches plan name (auto mode)
				if !strings.Contains(string(content), "worktree: test-auto-worktree") {
					return fmt.Errorf("expected worktree to be auto-set to 'test-auto-worktree', got:\n%s", content)
				}
				
				return nil
			}),
			
			harness.NewStep("Test plan init with --worktree=custom-name flag", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return fmt.Errorf("failed to get flow binary: %w", err)
				}
				
				// Initialize plan with --worktree=custom-name
				cmd := command.New(flow, "plan", "init", "test-custom-worktree", "--worktree=my-custom-wt").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return fmt.Errorf("flow plan init failed: %w", result.Error)
				}
				
				// Verify .grove-plan.yml was created with custom worktree
				planConfigPath := filepath.Join(ctx.RootDir, "plans", "test-custom-worktree", ".grove-plan.yml")
				content, err := os.ReadFile(planConfigPath)
				if err != nil {
					return fmt.Errorf("failed to read .grove-plan.yml: %w", err)
				}
				
				// Check that worktree is set to custom value
				if !strings.Contains(string(content), "worktree: my-custom-wt") {
					return fmt.Errorf("expected worktree to be set to 'my-custom-wt', got:\n%s", content)
				}
				
				return nil
			}),
			
			harness.NewStep("Test empty plan appears in list", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return fmt.Errorf("failed to get flow binary: %w", err)
				}
				
				// Create another empty plan without --with-worktree
				cmd := command.New(flow, "plan", "init", "empty-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("flow plan init failed: %w", result.Error)
				}
				
				// Verify this plan is now active
				if !strings.Contains(result.Stdout, "Set active plan to: empty-plan") {
					return fmt.Errorf("expected init to set active plan to 'empty-plan', output:\n%s", result.Stdout)
				}
				
				// List plans
				cmd = command.New(flow, "plan", "list").Dir(ctx.RootDir)
				result = cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return fmt.Errorf("flow plan list failed: %w", result.Error)
				}
				
				// Check that all created plans appear in the list
				if !strings.Contains(result.Stdout, "test-auto-worktree") {
					return fmt.Errorf("expected 'test-auto-worktree' to appear in plan list, got:\n%s", result.Stdout)
				}
				if !strings.Contains(result.Stdout, "test-custom-worktree") {
					return fmt.Errorf("expected 'test-custom-worktree' to appear in plan list, got:\n%s", result.Stdout)
				}
				if !strings.Contains(result.Stdout, "empty-plan") {
					return fmt.Errorf("expected 'empty-plan' to appear in plan list, got:\n%s", result.Stdout)
				}
				
				// All should show 0 jobs
				lines := strings.Split(result.Stdout, "\n")
				foundEmptyPlanWithZeroJobs := false
				foundAutoWorktreeWithZeroJobs := false
				foundCustomWorktreeWithZeroJobs := false
				
				for _, line := range lines {
					if strings.Contains(line, "empty-plan") && strings.Contains(line, "0") {
						foundEmptyPlanWithZeroJobs = true
					}
					if strings.Contains(line, "test-auto-worktree") && strings.Contains(line, "0") {
						foundAutoWorktreeWithZeroJobs = true
					}
					if strings.Contains(line, "test-custom-worktree") && strings.Contains(line, "0") {
						foundCustomWorktreeWithZeroJobs = true
					}
				}
				
				if !foundEmptyPlanWithZeroJobs {
					return fmt.Errorf("expected 'empty-plan' to show 0 jobs in list")
				}
				if !foundAutoWorktreeWithZeroJobs {
					return fmt.Errorf("expected 'test-auto-worktree' to show 0 jobs in list")
				}
				if !foundCustomWorktreeWithZeroJobs {
					return fmt.Errorf("expected 'test-custom-worktree' to show 0 jobs in list")
				}
				
				return nil
			}),
			
			harness.NewStep("Test model override with --model flag", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return fmt.Errorf("failed to get flow binary: %w", err)
				}
				
				// Initialize plan with explicit model override
				cmd := command.New(flow, "plan", "init", "model-override-plan", "--model", "claude-3-5-sonnet-20241022").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return fmt.Errorf("flow plan init failed: %w", result.Error)
				}
				
				// Verify model was overridden
				planConfigPath := filepath.Join(ctx.RootDir, "plans", "model-override-plan", ".grove-plan.yml")
				content, err := os.ReadFile(planConfigPath)
				if err != nil {
					return fmt.Errorf("failed to read .grove-plan.yml: %w", err)
				}
				
				if !strings.Contains(string(content), "model: claude-3-5-sonnet-20241022") {
					return fmt.Errorf("expected model to be overridden to 'claude-3-5-sonnet-20241022', got:\n%s", content)
				}
				
				return nil
			}),
			
			harness.NewStep("Test plan init with --extract-all-from and --worktree", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return fmt.Errorf("failed to get flow binary: %w", err)
				}
				
				// Create a source markdown file with jobs to extract
				sourceFile := filepath.Join(ctx.RootDir, "test-jobs.md")
				sourceContent := `# Test Jobs Document

<!-- grove: {"id": "job1"} -->
## Job 1

This is the first job.

---

<!-- grove: {"id": "job2"} -->
## Job 2

This is the second job.
`
				if err := os.WriteFile(sourceFile, []byte(sourceContent), 0644); err != nil {
					return fmt.Errorf("failed to create source file: %w", err)
				}
				
				// Initialize plan with --extract-all-from and --worktree (auto mode)
				cmd := command.New(flow, "plan", "init", "extract-worktree-test", 
					"--extract-all-from", sourceFile,
					"--worktree").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return fmt.Errorf("flow plan init with extract failed: %w", result.Error)
				}
				
				// Verify plan was created with correct name
				if !strings.Contains(result.Stdout, "Set active plan to: extract-worktree-test") {
					return fmt.Errorf("expected plan name to be 'extract-worktree-test', output:\n%s", result.Stdout)
				}
				
				// Verify .grove-plan.yml has correct worktree
				planConfigPath := filepath.Join(ctx.RootDir, "plans", "extract-worktree-test", ".grove-plan.yml")
				content, err := os.ReadFile(planConfigPath)
				if err != nil {
					return fmt.Errorf("failed to read .grove-plan.yml: %w", err)
				}
				
				// Check that worktree matches plan name (base name)
				if !strings.Contains(string(content), "worktree: extract-worktree-test") {
					return fmt.Errorf("expected worktree to be set to 'extract-worktree-test', got:\n%s", content)
				}
				
				// Verify jobs were extracted
				planDir := filepath.Join(ctx.RootDir, "plans", "extract-worktree-test")
				files, err := os.ReadDir(planDir)
				if err != nil {
					return fmt.Errorf("failed to read plan directory: %w", err)
				}
				
				// Should have .grove-plan.yml and at least one extracted job file
				if len(files) < 2 {
					return fmt.Errorf("expected at least 2 files (config + extracted job), got %d", len(files))
				}
				
				// Check extraction output mentions extraction
				if !strings.Contains(result.Stdout, "Extracted content") {
					return fmt.Errorf("expected extraction message in output:\n%s", result.Stdout)
				}
				
				// Verify the extracted job has worktree in its frontmatter
				var extractedJobPath string
				for _, file := range files {
					if file.Name() != ".grove-plan.yml" {
						extractedJobPath = filepath.Join(planDir, file.Name())
						break
					}
				}
				
				if extractedJobPath == "" {
					return fmt.Errorf("no extracted job file found")
				}
				
				jobContent, err := os.ReadFile(extractedJobPath)
				if err != nil {
					return fmt.Errorf("failed to read extracted job file: %w", err)
				}
				
				// Check that the job frontmatter includes the worktree
				if !strings.Contains(string(jobContent), "worktree: extract-worktree-test") {
					return fmt.Errorf("expected extracted job to have worktree in frontmatter, got:\n%s", jobContent)
				}
				
				return nil
			}),
			
			harness.NewStep("Test plan init with path argument and --worktree", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return fmt.Errorf("failed to get flow binary: %w", err)
				}
				
				// Create a subdirectory for plans
				subDir := filepath.Join(ctx.RootDir, "subplans")
				if err := os.MkdirAll(subDir, 0755); err != nil {
					return fmt.Errorf("failed to create subdirectory: %w", err)
				}
				
				// Initialize plan with a path argument that includes directory separators
				cmd := command.New(flow, "plan", "init", "subplans/path-test-plan", "--worktree").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return fmt.Errorf("flow plan init with path failed: %w", result.Error)
				}
				
				// Verify plan name is the base name, not the full path
				if !strings.Contains(result.Stdout, "Set active plan to: path-test-plan") {
					return fmt.Errorf("expected plan name to be 'path-test-plan', output:\n%s", result.Stdout)
				}
				
				// Verify .grove-plan.yml has correct worktree (should be base name)
				planConfigPath := filepath.Join(ctx.RootDir, "plans", "subplans", "path-test-plan", ".grove-plan.yml")
				content, err := os.ReadFile(planConfigPath)
				if err != nil {
					return fmt.Errorf("failed to read .grove-plan.yml: %w", err)
				}
				
				// Check that worktree matches the base name of the path
				if !strings.Contains(string(content), "worktree: path-test-plan") {
					return fmt.Errorf("expected worktree to be set to 'path-test-plan' (base name), got:\n%s", content)
				}
				
				return nil
			}),
			
			harness.NewStep("Test plan init with --recipe and --extract-all-from together", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return fmt.Errorf("failed to get flow binary: %w", err)
				}
				
				// Create a source file for extraction
				sourceFile := filepath.Join(ctx.RootDir, "spec.md")
				sourceContent := `# Feature Specification

This is the specification for my new feature.

## Requirements

- Must be fast
- Must be reliable
- Must be secure
`
				if err := os.WriteFile(sourceFile, []byte(sourceContent), 0644); err != nil {
					return fmt.Errorf("failed to create source file: %w", err)
				}
				
				// Initialize plan with both recipe and extraction
				cmd := command.New(flow, "plan", "init", "recipe-with-extract",
					"--recipe", "standard-feature",
					"--extract-all-from", sourceFile,
					"--worktree").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return fmt.Errorf("flow plan init with recipe and extract failed: %w", result.Error)
				}
				
				// Verify output mentions both recipe and extraction
				if !strings.Contains(result.Stdout, "Using recipe: standard-feature") {
					return fmt.Errorf("expected recipe usage in output:\n%s", result.Stdout)
				}
				if !strings.Contains(result.Stdout, "Extracted content from") {
					return fmt.Errorf("expected extraction message in output:\n%s", result.Stdout)
				}
				
				// Verify files were created
				planDir := filepath.Join(ctx.RootDir, "plans", "recipe-with-extract")
				files, err := os.ReadDir(planDir)
				if err != nil {
					return fmt.Errorf("failed to read plan directory: %w", err)
				}
				
				// Should have: .grove-plan.yml + 5 recipe jobs (standard-feature has 5 jobs total) = 6 files
				expectedFiles := 6
				if len(files) != expectedFiles {
					var fileNames []string
					for _, f := range files {
						fileNames = append(fileNames, f.Name())
					}
					return fmt.Errorf("expected exactly %d files, got %d: %v", expectedFiles, len(files), fileNames)
				}
				
				// Check that 01-spec.md exists and contains merged content
				specPath := filepath.Join(planDir, "01-spec.md")
				specContent, err := os.ReadFile(specPath)
				if err != nil {
					return fmt.Errorf("failed to read spec file: %w", err)
				}
				
				// Verify it has recipe's frontmatter
				if !strings.Contains(string(specContent), "id: spec") {
					return fmt.Errorf("spec should have recipe's id: spec")
				}
				
				// Verify spec has worktree in frontmatter
				if !strings.Contains(string(specContent), "worktree: recipe-with-extract") {
					return fmt.Errorf("expected spec to have worktree, got:\n%s", specContent)
				}
				
				// Verify spec has the extracted content as body
				if !strings.Contains(string(specContent), "Feature Specification") {
					return fmt.Errorf("expected spec to contain extracted content")
				}
				
				// Verify implementation file exists (02-implement.md from the recipe)
				implementPath := filepath.Join(planDir, "02-implement.md")
				if _, err := os.Stat(implementPath); err != nil {
					return fmt.Errorf("expected 02-implement.md from recipe to exist: %w", err)
				}
				
				// Verify .grove-plan.yml has worktree set
				configPath := filepath.Join(planDir, ".grove-plan.yml")
				configContent, err := os.ReadFile(configPath)
				if err != nil {
					return fmt.Errorf("failed to read plan config: %w", err)
				}
				if !strings.Contains(string(configContent), "worktree: recipe-with-extract") {
					return fmt.Errorf("expected plan config to have worktree set")
				}
				
				return nil
			}),
		},
	}
}

// PlanInitContextRulesScenario tests that default context rules are applied on plan init.
func PlanInitContextRulesScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-init-context-rules",
		Description: "Tests that default context rules are applied to new worktrees during plan init",
		Tags:        []string{"plan", "init", "context", "rules"},
		Steps: []harness.Step{
			// Step 1: Setup project with default context rules defined in grove.yml
			harness.NewStep("Setup project with default context rules in grove.yml", func(ctx *harness.Context) error {
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Create grove.yml with context.default_rules_path
				groveConfig := `name: test-project
flow:
  plans_directory: ./plans
context:
  default_rules_path: .grove/default.rules
`
				if err := fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig); err != nil {
					return err
				}

				// Create the default rules file that grove.yml points to
				defaultRulesContent := `# Default rules from grove.yml
*.go
!*_test.go
`
				return fs.WriteString(filepath.Join(ctx.RootDir, ".grove", "default.rules"), defaultRulesContent)
			}),

			// Step 2: Initialize a plan with a worktree
			harness.NewStep("Initialize plan with worktree", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "init", "context-rules-test", "--worktree").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return fmt.Errorf("plan init failed: %w", result.Error)
				}

				// Verify the output mentions applying rules
				if !strings.Contains(result.Stdout, "Applied default context rules to:") {
					return fmt.Errorf("expected output to mention applying default context rules")
				}
				return nil
			}),

			// Step 3: Verify the worktree contains the correct default rules
			harness.NewStep("Verify worktree contains default rules", func(ctx *harness.Context) error {
				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "context-rules-test")
				rulesPath := filepath.Join(worktreePath, ".grove", "rules")

				if !fs.Exists(rulesPath) {
					return fmt.Errorf("expected .grove/rules to be created in the worktree")
				}

				content, err := fs.ReadString(rulesPath)
				if err != nil {
					return fmt.Errorf("failed to read worktree rules file: %w", err)
				}

				if !strings.Contains(content, "# Default rules from grove.yml") {
					return fmt.Errorf("worktree rules file does not match the default rules content")
				}
				if !strings.Contains(content, "*.go") || !strings.Contains(content, "!*_test.go") {
					return fmt.Errorf("worktree rules file is missing expected patterns")
				}
				return nil
			}),

			// Step 4: Test boilerplate fallback when default_rules_path is not configured
			harness.NewStep("Modify grove.yml to remove default rules config", func(ctx *harness.Context) error {
				groveConfig := `name: test-project
flow:
  plans_directory: ./plans
`
				return fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig)
			}),

			harness.NewStep("Initialize a new plan for boilerplate test", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "init", "boilerplate-rules-test", "--worktree").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return fmt.Errorf("plan init failed for boilerplate test: %w", result.Error)
				}

				// Verify output mentions applying rules
				if !strings.Contains(result.Stdout, "Applied default context rules to:") {
					return fmt.Errorf("expected output to mention applying default context rules")
				}
				return nil
			}),

			harness.NewStep("Verify worktree contains boilerplate rules", func(ctx *harness.Context) error {
				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "boilerplate-rules-test")
				rulesPath := filepath.Join(worktreePath, ".grove", "rules")

				if !fs.Exists(rulesPath) {
					return fmt.Errorf("expected .grove/rules to be created in the worktree")
				}

				content, err := fs.ReadString(rulesPath)
				if err != nil {
					return fmt.Errorf("failed to read worktree rules file: %w", err)
				}

				// Check for boilerplate content (should include "*" pattern)
				if !strings.Contains(content, "*") {
					return fmt.Errorf("worktree rules file missing boilerplate pattern, got:\n%s", content)
				}
				return nil
			}),
		},
	}
}