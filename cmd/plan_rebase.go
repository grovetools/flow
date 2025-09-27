package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/mattsolo1/grove-core/git"
	"github.com/mattsolo1/grove-core/pkg/workspace"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

var (
	rebaseYes      bool
	rebaseAbort    bool
	rebaseContinue bool
)

// newPlanRebaseCmd creates the `plan rebase` command.
func newPlanRebaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rebase [target]",
		Short: "Rebase branches for the plan's worktree(s)",
		Long: `A powerful command with two modes for managing branches related to a plan's worktree.

**1. Standard Rebase (Update Feature Branch)**
   - **Usage**: ` + "`flow plan rebase` or `flow plan rebase main`" + `
   - **Action**: Updates the plan's feature branch by rebasing it ON TOP OF the latest 'main' branch.
     This is the standard, safe way to keep your work-in-progress up-to-date with the main repository.
     For ecosystem plans, this operation is applied to each repository's worktree.

**2. Integration Rebase (Test on Main)**
   - **Usage**: ` + "`flow plan rebase <feature-branch-name>`" + `
   - **Action**: Temporarily applies the feature branch's changes ONTO the 'main' branch in the main repository checkout.
     This is for integration testing, simulating a merged state locally without pushing.
     For ecosystem plans, this is applied to each source repository's main checkout.

**Conflict Resolution**:
If a rebase results in conflicts, resolve them in the affected repository, then use the corresponding flag to proceed:
  - ` + "`flow plan rebase --continue`" + `
  - ` + "`flow plan rebase --abort`" + `
`,
		RunE: runPlanRebase,
	}

	cmd.Flags().BoolVarP(&rebaseYes, "yes", "y", false, "Skip confirmation prompts before rebasing")
	cmd.Flags().BoolVar(&rebaseAbort, "abort", false, "Abort an in-progress rebase")
	cmd.Flags().BoolVar(&rebaseContinue, "continue", false, "Continue an in-progress rebase after resolving conflicts")

	return cmd
}

func runPlanRebase(cmd *cobra.Command, args []string) error {
	// First, handle --abort or --continue flags as they are special modes.
	if rebaseAbort || rebaseContinue {
		return handleRebaseInProgress(rebaseContinue)
	}

	// Resolve plan path from arguments or active plan state.
	var planDir string
	if len(args) > 0 && (args[0] != "main" && !isFlag(args[0])) {
		// If arg is not 'main' or a flag, it could be either a directory or a branch target.
		// We'll determine the correct interpretation below.
		planDir = args[0]
	}

	resolvedPath, err := resolvePlanPathWithActiveJob(planDir)
	if err != nil {
		// If we can't resolve as a plan path, maybe it's a branch name for the current plan
		if len(args) > 0 {
			// Try with empty string to get current/active plan
			resolvedPath, err = resolvePlanPathWithActiveJob("")
			if err != nil {
				return fmt.Errorf("could not resolve plan path: %w", err)
			}
			// args[0] is the target branch
		} else {
			return fmt.Errorf("could not resolve plan path: %w", err)
		}
	}

	plan, err := orchestration.LoadPlan(resolvedPath)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	// Determine the rebase target.
	var target string
	if len(args) > 0 {
		// Check if first arg is actually the resolved plan path
		absArg, _ := filepath.Abs(args[0])
		absResolved, _ := filepath.Abs(resolvedPath)
		if absArg != absResolved {
			// It's not the plan path, so it must be the target
			target = args[0]
		}
	}

	// Route to the correct workflow.
	if target == "" || target == "main" {
		return runRebaseWorktreeOntoMain(plan)
	} else if plan.Config != nil && target == plan.Config.Worktree {
		return runRebaseMainOntoWorktree(plan, target)
	} else {
		// If we have a target that's not main or the worktree name, it's an error
		worktreeName := ""
		if plan.Config != nil {
			worktreeName = plan.Config.Worktree
		}
		return fmt.Errorf("invalid rebase target '%s'. Must be 'main' or the plan's worktree name ('%s')", target, worktreeName)
	}
}

