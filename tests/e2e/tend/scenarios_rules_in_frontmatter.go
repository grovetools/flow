package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/assert"
	"github.com/mattsolo1/grove-tend/pkg/command"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// RulesInFrontmatterScenario tests job-specific context rules via frontmatter
func RulesInFrontmatterScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-rules-in-frontmatter",
		Description: "Test job-specific context rules via rules_file in frontmatter",
		Tags:        []string{"plan", "context", "rules"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with git repo", func(ctx *harness.Context) error {
				// Setup git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				
				// Create test files for context
				testDir := filepath.Join(ctx.RootDir, "src")
				fs.CreateDir(testDir)
				fs.WriteString(filepath.Join(testDir, "main.go"), "package main\n// Main file")
				fs.WriteString(filepath.Join(testDir, "helper.go"), "package main\n// Helper file")
				fs.WriteString(filepath.Join(testDir, "test.go"), "package main\n// Test file")
				
				docsDir := filepath.Join(ctx.RootDir, "docs")
				fs.CreateDir(docsDir)
				fs.WriteString(filepath.Join(docsDir, "readme.md"), "# Documentation")
				fs.WriteString(filepath.Join(docsDir, "guide.md"), "# Guide")
				
				// Create initial commit
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")
				
				// Write grove.yml with a mock LLM model
				configContent := `name: test-project
flow:
  plans_directory: ./plans
  oneshot_model: mock
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)
				
				// Create mock response file for the real mock-llm binary to use
				mockResponseContent := "Task completed successfully."
				fs.WriteString(filepath.Join(ctx.RootDir, "mock-response.txt"), mockResponseContent)
				
				// Setup mock mocks directory
				mockDir := filepath.Join(ctx.RootDir, "mocks")
				fs.CreateDir(mockDir)
				
				// Setup mock cx that supports required operations
				mockCxScript := `#!/bin/bash
case "$1" in
    "generate")
        # Create empty context files
        mkdir -p .grove
        touch .grove/context
        touch .grove/context-files
        echo "Context generated"
        ;;
    "reset")
        # Create default rules
        mkdir -p .grove
        echo "# Default rules" > .grove/rules
        echo "src/*.go" >> .grove/rules
        echo "Context reset to defaults"
        ;;
    *)
        echo "cx mock: $@"
        ;;
esac
exit 0
`
				mockCxPath := filepath.Join(mockDir, "cx")
				fs.WriteString(mockCxPath, mockCxScript)
				os.Chmod(mockCxPath, 0755)
				
				return nil
			}),
			
			harness.NewStep("Initialize new plan", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				cmd := command.New(flow, "plan", "init", "test-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("plan init failed: %v", result.Error)
				}
				
				planPath := filepath.Join(ctx.RootDir, "plans", "test-plan")
				if !fs.Exists(planPath) {
					return fmt.Errorf("plan directory should exist")
				}
				
				return nil
			}),
			
			harness.NewStep("Create custom rules files for different jobs", func(ctx *harness.Context) error {
				planPath := filepath.Join(ctx.RootDir, "plans", "test-plan")
				
				// Create a rules file that includes only Go files
				goRulesContent := `# Go files only
