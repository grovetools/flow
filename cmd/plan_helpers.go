package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-core/command"
	"github.com/mattsolo1/grove-core/config"
	"github.com/mattsolo1/grove-core/git"
	"github.com/mattsolo1/grove-core/pkg/workspace"
	"github.com/mattsolo1/grove-core/state"
)

// expandFlowPath expands path variables like {{REPO}} and {{BRANCH}} correctly,
// handling git worktrees properly by differentiating repository name from branch name.
func expandFlowPath(path string) (string, error) {
	// 1. Expand home directory
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("could not get user home directory: %w", err)
		}
		path = filepath.Join(home, path[2:])
	}

	// 2. Expand environment variables
	path = os.ExpandEnv(path)

	// 3. Expand git-related variables with worktree-aware logic
	if strings.Contains(path, "${REPO}") || strings.Contains(path, "{{REPO}}") ||
		strings.Contains(path, "${BRANCH}") || strings.Contains(path, "{{BRANCH}}") {

		gitRoot, err := git.GetGitRoot(".")
		if err == nil {
			// Get the repository name - handle worktrees correctly
			repoName, err := getRepositoryName(".")
			if err != nil {
				// Fallback to git root basename if we can't determine repo name
				repoName = filepath.Base(gitRoot)
			}

			// Get current branch name
			_, branchName, _ := git.GetRepoInfo(".")

			path = strings.ReplaceAll(path, "${REPO}", repoName)
			path = strings.ReplaceAll(path, "{{REPO}}", repoName)
			path = strings.ReplaceAll(path, "${BRANCH}", branchName)
			path = strings.ReplaceAll(path, "{{BRANCH}}", branchName)
		}
	}

	return filepath.Abs(path)
}

// getRepositoryName returns the name of the repository, handling worktrees correctly.
// For worktrees, it finds the main repository directory rather than using the worktree path.
func getRepositoryName(dir string) (string, error) {
	cmdBuilder := command.NewSafeBuilder()

	// Get the common git directory (points to main repo even in worktrees)
	cmd, err := cmdBuilder.Build(context.Background(), "git", "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("failed to build command: %w", err)
	}
	execCmd := cmd.Exec()
	execCmd.Dir = dir
	output, err := execCmd.Output()
	if err != nil {
		return "", fmt.Errorf("get git common dir: %w", err)
	}

	commonDir := strings.TrimSpace(string(output))

	// Convert to absolute path if relative
	if !filepath.IsAbs(commonDir) {
		absDir, err := filepath.Abs(filepath.Join(dir, commonDir))
		if err != nil {
			return "", fmt.Errorf("resolve absolute path: %w", err)
		}
		commonDir = absDir
	}

	// The repository name is the basename of the parent directory of .git
	repoPath := filepath.Dir(commonDir)
	return filepath.Base(repoPath), nil
}

// resolvePlanPath determines the absolute path for a plan directory.
// It uses the new NotebookLocator to support both Local Mode (default) and Centralized Mode (opt-in).
func resolvePlanPath(planName string) (string, error) {
	// If planName is already an absolute path, use it directly.
	if filepath.IsAbs(planName) {
		return planName, nil
	}

	// 1. Get the current workspace node.
	node, err := workspace.GetProjectByPath(".")
	if err != nil {
		// Fallback: if we can't determine workspace, use local directory
		return filepath.Abs(planName)
	}

	// 2. Load config and initialize the locator.
	coreCfg, err := config.LoadDefault()
	if err != nil {
		// Proceed with default config if none exists (Local Mode).
		coreCfg = &config.Config{}
	}
	locator := workspace.NewNotebookLocator(coreCfg)

	// 3. Check if notebooks configuration exists
	// If it does, use it; otherwise fall back to deprecated flow.plans_directory
	// A more robust check to see if any actual notebook configuration is defined.
	hasNotebooksConfig := coreCfg != nil && coreCfg.Notebooks != nil &&
		((coreCfg.Notebooks.Rules != nil && (coreCfg.Notebooks.Rules.Default != "" || (coreCfg.Notebooks.Rules.Global != nil && coreCfg.Notebooks.Rules.Global.RootDir != ""))) ||
			(coreCfg.Notebooks.Definitions != nil && len(coreCfg.Notebooks.Definitions) > 0))

	if !hasNotebooksConfig {
		// No notebooks config, check for deprecated flow.plans_directory configuration
		flowCfg, err := loadFlowConfig()
		if err == nil && flowCfg.PlansDirectory != "" {
			// Legacy configuration detected - use it for backward compatibility
			fmt.Fprintln(os.Stderr, "⚠️  Warning: The 'flow.plans_directory' config is deprecated. Please configure 'notebook.root_dir' in your global grove.yml instead.")
			expandedBasePath, err := expandFlowPath(flowCfg.PlansDirectory)
			if err != nil {
				return "", fmt.Errorf("could not expand plans_directory path: %w", err)
			}
			fullPath := filepath.Join(expandedBasePath, planName)
			return filepath.Abs(fullPath)
		}
	}

	// 4. Get the base plans directory for the current workspace using NotebookLocator.
	plansDir, err := locator.GetPlansDir(node)
	if err != nil {
		return "", fmt.Errorf("could not resolve plans directory: %w", err)
	}

	// 5. Join with the specific plan name.
	fullPath := filepath.Join(plansDir, planName)
	return filepath.Abs(fullPath)
}

