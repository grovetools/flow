package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-core/config"
	"github.com/mattsolo1/grove-core/git"
	"github.com/grovepm/grove-jobs/pkg/state"
)

// resolvePlanPath determines the absolute path for a plan directory.
// If config.Orchestration.PlansDirectory is set, it's used as the base.
// Otherwise, the planName is treated as a path relative to the current directory.
func resolvePlanPath(planName string, cfg *config.Config) (string, error) {
	if cfg == nil || cfg.Orchestration.PlansDirectory == "" {
		// No custom directory configured, use the provided name as-is.
		return filepath.Abs(planName)
	}

	// A custom plans directory is configured.
	basePath := cfg.Orchestration.PlansDirectory

	// 1. Expand home directory character '~'.
	if strings.HasPrefix(basePath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("could not get user home directory: %w", err)
		}
		basePath = filepath.Join(home, basePath[2:])
	}

	// 2. Expand git-related variables.
	repo, branch, err := git.GetRepoInfo(".")
	if err != nil {
		// Don't fail, just proceed without these variables.
		fmt.Printf("Warning: could not get git info for path expansion: %v\n", err)
	} else {
		// Support both ${VAR} and {{VAR}} patterns
		basePath = strings.ReplaceAll(basePath, "${REPO}", repo)
		basePath = strings.ReplaceAll(basePath, "${BRANCH}", branch)
		basePath = strings.ReplaceAll(basePath, "{{REPO}}", repo)
		basePath = strings.ReplaceAll(basePath, "{{BRANCH}}", branch)
	}

	// 3. Join the base path with the plan name.
	fullPath := filepath.Join(basePath, planName)

	return filepath.Abs(fullPath)
}

// resolvePlanPathWithActiveJob resolves a plan path, using the active job if no path is provided.
func resolvePlanPathWithActiveJob(planName string, cfg *config.Config) (string, error) {
	// If no plan name provided, try to use active job
	if planName == "" {
		activeJob, err := state.GetActiveJob()
		if err != nil {
			return "", fmt.Errorf("get active job: %w", err)
		}
		if activeJob == "" {
			return "", fmt.Errorf("no plan directory specified and no active job set (use 'grove jobs set <plan-directory>' to set one)")
		}
		planName = activeJob
	}
	
	return resolvePlanPath(planName, cfg)
}