func isFlag(s string) bool {
	return strings.HasPrefix(s, "-")
}

// runRebaseWorktreeOntoMain rebases the worktree branches on top of main
func runRebaseWorktreeOntoMain(plan *orchestration.Plan) error {
	fmt.Println(color.CyanString("Rebasing worktree branch(es) on top of latest 'main'..."))

	// Find the Git root from the plan directory.
	gitRoot, err := orchestration.GetGitRootSafe(plan.Directory)
	if err != nil {
		return fmt.Errorf("could not determine git root: %w", err)
	}

	var worktreePaths []string
	var repoNames []string
	
	if plan.Config != nil && len(plan.Config.Repos) > 0 {
		// Ecosystem Plan
		fmt.Printf("Detected ecosystem plan with %d repositories.\n", len(plan.Config.Repos))
		baseWorktreePath := filepath.Join(gitRoot, ".grove-worktrees", plan.Config.Worktree)
		for _, repoName := range plan.Config.Repos {
			worktreePath := filepath.Join(baseWorktreePath, repoName)
			worktreePaths = append(worktreePaths, worktreePath)
			repoNames = append(repoNames, repoName)
		}
	} else if plan.Config != nil && plan.Config.Worktree != "" {
		// Single Repository Plan
		worktreePath := filepath.Join(gitRoot, ".grove-worktrees", plan.Config.Worktree)
		worktreePaths = append(worktreePaths, worktreePath)
		repoNames = append(repoNames, filepath.Base(worktreePath))
	} else {
		return fmt.Errorf("no worktree configured for this plan. Cannot perform rebase")
	}

	// Perform rebase for each path.
	results := make(map[string]error)
	for i, path := range worktreePaths {
		repoName := repoNames[i]
		fmt.Printf("\nProcessing %s...\n", color.YellowString(repoName))
		results[repoName] = performRebase(path, "origin/main")
	}

	// Display summary.
	fmt.Println("\n" + color.CyanString("--- Rebase Summary ---"))
	hasErrors := false
	for _, repoName := range repoNames {
		err := results[repoName]
		if err != nil {
			hasErrors = true
			fmt.Printf("%s %s: %s\n", color.RedString("✗"), repoName, err.Error())
		} else {
			fmt.Printf("%s %s: Rebased successfully.\n", color.GreenString("✓"), repoName)
		}
	}

	if hasErrors {
		return fmt.Errorf("some rebases failed. See details above")
	}
	return nil
}

