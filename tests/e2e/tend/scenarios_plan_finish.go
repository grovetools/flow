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

// PlanFinishScenario tests the `flow plan finish` command
func PlanFinishScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-finish",
		Description: "Tests the interactive and flag-based plan cleanup workflow",
		Tags:        []string{"plan", "finish", "cleanup"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with a plan and worktree", func(ctx *harness.Context) error {
				// Init git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Create grove.yml
				configContent := `name: test-project
flow:
  plans_directory: ./plans
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)

				// Create plan and set its worktree
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "init", "finish-test", "--with-worktree").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to init plan: %w", err)
				}

				// Manually create the worktree and branch to simulate a real scenario
				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "finish-test")
				git.CreateWorktree(ctx.RootDir, "finish-test", worktreePath)

				return nil
			}),
			setupTestEnvironment(),
			harness.NewStep("Create tmux session marker", func(ctx *harness.Context) error {
				// Simply create the marker file that the mock tmux will check for
				sessionName := "finish-test"
				markerFile := fmt.Sprintf("/tmp/tmux_session_%s", sessionName)
				if err := fs.WriteString(markerFile, "active"); err != nil {
					return fmt.Errorf("failed to create tmux session marker: %w", err)
				}
				return nil
			}),
			harness.NewStep("Test finish command with --yes flag (select all)", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				// Use --yes flag to automatically select all available actions
				cmd := ctx.Command(flow, "plan", "finish", "finish-test", "--yes").Dir(ctx.RootDir)

				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return result.Error
				}

				// Verify key actions were performed
				output := result.Stdout
				expectedMessages := []string{
					"Mark plan as finished",
					"Performing selected actions",
					"Plan cleanup finished",
				}
				
				for _, msg := range expectedMessages {
					if !strings.Contains(output, msg) {
						return fmt.Errorf("expected message '%s' not found in output", msg)
					}
				}

				return nil
			}),
			harness.NewStep("Verify plan state after finish", func(ctx *harness.Context) error {
				// Verify .grove-plan.yml is marked as finished
				configPath := filepath.Join(ctx.RootDir, "plans", "finish-test", ".grove-plan.yml")
				content, _ := fs.ReadString(configPath)
				if !strings.Contains(content, "status: finished") {
					return fmt.Errorf(".grove-plan.yml was not marked as finished")
				}

				// Verify worktree is gone
				result := command.New("git", "worktree", "list").Dir(ctx.RootDir).Run()
				if strings.Contains(result.Stdout, "finish-test") {
					return fmt.Errorf("worktree should have been removed")
				}

				return nil
			}),
		},
	}
}

// PlanFinishFlagsScenario tests the flag-based usage of plan finish
func PlanFinishFlagsScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-finish-flags",
		Description: "Tests plan finish with specific flags",
		Tags:        []string{"plan", "finish", "cleanup", "flags"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with a plan and worktree", func(ctx *harness.Context) error {
				// Init git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Create grove.yml
				configContent := `name: test-project
flow:
  plans_directory: ./plans
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)

				// Create plan with worktree
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "init", "flags-test", "--with-worktree").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to init plan: %w", err)
				}

				// Create worktree
				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "flags-test")
				git.CreateWorktree(ctx.RootDir, "flags-test", worktreePath)

				return nil
			}),
			setupTestEnvironment(),
			harness.NewStep("Test finish with --prune-worktree flag", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Run with specific flags
				cmd := ctx.Command(flow, "plan", "finish", "flags-test", "--prune-worktree").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return result.Error
				}

				// Verify only selected actions were performed
				output := result.Stdout
				if !strings.Contains(output, "Mark plan as finished") {
					return fmt.Errorf("plan status should always be updated")
				}
				if !strings.Contains(output, "Prune git worktree") {
					return fmt.Errorf("worktree prune action not found")
				}
				
				// Verify other actions were not performed
				if strings.Contains(output, "Archive plan directory") && strings.Contains(output, "Done") {
					return fmt.Errorf("archive action should not have been performed")
				}

				return nil
			}),
			harness.NewStep("Test finish with --yes flag", func(ctx *harness.Context) error {
				// Create another plan for this test
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "init", "yes-test", "--with-worktree").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to init plan: %w", err)
				}

				// Create worktree
				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "yes-test")
				git.CreateWorktree(ctx.RootDir, "yes-test", worktreePath)

				// Run with --yes flag
				cmd = ctx.Command(flow, "plan", "finish", "yes-test", "--yes").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return result.Error
				}

				// Verify no interactive prompts
				output := result.Stdout
				if strings.Contains(output, "Select cleanup actions") {
					return fmt.Errorf("should not show interactive prompt with --yes flag")
				}
				
				// Verify actions were performed
				if !strings.Contains(output, "Performing selected actions") {
					return fmt.Errorf("actions should be performed automatically with --yes")
				}

				return nil
			}),
		},
	}
}

// PlanFinishDevLinksScenario tests the dev links cleanup functionality
func PlanFinishDevLinksScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-finish-devlinks",
		Description: "Tests plan finish with development binary links cleanup",
		Tags:        []string{"plan", "finish", "cleanup", "devlinks"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with plan and dev links", func(ctx *harness.Context) error {
				// Init git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Create grove.yml
				configContent := `name: test-project
flow:
  plans_directory: ./plans
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)

				// Create plan
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "init", "devlinks-test", "--with-worktree").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to init plan: %w", err)
				}

				// Create worktree
				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "devlinks-test")
				git.CreateWorktree(ctx.RootDir, "devlinks-test", worktreePath)

				return nil
			}),
			setupTestEnvironment(),
			harness.NewStep("Test finish with --clean-dev-links flag", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// First test that we can at least run the command with the flag
				cmd := ctx.Command(flow, "plan", "finish", "devlinks-test", "--clean-dev-links").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return result.Error
				}

				// The command should have completed successfully
				output := result.Stdout
				if !strings.Contains(output, "Plan cleanup finished") {
					return fmt.Errorf("command did not complete successfully")
				}
				
				// At minimum, the plan should be marked as finished
				if !strings.Contains(output, "Mark plan as finished") {
					return fmt.Errorf("plan status was not updated")
				}

				return nil
			}),
			harness.NewStep("Test finish with specific dev links flag", func(ctx *harness.Context) error {
				// Create another plan
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "init", "interactive-devlinks", "--with-worktree").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to init plan: %w", err)
				}

				// Create worktree
				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "interactive-devlinks")
				git.CreateWorktree(ctx.RootDir, "interactive-devlinks", worktreePath)

				// Use the --clean-dev-links flag specifically
				cmd = ctx.Command(flow, "plan", "finish", "interactive-devlinks", "--clean-dev-links").Dir(ctx.RootDir)
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return result.Error
				}

				// Verify the command completed successfully
				output := result.Stdout
				if !strings.Contains(output, "Plan cleanup finished") {
					return fmt.Errorf("command did not complete successfully")
				}
				
				// The plan should be marked as finished
				if !strings.Contains(output, "Mark plan as finished") {
					return fmt.Errorf("plan status was not updated")
				}

				return nil
			}),
		},
	}
}