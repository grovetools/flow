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

// SimpleOrchestrationScenario tests basic job dependencies using flow plan add
func SimpleOrchestrationScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-simple-orchestration",
		Description: "Test basic job orchestration with dependencies using flow plan add",
		Tags:        []string{"plan", "orchestration"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with git repo", func(ctx *harness.Context) error {
				// Setup git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				// Create initial commit
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")
				
				// Write grove.yml with LLM config
				configContent := `name: test-project
flow:
  plans_directory: ./plans
llm:
  provider: openai
  model: test
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)
				
				// Setup mock LLM
				mockDir := filepath.Join(ctx.RootDir, "mocks")
				fs.CreateDir(mockDir)
				
				mockLLMScript := `#!/bin/bash
echo "Task completed successfully."
`
				mockPath := filepath.Join(mockDir, "llm")
				fs.WriteString(mockPath, mockLLMScript)
				os.Chmod(mockPath, 0755)
				
				// Store the mock directory for later use
				ctx.Set("test_bin_dir", mockDir)
				
				return nil
			}),
			
			harness.NewStep("Initialize new plan", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				cmd := command.New(flow, "plan", "init", "simple-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("plan init failed: %v", result.Error)
				}
				
				planPath := filepath.Join(ctx.RootDir, "plans", "simple-plan")
				if !fs.Exists(planPath) {
					return fmt.Errorf("plan directory should exist")
				}
				
				return nil
			}),
			
			harness.NewStep("Add first job with no dependencies", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				cmd := command.New(flow, "plan", "add", "simple-plan",
					"--title", "Setup Environment",
					"--type", "shell",
					"-p", "echo 'Setting up environment'",
				).Dir(ctx.RootDir)
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("failed to add first job: %v", result.Error)
				}
				
				// Verify job file was created
				jobFile := filepath.Join(ctx.RootDir, "plans", "simple-plan", "01-setup-environment.md")
				if !fs.Exists(jobFile) {
					return fmt.Errorf("job file should exist: %s", jobFile)
				}
				
				// Remove worktree from job file to avoid directory requirement
				content, _ := fs.ReadString(jobFile)
				lines := strings.Split(content, "\n")
				var newLines []string
				for _, line := range lines {
					if !strings.HasPrefix(line, "worktree:") {
						newLines = append(newLines, line)
					}
				}
				fs.WriteString(jobFile, strings.Join(newLines, "\n"))
				
				return nil
			}),
			
			harness.NewStep("Add second job depending on first", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				cmd := command.New(flow, "plan", "add", "simple-plan",
					"--title", "Install Dependencies",
					"--type", "shell",
					"-p", "echo 'Installing dependencies'",
					"--depends-on", "01-setup-environment.md",
				).Dir(ctx.RootDir)
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("failed to add second job: %v", result.Error)
				}
				
				// Remove worktree from job file
				jobFile := filepath.Join(ctx.RootDir, "plans", "simple-plan", "02-install-dependencies.md")
				content, _ := fs.ReadString(jobFile)
				content = strings.ReplaceAll(content, "worktree: simple-plan\n", "")
				fs.WriteString(jobFile, content)
				
				return nil
			}),
			
			harness.NewStep("Add third job depending on second", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				cmd := command.New(flow, "plan", "add", "simple-plan",
					"--title", "Run Tests",
					"--type", "shell",
					"-p", "echo 'Running tests'",
					"--depends-on", "02-install-dependencies.md",
				).Dir(ctx.RootDir)
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("failed to add third job: %v", result.Error)
				}
				
				// Remove worktree from job file
				jobFile := filepath.Join(ctx.RootDir, "plans", "simple-plan", "03-run-tests.md")
				content, _ := fs.ReadString(jobFile)
				content = strings.ReplaceAll(content, "worktree: simple-plan\n", "")
				fs.WriteString(jobFile, content)
				
				return nil
			}),
			
			harness.NewStep("Check plan status", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				cmd := ctx.Command(flow, "plan", "status", "simple-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("plan status failed: %v", result.Error)
				}
				
				// Verify all jobs show up
				output := result.Stdout
				if !strings.Contains(output, "01-setup-environment.md") {
					return fmt.Errorf("status should show setup job")
				}
				if !strings.Contains(output, "02-install-dependencies.md") {
					return fmt.Errorf("status should show install job")
				}
				if !strings.Contains(output, "03-run-tests.md") {
					return fmt.Errorf("status should show test job")
				}
				
				return nil
			}),
			
			harness.NewStep("Try to run job with unmet dependencies", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Try to run job 3 (should fail)
				// First set the active plan
				setCmd := ctx.Command(flow, "plan", "set", "simple-plan").Dir(ctx.RootDir)
				setCmd.Run()
				
				cmd := ctx.Command(flow, "plan", "run", filepath.Join("plans", "simple-plan", "03-run-tests.md")).Dir(ctx.RootDir)
				result := cmd.Run()
				
				// This should fail
				if result.Error == nil {
					return fmt.Errorf("running job with unmet dependencies should fail")
				}
				
				// Verify the error message mentions dependencies
				if !strings.Contains(result.Stderr, "depend") {
					return fmt.Errorf("expected dependency error, got: %s", result.Stderr)
				}
				
				return nil
			}),
			
			harness.NewStep("Run jobs in correct order", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Set active plan
				setCmd := ctx.Command(flow, "plan", "set", "simple-plan").Dir(ctx.RootDir)
				setCmd.Run()
				
				// Run job 1
				cmd := ctx.Command(flow, "plan", "run", filepath.Join("plans", "simple-plan", "01-setup-environment.md")).Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("setup job failed: %v", result.Error)
				}
				
				// Run job 2
				cmd = ctx.Command(flow, "plan", "run", filepath.Join("plans", "simple-plan", "02-install-dependencies.md")).Dir(ctx.RootDir)
				result = cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("install job failed: %v", result.Error)
				}
				
				// Now job 3 should succeed
				cmd = ctx.Command(flow, "plan", "run", filepath.Join("plans", "simple-plan", "03-run-tests.md")).Dir(ctx.RootDir)
				result = cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("test job failed: %v", result.Error)
				}
				
				return nil
			}),
			
			harness.NewStep("Verify all jobs completed", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				cmd := ctx.Command(flow, "plan", "status", "simple-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("final status check failed: %v", result.Error)
				}
				
				// All jobs should be completed
				output := result.Stdout
				// Check for "Completed: 3" in the status
				if !strings.Contains(output, "Completed: 3") {
					return fmt.Errorf("expected all 3 jobs to be completed, status shows: %s", output)
				}
				
				return nil
			}),
		},
	}
}