// runRebaseMainOntoWorktree rebases main on top of the worktree branch (for integration testing)
func runRebaseMainOntoWorktree(plan *orchestration.Plan, featureBranch string) error {
	fmt.Println(color.CyanString("Rebasing 'main' on top of feature branch '%s' for integration testing...", featureBranch))
	fmt.Println(color.YellowString("WARNING: This will temporarily modify your main branch for testing purposes."))

	// Find the Git root from the plan directory.
	gitRoot, err := orchestration.GetGitRootSafe(plan.Directory)
	if err != nil {
		return fmt.Errorf("could not determine git root: %w", err)
	}

	var sourceRepoPaths []string
	var repoNames []string
	
	if plan.Config != nil && len(plan.Config.Repos) > 0 {
		// Ecosystem Plan
		fmt.Printf("Detected ecosystem plan with %d repositories.\n", len(plan.Config.Repos))
		ctx := context.Background()
		localWorkspaces, err := workspace.DiscoverLocalWorkspaces(ctx)
		if err != nil {
			return fmt.Errorf("could not discover local workspaces for source repositories: %w", err)
		}
		for _, repoName := range plan.Config.Repos {
			if path, ok := localWorkspaces[repoName]; ok {
				sourceRepoPaths = append(sourceRepoPaths, path)
				repoNames = append(repoNames, repoName)
			} else {
				return fmt.Errorf("could not find source repository for '%s'", repoName)
			}
		}
	} else if plan.Config != nil && plan.Config.Worktree != "" {
		// Single Repository Plan
		sourceRepoPaths = append(sourceRepoPaths, gitRoot)
		repoNames = append(repoNames, filepath.Base(gitRoot))
	} else {
		return fmt.Errorf("no worktree configured for this plan. Cannot perform rebase")
	}

	// Confirm with user unless --yes flag is set
	if !rebaseYes {
		fmt.Printf("\nThis will rebase 'main' onto '%s' in %d repository(ies).\n", featureBranch, len(sourceRepoPaths))
		fmt.Print("Are you sure you want to continue? (y/N): ")
		
		var response string
		fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	// Perform rebase on the main checkout for each source repo.
	results := make(map[string]error)
	for i, path := range sourceRepoPaths {
		repoName := repoNames[i]
		fmt.Printf("\nProcessing %s...\n", color.YellowString(repoName))

		// Switch to main branch first
		checkoutCmd := exec.Command("git", "checkout", "main")
		checkoutCmd.Dir = path
		if output, err := checkoutCmd.CombinedOutput(); err != nil {
			results[repoName] = fmt.Errorf("failed to checkout main: %w\n%s", err, string(output))
			continue
		}

		// Perform the rebase
		results[repoName] = performRebase(path, featureBranch)
	}

	// Display summary.
	fmt.Println("\n" + color.CyanString("--- Integration Rebase Summary ---"))
	hasErrors := false
	successfulRepos := []string{}
	
	for _, repoName := range repoNames {
		err := results[repoName]
		if err != nil {
			hasErrors = true
			fmt.Printf("%s %s: %s\n", color.RedString("✗"), repoName, err.Error())
		} else {
			successfulRepos = append(successfulRepos, repoName)
			fmt.Printf("%s %s: Rebased 'main' successfully.\n", color.GreenString("✓"), repoName)
		}
	}

	if len(successfulRepos) > 0 {
		fmt.Println("\n" + color.YellowString("⚠️  WARNING: Your 'main' branch(es) are now in a temporary state for testing."))
		fmt.Println(color.YellowString("To revert to the original state, run one of the following:"))
		fmt.Println(color.YellowString("  • In each repo: git reset --hard origin/main"))
		fmt.Println(color.YellowString("  • Or checkout the original branch: git checkout -"))
	}

	if hasErrors {
		return fmt.Errorf("some rebases failed. See details above")
	}
	return nil
}

// performRebase is the core function that executes git commands for a rebase.
func performRebase(repoPath, rebaseTarget string) error {
	// 1. Safety Check: Ensure the repository is clean.
	status, err := git.GetStatus(repoPath)
	if err != nil {
		return fmt.Errorf("could not get git status: %w", err)
	}
	if status.IsDirty {
		return fmt.Errorf("repository has uncommitted changes. Please commit or stash them first")
	}

	// 2. Safety Check: Check for an in-progress rebase.
	rebaseMergePath := filepath.Join(repoPath, ".git", "rebase-merge")
	rebaseApplyPath := filepath.Join(repoPath, ".git", "rebase-apply")
	if _, err := os.Stat(rebaseMergePath); err == nil {
		return fmt.Errorf("a rebase is already in progress. Resolve conflicts and use --continue, or use --abort")
	}
	if _, err := os.Stat(rebaseApplyPath); err == nil {
		return fmt.Errorf("a rebase is already in progress. Resolve conflicts and use --continue, or use --abort")
	}

	// 3. Git Operation: Fetch latest from origin.
	fmt.Printf("  Fetching latest from origin...\n")
	fetchCmd := exec.Command("git", "fetch", "origin")
	fetchCmd.Dir = repoPath
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch failed: %w\n%s", err, string(output))
	}

	// 4. Git Operation: Perform the rebase.
	fmt.Printf("  Rebasing onto %s...\n", rebaseTarget)
	rebaseCmd := exec.Command("git", "rebase", rebaseTarget)
	rebaseCmd.Dir = repoPath
	if output, err := rebaseCmd.CombinedOutput(); err != nil {
		outputStr := string(output)
		if strings.Contains(outputStr, "CONFLICT") || strings.Contains(outputStr, "conflict") {
			fmt.Println(color.YellowString("  ⚠️  Rebase resulted in conflicts."))
			fmt.Println("  Please resolve conflicts in: " + repoPath)
			fmt.Println("  Then run: flow plan rebase --continue")
			fmt.Println("  Or to abort: flow plan rebase --abort")
			return fmt.Errorf("conflicts detected")
		}
		return fmt.Errorf("git rebase failed: %w\n%s", err, outputStr)
	}

	// Check if it was a no-op
	currentCmd := exec.Command("git", "rev-parse", "HEAD")
	currentCmd.Dir = repoPath
	currentOutput, _ := currentCmd.Output()
	
	targetCmd := exec.Command("git", "rev-parse", rebaseTarget)
	targetCmd.Dir = repoPath
	targetOutput, _ := targetCmd.Output()
	
	if strings.TrimSpace(string(currentOutput)) == strings.TrimSpace(string(targetOutput)) {
		fmt.Printf("  Already up to date.\n")
	} else {
		fmt.Printf("  Rebase completed successfully.\n")
	}

	return nil
}

