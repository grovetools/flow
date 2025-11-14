// File: tests/e2e/tend/scenarios_status_tui.go
package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/command"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// StatusTUIScenario tests the status command enhancements and TUI flag
func StatusTUIScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-status-enhanced",
		Description: "Tests the enhanced status command with TUI flag validation",
		Tags:        []string{"plan", "status"},
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
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig)
				return nil
			}),
			harness.NewStep("Initialize plan with test jobs", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Initialize plan
				cmd := command.New(flow, "plan", "init", "tui-test").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to init plan: %w", err)
				}
				
				// Create a job hierarchy with different statuses
				// Job 1: Completed root job
				cmd = command.New(flow, "plan", "add", "tui-test",
					"--title", "Setup",
					"--type", "shell",
					"-p", "echo 'Setup complete'").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to add job 1: %w", err)
				}
				
				// Manually mark job 1 as completed
				job1Path := filepath.Join(ctx.RootDir, "plans", "tui-test", "01-setup.md")
				content, _ := fs.ReadString(job1Path)
				content = strings.Replace(content, "status: pending", "status: completed", 1)
				fs.WriteString(job1Path, content)
				
				// Job 2: Pending job depending on job 1
				cmd = command.New(flow, "plan", "add", "tui-test",
					"--title", "Build",
					"--type", "shell",
					"--depends-on", "01-setup.md",
					"-p", "echo 'Building'").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to add job 2: %w", err)
				}
				
				// Job 3: Running job depending on job 1
				cmd = command.New(flow, "plan", "add", "tui-test",
					"--title", "Test",
					"--type", "shell",
					"--depends-on", "01-setup.md",
					"-p", "echo 'Testing'").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to add job 3: %w", err)
				}
				
				// Manually mark job 3 as running
				job3Path := filepath.Join(ctx.RootDir, "plans", "tui-test", "03-test.md")
				content, _ = fs.ReadString(job3Path)
				content = strings.Replace(content, "status: pending", "status: running", 1)
				fs.WriteString(job3Path, content)
				
				// Job 4: Failed job depending on job 2
				cmd = command.New(flow, "plan", "add", "tui-test",
					"--title", "Deploy",
					"--type", "shell",
					"--depends-on", "02-build.md",
					"-p", "echo 'Deploying'").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to add job 4: %w", err)
				}
				
				// Manually mark job 4 as failed
				job4Path := filepath.Join(ctx.RootDir, "plans", "tui-test", "04-deploy.md")
				content, _ = fs.ReadString(job4Path)
				content = strings.Replace(content, "status: pending", "status: failed", 1)
				fs.WriteString(job4Path, content)
				
				return nil
			}),
			harness.NewStep("Test status command shows job hierarchy", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()

				// Run regular status command
				cmd := command.New(flow, "plan", "status", "tui-test").Dir(ctx.RootDir)
				// Skip PID checks for test environment (fake jobs have no running process)
				cmd.Env("GROVE_SKIP_PID_CHECK=true")
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return fmt.Errorf("status command failed: %w", result.Error)
				}

				// Verify output contains expected elements
				output := result.Stdout

				// 1. Check plan name appears
				if !strings.Contains(output, "tui-test") {
					return fmt.Errorf("output should contain plan name 'tui-test'")
				}

				// 2. Check all job names appear
				jobs := []string{"01-setup.md", "02-build.md", "03-test.md", "04-deploy.md"}
				for _, job := range jobs {
					if !strings.Contains(output, job) {
						return fmt.Errorf("output should contain job %s", job)
					}
				}

				// 3. Check status indicators appear (text or icons)
				// We should see indicators for: completed, running, failed, pending
				statusCount := 0
				if strings.Contains(output, "Completed") || strings.Contains(output, "completed") {
					statusCount++
				}
				if strings.Contains(output, "Running") || strings.Contains(output, "running") {
					statusCount++
				}
				if strings.Contains(output, "Failed") || strings.Contains(output, "failed") {
					statusCount++
				}
				if strings.Contains(output, "Pending") || strings.Contains(output, "pending") {
					statusCount++
				}

				if statusCount < 3 {
					return fmt.Errorf("output should show status information for different job states")
				}

				// 4. Verify command succeeded (already checked above via result.Error)

				return nil
			}),
			harness.NewStep("Test TUI flag is recognized", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Try to run with TUI flag - it will fail due to no TTY, but should recognize the flag
				cmd := command.New(flow, "plan", "status", "tui-test", "--tui").Dir(ctx.RootDir)
				result := cmd.Run()
				
				// We expect it to fail with TTY error, not flag error
				if result.Error == nil {
					return fmt.Errorf("expected TTY error")
				}
				
				if !strings.Contains(result.Stderr, "could not open a new TTY") {
					return fmt.Errorf("expected TTY error message, got: %s", result.Stderr)
				}
				
				return nil
			}),
			harness.NewStep("Test verbose status output", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Run status with verbose flag
				cmd := command.New(flow, "plan", "status", "tui-test", "-v").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("verbose status command failed: %w", result.Error)
				}
				
				// Verbose output should include job titles in parentheses
				output := result.Stdout
				if !strings.Contains(output, "(Setup)") {
					return fmt.Errorf("verbose output should include job titles")
				}
				
				return nil
			}),
		},
	}
}