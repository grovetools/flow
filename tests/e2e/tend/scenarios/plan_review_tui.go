package scenarios

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/grovetools/tend/pkg/assert"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
)

// PlanReviewNoWorktreeScenario tests that the plan review command works for plans without worktrees
// This verifies the bug fix where review previously required a worktree
var PlanReviewNoWorktreeScenario = harness.NewScenario(
	"plan-review-no-worktree",
	"Verifies that plans without worktrees can be reviewed.",
	[]string{"plan", "review"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment with project", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "review-test-project")
			if err != nil {
				return err
			}

			ctx.Set("project_dir", projectDir)
			ctx.Set("notebooks_root", notebooksRoot)
			return nil
		}),

		harness.NewStep("Create plan WITH worktree", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "plan-with-worktree", "--worktree")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init with worktree failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "review-test-project", "plans", "plan-with-worktree")
			ctx.Set("plan_with_worktree_path", planPath)

			// Verify worktree was created
			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "plan-with-worktree")
			return fs.AssertExists(worktreePath)
		}),

		harness.NewStep("Wait for different modification times", func(ctx *harness.Context) error {
			// Ensure plans have different timestamps for proper ordering
			time.Sleep(1100 * time.Millisecond)
			return nil
		}),

		harness.NewStep("Create plan WITHOUT worktree", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "plan-no-worktree")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init without worktree failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "review-test-project", "plans", "plan-no-worktree")
			ctx.Set("plan_no_worktree_path", planPath)

			// Verify no worktree was created
			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "plan-no-worktree")
			return fs.AssertNotExists(worktreePath)
		}),

		harness.NewStep("Review plan WITHOUT worktree via CLI", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planNoWorktreePath := ctx.GetString("plan_no_worktree_path")

			// This is the key test - reviewing a plan without a worktree should succeed
			cmd := ctx.Bin("plan", "review", "plan-no-worktree")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("review command failed for plan without worktree: %w", err)
			}

			// Verify the plan's status was updated to 'review' in the config file
			planConfigPath := filepath.Join(planNoWorktreePath, ".grove-plan.yml")
			return assert.YAMLField(planConfigPath, "status", "review", "Plan without worktree should be marked for review")
		}),

		harness.NewStep("Review plan WITH worktree via CLI (regression test)", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planWithWorktreePath := ctx.GetString("plan_with_worktree_path")

			// Verify that plans with worktrees can still be reviewed
			cmd := ctx.Bin("plan", "review", "plan-with-worktree")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("review command failed for plan with worktree: %w", err)
			}

			// Verify the plan's status was updated to 'review' in the config file
			planConfigPath := filepath.Join(planWithWorktreePath, ".grove-plan.yml")
			return assert.YAMLField(planConfigPath, "status", "review", "Plan with worktree should be marked for review")
		}),

		harness.NewStep("Verify both plans appear in list", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "list")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Both plans should be visible in the list
			if err := assert.Contains(result.Stdout, "plan-with-worktree", "plan with worktree should be in list"); err != nil {
				return err
			}
			return assert.Contains(result.Stdout, "plan-no-worktree", "plan without worktree should be in list")
		}),
	},
)
