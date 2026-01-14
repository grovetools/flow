package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	grovecontext "github.com/mattsolo1/grove-context/pkg/context"
	"github.com/mattsolo1/grove-core/git"
	grovelogging "github.com/mattsolo1/grove-core/logging"
	"github.com/mattsolo1/grove-core/pkg/workspace"
	"github.com/mattsolo1/grove-core/tui/theme"
)

var helpersUlog = grovelogging.NewUnifiedLogger("grove-flow.helpers")

// configureGroveHooks copies the Claude hook settings to a worktree
func configureGroveHooks(worktreePath string) error {
	// Create .claude directory
	claudeDir := filepath.Join(worktreePath, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		return fmt.Errorf("failed to create .claude directory in worktree: %w", err)
	}

	// Find the grove ecosystem root to locate the hook settings
	ecosystemRoot, err := findGroveEcosystemRoot()
	if err != nil {
		// Log warning but don't fail - hooks are optional
		ctx := context.Background()
		helpersUlog.Warn("Could not find grove ecosystem root").
			Err(err).
			Pretty(theme.IconWarning + "  Warning: Could not find grove ecosystem root: " + err.Error() + "\n   Grove hooks will not be configured.").
			Log(ctx)
		return nil
	}

	sourceHookSettings := filepath.Join(ecosystemRoot, "grove-hooks", "configs", "claude-hooks-settings.json")
	destHookSettings := filepath.Join(claudeDir, "settings.local.json")

	// Check if source file exists
	if _, err := os.Stat(sourceHookSettings); err != nil {
		if os.IsNotExist(err) {
			ctx := context.Background()
			helpersUlog.Warn("Hook settings file not found").
				Field("settings_path", sourceHookSettings).
				Pretty(theme.IconWarning + "  Warning: Hook settings file not found at " + sourceHookSettings + "\n   Grove hooks will not be configured.").
				Log(ctx)
			return nil
		}
		return fmt.Errorf("failed to check hook settings file: %w", err)
	}

	// Copy hook settings
	sourceBytes, err := os.ReadFile(sourceHookSettings)
	if err != nil {
		return fmt.Errorf("failed to read hook settings from %s: %w", sourceHookSettings, err)
	}

	if err := os.WriteFile(destHookSettings, sourceBytes, 0644); err != nil {
		return fmt.Errorf("failed to write hook settings to %s: %w", destHookSettings, err)
	}

	ctx := context.Background()
	helpersUlog.Success("Configured grove hooks in worktree").
		Field("worktree_path", worktreePath).
		Pretty(theme.IconSuccess + " Configured grove hooks in worktree.").
		Log(ctx)
	return nil
}

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
	if err := os.MkdirAll(groveDir, 0755); err != nil {
		return fmt.Errorf("failed to create .grove directory in %s: %w", repoPath, err)
	}

	// Write the rules to .grove/rules within the target repo.
	if err := os.WriteFile(rulesDestPath, defaultContent, 0644); err != nil {
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

// findGroveEcosystemRoot attempts to find the Grove ecosystem repository root directory
func findGroveEcosystemRoot() (string, error) {
	// Start from current directory and walk up
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %w", err)
	}

	startDir := dir // Remember where we started for error message

	for dir != "" {
		// Check if this is the Grove ecosystem root (has grove-core as a subdirectory)
		corePath := filepath.Join(dir, "grove-core")
		if _, err := os.Stat(corePath); err == nil {
			return dir, nil
		}

		// Check if we've reached the root
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	// If not found from current directory, check environment variable
	if envPath := os.Getenv("GROVE_ECOSYSTEM_ROOT"); envPath != "" {
		corePath := filepath.Join(envPath, "grove-core")
		if _, err := os.Stat(corePath); err == nil {
			return envPath, nil
		}
	}

	// Check common locations
	homeDir, _ := os.UserHomeDir()
	commonPaths := []string{
		filepath.Join(homeDir, "Code", "grove-ecosystem"),
		filepath.Join(homeDir, "code", "grove-ecosystem"),
		filepath.Join(homeDir, "src", "grove-ecosystem"),
		filepath.Join(homeDir, "projects", "grove-ecosystem"),
		"/opt/grove-ecosystem",
		"/usr/local/grove-ecosystem",
	}

	for _, path := range commonPaths {
		corePath := filepath.Join(path, "grove-core")
		if _, err := os.Stat(corePath); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("grove ecosystem root not found (started from %s)", startDir)
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

		if err := os.WriteFile(filepath.Join(worktreePath, "go.work"), []byte(content.String()), 0644); err != nil {
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
