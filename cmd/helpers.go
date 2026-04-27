package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/core/git"
	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/tui/theme"
	grovecontext "github.com/grovetools/cx/pkg/context"
)

var helpersUlog = grovelogging.NewUnifiedLogger("grove-flow.helpers")

// configureDefaultContextRules applies default context rules to a given repository path.
func configureDefaultContextRules(repoPath string) error {
	// Check for zombie worktree - refuse to create rules in deleted worktrees
	if grovecontext.IsZombieWorktree(repoPath) {
		return fmt.Errorf("cannot create rules file: worktree has been deleted")
	}

	// Create a context manager scoped to the repository path. This is crucial
	// for it to find the correct grove.yml for that specific repository.
	mgr := grovecontext.NewManager(repoPath)

	// Load only the default rules content as defined by the repo's grove.yml.
	// This function doesn't read any existing .grove/rules file.
	defaultContent, rulesDestPath := mgr.LoadDefaultRulesContent()

	// If no default is configured in grove.yml, create a basic boilerplate.
	if defaultContent == nil {
		defaultContent = []byte("# Default context rules: include all non-gitignored files.\n*\n")
	}

	// Ensure the .grove directory exists within the target repo path.
	groveDir := filepath.Dir(rulesDestPath)
	if err := os.MkdirAll(groveDir, 0o755); err != nil {
		return fmt.Errorf("failed to create .grove directory in %s: %w", repoPath, err)
	}

	// Write the rules to .grove/rules within the target repo.
	if err := os.WriteFile(rulesDestPath, defaultContent, 0o600); err != nil {
		return fmt.Errorf("failed to write default rules to %s: %w", rulesDestPath, err)
	}

	ctx := context.Background()
	helpersUlog.Success("Applied default context rules").
		Field("repo_path", repoPath).
		Field("rules_path", rulesDestPath).
		Pretty(theme.IconSuccess + " Applied default context rules to: " + repoPath).
		Log(ctx)
	return nil
}

// configureGoWorkspace creates a go.work file for both ecosystem and single-repo worktrees.
func configureGoWorkspace(worktreePath string, repos []string, provider *workspace.Provider) error {
	if len(repos) > 0 { // Case 1: Ecosystem worktree.
		// Find the root go.work to get the go version.
		gitRoot, err := git.GetGitRoot(worktreePath)
		if err != nil {
			return nil // Not a git repo, can't find go.work.
		}
		goWorkConfig, err := workspace.FindRootGoWorkspace(gitRoot)
		goVersion := "go 1.24.4" // Fallback.
		if err == nil && goWorkConfig != nil && goWorkConfig.GoVersion != "" {
			goVersion = goWorkConfig.GoVersion
		}

		// Check which of the repos are Go modules.
		var goRepos []string
		for _, repo := range repos {
			repoGoModPath := filepath.Join(worktreePath, repo, "go.mod")
			if _, err := os.Stat(repoGoModPath); err == nil {
				goRepos = append(goRepos, repo)
			}
		}

		if len(goRepos) == 0 {
			return nil
		}

		var content strings.Builder
		content.WriteString(goVersion + "\n\n")
		content.WriteString("use (\n")
		content.WriteString("\t.\n") // The root of an ecosystem worktree can also be a module.
		for _, repo := range goRepos {
			content.WriteString(fmt.Sprintf("\t./%s\n", repo))
		}
		content.WriteString(")\n")

		if err := os.WriteFile(filepath.Join(worktreePath, "go.work"), []byte(content.String()), 0o600); err != nil {
			return fmt.Errorf("failed to write go.work for ecosystem worktree: %w", err)
		}
		ctx := context.Background()
		helpersUlog.Success("Configured go.work in ecosystem worktree").
			Field("worktree_path", worktreePath).
			Field("go_modules_count", len(goRepos)).
			Field("go_modules", goRepos).
			Pretty(fmt.Sprintf(theme.IconSuccess+" Configured go.work in ecosystem worktree with %d Go modules.", len(goRepos))).
			Log(ctx)
	} else {
		// Case 2: Single-repo worktree.
		// Use the SetupGoWorkspaceForWorktree function which parses go.mod
		// and filters to only include required dependencies.

		// First, we need to find the git root of the worktree to locate go.mod
		gitRoot, err := git.GetGitRoot(worktreePath)
		if err != nil {
			return nil // Not a git repo, nothing to do
		}

		// Use the centralized workspace function that handles dependency filtering
		if err := workspace.SetupGoWorkspaceForWorktree(worktreePath, gitRoot); err != nil {
			return fmt.Errorf("failed to setup go workspace for worktree: %w", err)
		}

		// Only print success message if a go.work file was actually created
		goWorkPath := filepath.Join(worktreePath, "go.work")
		if _, err := os.Stat(goWorkPath); err == nil {
			// Read the file to count dependencies (optional, for better messaging)
			config, _ := workspace.FindRootGoWorkspace(gitRoot)
			if config != nil {
				ctx := context.Background()
				helpersUlog.Success("Configured go.work with workspace dependencies").
					Field("worktree_path", worktreePath).
					Pretty(theme.IconSuccess + " Configured go.work with workspace dependencies.").
					Log(ctx)
			}
		}
	}
	return nil
}
