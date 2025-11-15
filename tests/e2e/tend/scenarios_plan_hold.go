// File: tests/e2e/tend/scenarios_plan_hold.go
package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// PlanHoldScenario tests the `flow plan hold` and `unhold` commands and their effects.
func PlanHoldScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-hold-workflow",
		Description: "Tests setting plans to 'hold' status, filtering them from lists, and resuming them.",
		Tags:        []string{"plan", "hold", "status"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with a plan", func(ctx *harness.Context) error {
				// Init git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Setup empty global config in sandboxed environment
				if err := setupEmptyGlobalConfig(ctx); err != nil {
					return err
				}

				// Create grove.yml
				configContent := `name: test-project
flow:
  plans_directory: ./plans
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)

				// Create plan and add a job
				flow, _ := getFlowBinary()
				cmd := ctx.Command(flow, "plan", "init", "hold-test-plan").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to init plan: %w", err)
				}
				cmd = ctx.Command(flow, "plan", "add", "hold-test-plan",
					"--title", "Test Shell Job",
					"--type", "shell",
					"-p", "echo 'This should not run while on hold'").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to add job: %w", err)
				}
				return nil
			}),
			setupTestEnvironment(),
			harness.NewStep("Set plan status to 'hold'", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := ctx.Command(flow, "plan", "hold", "hold-test-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}

				// Verify .grove-plan.yml
				planConfig := filepath.Join(ctx.RootDir, "plans", "hold-test-plan", ".grove-plan.yml")
				content, err := fs.ReadString(planConfig)
				if err != nil {
					return err
				}
				if !strings.Contains(content, "status: hold") {
					return fmt.Errorf("plan config should contain 'status: hold'")
				}
				return nil
			}),
			harness.NewStep("Verify 'plan list' hides the on-hold plan", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := ctx.Command(flow, "plan", "list").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}
				if strings.Contains(result.Stdout, "hold-test-plan") {
					return fmt.Errorf("on-hold plan should be hidden from default list view")
				}
				return nil
			}),
			harness.NewStep("Verify 'plan list --show-hold' shows the plan", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := ctx.Command(flow, "plan", "list", "--show-hold").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}
				if !strings.Contains(result.Stdout, "hold-test-plan") {
					return fmt.Errorf("on-hold plan should be visible with --show-hold flag")
				}
				return nil
			}),
			harness.NewStep("Verify 'plan run' is blocked for on-hold plan", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := ctx.Command(flow, "plan", "run", "hold-test-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error == nil {
					return fmt.Errorf("expected 'plan run' to fail for an on-hold plan")
				}
				if !strings.Contains(result.Stderr, "plan is on hold") {
					return fmt.Errorf("expected error message about plan being on hold")
				}
				return nil
			}),
			harness.NewStep("Remove plan hold status with unhold", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := ctx.Command(flow, "plan", "unhold", "hold-test-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}

				// Verify .grove-plan.yml no longer contains hold status
				planConfig := filepath.Join(ctx.RootDir, "plans", "hold-test-plan", ".grove-plan.yml")
				content, err := fs.ReadString(planConfig)
				if err != nil {
					return err
				}
				if strings.Contains(content, "status: hold") {
					return fmt.Errorf("plan config should not contain 'status: hold' after unhold")
				}
				return nil
			}),
			harness.NewStep("Verify 'plan list' shows the plan again", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := ctx.Command(flow, "plan", "list").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}
				if !strings.Contains(result.Stdout, "hold-test-plan") {
					return fmt.Errorf("un-held plan should be visible in default list view")
				}
				return nil
			}),
			harness.NewStep("Verify 'plan run' now succeeds", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := ctx.Command(flow, "plan", "run", "hold-test-plan", "--yes").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return fmt.Errorf("expected 'plan run' to succeed after unholding the plan")
				}
				if !strings.Contains(result.Stdout, "All jobs completed") {
					return fmt.Errorf("expected all jobs to complete successfully")
				}
				return nil
			}),
		},
	}
}