// resolveChatsDir determines the absolute path to the chats directory for the current workspace.
// It uses the new NotebookLocator to support both Local Mode (default) and Centralized Mode (opt-in).
func resolveChatsDir() (string, error) {
	// 1. Get the current workspace node.
	node, err := workspace.GetProjectByPath(".")
	if err != nil {
		// Fallback: if we can't determine workspace, use local directory
		return filepath.Abs("chats")
	}

	// 2. Load config and initialize the locator.
	coreCfg, err := config.LoadDefault()
	if err != nil {
		// Proceed with default config if none exists (Local Mode).
		coreCfg = &config.Config{}
	}
	locator := workspace.NewNotebookLocator(coreCfg)

	// 3. Check if notebooks configuration exists
	// If it does, use it; otherwise fall back to deprecated flow.chat_directory
	// A more robust check to see if any actual notebook configuration is defined.
	hasNotebooksConfig := coreCfg != nil && coreCfg.Notebooks != nil &&
		((coreCfg.Notebooks.Rules != nil && (coreCfg.Notebooks.Rules.Default != "" || (coreCfg.Notebooks.Rules.Global != nil && coreCfg.Notebooks.Rules.Global.RootDir != ""))) ||
			(coreCfg.Notebooks.Definitions != nil && len(coreCfg.Notebooks.Definitions) > 0))

	if !hasNotebooksConfig {
		// No notebooks config, check for deprecated flow.chat_directory configuration
		flowCfg, err := loadFlowConfig()
		if err == nil && flowCfg.ChatDirectory != "" {
			// Legacy configuration detected - use it for backward compatibility
			fmt.Fprintln(os.Stderr, "⚠️  Warning: The 'flow.chat_directory' config is deprecated. Please configure 'notebook.root_dir' in your global grove.yml instead.")
			return expandFlowPath(flowCfg.ChatDirectory)
		}
	}

	// 4. Get the chats directory for the current workspace using NotebookLocator.
	chatsDir, err := locator.GetChatsDir(node)
	if err != nil {
		return "", fmt.Errorf("could not resolve chats directory: %w", err)
	}

	return filepath.Abs(chatsDir)
}

// getActivePlanWithMigration gets the active plan and automatically migrates old state format to new format.
func getActivePlanWithMigration() (string, error) {
	// Try new key first
	activePlan, err := state.GetString("flow.active_plan")
	if err != nil {
		return "", err
	}

	if activePlan != "" {
		return activePlan, nil
	}

	// Check for old key
	oldActivePlan, err := state.GetString("active_plan")
	if err != nil {
		return "", err
	}

	if oldActivePlan != "" {
		// Migrate: set new key and delete old key
		if err := state.Set("flow.active_plan", oldActivePlan); err != nil {
			return "", fmt.Errorf("migrate state: %w", err)
		}
		if err := state.Delete("active_plan"); err != nil {
			// Log but don't fail - the new key is set
			fmt.Fprintf(os.Stderr, "Warning: failed to delete old state key: %v\n", err)
		}
		return oldActivePlan, nil
	}

	return "", nil
}

