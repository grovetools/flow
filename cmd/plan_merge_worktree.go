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

// planMergeWorktreeCmd implements the `flow plan merge-worktree` command
var planMergeWorktreeCmd = &cobra.Command{
	Use:   "merge-worktree [plan-name]",
	Short: "Merge plan worktree branch to main",
	Long: `Merge the plan's worktree branch into the main branch using fast-forward.
This is equivalent to pressing 'M' in the plan TUI.

The command will:
1. Checkout the main branch in the source repo
2. Fast-forward merge the worktree branch into main
3. Synchronize the worktree branch with the updated main

If no plan name is provided, uses the active plan.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanMergeWorktree,
}

func init() {
	planCmd.AddCommand(planMergeWorktreeCmd)
}

func runPlanMergeWorktree(cmd *cobra.Command, args []string) error {
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
		return mergeWorktreeEcosystem(plan, worktreeName)
	}

	// Handle single-repo plans
	return mergeWorktreeSingleRepo(worktreeName)
}

func mergeWorktreeSingleRepo(worktreeName string) error {
	// Get git root
	gitRoot, err := git.GetGitRoot(".")
	if err != nil {
		return fmt.Errorf("not in a git repository: %w", err)
	}

	// Check if we're on the correct branch
	_, currentBranch, err := git.GetRepoInfo(gitRoot)
	if err != nil {
		return fmt.Errorf("could not determine current branch: %w", err)
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

	if currentBranch != defaultBranch {
		return fmt.Errorf("must be on '%s' branch to merge. current branch: '%s'", defaultBranch, currentBranch)
	}

	fmt.Printf("Merging worktree '%s' into '%s'...\n", worktreeName, defaultBranch)

	// Perform the merge
	if err := rebaseAndMergeRepo(gitRoot, worktreeName, defaultBranch); err != nil {
		return fmt.Errorf("failed to merge: %w", err)
	}

	fmt.Printf("* Successfully merged '%s' into '%s' and synchronized the worktree\n", worktreeName, defaultBranch)
	return nil
}

func mergeWorktreeEcosystem(plan *orchestration.Plan, worktreeName string) error {
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

		fmt.Printf("Merging %s worktree into %s...\n", repoName, defaultBranch)
		if err := rebaseAndMergeRepo(repoPath, worktreeName, defaultBranch); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", repoName, err))
		} else {
			results = append(results, repoName)
		}
	}

	// Print summary
	if len(results) > 0 {
		fmt.Printf("\n* Successfully merged %d repos: %s\n", len(results), strings.Join(results, ", "))
	}
	if len(errors) > 0 {
		return fmt.Errorf("failed to merge %d repos:\n  %s", len(errors), strings.Join(errors, "\n  "))
	}

	return nil
}
