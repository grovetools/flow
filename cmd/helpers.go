package cmd

import (
	"fmt"
	"os"
	"path/filepath"
)

// configureCanopyHooks copies the Claude hook settings to a worktree
func configureCanopyHooks(worktreePath string) error {
	// Create .claude directory
	claudeDir := filepath.Join(worktreePath, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		return fmt.Errorf("failed to create .claude directory in worktree: %w", err)
	}
	
	// Find the grove ecosystem root to locate the hook settings
	ecosystemRoot := findGroveEcosystemRoot()
	if ecosystemRoot == "" {
		// No ecosystem root found, skip hook configuration
		return nil
	}
	
	sourceHookSettings := filepath.Join(ecosystemRoot, "grove-canopy", "configs", "claude-hooks-settings.json")
	destHookSettings := filepath.Join(claudeDir, "settings.local.json")
	
	// Copy hook settings if source exists
	if sourceBytes, err := os.ReadFile(sourceHookSettings); err == nil {
		if err := os.WriteFile(destHookSettings, sourceBytes, 0644); err == nil {
			fmt.Printf("✓ Configured canopy hooks in new worktree.\n")
		} else {
			return fmt.Errorf("failed to write hook settings: %w", err)
		}
	}
	
	return nil
}

// findGroveEcosystemRoot attempts to find the Grove ecosystem repository root directory
func findGroveEcosystemRoot() string {
	// Start from current directory and walk up
	dir, _ := os.Getwd()
	
	for dir != "" {
		// Check if this is the Grove ecosystem root (has grove-canopy, grove-core, etc. as subdirectories)
		if _, err := os.Stat(filepath.Join(dir, "grove-canopy")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "grove-core")); err == nil {
				return dir
			}
		}
		
		// Check if we've reached the root
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	
	// If not found from current directory, check the specific known location
	knownPath := "/Users/solom4/Code/grove-ecosystem"
	if _, err := os.Stat(filepath.Join(knownPath, "grove-canopy")); err == nil {
		if _, err := os.Stat(filepath.Join(knownPath, "grove-core")); err == nil {
			return knownPath
		}
	}
	
	return ""
}