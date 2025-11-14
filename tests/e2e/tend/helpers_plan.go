package main

import (
	"github.com/mattsolo1/grove-tend/pkg/command"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// setupPlanInExpectedLocation creates a plan in the location that flow expects,
// taking into account any global plans_directory configuration.
func setupPlanInExpectedLocation(ctx *harness.Context, planName string) (string, error) {
	// First, check if plans_directory is configured globally
	groveConfig := filepath.Join(ctx.RootDir, "grove.yml")
	if fs.Exists(groveConfig) {
		content, err := fs.ReadString(groveConfig)
		if err != nil {
			return "", fmt.Errorf("failed to read grove.yml: %w", err)
		}
		
		// Simple check for plans_directory config
		// In production, we'd use proper YAML parsing
		if contains(content, "plans_directory:") {
			// Create a local grove.yml without plans_directory for this test
			localConfig := `flow:
  target_agent_container: fake-container`
			if err := fs.WriteString(groveConfig, localConfig); err != nil {
				return "", fmt.Errorf("failed to write local grove.yml: %w", err)
			}
		}
	}
	
	// Initialize the plan in the expected location
	flow, err := getFlowBinary()
	if err != nil {
		return "", err
	}
	
	cmd := ctx.Command(flow, "plan", "init", planName).Dir(ctx.RootDir)
	result := cmd.Run()
	if result.Error != nil {
		return "", fmt.Errorf("failed to init plan: %w", result.Error)
	}
	
	// Parse the actual plan path from the output
	// Look for "Initializing orchestration plan in:" followed by the path
	lines := strings.Split(result.Stdout, "\n")
	for i, line := range lines {
		if strings.Contains(line, "Initializing orchestration plan in:") && i+1 < len(lines) {
			planPath := strings.TrimSpace(lines[i+1])
			return planPath, nil
		}
	}
	
	// Fallback to expected path
	return resolvePlanPath(ctx, planName), nil
}

// resolvePlanPath determines where a plan should be located based on the current configuration
func resolvePlanPath(ctx *harness.Context, planName string) string {
	// By default, plans are created relative to the current directory
	return filepath.Join(ctx.RootDir, planName)
}

// getPlanAbsolutePath gets the absolute path for a plan, handling different working directories
func getPlanAbsolutePath(ctx *harness.Context, planPath string) (string, error) {
	if filepath.IsAbs(planPath) {
		return planPath, nil
	}
	
	// Try relative to root dir first
	absPath := filepath.Join(ctx.RootDir, planPath)
	if fs.Exists(absPath) {
		return absPath, nil
	}
	
	// Try relative to git root (if different from root dir)
	gitRoot, err := getGitRoot(ctx.RootDir)
	if err == nil && gitRoot != ctx.RootDir {
		gitPath := filepath.Join(gitRoot, planPath)
		if fs.Exists(gitPath) {
			return gitPath, nil
		}
	}
	
	return "", fmt.Errorf("plan not found at %s", planPath)
}

// getGitRoot finds the git repository root from a given directory
func getGitRoot(dir string) (string, error) {
	cmd := command.New("git", "rev-parse", "--show-toplevel").Dir(dir)
	result := cmd.Run()
	if result.Error != nil {
		return "", result.Error
	}
	return filepath.Clean(result.Stdout), nil
}

// createTestGroveConfig creates a minimal grove.yml for testing without global plans_directory
func createTestGroveConfig(ctx *harness.Context) error {
	config := `flow:
  target_agent_container: fake-container
  plans_directory: ./plans`
	return fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), config)
}

// setupEmptyGlobalConfig creates an empty global grove config in the sandboxed XDG_CONFIG_HOME.
// This is required when using ctx.Command() which sandboxes the config directory.
// Should be called after git.Commit() in test setup steps.
func setupEmptyGlobalConfig(ctx *harness.Context) error {
	globalConfigDir := filepath.Join(ctx.ConfigDir(), "grove")
	if err := fs.CreateDir(globalConfigDir); err != nil {
		return err
	}
	emptyGlobalConfig := "version: \"1.0\"\n"
	return fs.WriteString(filepath.Join(globalConfigDir, "grove.yml"), emptyGlobalConfig)
}

// contains is a simple string contains helper
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}