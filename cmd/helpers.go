package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-context/pkg/context"
	"github.com/mattsolo1/grove-core/pkg/workspace"
	"golang.org/x/mod/modfile"
)

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
		fmt.Printf("⚠️  Warning: Could not find grove ecosystem root: %v\n", err)
		fmt.Printf("   Grove hooks will not be configured.\n")
		return nil
	}

	sourceHookSettings := filepath.Join(ecosystemRoot, "grove-hooks", "configs", "claude-hooks-settings.json")
	destHookSettings := filepath.Join(claudeDir, "settings.local.json")

	// Check if source file exists
	if _, err := os.Stat(sourceHookSettings); err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("⚠️  Warning: Hook settings file not found at %s\n", sourceHookSettings)
			fmt.Printf("   Grove hooks will not be configured.\n")
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

	fmt.Printf("✓ Configured grove hooks in worktree.\n")
	return nil
}

// configureDefaultContextRules applies default context rules to a given repository path.
func configureDefaultContextRules(repoPath string) error {
	// Create a context manager scoped to the repository path. This is crucial
	// for it to find the correct grove.yml for that specific repository.
	mgr := context.NewManager(repoPath)

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

	fmt.Printf("✓ Applied default context rules to: %s\n", repoPath)
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
	if len(repos) > 0 {
		// Case 1: Ecosystem worktree (multiple repos inside).
		// First, check if any of the repos are Go modules.
		var goRepos []string
		for _, repo := range repos {
			repoGoModPath := filepath.Join(worktreePath, repo, "go.mod")
			if _, err := os.Stat(repoGoModPath); err == nil {
				goRepos = append(goRepos, repo)
			}
		}

		// Only create go.work if we found at least one Go module.
		if len(goRepos) == 0 {
			return nil
		}

		var content strings.Builder
		content.WriteString("go 1.24.4\n\n") // Using Go version from project's go.mod
		content.WriteString("use (\n")
		content.WriteString("\t.\n") // The root of an ecosystem worktree can also be a module.
		for _, repo := range goRepos {
			content.WriteString(fmt.Sprintf("\t./%s\n", repo))
		}
		content.WriteString(")\n")

		goWorkPath := filepath.Join(worktreePath, "go.work")
		if err := os.WriteFile(goWorkPath, []byte(content.String()), 0644); err != nil {
			return fmt.Errorf("failed to write go.work for ecosystem worktree: %w", err)
		}
		fmt.Printf("✓ Configured go.work in ecosystem worktree with %d Go modules.\n", len(goRepos))
	} else {
		// Case 2: Single-repo worktree.
		// Parse its go.mod to find local dependencies within the ecosystem.
		goModPath := filepath.Join(worktreePath, "go.mod")
		if _, err := os.Stat(goModPath); os.IsNotExist(err) {
			// Not a Go module, so there's nothing to do.
			return nil
		}

		if provider == nil {
			fmt.Printf("⚠️  Warning: cannot generate go.work, workspace provider not available.\n")
			return nil
		}

		content, err := os.ReadFile(goModPath)
		if err != nil {
			return fmt.Errorf("failed to read go.mod in worktree: %w", err)
		}

		modFile, err := modfile.Parse(goModPath, content, nil)
		if err != nil {
			return fmt.Errorf("failed to parse go.mod: %w", err)
		}

		// Find the worktree's workspace node to determine its ecosystem
		worktreeNode := provider.FindByPath(worktreePath)
		if worktreeNode == nil {
			return nil // Worktree not found in workspace discovery
		}

		// Get the root ecosystem path for this worktree
		ecosystemRoot := worktreeNode.RootEcosystemPath
		if ecosystemRoot == "" {
			return nil // Not part of an ecosystem
		}

		// Get workspaces from the same ecosystem (avoids name collisions with other ecosystems)
		localWorkspaces := provider.LocalWorkspacesInEcosystem(ecosystemRoot)
		if len(localWorkspaces) == 0 {
			return nil // No other local repos to link to.
		}

		// Find which of the go.mod dependencies are present locally.
		var localDeps []string
		modulePrefix := "github.com/mattsolo1/" // The common module prefix for the ecosystem.

		for _, require := range modFile.Require {
			if strings.HasPrefix(require.Mod.Path, modulePrefix) {
				// This is an ecosystem module. Check if we have it locally.
				repoName := strings.TrimPrefix(require.Mod.Path, modulePrefix)
				if localPath, ok := localWorkspaces[repoName]; ok {
					localDeps = append(localDeps, localPath)
				}
			}
		}

		// If we found local dependencies, generate the go.work file.
		if len(localDeps) > 0 {
			var goWorkContent strings.Builder
			goWorkContent.WriteString("go 1.24.4\n\n")
			goWorkContent.WriteString("use (\n")
			goWorkContent.WriteString("\t.\n") // Include the current worktree itself.

			for _, depPath := range localDeps {
				goWorkContent.WriteString(fmt.Sprintf("\t%s\n", depPath))
			}
			goWorkContent.WriteString(")\n")

			goWorkPath := filepath.Join(worktreePath, "go.work")
			if err := os.WriteFile(goWorkPath, []byte(goWorkContent.String()), 0644); err != nil {
				return fmt.Errorf("failed to write go.work for single-repo worktree: %w", err)
			}
			fmt.Printf("✓ Configured go.work with %d local dependencies.\n", len(localDeps))
		}
	}
	return nil
}