// handleRebaseInProgress handles --continue and --abort flags
func handleRebaseInProgress(isContinue bool) error {
	action := "abort"
	if isContinue {
		action = "continue"
	}
	
	fmt.Println(color.CyanString("Attempting to %s in-progress rebase...", action))

	// Resolve a plan path to find the git root
	resolvedPath, err := resolvePlanPathWithActiveJob("")
	if err != nil {
		// If we can't find an active job, try current directory
		resolvedPath = "."
	}
	
	plan, err := orchestration.LoadPlan(resolvedPath)
	if err != nil {
		// If we can't load a plan, just try to handle rebase in current directory
		return handleRebaseInCurrentDir(isContinue)
	}

	gitRoot, err := orchestration.GetGitRootSafe(plan.Directory)
	if err != nil {
		return fmt.Errorf("could not determine git root: %w", err)
	}

	// Discover all possible worktrees and source repos to check.
	searchPaths, err := findAllPossibleRebaseLocations(gitRoot, plan)
	if err != nil {
		return err
	}

	// Find the first location with a rebase in progress.
	for _, path := range searchPaths {
		rebaseMergePath := filepath.Join(path, ".git", "rebase-merge")
		rebaseApplyPath := filepath.Join(path, ".git", "rebase-apply")
		
		hasRebase := false
		if _, err := os.Stat(rebaseMergePath); err == nil {
			hasRebase = true
		} else if _, err := os.Stat(rebaseApplyPath); err == nil {
			hasRebase = true
		}
		
		if hasRebase {
			fmt.Printf("Found in-progress rebase at: %s\n", path)
			
			var gitAction string
			if isContinue {
				gitAction = "--continue"
			} else {
				gitAction = "--abort"
			}

			rebaseCmd := exec.Command("git", "rebase", gitAction)
			rebaseCmd.Dir = path
			rebaseCmd.Stdout = os.Stdout
			rebaseCmd.Stderr = os.Stderr
			if err := rebaseCmd.Run(); err != nil {
				return fmt.Errorf("git rebase %s failed: %w", gitAction, err)
			}
			fmt.Printf("%s Rebase %s successful.\n", color.GreenString("✓"), action)
			return nil
		}
	}

	return fmt.Errorf("no in-progress rebase found in any known worktree or source repository")
}

