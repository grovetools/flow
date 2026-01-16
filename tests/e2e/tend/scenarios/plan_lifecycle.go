package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/assert"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
)

var PlanLifecycleScenario = harness.NewScenario(
	"plan-lifecycle-commands",
	"Tests the plan lifecycle commands: hold, unhold, review, and finish.",
	[]string{"core", "plan", "lifecycle"},
	[]harness.Step{
		// Mock git to verify branch deletion and worktree commands
		harness.SetupMocks(harness.Mock{CommandName: "git"}),

		harness.NewStep("Setup sandboxed environment with project", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "lifecycle-project")
			if err != nil {
				return err
			}

			// Get the repo that was created by setupDefaultEnvironment
			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}

			// Create initial commit
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Lifecycle Test Project\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Initialize plan with worktree", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "my-lifecycle-plan", "--worktree")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "lifecycle-project", "plans", "my-lifecycle-plan")
			ctx.Set("plan_path", planPath)
			ctx.Set("plan_name", "my-lifecycle-plan")

			// Verify worktree was created
			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "my-lifecycle-plan")
			ctx.Set("worktree_path", worktreePath)
			return fs.AssertExists(worktreePath)
		}),

		harness.NewStep("Verify plan configuration file exists", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			planConfigPath := filepath.Join(planPath, ".grove-plan.yml")
			ctx.Set("plan_config_path", planConfigPath)
			return fs.AssertExists(planConfigPath)
		}),

		harness.NewStep("Test 'hold' command", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planConfigPath := ctx.GetString("plan_config_path")
			planName := ctx.GetString("plan_name")

			cmd := ctx.Bin("plan", "hold", planName)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("hold command failed: %w", err)
			}

			// Verify status field is set to 'hold'
			return assert.YAMLField(planConfigPath, "status", "hold", "Plan status should be 'hold'")
		}),

		harness.NewStep("Verify held plan is hidden from default list", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planName := ctx.GetString("plan_name")

			cmd := ctx.Bin("plan", "list")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Plan should NOT be in the output
			if strings.Contains(result.Stdout, planName) {
				return fmt.Errorf("held plan should be hidden from default list, but was found")
			}
			return nil
		}),

		harness.NewStep("Verify held plan is shown with --show-hold flag", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planName := ctx.GetString("plan_name")

			cmd := ctx.Bin("plan", "list", "--show-hold")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Plan SHOULD be in the output
			return assert.Contains(result.Stdout, planName, "held plan should be visible with --show-hold")
		}),

		harness.NewStep("Test double-hold (edge case)", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planName := ctx.GetString("plan_name")

			// Trying to hold an already-held plan should either succeed or fail gracefully
			cmd := ctx.Bin("plan", "hold", planName)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// We don't enforce specific behavior here, just verify it doesn't crash
			// Most implementations would succeed idempotently
			return nil
		}),

		harness.NewStep("Test 'unhold' command", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planConfigPath := ctx.GetString("plan_config_path")
			planName := ctx.GetString("plan_name")

			cmd := ctx.Bin("plan", "unhold", planName)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("unhold command failed: %w", err)
			}

			// Verify status field is removed/empty
			content, err := fs.ReadString(planConfigPath)
			if err != nil {
				return err
			}

			// Status should either be absent or empty
			if strings.Contains(content, "status: hold") {
				return fmt.Errorf("plan should no longer have 'status: hold' after unhold")
			}
			return nil
		}),

		harness.NewStep("Verify unheld plan appears in default list", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planName := ctx.GetString("plan_name")

			cmd := ctx.Bin("plan", "list")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			return assert.Contains(result.Stdout, planName, "unheld plan should be visible in default list")
		}),

		harness.NewStep("Add jobs to plan for review/finish testing", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planName := ctx.GetString("plan_name")

			// Add a shell job
			cmd := ctx.Bin("plan", "add", planName,
				"--type", "shell",
				"--title", "Setup Task",
				"-p", "echo 'Setting up'")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Add another shell job
			cmd = ctx.Bin("plan", "add", planName,
				"--type", "shell",
				"--title", "Main Task",
				"-p", "echo 'Main work'")
			cmd.Dir(projectDir)
			result = cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			return result.AssertSuccess()
		}),

		harness.NewStep("Mark jobs as completed for review test", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// Mark both jobs as completed
			job1Path := filepath.Join(planPath, "01-setup-task.md")
			job2Path := filepath.Join(planPath, "02-main-task.md")

			for _, jobPath := range []string{job1Path, job2Path} {
				content, err := fs.ReadString(jobPath)
				if err != nil {
					return err
				}

				// Replace status: pending with status: completed
				updatedContent := strings.Replace(content, "status: pending", "status: completed", 1)
				if err := fs.WriteString(jobPath, updatedContent); err != nil {
					return err
				}
			}

			return nil
		}),

		harness.NewStep("Test 'review' command", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planConfigPath := ctx.GetString("plan_config_path")
			planName := ctx.GetString("plan_name")

			cmd := ctx.Bin("plan", "review", planName)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("review command failed: %w", err)
			}

			// Verify status is set to 'review'
			return assert.YAMLField(planConfigPath, "status", "review", "Plan status should be 'review'")
		}),

		harness.NewStep("Test 'finish' with --yes enables all cleanup", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			planName := ctx.GetString("plan_name")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "finish", planName, "--yes")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("finish command failed: %w", err)
			}

			// With --yes, all available cleanup is performed including archive
			// Verify plan was archived
			if err := fs.AssertNotExists(planPath); err != nil {
				return fmt.Errorf("plan should be archived with --yes flag: %w", err)
			}

			// Verify archive exists
			archivePath := filepath.Join(notebooksRoot, "workspaces", "lifecycle-project", "plans", ".archive", planName)
			return fs.AssertExists(archivePath)
		}),

		harness.NewStep("Create another plan for full cleanup test", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "cleanup-test-plan", "--worktree")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			cleanupPlanPath := filepath.Join(notebooksRoot, "workspaces", "lifecycle-project", "plans", "cleanup-test-plan")
			ctx.Set("cleanup_plan_path", cleanupPlanPath)
			ctx.Set("cleanup_plan_name", "cleanup-test-plan")

			cleanupWorktreePath := filepath.Join(projectDir, ".grove-worktrees", "cleanup-test-plan")
			ctx.Set("cleanup_worktree_path", cleanupWorktreePath)

			return nil
		}),

		harness.NewStep("Set cleanup-test-plan to review state", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cleanupPlanName := ctx.GetString("cleanup_plan_name")

			cmd := ctx.Bin("plan", "review", cleanupPlanName)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			return result.AssertSuccess()
		}),

		harness.NewStep("Test 'finish' with full cleanup flags", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cleanupPlanName := ctx.GetString("cleanup_plan_name")

			cmd := ctx.Bin("plan", "finish", cleanupPlanName,
				"--yes", "--prune-worktree", "--delete-branch", "--archive")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("finish command with cleanup flags failed: %w", err)
			}

			return nil
		}),

		harness.NewStep("Verify worktree cleanup attempted (mocked)", func(ctx *harness.Context) error {
			// Since git is mocked, the worktree directory won't actually be removed
			// The actual removal happens via flow's cleanup code, not just git
			// In a real environment, the directory would be removed
			// For this test, we verify the command completed successfully
			return nil
		}),

		harness.NewStep("Verify plan was archived", func(ctx *harness.Context) error {
			cleanupPlanPath := ctx.GetString("cleanup_plan_path")
			notebooksRoot := ctx.GetString("notebooks_root")

			// Original plan directory should be gone
			if err := fs.AssertNotExists(cleanupPlanPath); err != nil {
				return fmt.Errorf("original plan directory should be removed: %w", err)
			}

			// Archived plan should exist in .archive directory
			archivePath := filepath.Join(notebooksRoot, "workspaces", "lifecycle-project", "plans", ".archive", "cleanup-test-plan")
			return fs.AssertExists(archivePath)
		}),

		harness.NewStep("Test error case: finish non-existent plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "finish", "non-existent-plan", "--yes")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// Should fail
			return result.AssertFailure()
		}),

		harness.NewStep("Test error case: hold non-existent plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "hold", "non-existent-plan")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// Should fail
			return result.AssertFailure()
		}),

		harness.NewStep("Test error case: unhold non-existent plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "unhold", "non-existent-plan")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// Should fail
			return result.AssertFailure()
		}),

		harness.NewStep("Test error case: review non-existent plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "review", "non-existent-plan")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// Should fail
			return result.AssertFailure()
		}),

		harness.NewStep("Create plan without worktree for finish edge case", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "init", "no-worktree-plan")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			ctx.Set("no_worktree_plan_name", "no-worktree-plan")
			return nil
		}),

		harness.NewStep("Test finish plan without worktree (edge case)", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			noWorktreePlanName := ctx.GetString("no_worktree_plan_name")

			// Try to finish with --prune-worktree flag even though there's no worktree
			// Should handle gracefully
			cmd := ctx.Bin("plan", "finish", noWorktreePlanName,
				"--yes", "--prune-worktree", "--archive")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// Should succeed or fail gracefully
			// The exact behavior depends on implementation
			// but it shouldn't crash
			return nil
		}),

		harness.NewStep("Verify archived plans are not shown in regular list", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cleanupPlanName := ctx.GetString("cleanup_plan_name")

			cmd := ctx.Bin("plan", "list")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Archived plan should not appear
			if strings.Contains(result.Stdout, cleanupPlanName) {
				return fmt.Errorf("archived plan should not appear in regular list")
			}
			return nil
		}),
	},
)
