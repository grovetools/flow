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
// 2. --with-worktree flag auto-sets worktree name
func PlanInitImprovementsScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-init-improvements",
		Description: "Tests plan init improvements: empty plan listing and --with-worktree flag",
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
			
			harness.NewStep("Test plan init with --with-worktree flag and auto-activation", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return fmt.Errorf("failed to get flow binary: %w", err)
				}
				
				// Initialize plan with --with-worktree
				cmd := command.New(flow, "plan", "init", "test-worktree-plan", "--with-worktree").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return fmt.Errorf("flow plan init failed: %w", result.Error)
				}
				
				// Verify output shows plan was set as active
				if !strings.Contains(result.Stdout, "Set active plan to: test-worktree-plan") {
					return fmt.Errorf("expected init to set active plan, output:\n%s", result.Stdout)
				}
				
				// Verify .grove-plan.yml was created with correct content
				planConfigPath := filepath.Join(ctx.RootDir, "plans", "test-worktree-plan", ".grove-plan.yml")
				content, err := os.ReadFile(planConfigPath)
				if err != nil {
					return fmt.Errorf("failed to read .grove-plan.yml: %w", err)
				}
				
				// Check that worktree matches plan name
				if !strings.Contains(string(content), "worktree: test-worktree-plan") {
					return fmt.Errorf("expected worktree to be set to 'test-worktree-plan', got:\n%s", content)
				}
				
				// Check that model is commented out (not automatically set from grove.yml)
				if !strings.Contains(string(content), "# model: gemini-2.5-pro") {
					return fmt.Errorf("expected model to be commented out, got:\n%s", content)
				}
				
				// Verify plan is set as active using plan current
				cmd = command.New(flow, "plan", "current").Dir(ctx.RootDir)
				result = cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("flow plan current failed: %w", result.Error)
				}
				if !strings.Contains(result.Stdout, "test-worktree-plan") {
					return fmt.Errorf("expected current plan to be 'test-worktree-plan', got:\n%s", result.Stdout)
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
				
				// Check that both plans appear in the list
				if !strings.Contains(result.Stdout, "test-worktree-plan") {
					return fmt.Errorf("expected 'test-worktree-plan' to appear in plan list, got:\n%s", result.Stdout)
				}
				if !strings.Contains(result.Stdout, "empty-plan") {
					return fmt.Errorf("expected 'empty-plan' to appear in plan list, got:\n%s", result.Stdout)
				}
				
				// Both should show 0 jobs
				lines := strings.Split(result.Stdout, "\n")
				foundEmptyPlanWithZeroJobs := false
				foundWorktreePlanWithZeroJobs := false
				
				for _, line := range lines {
					if strings.Contains(line, "empty-plan") && strings.Contains(line, "0") {
						foundEmptyPlanWithZeroJobs = true
					}
					if strings.Contains(line, "test-worktree-plan") && strings.Contains(line, "0") {
						foundWorktreePlanWithZeroJobs = true
					}
				}
				
				if !foundEmptyPlanWithZeroJobs {
					return fmt.Errorf("expected 'empty-plan' to show 0 jobs in list")
				}
				if !foundWorktreePlanWithZeroJobs {
					return fmt.Errorf("expected 'test-worktree-plan' to show 0 jobs in list")
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
		},
	}
}