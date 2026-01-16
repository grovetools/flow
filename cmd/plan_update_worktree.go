package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/core/git"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

// planUpdateWorktreeCmd implements the `flow plan update-worktree` command
var planUpdateWorktreeCmd = &cobra.Command{
	Use:   "update-worktree [plan-name]",
	Short: "Update plan worktree by rebasing on main",
	Long: `Update the plan's worktree branch by rebasing it on top of the main branch.
This is equivalent to pressing 'U' in the plan TUI.

If no plan name is provided, uses the active plan.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanUpdateWorktree,
}

func init() {
	planCmd.AddCommand(planUpdateWorktreeCmd)
}

func runPlanUpdateWorktree(cmd *cobra.Command, args []string) error {
	// Resolve plan path
	var dir string
	if len(args) > 0 {
		dir = args[0]
	}

	planPath, err := resolvePlanPathWithActiveJob(dir)
	if err != nil {
		return err
	}

	// Load the plan
	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	// Check if plan has a worktree
	if plan.Config == nil || plan.Config.Worktree == "" {
		return fmt.Errorf("plan '%s' has no associated worktree", plan.Name)
	}

	worktreeName := plan.Config.Worktree

	// Handle ecosystem plans
	if plan.Config != nil && len(plan.Config.Repos) > 0 {
		return updateWorktreeEcosystem(plan, worktreeName)
	}

	// Handle single-repo plans
	return updateWorktreeSingleRepo(worktreeName)
}

func updateWorktreeSingleRepo(worktreeName string) error {
	// Get git root
	gitRoot, err := git.GetGitRoot(".")
	if err != nil {
		return fmt.Errorf("not in a git repository: %w", err)
	}

	worktreePath := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return fmt.Errorf("worktree not found: %s", worktreePath)
	}

	// Determine default branch
	defaultBranch := "main"
	if _, err := os.Stat(filepath.Join(gitRoot, ".git", "refs", "heads", "main")); os.IsNotExist(err) {
		if _, err := os.Stat(filepath.Join(gitRoot, ".git", "refs", "heads", "master")); err == nil {
			defaultBranch = "master"
		} else {
			return fmt.Errorf("neither 'main' nor 'master' branch found")
		}
	}

	fmt.Printf("Updating worktree '%s' from '%s'...\n", worktreeName, defaultBranch)

	// Rebase worktree branch on default branch
	if err := rebaseWorktreeBranch(worktreePath, defaultBranch); err != nil {
		return fmt.Errorf("failed to update: %w", err)
	}

	fmt.Printf("* Successfully updated worktree '%s' from '%s'\n", worktreeName, defaultBranch)
	return nil
}

func updateWorktreeEcosystem(plan *orchestration.Plan, worktreeName string) error {
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)
	discoveryService := workspace.NewDiscoveryService(logger)
	discoveryResult, err := discoveryService.DiscoverAll()
	if err != nil {
		return fmt.Errorf("failed to discover workspaces: %w", err)
	}
	provider := workspace.NewProvider(discoveryResult)
	localWorkspaces := provider.LocalWorkspaces()

	var results []string
	var errors []string

	for _, repoName := range plan.Config.Repos {
		repoPath, exists := localWorkspaces[repoName]
		if !exists {
			errors = append(errors, fmt.Sprintf("%s: repo not found locally", repoName))
			continue
		}

		worktreePath := filepath.Join(repoPath, ".grove-worktrees", worktreeName)
		if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
			// worktree for this specific repo doesn't exist, skip
			continue
		}

		// Determine default branch for this repo
		defaultBranch := "main"
		if _, err := os.Stat(filepath.Join(repoPath, ".git", "refs", "heads", "main")); os.IsNotExist(err) {
			if _, err := os.Stat(filepath.Join(repoPath, ".git", "refs", "heads", "master")); err == nil {
				defaultBranch = "master"
			} else {
				errors = append(errors, fmt.Sprintf("%s: no main or master branch", repoName))
				continue
			}
		}

		fmt.Printf("Updating %s worktree from %s...\n", repoName, defaultBranch)
		if err := rebaseWorktreeBranch(worktreePath, defaultBranch); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", repoName, err))
		} else {
			results = append(results, repoName)
		}
	}

	// Print summary
	if len(results) > 0 {
		fmt.Printf("\n* Successfully updated %d repos: %s\n", len(results), strings.Join(results, ", "))
	}
	if len(errors) > 0 {
		return fmt.Errorf("failed to update %d repos:\n  %s", len(errors), strings.Join(errors, "\n  "))
	}

	return nil
}
