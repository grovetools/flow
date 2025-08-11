// File: tests/e2e/tend/scenarios_plan_step.go
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

// PlanStepCommandScenario tests the interactive flow plan step command
func PlanStepCommandScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-step-command",
		Description: "Tests the interactive plan step command by simulating user input",
		Tags:        []string{"plan", "step", "interactive"},
		Steps: []harness.Step{
			harness.NewStep("Setup git repository and config", func(ctx *harness.Context) error {
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")
				
				// Create grove.yml config
				groveConfig := `name: test-project
flow:
  plans_directory: ./plans
  target_agent_container: fake-container
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig)
				return nil
			}),
			harness.NewStep("Initialize plan and create job chain", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Initialize plan
				cmd := command.New(flow, "plan", "init", "step-plan").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to init plan: %w", err)
				}
				
				// Create a three-job linear dependency chain
				// Job 1: Shell job (no dependencies)
				cmd = command.New(flow, "plan", "add", "step-plan",
					"--title", "Setup Environment",
					"--type", "shell",
					"-p", "echo 'Setting up environment'").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to add job 1: %w", err)
				}
				
				// Job 2: Oneshot job (depends on job 1)
				cmd = command.New(flow, "plan", "add", "step-plan",
					"--title", "Analyze Code",
					"--type", "oneshot",
					"--depends-on", "01-setup-environment.md",
					"-p", "Analyze the codebase").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to add job 2: %w", err)
				}
				
				// Job 3: Agent job (depends on job 2)
				cmd = command.New(flow, "plan", "add", "step-plan",
					"--title", "Implement Feature",
					"--type", "agent",
					"--depends-on", "02-analyze-code.md",
					"-p", "Implement the new feature").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to add job 3: %w", err)
				}
				
				return nil
			}),
			setupTestEnvironment(),
			harness.NewStep("Run first step (Run shell job)", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Run flow plan step and send "R" to run the first job, then "Q" to quit
				cmd := command.New(flow, "plan", "step", "step-plan").Dir(ctx.RootDir)
				cmd.Stdin(strings.NewReader("R\nQ\n"))
				
				// Add test bin directory to PATH if it exists
				binDir := ctx.GetString("test_bin_dir")
				if binDir != "" {
					currentPath := os.Getenv("PATH")
					cmd.Env(fmt.Sprintf("PATH=%s:%s", binDir, currentPath))
				}
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return result.Error
				}
				
				// Verify output shows the job was run
				if !strings.Contains(result.Stdout, "Running job:") {
					return fmt.Errorf("output should indicate job is being run")
				}
				
				// Verify the first job is now completed
				jobPath := filepath.Join(ctx.RootDir, "plans", "step-plan", "01-setup-environment.md")
				content, err := fs.ReadString(jobPath)
				if err != nil {
					return err
				}
				if !strings.Contains(content, "status: completed") {
					return fmt.Errorf("first job should be marked as completed")
				}
				
				return nil
			}),
			harness.NewStep("Skip second job", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Run flow plan step and send "S" to skip the second job, then "Q" to quit
				cmd := command.New(flow, "plan", "step", "step-plan").Dir(ctx.RootDir)
				cmd.Stdin(strings.NewReader("S\nQ\n"))
				
				// Add test bin directory to PATH if it exists
				binDir := ctx.GetString("test_bin_dir")
				if binDir != "" {
					currentPath := os.Getenv("PATH")
					cmd.Env(fmt.Sprintf("PATH=%s:%s", binDir, currentPath))
				}
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return result.Error
				}
				
				// Verify output shows the job was skipped
				if !strings.Contains(result.Stdout, "Skipping job:") {
					return fmt.Errorf("output should indicate job is being skipped")
				}
				
				// Verify the second job is now completed (skipped jobs are marked as completed)
				jobPath := filepath.Join(ctx.RootDir, "plans", "step-plan", "02-analyze-code.md")
				content, err := fs.ReadString(jobPath)
				if err != nil {
					return err
				}
				if !strings.Contains(content, "status: completed") {
					return fmt.Errorf("skipped job should be marked as completed")
				}
				
				return nil
			}),
			harness.NewStep("Verify third job is ready", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Run flow plan step and send "Q" to quit
				cmd := command.New(flow, "plan", "step", "step-plan").Dir(ctx.RootDir)
				cmd.Stdin(strings.NewReader("Q\n"))
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return result.Error
				}
				
				// Verify the third job is listed as runnable
				if !strings.Contains(result.Stdout, "Implement Feature") {
					return fmt.Errorf("third job should be listed as runnable")
				}
				
				// Verify it shows as agent type
				if !strings.Contains(result.Stdout, "agent") {
					return fmt.Errorf("third job should show as agent type")
				}
				
				return nil
			}),
		},
	}
}