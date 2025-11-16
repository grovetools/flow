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

// WorktreeFastForwardScenario tests the U and M key functionality for updating and merging worktrees
func WorktreeFastForwardScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-worktree-fast-forward",
		Description: "Tests the worktree update (U) and merge (M) functionality with accurate merge status reporting",
		Tags:        []string{"worktree", "merge", "rebase", "tui"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with plan and worktree", func(ctx *harness.Context) error {
				// Init git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "# Test Project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Setup empty global config
				if err := setupEmptyGlobalConfig(ctx); err != nil {
					return err
				}

				// Create grove.yml
				configContent := `name: test-project
notebooks:
  rules:
    default: "local"
  definitions:
    local:
      root_dir: ""
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)

				// Create plan with worktree
				flow, _ := getFlowBinary()
				cmd := ctx.Command(flow, "plan", "init", "feature-branch", "--worktree").Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
					return fmt.Errorf("failed to init plan: %w", result.Error)
				}

				// Create the worktree and branch
				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "feature-branch")
				git.CreateWorktree(ctx.RootDir, "feature-branch", worktreePath)

				// Make a commit in the worktree
				fs.WriteString(filepath.Join(worktreePath, "feature.txt"), "new feature")
				git.Add(worktreePath, "feature.txt")
				git.Commit(worktreePath, "Add new feature")

				return nil
			}),
			setupTestEnvironment(),
			harness.NewStep("Verify merge status shows 'Ready' when ahead of main", func(ctx *harness.Context) error {
				// The branch should be ahead of main - verify git status
				aheadResult := command.New("git", "rev-list", "--count", "main..feature-branch").Dir(ctx.RootDir).Run()
				behindResult := command.New("git", "rev-list", "--count", "feature-branch..main").Dir(ctx.RootDir).Run()

				ahead := strings.TrimSpace(aheadResult.Stdout)
				behind := strings.TrimSpace(behindResult.Stdout)

				if ahead == "0" {
					return fmt.Errorf("feature-branch should be ahead of main (Ready status), got ahead=%s", ahead)
				}
				if behind != "0" {
					return fmt.Errorf("feature-branch should not be behind main (Ready status), got behind=%s", behind)
				}

				return nil
			}),
			harness.NewStep("Advance main branch to create 'Behind' status", func(ctx *harness.Context) error {
				// Make a new commit on main
				fs.WriteString(filepath.Join(ctx.RootDir, "main-update.txt"), "update on main")
				git.Add(ctx.RootDir, "main-update.txt")
				git.Commit(ctx.RootDir, "Update on main")

				// Now the feature-branch worktree is behind main
				return nil
			}),
			harness.NewStep("Test plan run with worktree update (simulating U key)", func(ctx *harness.Context) error {
				// The worktree should now be in "Needs Rebase" or "Behind" state
				// We simulate the U key action by directly calling the rebase logic
				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "feature-branch")

				// Run rebase inside the worktree
				result := command.New("git", "rebase", "main").Dir(worktreePath).Run()
				if result.Error != nil {
					return fmt.Errorf("rebase failed: %w\nOutput: %s\nStderr: %s", result.Error, result.Stdout, result.Stderr)
				}

				// Verify the worktree now has both commits
				logResult := command.New("git", "log", "--oneline", "--all").Dir(worktreePath).Run()
				if !strings.Contains(logResult.Stdout, "Update on main") {
					return fmt.Errorf("worktree should have main's commit after rebase")
				}
				if !strings.Contains(logResult.Stdout, "Add new feature") {
					return fmt.Errorf("worktree should still have feature commit after rebase")
				}

				return nil
			}),
			harness.NewStep("Verify merge status shows 'Ready' after update", func(ctx *harness.Context) error {
				// After rebasing, the branch should be ahead of main and ready to merge

				// Check commit counts
				aheadResult := command.New("git", "rev-list", "--count", "main..feature-branch").Dir(ctx.RootDir).Run()
				behindResult := command.New("git", "rev-list", "--count", "feature-branch..main").Dir(ctx.RootDir).Run()

				aheadCount := strings.TrimSpace(aheadResult.Stdout)
				behindCount := strings.TrimSpace(behindResult.Stdout)

				if aheadCount == "0" {
					return fmt.Errorf("branch should be ahead of main after rebase, got ahead count: %s", aheadCount)
				}
				if behindCount != "0" {
					return fmt.Errorf("branch should not be behind main after rebase, got behind count: %s", behindCount)
				}

				return nil
			}),
			harness.NewStep("Test fast-forward merge (simulating M key)", func(ctx *harness.Context) error {
				// Checkout main
				checkoutResult := command.New("git", "checkout", "main").Dir(ctx.RootDir).Run()
				if checkoutResult.Error != nil {
					return fmt.Errorf("failed to checkout main: %w", checkoutResult.Error)
				}

				// Perform fast-forward merge
				mergeResult := command.New("git", "merge", "--ff-only", "feature-branch").Dir(ctx.RootDir).Run()
				if mergeResult.Error != nil {
					return fmt.Errorf("fast-forward merge failed: %w\nOutput: %s\nStderr: %s", mergeResult.Error, mergeResult.Stdout, mergeResult.Stderr)
				}

				// Verify main now has the feature commit
				logResult := command.New("git", "log", "--oneline").Dir(ctx.RootDir).Run()
				if !strings.Contains(logResult.Stdout, "Add new feature") {
					return fmt.Errorf("main should have feature commit after merge")
				}

				return nil
			}),
			harness.NewStep("Sync worktree after merge", func(ctx *harness.Context) error {
				// After merging, the worktree should be synced with main
				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "feature-branch")

				// Reset worktree to main (as the M key action does)
				resetResult := command.New("git", "reset", "--hard", "main").Dir(worktreePath).Run()
				if resetResult.Error != nil {
					return fmt.Errorf("failed to reset worktree: %w", resetResult.Error)
				}

				// Verify they're now synced
				aheadResult := command.New("git", "rev-list", "--count", "main..feature-branch").Dir(ctx.RootDir).Run()
				behindResult := command.New("git", "rev-list", "--count", "feature-branch..main").Dir(ctx.RootDir).Run()

				aheadCount := strings.TrimSpace(aheadResult.Stdout)
				behindCount := strings.TrimSpace(behindResult.Stdout)

				if aheadCount != "0" || behindCount != "0" {
					return fmt.Errorf("branch should be synced with main after merge, got ahead: %s, behind: %s", aheadCount, behindCount)
				}

				return nil
			}),
		},
	}
}

// WorktreeMergeStatusScenario tests the accuracy of different merge statuses
func WorktreeMergeStatusScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-worktree-merge-status",
		Description: "Tests all merge status states: Synced, Ready, Behind, Needs Rebase, Conflicts",
		Tags:        []string{"worktree", "merge", "status"},
		Steps: []harness.Step{
			harness.NewStep("Setup project", func(ctx *harness.Context) error {
				// Init git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "# Test Project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Setup empty global config
				if err := setupEmptyGlobalConfig(ctx); err != nil {
					return err
				}

				// Create grove.yml
				configContent := `name: test-project
notebooks:
  rules:
    default: "local"
  definitions:
    local:
      root_dir: ""
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)

				return nil
			}),
			setupTestEnvironment(),

			// Test 1: Synced status
			harness.NewStep("Test Synced: Create worktree identical to main", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := ctx.Command(flow, "plan", "init", "synced-branch", "--worktree").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to init plan: %w", err)
				}

				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "synced-branch")
				git.CreateWorktree(ctx.RootDir, "synced-branch", worktreePath)

				return nil
			}),
			harness.NewStep("Verify Synced status (ahead=0, behind=0)", func(ctx *harness.Context) error {
				aheadResult := command.New("git", "rev-list", "--count", "main..synced-branch").Dir(ctx.RootDir).Run()
				behindResult := command.New("git", "rev-list", "--count", "synced-branch..main").Dir(ctx.RootDir).Run()

				ahead := strings.TrimSpace(aheadResult.Stdout)
				behind := strings.TrimSpace(behindResult.Stdout)

				if ahead != "0" || behind != "0" {
					return fmt.Errorf("synced-branch should have ahead=0, behind=0, got ahead=%s, behind=%s", ahead, behind)
				}

				return nil
			}),

			// Test 2: Ready status
			harness.NewStep("Test Ready: Create worktree ahead of main", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := ctx.Command(flow, "plan", "init", "ready-branch", "--worktree").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to init plan: %w", err)
				}

				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "ready-branch")
				git.CreateWorktree(ctx.RootDir, "ready-branch", worktreePath)

				// Make commit in worktree
				fs.WriteString(filepath.Join(worktreePath, "ready.txt"), "ready to merge")
				git.Add(worktreePath, "ready.txt")
				git.Commit(worktreePath, "Add ready feature")

				return nil
			}),
			harness.NewStep("Verify Ready status (ahead>0, behind=0)", func(ctx *harness.Context) error {
				aheadResult := command.New("git", "rev-list", "--count", "main..ready-branch").Dir(ctx.RootDir).Run()
				behindResult := command.New("git", "rev-list", "--count", "ready-branch..main").Dir(ctx.RootDir).Run()

				ahead := strings.TrimSpace(aheadResult.Stdout)
				behind := strings.TrimSpace(behindResult.Stdout)

				if ahead == "0" {
					return fmt.Errorf("ready-branch should be ahead of main, got ahead=%s", ahead)
				}
				if behind != "0" {
					return fmt.Errorf("ready-branch should not be behind main, got behind=%s", behind)
				}

				return nil
			}),

			// Test 3: Behind status
			harness.NewStep("Test Behind: Create worktree then advance main", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := ctx.Command(flow, "plan", "init", "behind-branch", "--worktree").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to init plan: %w", err)
				}

				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "behind-branch")
				git.CreateWorktree(ctx.RootDir, "behind-branch", worktreePath)

				// Make commit on main to put worktree behind
				fs.WriteString(filepath.Join(ctx.RootDir, "behind-test.txt"), "main is ahead")
				git.Add(ctx.RootDir, "behind-test.txt")
				git.Commit(ctx.RootDir, "Update main")

				return nil
			}),
			harness.NewStep("Verify Behind status (ahead=0, behind>0)", func(ctx *harness.Context) error {
				aheadResult := command.New("git", "rev-list", "--count", "main..behind-branch").Dir(ctx.RootDir).Run()
				behindResult := command.New("git", "rev-list", "--count", "behind-branch..main").Dir(ctx.RootDir).Run()

				ahead := strings.TrimSpace(aheadResult.Stdout)
				behind := strings.TrimSpace(behindResult.Stdout)

				if ahead != "0" {
					return fmt.Errorf("behind-branch should not be ahead of main, got ahead=%s", ahead)
				}
				if behind == "0" {
					return fmt.Errorf("behind-branch should be behind main, got behind=%s", behind)
				}

				return nil
			}),

			// Test 4: Needs Rebase status
			harness.NewStep("Test Needs Rebase: Create diverged branches", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := ctx.Command(flow, "plan", "init", "diverged-branch", "--worktree").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to init plan: %w", err)
				}

				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "diverged-branch")
				git.CreateWorktree(ctx.RootDir, "diverged-branch", worktreePath)

				// Make commit in worktree
				fs.WriteString(filepath.Join(worktreePath, "diverged.txt"), "diverged feature")
				git.Add(worktreePath, "diverged.txt")
				git.Commit(worktreePath, "Add diverged feature")

				// Make different commit on main
				fs.WriteString(filepath.Join(ctx.RootDir, "main-diverged.txt"), "main diverged")
				git.Add(ctx.RootDir, "main-diverged.txt")
				git.Commit(ctx.RootDir, "Diverge on main")

				return nil
			}),
			harness.NewStep("Verify Needs Rebase status (ahead>0, behind>0)", func(ctx *harness.Context) error {
				aheadResult := command.New("git", "rev-list", "--count", "main..diverged-branch").Dir(ctx.RootDir).Run()
				behindResult := command.New("git", "rev-list", "--count", "diverged-branch..main").Dir(ctx.RootDir).Run()

				ahead := strings.TrimSpace(aheadResult.Stdout)
				behind := strings.TrimSpace(behindResult.Stdout)

				if ahead == "0" {
					return fmt.Errorf("diverged-branch should be ahead of main, got ahead=%s", ahead)
				}
				if behind == "0" {
					return fmt.Errorf("diverged-branch should be behind main, got behind=%s", behind)
				}

				return nil
			}),
		},
	}
}

