// File: tests/e2e/tend/scenarios_template_symlink_from_main.go
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

// TemplateSymlinkFromMainScenario tests that job templates are symlinked when running from main project directory.
func TemplateSymlinkFromMainScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "template-symlink-from-main",
		Description: "Tests that job templates are symlinked when running flow plan run from main project directory",
		Tags:        []string{"template", "worktree", "symlink", "oneshot"},
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

				// Create .grove/job-templates directory with custom templates
				templatesDir := filepath.Join(ctx.RootDir, ".grove", "job-templates")
				if err := os.MkdirAll(templatesDir, 0755); err != nil {
					return err
				}

				// Create multiple custom templates like in the real scenario
				templates := map[string]string{
					"chef.md": `---
description: "Chef template"
type: "oneshot"
---

You are a chef. Please analyze the provided code and suggest improvements.
`,
					"cook.md": `---
description: "Cook template"
type: "oneshot"
---

You are a cook. Please prepare the code as requested.
`,
					"critic.md": `---
description: "Critic template"
type: "oneshot"
---

You are a critic. Please review the provided code critically.
`,
				}

				for name, content := range templates {
					if err := fs.WriteString(filepath.Join(templatesDir, name), content); err != nil {
						return err
					}
				}

				// Create initial commit
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "# Test Project\n")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit with custom templates")

				return nil
			}),
			harness.NewStep("Initialize plan with worktree", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return err
				}
				cmd := command.New(flow, "plan", "init", "ramen").Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to init plan: %w\nStdout: %s\nStderr: %s", result.Error, result.Stdout, result.Stderr)
				}
				return nil
			}),
			harness.NewStep("Add multiple jobs with custom templates and worktree", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				planPath := filepath.Join(ctx.RootDir, "plans", "ramen")

				// Add multiple jobs like in the real scenario
				jobs := []struct {
					title    string
					template string
					prompt   string
				}{
					{"Chef v1", "chef", "First chef analysis"},
					{"Cook v1", "cook", "First cooking task"},
					{"Critic v1", "critic", "First critical review"},
					{"Chef v2", "chef", "Second chef analysis"},
					{"Cook v2", "cook", "Second cooking task"},
					{"Critic v2", "critic", "Second critical review"},
					{"Chef v3", "chef", "Third chef analysis"},
				}

				for i, job := range jobs {
					cmdAdd := command.New(flow, "plan", "add", planPath,
						"--title", job.title,
						"--type", "oneshot",
						"--template", job.template,
						"--worktree", "ramen",
						"-p", job.prompt).Dir(ctx.RootDir)
					resultAdd := cmdAdd.Run()
					if i == 0 {
						ctx.ShowCommandOutput(cmdAdd.String(), resultAdd.Stdout, resultAdd.Stderr)
					}
					if resultAdd.Error != nil {
						return fmt.Errorf("failed to add job %s: %w", job.title, resultAdd.Error)
					}
				}

				// Mark first 6 jobs as completed to simulate the real scenario
				planDir := filepath.Join(ctx.RootDir, "plans", "ramen")
				for i := 1; i <= 6; i++ {
					jobFile := fmt.Sprintf("%02d-*.md", i)
					matches, _ := filepath.Glob(filepath.Join(planDir, jobFile))
					if len(matches) > 0 {
						content, _ := os.ReadFile(matches[0])
						contentStr := string(content)
						contentStr = strings.Replace(contentStr, "status: pending", "status: completed", 1)
						fs.WriteString(matches[0], contentStr)
					}
				}

				return nil
			}),
			harness.NewStep("Set active plan", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "set", "ramen").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return fmt.Errorf("failed to set active plan: %w", result.Error)
				}
				return nil
			}),
			harness.NewStep("Run plan from main directory (simulating the exact scenario)", func(ctx *harness.Context) error {
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
				mockResponse := `# Chef v3 Analysis

I have analyzed the code as a chef and here are my suggestions.
This response confirms the template was accessible.
`
				fs.WriteString(filepath.Join(ctx.RootDir, "mock-response.txt"), mockResponse)

				// Run from the main project directory (NOT from within the worktree)
				// This simulates exactly what the user is doing
				cmd := command.New(flow, "plan", "run", "--yes").
					Dir(ctx.RootDir). // Running from main directory
					Env(env...)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return fmt.Errorf("failed to run plan: %w", result.Error)
				}

				// Check for debug output to verify our code is running
				if !strings.Contains(result.Stdout, "DEBUG: ONESHOT EXECUTOR STARTED") &&
					!strings.Contains(result.Stdout, "DEBUG: CREATING SYMLINKS DIRECTLY") {
					ctx.ShowCommandOutput("Missing debug output", 
						"Expected to see DEBUG output from oneshot executor", 
						"This suggests the executor might not be running our modified code")
				}

				return nil
			}),
			harness.NewStep("Verify templates are symlinked in worktree", func(ctx *harness.Context) error {
				// Find the worktree directory
				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "ramen")

				// Check if worktree exists
				if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
					return fmt.Errorf("worktree not found at expected location: %s", worktreePath)
				}

				// Check if .grove directory exists in worktree
				groveDir := filepath.Join(worktreePath, ".grove")
				if _, err := os.Stat(groveDir); os.IsNotExist(err) {
					// List what's in the worktree for debugging
					entries, _ := os.ReadDir(worktreePath)
					var found []string
					for _, e := range entries {
						found = append(found, e.Name())
					}
					return fmt.Errorf(".grove directory not found in worktree: %s\nFound: %v", groveDir, found)
				}

				// Check if job-templates symlink exists
				templatesSymlink := filepath.Join(groveDir, "job-templates")
				symlinkInfo, err := os.Lstat(templatesSymlink)
				if err != nil {
					// List what's in .grove for debugging
					entries, _ := os.ReadDir(groveDir)
					var found []string
					for _, e := range entries {
						found = append(found, e.Name())
					}
					return fmt.Errorf("job-templates symlink not found in worktree .grove: %w\nFound in .grove: %v", err, found)
				}

				// Verify it's actually a symlink
				if symlinkInfo.Mode()&os.ModeSymlink == 0 {
					return fmt.Errorf("job-templates is not a symlink, mode: %v", symlinkInfo.Mode())
				}

				// Verify the symlink target
				target, err := os.Readlink(templatesSymlink)
				if err != nil {
					return fmt.Errorf("failed to read symlink target: %w", err)
				}

				// Verify we can access all three templates through the symlink
				for _, templateName := range []string{"chef.md", "cook.md", "critic.md"} {
					templatePath := filepath.Join(templatesSymlink, templateName)
					content, err := os.ReadFile(templatePath)
					if err != nil {
						return fmt.Errorf("failed to read %s through symlink: %w", templateName, err)
					}

					contentStr := string(content)
					expectedContent := strings.Title(templateName[:len(templateName)-3]) + " template"
					if !strings.Contains(contentStr, expectedContent) {
						return fmt.Errorf("%s content not correct through symlink:\n%s", templateName, contentStr)
					}
				}

				ctx.ShowCommandOutput("Template symlink verification", 
					fmt.Sprintf("✓ All templates symlinked successfully\n  Symlink: %s\n  Target: %s\n  All templates accessible (chef.md, cook.md, critic.md)", templatesSymlink, target), 
					"")

				return nil
			}),
			harness.NewStep("Verify job completed with template content", func(ctx *harness.Context) error {
				// Check the 7th job file (chef-v3) to verify it completed
				jobFilePath := filepath.Join(ctx.RootDir, "plans", "ramen", "07-chef-v3.md")
				jobContent, err := os.ReadFile(jobFilePath)
				if err != nil {
					return fmt.Errorf("failed to read job file: %w", err)
				}

				jobContentStr := string(jobContent)

				// Verify job completed
				if !strings.Contains(jobContentStr, "status: completed") {
					return fmt.Errorf("job did not complete successfully, still shows as: %s", jobContentStr)
				}

				// Verify the job used the chef template
				if !strings.Contains(jobContentStr, "You are a chef") {
					return fmt.Errorf("chef template content not found in job file")
				}

				ctx.ShowCommandOutput("Job completion verification", 
					"✓ Job completed successfully using the chef template", 
					"")

				return nil
			}),
		},
	}
}