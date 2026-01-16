package scenarios

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/assert"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
)

var PlanMergeUpdateWorktreeScenario = harness.NewScenarioWithOptions(
	"plan-merge-update-worktree",
	"Tests 'flow plan merge-worktree' and 'flow plan update-worktree' commands.",
	[]string{"core", "plan", "worktree", "git"},
	[]harness.Step{
		// We don't mock git for this test as we need to verify real rebase/merge operations.
		harness.SetupMocks(
			harness.Mock{CommandName: "claude"},
			harness.Mock{CommandName: "tmux"},
		),

		harness.NewStep("Setup sandboxed environment with a git repo", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "merge-update-project")
			if err != nil {
				return err
			}

			// Use a real git repo for this test
			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			ctx.Set("repo", repo)

			// Create initial commit
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Merge/Update Test Project\n"); err != nil {
				return err
			}
			return repo.AddCommit("Initial commit")
		}),

		harness.NewStep("Create plan-a and commit a change", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Bin("plan", "init", "plan-a", "--worktree")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}

			// Commit a change in the worktree
			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "plan-a")
			if err := fs.WriteString(filepath.Join(worktreePath, "file-a.txt"), "content from plan a"); err != nil {
				return err
			}
			// Create a git instance pointing at the worktree
			worktreeGit := git.New(worktreePath)
			return worktreeGit.AddCommit("feat: add file-a")
		}),

		harness.NewStep("Create plan-b and commit a change", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Bin("plan", "init", "plan-b", "--worktree")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}

			// Commit a change in the worktree
			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "plan-b")
			if err := fs.WriteString(filepath.Join(worktreePath, "file-b.txt"), "content from plan b"); err != nil {
				return err
			}
			// Create a git instance pointing at the worktree
			worktreeGit := git.New(worktreePath)
			return worktreeGit.AddCommit("feat: add file-b")
		}),

		harness.NewStep("Merge plan-a into main", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Bin("plan", "merge-worktree", "plan-a")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Verify main branch has the commit from plan-a
			gitCmd := exec.Command("git", "log", "main", "--oneline")
			gitCmd.Dir = projectDir
			output, err := gitCmd.Output()
			if err != nil {
				return fmt.Errorf("failed to get git log: %w", err)
			}
			return assert.Contains(string(output), "feat: add file-a", "main branch should contain commit from plan-a")
		}),

		harness.NewStep("Update plan-b from main", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Bin("plan", "update-worktree", "plan-b")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Verify plan-b branch is now ahead of main and contains plan-a's commit
			gitCmd := exec.Command("git", "log", "plan-b", "--oneline")
			gitCmd.Dir = projectDir
			output, err := gitCmd.Output()
			if err != nil {
				return fmt.Errorf("failed to get git log: %w", err)
			}
			log := string(output)
			if err := assert.Contains(log, "feat: add file-a"); err != nil {
				return fmt.Errorf("plan-b branch should have been rebased onto main: %w", err)
			}
			if !strings.Contains(log, "feat: add file-b") {
				return fmt.Errorf("plan-b branch is missing its own commit after rebase")
			}
			return nil
		}),

		harness.NewStep("Merge updated plan-b into main", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Bin("plan", "merge-worktree", "plan-b")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			return result.AssertSuccess()
		}),

		harness.NewStep("Set plans to review status for cleanup", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			if err := ctx.Bin("plan", "review", "plan-a").Dir(projectDir).Run().AssertSuccess(); err != nil {
				return err
			}
			return ctx.Bin("plan", "review", "plan-b").Dir(projectDir).Run().AssertSuccess()
		}),

		harness.NewStep("Finish both plans and verify cleanup", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmdA := ctx.Bin("plan", "finish", "plan-a", "--yes", "--prune-worktree", "--delete-branch", "--archive")
			cmdA.Dir(projectDir)
			if err := cmdA.Run().AssertSuccess(); err != nil {
				return fmt.Errorf("failed to finish plan-a: %w", err)
			}

			cmdB := ctx.Bin("plan", "finish", "plan-b", "--yes", "--prune-worktree", "--delete-branch", "--archive")
			cmdB.Dir(projectDir)
			if err := cmdB.Run().AssertSuccess(); err != nil {
				return fmt.Errorf("failed to finish plan-b: %w", err)
			}

			// Verify worktrees are gone
			worktreeAPath := filepath.Join(projectDir, ".grove-worktrees", "plan-a")
			if err := fs.AssertNotExists(worktreeAPath); err != nil {
				return fmt.Errorf("worktree plan-a should be removed: %w", err)
			}
			worktreeBPath := filepath.Join(projectDir, ".grove-worktrees", "plan-b")
			if err := fs.AssertNotExists(worktreeBPath); err != nil {
				return fmt.Errorf("worktree plan-b should be removed: %w", err)
			}

			// Verify branches are gone
			branchCmd := exec.Command("git", "branch", "--list")
			branchCmd.Dir = projectDir
			output, err := branchCmd.Output()
			if err != nil {
				return fmt.Errorf("failed to list branches: %w", err)
			}
			branches := string(output)
			if strings.Contains(branches, "plan-a") {
				return fmt.Errorf("branch plan-a should be deleted")
			}
			if strings.Contains(branches, "plan-b") {
				return fmt.Errorf("branch plan-b should be deleted")
			}
			return nil
		}),

		harness.NewStep("Verify final git history", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			gitCmd := exec.Command("git", "log", "main", "--oneline")
			gitCmd.Dir = projectDir
			output, err := gitCmd.Output()
			if err != nil {
				return fmt.Errorf("failed to get git log: %w", err)
			}
			log := string(output)

			if err := assert.Contains(log, "feat: add file-a"); err != nil {
				return err
			}
			if err := assert.Contains(log, "feat: add file-b"); err != nil {
				return err
			}

			// Check order: file-b commit should be on top
			indexA := strings.Index(log, "feat: add file-a")
			indexB := strings.Index(log, "feat: add file-b")
			if indexB > indexA {
				return fmt.Errorf("commit for file-b should appear before file-a in the log (most recent first)")
			}
			return nil
		}),
	},
	true, // localOnly, since it uses a real git repo
	false,
)