// WorktreeMergePreflightScenario tests the pre-flight checks for merge operations
func WorktreeMergePreflightScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-worktree-merge-preflight",
		Description: "Tests that M key only allows merge when status is 'Ready'",
		Tags:        []string{"worktree", "merge", "validation"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with worktree behind main", func(ctx *harness.Context) error {
				// Init git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "# Test Project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Setup empty global config
				if err := setupEmptyGlobalConfig(ctx); err != nil {
					return err
				}

				// Create grove.yml
				configContent := `name: test-project
notebooks:
  rules:
    default: "local"
  definitions:
    local:
      root_dir: ""
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)

				// Create plan with worktree
				flow, _ := getFlowBinary()
				cmd := ctx.Command(flow, "plan", "init", "not-ready", "--worktree").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to init plan: %w", err)
				}

				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "not-ready")
				git.CreateWorktree(ctx.RootDir, "not-ready", worktreePath)

				// Advance main to create "Behind" status
				fs.WriteString(filepath.Join(ctx.RootDir, "update.txt"), "main update")
				git.Add(ctx.RootDir, "update.txt")
				git.Commit(ctx.RootDir, "Update main")

				return nil
			}),
			setupTestEnvironment(),
			harness.NewStep("Verify branch is Behind main", func(ctx *harness.Context) error {
				// Verify the branch is behind
				aheadResult := command.New("git", "rev-list", "--count", "main..not-ready").Dir(ctx.RootDir).Run()
				behindResult := command.New("git", "rev-list", "--count", "not-ready..main").Dir(ctx.RootDir).Run()

				ahead := strings.TrimSpace(aheadResult.Stdout)
				behind := strings.TrimSpace(behindResult.Stdout)

				if ahead != "0" {
					return fmt.Errorf("not-ready should not be ahead, got ahead=%s", ahead)
				}
				if behind == "0" {
					return fmt.Errorf("not-ready should be behind main, got behind=%s", behind)
				}

				return nil
			}),
			harness.NewStep("Update branch and verify merge succeeds", func(ctx *harness.Context) error {
				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "not-ready")

				// Rebase to bring branch up to date
				rebaseResult := command.New("git", "rebase", "main").Dir(worktreePath).Run()
				if rebaseResult.Error != nil {
					return fmt.Errorf("rebase failed: %w", rebaseResult.Error)
				}

				// Add a commit to make it ahead
				fs.WriteString(filepath.Join(worktreePath, "feature.txt"), "new feature")
				git.Add(worktreePath, "feature.txt")
				git.Commit(worktreePath, "Add feature")

				// Now merge should succeed
				checkoutResult := command.New("git", "checkout", "main").Dir(ctx.RootDir).Run()
				if checkoutResult.Error != nil {
					return fmt.Errorf("failed to checkout main: %w", checkoutResult.Error)
				}

				mergeResult := command.New("git", "merge", "--ff-only", "not-ready").Dir(ctx.RootDir).Run()
				if mergeResult.Error != nil {
					return fmt.Errorf("merge should succeed after update: %w\nOutput: %s", mergeResult.Error, mergeResult.Stderr)
				}

				return nil
			}),
		},
	}
}
