package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/grovetools/core/config"
	"github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/sirupsen/logrus"
)

// resolvePlanPath determines the absolute path for a plan directory.
// It uses the NotebookLocator to support both Local Mode (default) and Centralized Mode (opt-in).
// It falls back to a global search if the plan is not found in the current context.
func resolvePlanPath(planName, contextDir string) (string, error) {
	// If planName is already an absolute path, use it directly.
	if filepath.IsAbs(planName) {
		return planName, nil
	}

	// 1. Try direct path relative to the context directory
	directPath := filepath.Join(contextDir, planName)
	if info, err := os.Stat(directPath); err == nil && info.IsDir() {
		return filepath.Abs(directPath)
	}

	// 2. Try resolving via the workspace context
	node, err := workspace.GetProjectByPath(contextDir)
	if err == nil && node != nil {
		coreCfg, err := config.LoadDefault()
		if err != nil {
			coreCfg = &config.Config{}
		}
		locator := workspace.NewNotebookLocator(coreCfg)

		plansDir, err := locator.GetPlansDir(node)
		if err == nil {
			fullPath := filepath.Join(plansDir, planName)
			if info, err := os.Stat(fullPath); err == nil && info.IsDir() {
				return filepath.Abs(fullPath)
			}
		}
	}

	// 3. Fallback: Global search across all workspaces
	matches, err := findPlanGlobally(planName)
	if err != nil {
		return "", fmt.Errorf("error searching globally for plan: %w", err)
	}

	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("multiple plans found named '%s'. Please specify the absolute path or use --dir to specify the workspace", planName)
	}

	// 4. Provide a clear, actionable error message if not found
	return "", fmt.Errorf("plan '%s' not found.\nHint: run from the project directory, or use --dir /path/to/workspace", planName)
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

	// 3. Get the chats directory for the current workspace using NotebookLocator.
	chatsDir, err := locator.GetChatsDir(node)
	if err != nil {
		return "", fmt.Errorf("could not resolve chats directory: %w", err)
	}

	return filepath.Abs(chatsDir)
}

// getActivePlanWithMigration gets the active plan using the shared core/pkg/plan detection.
// This handles state lookup, legacy key migration, and branch-to-plan matching.
func getActivePlanWithMigration() (string, error) {
	name := plan.ActivePlanForPath(".")
	return name, nil
}

// resolvePlanPathWithActiveJob resolves a plan path, using the active job if no path is provided.
// If no active job is set, it falls back to the rolling plan, creating it if necessary.
func resolvePlanPathWithActiveJob(planName, contextDir string) (string, error) {
	usingRollingPlan := false

	// If no plan name provided, try to use active job
	if planName == "" {
		activeJob, err := getActivePlanWithMigration()
		if err != nil {
			return "", fmt.Errorf("get active job: %w", err)
		}
		if activeJob != "" {
			planName = activeJob
		} else {
			// Fallback to the rolling plan
			planName = RollingPlanName
			usingRollingPlan = true
		}
	}

	// For the rolling plan, we need to ensure we have a valid workspace context.
	// Don't create "rolling/" in random directories if workspace detection fails.
	if usingRollingPlan {
		resolvedPath, err := resolvePlanPathInWorkspace(planName, contextDir)
		if err != nil {
			return "", fmt.Errorf("cannot use rolling plan: %w", err)
		}
		if err := ensureRollingPlanExists(resolvedPath); err != nil {
			return "", fmt.Errorf("ensuring rolling plan exists: %w", err)
		}
		return resolvedPath, nil
	}

	return resolvePlanPath(planName, contextDir)
}

// resolvePlanPathInWorkspace resolves a plan path but returns an error if workspace detection fails.
// Unlike resolvePlanPath, it does not fall back to the local directory or global search.
func resolvePlanPathInWorkspace(planName, contextDir string) (string, error) {
	// If planName is already an absolute path, use it directly.
	if filepath.IsAbs(planName) {
		return planName, nil
	}

	// Get the workspace node for the context directory
	node, err := workspace.GetProjectByPath(contextDir)
	if err != nil {
		return "", fmt.Errorf("not in a workspace (no git repository found at %s): %w", contextDir, err)
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

// RollingPlanName is the name of the auto-created rolling plan used when no plan is specified.
const RollingPlanName = "rolling"

// ensureRollingPlanExists checks if the rolling plan directory exists, creating it if necessary.
func ensureRollingPlanExists(planPath string) error {
	// Check if the directory already exists
	if _, err := os.Stat(planPath); err == nil {
		return nil // Already exists, nothing to do
	} else if !os.IsNotExist(err) {
		// Another error occurred (e.g., permissions)
		return fmt.Errorf("checking rolling plan path: %w", err)
	}

	// Directory does not exist, so create it
	if err := os.MkdirAll(planPath, 0o755); err != nil {
		return fmt.Errorf("creating rolling plan directory: %w", err)
	}

	// Create a minimal .grove-plan.yml file
	configPath := filepath.Join(planPath, ".grove-plan.yml")
	configContent := []byte("# Rolling plan - auto-created for quick tasks without a formal plan.\n")
	if err := os.WriteFile(configPath, configContent, 0o600); err != nil {
		return fmt.Errorf("creating rolling plan .grove-plan.yml: %w", err)
	}

	// Notify the user on stderr that the rolling plan is being used for the first time
	fmt.Fprintf(os.Stderr, "No active plan set. Using rolling plan at: %s\n", planPath)

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

// findPlanGlobally searches all known workspaces on the system for a plan matching the given slug.
// It returns a list of absolute paths to all matching plan directories.
func findPlanGlobally(slug string) ([]string, error) {
	var matches []string
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel) // Suppress debug output

	// Discover all workspaces
	discoverer := workspace.NewDiscoveryService(logger)
	result, err := discoverer.DiscoverAll()
	if err != nil {
		return nil, fmt.Errorf("failed to discover workspaces: %w", err)
	}
	provider := workspace.NewProvider(result)

	// Load global config
	coreCfg, err := config.LoadDefault()
	if err != nil {
		coreCfg = &config.Config{}
	}
	locator := workspace.NewNotebookLocator(coreCfg)

	// Scan all workspaces for their plan directories
	scannedDirs, err := locator.ScanForAllPlans(provider)
	if err != nil {
		return nil, fmt.Errorf("failed to scan for plans: %w", err)
	}

	// Check if the slug exists inside any of the discovered plan base directories
	for _, scannedDir := range scannedDirs {
		planPath := filepath.Join(scannedDir.Path, slug)
		if info, err := os.Stat(planPath); err == nil && info.IsDir() {
			matches = append(matches, planPath)
		}
	}

	return matches, nil
}