src/*.go
`
				fs.WriteString(filepath.Join(planPath, "go-only.rules"), goRulesContent)
				
				// Create a rules file that includes only docs
				docsRulesContent := `# Documentation files only
docs/*.md
`
				fs.WriteString(filepath.Join(planPath, "docs-only.rules"), docsRulesContent)
				
				// Create a rules file that includes everything
				allRulesContent := `# All files
src/*.go
docs/*.md
`
				fs.WriteString(filepath.Join(planPath, "all-files.rules"), allRulesContent)
				
				return nil
			}),
			
			harness.NewStep("Add job with custom Go-only rules", func(ctx *harness.Context) error {
				planPath := filepath.Join(ctx.RootDir, "plans", "test-plan")
				
				// Create job with rules_file pointing to go-only.rules
				jobContent := `---
id: go-analysis
title: "Analyze Go files"
type: oneshot
status: pending
rules_file: go-only.rules
---

Please analyze the Go source files in this project.
`
				jobPath := filepath.Join(planPath, "01-go-analysis.md")
				fs.WriteString(jobPath, jobContent)
				
				return nil
			}),
			
			harness.NewStep("Add job with custom docs-only rules", func(ctx *harness.Context) error {
				planPath := filepath.Join(ctx.RootDir, "plans", "test-plan")
				
				// Create job with rules_file pointing to docs-only.rules
				jobContent := `---
id: docs-review
title: "Review documentation"
type: oneshot
status: pending
rules_file: docs-only.rules
---

Please review the documentation files in this project.
`
				jobPath := filepath.Join(planPath, "02-docs-review.md")
				fs.WriteString(jobPath, jobContent)
				
				return nil
			}),
			
			harness.NewStep("Add job without custom rules (uses default)", func(ctx *harness.Context) error {
				planPath := filepath.Join(ctx.RootDir, "plans", "test-plan")
				
				// Create job without rules_file (should use default .grove/rules if present)
				jobContent := `---
id: general-task
title: "General task"
type: oneshot
status: pending
---

Please perform a general analysis of the project.
`
				jobPath := filepath.Join(planPath, "03-general-task.md")
				fs.WriteString(jobPath, jobContent)
				
				return nil
			}),
			
			harness.NewStep("Run job with Go-only rules", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				cmd := command.New(flow, "plan", "run", filepath.Join("plans", "test-plan", "01-go-analysis.md"), "-y").
					Dir(ctx.RootDir)
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("failed to run job with Go-only rules: %v", result.Error)
				}
				
				// Verify that job used the custom rules file
				if !strings.Contains(result.Stdout, "Using job-specific context from") ||
				   !strings.Contains(result.Stdout, "go-only.rules") {
					return fmt.Errorf("job should have used go-only.rules file")
				}
				
				// Verify job completed successfully
				if !strings.Contains(result.Stdout, "✓ Job completed: Analyze Go files") {
					return fmt.Errorf("job should have completed successfully")
				}
				
				return nil
			}),
			
			harness.NewStep("Run job with docs-only rules", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				cmd := command.New(flow, "plan", "run", filepath.Join("plans", "test-plan", "02-docs-review.md"), "-y").
					Dir(ctx.RootDir)
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("failed to run job with docs-only rules: %v", result.Error)
				}
				
				// Verify that job used the custom rules file
				if !strings.Contains(result.Stdout, "Using job-specific context from") ||
				   !strings.Contains(result.Stdout, "docs-only.rules") {
					return fmt.Errorf("job should have used docs-only.rules file")
				}
				
				// Verify job completed successfully
				if !strings.Contains(result.Stdout, "✓ Job completed: Review documentation") {
					return fmt.Errorf("job should have completed successfully")
				}
				
				return nil
			}),
			
			harness.NewStep("Run job without custom rules", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// First create a default .grove/rules file
				groveDir := filepath.Join(ctx.RootDir, ".grove")
				fs.CreateDir(groveDir)
				defaultRules := `# Default rules
README.md
`
				fs.WriteString(filepath.Join(groveDir, "rules"), defaultRules)
				
				cmd := command.New(flow, "plan", "run", filepath.Join("plans", "test-plan", "03-general-task.md"), "-y").
					Dir(ctx.RootDir)
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("failed to run job without custom rules: %v", result.Error)
				}
				
				// Verify that job did NOT use a custom rules file
				if strings.Contains(result.Stdout, "Using job-specific context from") {
					return fmt.Errorf("job should not have used a custom rules file")
				}
				
				// Verify job completed successfully
				if !strings.Contains(result.Stdout, "✓ Job completed: General task") {
					return fmt.Errorf("job should have completed successfully")
				}
				
				return nil
			}),
			
			harness.NewStep("Verify job status after runs", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				cmd := command.New(flow, "plan", "status", "test-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("plan status failed: %v", result.Error)
				}
				
				// All three jobs should be completed
				return assert.Contains(result.Stdout, "Completed: 3")
			}),
			
			harness.NewStep("Test rules file fallback to git root", func(ctx *harness.Context) error {
				// Create a rules file in the git root
				gitRootRulesContent := `# Root-level rules
*.go
*.md
`
				fs.WriteString(filepath.Join(ctx.RootDir, "root-level.rules"), gitRootRulesContent)
				
				planPath := filepath.Join(ctx.RootDir, "plans", "test-plan")
				
				// Create job that references rules file not in plan dir but in git root
				jobContent := `---
id: fallback-test
title: "Test fallback resolution"
type: oneshot
status: pending
rules_file: root-level.rules
---

Please test the fallback resolution for rules files.
`
				jobPath := filepath.Join(planPath, "04-fallback-test.md")
				fs.WriteString(jobPath, jobContent)
				
				flow, _ := getFlowBinary()
				
				cmd := command.New(flow, "plan", "run", filepath.Join("plans", "test-plan", "04-fallback-test.md"), "-y").
					Dir(ctx.RootDir)
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("failed to run job with git-root rules: %v", result.Error)
				}
				
				// Verify that job found and used the rules file from git root
				if !strings.Contains(result.Stdout, "Using job-specific context from") ||
				   !strings.Contains(result.Stdout, "root-level.rules") {
					return fmt.Errorf("job should have found root-level.rules file in git root")
				}
				
				// Verify job completed successfully
				if !strings.Contains(result.Stdout, "✓ Job completed: Test fallback resolution") {
					return fmt.Errorf("job should have completed successfully")
				}
				
				return nil
			}),
			
			harness.NewStep("Test rules file fallback with subdirectory path", func(ctx *harness.Context) error {
				// Create a docs directory in git root with a rules file
				docsDir := filepath.Join(ctx.RootDir, "docs")
				fs.CreateDir(docsDir)
				
				docsRulesContent := `# Documentation-specific rules
*.md
README.*
docs/**
`
				fs.WriteString(filepath.Join(docsDir, "root-level.rules"), docsRulesContent)
				
				planPath := filepath.Join(ctx.RootDir, "plans", "test-plan")
				
				// Create job that references rules file with subdirectory path
				jobContent := `---
id: subdirectory-fallback-test
title: "Test subdirectory fallback resolution"
type: oneshot
status: pending
rules_file: docs/root-level.rules
---

Please test the subdirectory fallback resolution for rules files.
`
				jobPath := filepath.Join(planPath, "05-subdirectory-fallback-test.md")
				fs.WriteString(jobPath, jobContent)
				
				flow, _ := getFlowBinary()
				
				cmd := command.New(flow, "plan", "run", filepath.Join("plans", "test-plan", "05-subdirectory-fallback-test.md"), "-y").
					Dir(ctx.RootDir)
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("failed to run job with subdirectory rules: %v", result.Error)
				}
				
				// Verify that job found and used the rules file from git root/docs
				if !strings.Contains(result.Stdout, "Using job-specific context from") ||
				   !strings.Contains(result.Stdout, "docs/root-level.rules") {
					return fmt.Errorf("job should have found docs/root-level.rules file in git root")
				}
				
				// Verify job completed successfully
				if !strings.Contains(result.Stdout, "✓ Job completed: Test subdirectory fallback resolution") {
					return fmt.Errorf("job should have completed successfully")
				}
				
				return nil
			}),
		},
	}
}