// handleRebaseInCurrentDir handles rebase in current directory when we can't load a plan
func handleRebaseInCurrentDir(isContinue bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("could not get current directory: %w", err)
	}

	rebaseMergePath := filepath.Join(cwd, ".git", "rebase-merge")
	rebaseApplyPath := filepath.Join(cwd, ".git", "rebase-apply")
	
	hasRebase := false
	if _, err := os.Stat(rebaseMergePath); err == nil {
		hasRebase = true
	} else if _, err := os.Stat(rebaseApplyPath); err == nil {
		hasRebase = true
	}

	if !hasRebase {
		return fmt.Errorf("no in-progress rebase found in current directory")
	}

	action := "abort"
	gitAction := "--abort"
	if isContinue {
		action = "continue"
		gitAction = "--continue"
	}

	fmt.Printf("Found in-progress rebase in current directory\n")
	
	rebaseCmd := exec.Command("git", "rebase", gitAction)
	rebaseCmd.Stdout = os.Stdout
	rebaseCmd.Stderr = os.Stderr
	if err := rebaseCmd.Run(); err != nil {
		return fmt.Errorf("git rebase %s failed: %w", gitAction, err)
	}
	
	fmt.Printf("%s Rebase %s successful.\n", color.GreenString("✓"), action)
	return nil
}

// findAllPossibleRebaseLocations scans the entire ecosystem for potential rebase locations.
func findAllPossibleRebaseLocations(gitRoot string, plan *orchestration.Plan) ([]string, error) {
	var searchPaths []string

	// 1. Add the plan's worktree(s)
	if plan.Config != nil {
		if len(plan.Config.Repos) > 0 {
			// Ecosystem plan - add all repo worktrees
			baseWorktreePath := filepath.Join(gitRoot, ".grove-worktrees", plan.Config.Worktree)
			for _, repoName := range plan.Config.Repos {
				searchPaths = append(searchPaths, filepath.Join(baseWorktreePath, repoName))
			}
		} else if plan.Config.Worktree != "" {
			// Single repo plan
			searchPaths = append(searchPaths, filepath.Join(gitRoot, ".grove-worktrees", plan.Config.Worktree))
		}
	}

	// 2. Add all source repositories (main checkouts) for ecosystem plans.
	if plan.Config != nil && len(plan.Config.Repos) > 0 {
		ctx := context.Background()
		localWorkspaces, err := workspace.DiscoverLocalWorkspaces(ctx)
		if err == nil {
			for _, repoName := range plan.Config.Repos {
				if path, ok := localWorkspaces[repoName]; ok {
					searchPaths = append(searchPaths, path)
				}
			}
		}
	}

	// 3. Add the main ecosystem/repo root as well.
	searchPaths = append(searchPaths, gitRoot)

	// 4. Also scan all worktrees in the .grove-worktrees directory (in case user is in a different plan)
	worktreesBaseDir := filepath.Join(gitRoot, ".grove-worktrees")
	if _, err := os.Stat(worktreesBaseDir); err == nil {
		entries, _ := os.ReadDir(worktreesBaseDir)
		for _, entry := range entries {
			if entry.IsDir() {
				worktreePath := filepath.Join(worktreesBaseDir, entry.Name())
				// Check if it's an ecosystem worktree (has subdirectories)
				subEntries, _ := os.ReadDir(worktreePath)
				hasSubDirs := false
				for _, subEntry := range subEntries {
					if subEntry.IsDir() && !strings.HasPrefix(subEntry.Name(), ".") {
						hasSubDirs = true
						searchPaths = append(searchPaths, filepath.Join(worktreePath, subEntry.Name()))
					}
				}
				// If no subdirs, it's a single repo worktree
				if !hasSubDirs {
					searchPaths = append(searchPaths, worktreePath)
				}
			}
		}
	}

	// Remove duplicates
	seen := make(map[string]bool)
	var uniquePaths []string
	for _, path := range searchPaths {
		if !seen[path] {
			seen[path] = true
			uniquePaths = append(uniquePaths, path)
		}
	}

	return uniquePaths, nil
}

