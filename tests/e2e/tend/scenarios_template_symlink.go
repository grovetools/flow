// File: tests/e2e/tend/scenarios_template_symlink.go
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

// TemplateSymlinkWorktreeScenario tests that job templates are symlinked into worktrees.
func TemplateSymlinkWorktreeScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "template-symlink-worktree",
		Description: "Tests that job templates are automatically symlinked into worktrees",
		Tags:        []string{"template", "worktree", "symlink"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with custom templates", func(ctx *harness.Context) error {
				// Initialize git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)

				// Create grove.yml configuration
				groveConfig := `name: test-project
flow:
  plans_directory: ./plans
  oneshot_model: mock
`
				if err := fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig); err != nil {
					return err
				}

				// Create .grove/job-templates directory with a custom template
				templatesDir := filepath.Join(ctx.RootDir, ".grove", "job-templates")
				if err := os.MkdirAll(templatesDir, 0755); err != nil {
					return err
				}

				// Create a custom template
				customTemplate := `---
description: "Custom test template"
type: "oneshot"
---

This is a custom template for testing.
Please analyze the provided code.
`
				if err := fs.WriteString(filepath.Join(templatesDir, "test-template.md"), customTemplate); err != nil {
					return err
				}

				// Create initial commit
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "# Test Project\n")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit with custom template")

				// Setup empty global config in sandboxed environment
				if err := setupEmptyGlobalConfig(ctx); err != nil {
					return err
				}

				return nil
			}),
			harness.NewStep("Initialize plan", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return err
				}
				cmd := ctx.Command(flow, "plan", "init", "template-test").Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to init plan: %w\nStdout: %s\nStderr: %s", result.Error, result.Stdout, result.Stderr)
				}
				return nil
			}),
			harness.NewStep("Add job with custom template and worktree", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				planPath := filepath.Join(ctx.RootDir, "plans", "template-test")

				// Add a job with custom template and worktree
				cmdAdd := ctx.Command(flow, "plan", "add", planPath,
					"--title", "Test Template Job",
					"--type", "oneshot",
					"--template", "test-template",
					"--worktree", "template-test-wt",
					"-p", "Additional instructions for the template").Dir(ctx.RootDir)
				resultAdd := cmdAdd.Run()
				ctx.ShowCommandOutput(cmdAdd.String(), resultAdd.Stdout, resultAdd.Stderr)
				if resultAdd.Error != nil {
					return fmt.Errorf("failed to add job: %w", resultAdd.Error)
				}
				return nil
			}),
			harness.NewStep("Run job to trigger worktree creation", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return fmt.Errorf("failed to get flow binary: %w", err)
				}

				// Set environment for mock LLM
				env := append(os.Environ(),
					"GROVE_MOCK_LLM_RESPONSE_FILE="+filepath.Join(ctx.RootDir, "mock-response.txt"),
					"GROVE_MOCK_LLM_OUTPUT_MODE=plain",
				)

				// Create mock response file
				mockResponse := `# Template Test Results

I can see the template content and the additional instructions.
This is working correctly.
`
				fs.WriteString(filepath.Join(ctx.RootDir, "mock-response.txt"), mockResponse)

				// Run the job to trigger worktree creation
				planDir := filepath.Join(ctx.RootDir, "plans", "template-test")
				cmd := ctx.Command(flow, "plan", "run", "01-test-template-job.md").
					Dir(planDir).
					Env(env...)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return fmt.Errorf("failed to run plan: %w", result.Error)
				}

				return nil
			}),
			harness.NewStep("Verify templates are symlinked in worktree", func(ctx *harness.Context) error {
				// Find the worktree directory
				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "template-test-wt")

				// Check if worktree exists
				if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
					return fmt.Errorf("worktree not found at expected location: %s", worktreePath)
				}

				// Check if .grove directory exists in worktree
				groveDir := filepath.Join(worktreePath, ".grove")
				if _, err := os.Stat(groveDir); os.IsNotExist(err) {
					return fmt.Errorf(".grove directory not found in worktree: %s", groveDir)
				}

				// Check if job-templates symlink exists
				templatesSymlink := filepath.Join(groveDir, "job-templates")
				symlinkInfo, err := os.Lstat(templatesSymlink)
				if err != nil {
					return fmt.Errorf("job-templates symlink not found in worktree: %w", err)
				}

				// Verify it's actually a symlink
				if symlinkInfo.Mode()&os.ModeSymlink == 0 {
					return fmt.Errorf("job-templates is not a symlink")
				}

				// Verify the symlink target
				target, err := os.Readlink(templatesSymlink)
				if err != nil {
					return fmt.Errorf("failed to read symlink target: %w", err)
				}

				// Verify we can access the custom template through the symlink
				customTemplatePath := filepath.Join(templatesSymlink, "test-template.md")
				content, err := os.ReadFile(customTemplatePath)
				if err != nil {
					return fmt.Errorf("failed to read custom template through symlink: %w", err)
				}

				// Verify the content matches
				contentStr := string(content)
				if !strings.Contains(contentStr, "Custom test template") {
					return fmt.Errorf("custom template content not found through symlink:\n%s", contentStr)
				}

				if !strings.Contains(contentStr, "This is a custom template for testing") {
					return fmt.Errorf("custom template body not found through symlink:\n%s", contentStr)
				}

				ctx.ShowCommandOutput("Template symlink verification", 
					fmt.Sprintf("✓ Templates symlinked successfully\n  Symlink: %s\n  Target: %s\n  Template content accessible", templatesSymlink, target), 
					"")

				return nil
			}),
			harness.NewStep("Verify job used template content", func(ctx *harness.Context) error {
				// Check the job file to see if template content was embedded
				jobFilePath := filepath.Join(ctx.RootDir, "plans", "template-test", "01-test-template-job.md")
				jobContent, err := os.ReadFile(jobFilePath)
				if err != nil {
					return fmt.Errorf("failed to read job file: %w", err)
				}

				jobContentStr := string(jobContent)

				// Verify template content was embedded
				if !strings.Contains(jobContentStr, "This is a custom template for testing") {
					return fmt.Errorf("template content not embedded in job file:\n%s", jobContentStr)
				}

				// Verify additional instructions were appended
				if !strings.Contains(jobContentStr, "Additional Instructions") {
					return fmt.Errorf("additional instructions section not found in job file:\n%s", jobContentStr)
				}

				if !strings.Contains(jobContentStr, "Additional instructions for the template") {
					return fmt.Errorf("user prompt not found in job file:\n%s", jobContentStr)
				}

				ctx.ShowCommandOutput("Job content verification", 
					"✓ Template content embedded and additional instructions appended correctly", 
					"")

				return nil
			}),
		},
	}
}