package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mattsolo1/grove-context/pkg/context"
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
