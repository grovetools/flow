package scenarios

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mattsolo1/grove-tend/pkg/assert"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/harness"
	"github.com/mattsolo1/grove-tend/pkg/tui"
	"github.com/mattsolo1/grove-tend/pkg/verify"
)

// PlanReviewTUIActionScenario tests the plan review TUI action (pressing 'r')
// Specifically verifies that pressing 'r' works for plans without worktrees
var PlanReviewTUIActionScenario = harness.NewScenarioWithOptions(
	"plan-review-tui-action",
	"Verifies that the 'r' key in plan TUI works for plans without worktrees.",
	[]string{"tui", "plan", "review"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment with project", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "review-tui-action-project")
			if err != nil {
				return err
			}

			ctx.Set("project_dir", projectDir)
			ctx.Set("notebooks_root", notebooksRoot)
			return nil
		}),

		harness.NewStep("Create plan WITH worktree for comparison", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "plan-with-worktree", "--worktree")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init with worktree failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "review-tui-action-project", "plans", "plan-with-worktree")
			ctx.Set("plan_with_worktree_path", planPath)

			// Verify worktree was created
			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "plan-with-worktree")
			return fs.AssertExists(worktreePath)
		}),

		harness.NewStep("Wait for different modification times", func(ctx *harness.Context) error {
			// Ensure plans have different timestamps for proper ordering in TUI
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

			planPath := filepath.Join(notebooksRoot, "workspaces", "review-tui-action-project", "plans", "plan-no-worktree")
			ctx.Set("plan_no_worktree_path", planPath)

			// Verify no worktree was created
			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "plan-no-worktree")
			return fs.AssertNotExists(worktreePath)
		}),

		harness.NewStep("Launch plan TUI and verify both plans visible", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			flowBinary, err := findFlowBinary()
			if err != nil {
				return err
			}

			// Create a wrapper script that changes to the project directory before running flow
			wrapperScript := filepath.Join(ctx.RootDir, "run-plan-tui")
			scriptContent := fmt.Sprintf("#!/bin/bash\ncd %s\nexec %s plan tui\n", projectDir, flowBinary)
			if err := fs.WriteString(wrapperScript, scriptContent); err != nil {
				return fmt.Errorf("failed to create wrapper script: %w", err)
			}
			if err := os.Chmod(wrapperScript, 0755); err != nil {
				return fmt.Errorf("failed to make wrapper script executable: %w", err)
			}

			session, err := ctx.StartTUI(wrapperScript, []string{})
			if err != nil {
				return fmt.Errorf("failed to start `flow plan tui`: %w", err)
			}
			ctx.Set("tui_session", session)

			// Wait for the TUI to load by looking for the PLAN header
			if err := session.WaitForText("PLAN", 10*time.Second); err != nil {
				content, _ := session.Capture()
				return fmt.Errorf("TUI did not load: %w\nContent:\n%s", err, content)
			}

			// Verify both plans are visible
			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("plan-with-worktree is visible", nil, session.AssertContains("plan-with-worktree"))
				v.Equal("plan-no-worktree is visible", nil, session.AssertContains("plan-no-worktree"))
			})
		}),

		harness.NewStep("Press 'r' on plan WITHOUT worktree and verify no error", func(ctx *harness.Context) error {
			session := ctx.Get("tui_session").(*tui.Session)

			// The most recently created plan (plan-no-worktree) should be at the top/selected
			// Press 'r' to trigger the review action
			if err := session.SendKeys("r"); err != nil {
				return fmt.Errorf("failed to send 'r' key: %w", err)
			}

			// Wait for the external review command to complete
			time.Sleep(2 * time.Second)
			if err := session.WaitStable(); err != nil {
				return err
			}

			// Capture the screen content after review
			content, err := session.Capture()
			if err != nil {
				return fmt.Errorf("failed to capture screen: %w", err)
			}

			// The critical test: verify the old error message is NOT displayed
			// Before the fix, this would show: "No worktree to review for this plan."
			// This verifies the bug fix - pressing 'r' works for plans without worktrees
			return assert.NotContains(content, "No worktree to review for this plan.",
				"should not show worktree requirement error")
		}),

		harness.NewStep("Navigate to plan WITH worktree and press 'r' (regression)", func(ctx *harness.Context) error {
			session := ctx.Get("tui_session").(*tui.Session)

			// Press Escape first to ensure we're back to the list if any subprocess was started
			session.SendKeys("Escape")
			time.Sleep(300 * time.Millisecond)

			// Navigate down to plan-with-worktree (second item in the list)
			if err := session.SendKeys("Down"); err != nil {
				return fmt.Errorf("failed to send 'Down' key: %w", err)
			}

			// Wait for UI to update
			if err := session.WaitStable(); err != nil {
				return err
			}

			// Press 'r' to trigger review
			if err := session.SendKeys("r"); err != nil {
				return fmt.Errorf("failed to send 'r' key: %w", err)
			}

			// Wait for action to complete
			time.Sleep(500 * time.Millisecond)
			if err := session.WaitStable(); err != nil {
				return err
			}

			// Verify no error message for the plan with worktree either
			content, _ := session.Capture()
			return assert.NotContains(content, "No worktree to review for this plan.",
				"should not show error for plan with worktree")
		}),

		harness.NewStep("Quit the TUI", func(ctx *harness.Context) error {
			session := ctx.Get("tui_session").(*tui.Session)

			// Press 'q' to quit
			if err := session.SendKeys("q"); err != nil {
				return fmt.Errorf("failed to send 'q' key: %w", err)
			}

			// Give it a moment to exit
			time.Sleep(500 * time.Millisecond)
			return nil
		}),
	},
	true,  // localOnly = true, as it requires tmux
	false, // explicitOnly = false
)