// resolvePlanPathWithActiveJob resolves a plan path, using the active job if no path is provided.
// If no active job is set, it falls back to the "default" plan, creating it if necessary.
func resolvePlanPathWithActiveJob(planName string) (string, error) {
	usingDefaultPlan := false

	// If no plan name provided, try to use active job
	if planName == "" {
		activeJob, err := getActivePlanWithMigration()
		if err != nil {
			return "", fmt.Errorf("get active job: %w", err)
		}
		if activeJob != "" {
			planName = activeJob
		} else {
			// Fallback to the default plan
			planName = "default"
			usingDefaultPlan = true
		}
	}

	// For the default plan, we need to ensure we have a valid workspace context.
	// Don't create "default/" in random directories if workspace detection fails.
	if usingDefaultPlan {
		resolvedPath, err := resolvePlanPathInWorkspace(planName)
		if err != nil {
			return "", fmt.Errorf("cannot use default plan: %w", err)
		}
		if err := ensureDefaultPlanExists(resolvedPath); err != nil {
			return "", fmt.Errorf("ensuring default plan exists: %w", err)
		}
		return resolvedPath, nil
	}

	return resolvePlanPath(planName)
}

// resolvePlanPathInWorkspace resolves a plan path but returns an error if workspace detection fails.
// Unlike resolvePlanPath, it does not fall back to the local directory.
func resolvePlanPathInWorkspace(planName string) (string, error) {
	// If planName is already an absolute path, use it directly.
	if filepath.IsAbs(planName) {
		return planName, nil
	}

	// Get the current workspace node - required for this function
	node, err := workspace.GetProjectByPath(".")
	if err != nil {
		return "", fmt.Errorf("not in a workspace (no git repository found): %w", err)
	}

	// Load config and initialize the locator.
	coreCfg, err := config.LoadDefault()
	if err != nil {
		coreCfg = &config.Config{}
	}
	locator := workspace.NewNotebookLocator(coreCfg)

	// Get the base plans directory for the current workspace using NotebookLocator.
	plansDir, err := locator.GetPlansDir(node)
	if err != nil {
		return "", fmt.Errorf("could not resolve plans directory: %w", err)
	}

	// Join with the specific plan name.
	fullPath := filepath.Join(plansDir, planName)
	return filepath.Abs(fullPath)
}

// ensureDefaultPlanExists checks if the default plan directory exists, creating it if necessary.
func ensureDefaultPlanExists(planPath string) error {
	// Check if the directory already exists
	if _, err := os.Stat(planPath); err == nil {
		return nil // Already exists, nothing to do
	} else if !os.IsNotExist(err) {
		// Another error occurred (e.g., permissions)
		return fmt.Errorf("checking default plan path: %w", err)
	}

	// Directory does not exist, so create it
	if err := os.MkdirAll(planPath, 0755); err != nil {
		return fmt.Errorf("creating default plan directory: %w", err)
	}

	// Create a minimal .grove-plan.yml file
	configPath := filepath.Join(planPath, ".grove-plan.yml")
	configContent := []byte("# Default plan, automatically created by grove-flow.\n")
	if err := os.WriteFile(configPath, configContent, 0644); err != nil {
		return fmt.Errorf("creating default .grove-plan.yml: %w", err)
	}

	// Notify the user on stderr that the default plan is being used for the first time
	fmt.Fprintf(os.Stderr, "No active plan set. Using default plan at: %s\n", planPath)

	return nil
}

// loadFlowConfigWithDynamicRecipes is a helper to load flow config and extract the get_recipe_cmd.
func loadFlowConfigWithDynamicRecipes() (*FlowConfig, string, error) {
	coreCfg, err := config.LoadFrom(".")
	if err != nil {
		coreCfg = &config.Config{}
	}

	// Load the flow section as a generic map to find get_recipe_cmd
	var rawFlowConfig map[string]interface{}
	if err := coreCfg.UnmarshalExtension("flow", &rawFlowConfig); err != nil {
		return nil, "", fmt.Errorf("failed to parse 'flow' configuration: %w", err)
	}

	var getRecipeCmd string
	if recipes, ok := rawFlowConfig["recipes"].(map[string]interface{}); ok {
		if cmd, ok := recipes["get_recipe_cmd"].(string); ok {
			getRecipeCmd = cmd
			// Remove the key so it doesn't interfere with unmarshalling into FlowConfig
			delete(recipes, "get_recipe_cmd")
		}
	}
	
	// Now unmarshal into the typed FlowConfig struct
	var flowCfg FlowConfig
	if err := coreCfg.UnmarshalExtension("flow", &flowCfg); err != nil {
		return nil, "", fmt.Errorf("failed to parse 'flow' configuration into struct: %w", err)
	}
	
	return &flowCfg, getRecipeCmd, nil
}
