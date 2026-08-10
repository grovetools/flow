package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/grovetools/core/config"
	"github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/sirupsen/logrus"
)

// ensureDefaultNotebookFn runs nb's lazy default-notebook materialization
// (W1.11). Package-level var so tests can stub the exec.
var ensureDefaultNotebookFn = runNbEnsureNotebook

// ensureDefaultNotebook materializes + records the default notebook ahead of
// a note-writing action by delegating to `nb internal ensure-notebook` — nb
// owns the pass (create root, record it at creation time, announce through
// the attention log stream; no prompt). Best-effort: a missing or failing nb
// binary leaves plan creation on the resolver's existing fallback behavior.
func ensureDefaultNotebook() {
	out, err := ensureDefaultNotebookFn()
	if err != nil {
		return
	}
	// The result is the LAST line of stdout: nb's unified logger writes its
	// pretty announcement to stdout ahead of the JSON document.
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	payload := bytes.TrimSpace(lines[len(lines)-1])
	var res struct {
		Created  bool   `json:"created"`
		Recorded bool   `json:"recorded"`
		RootDir  string `json:"root_dir"`
	}
	if json.Unmarshal(payload, &res) != nil {
		return
	}
	if res.Created || res.Recorded {
		// The recording changed on-disk config after this process may have
		// already memoized a load; drop the cache so the locator resolves
		// against the recorded root.
		config.ResetLoadCache()
		fmt.Fprintf(os.Stderr, "Materialized default notebook at %s\n", res.RootDir)
	}
}

func runNbEnsureNotebook() ([]byte, error) {
	return exec.Command("nb", "internal", "ensure-notebook", "--json").Output()
}

// resolvePlanPathCtx is the context-aware front door to resolvePlanPath. When a
// unified `--at` target is present on ctx it short-circuits to the target's
// plan dir; otherwise it falls back to the existing cwd/global resolution
// 100% untouched. This LAYERS on top of the legacy resolver — nothing is
// stripped.
func resolvePlanPathCtx(ctx context.Context, planName, contextDir string) (string, error) {
	if target, ok := TargetFromContext(ctx); ok && target.PlanDir != "" {
		return target.PlanDir, nil
	}
	return resolvePlanPath(planName, contextDir)
}

// resolvePlanPathWithActiveJobCtx is the context-aware front door to
// resolvePlanPathWithActiveJob. Same layering contract as resolvePlanPathCtx.
func resolvePlanPathWithActiveJobCtx(ctx context.Context, planName, contextDir string) (string, error) {
	if target, ok := TargetFromContext(ctx); ok && target.PlanDir != "" {
		return target.PlanDir, nil
	}
	return resolvePlanPathWithActiveJob(planName, contextDir)
}

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
		return "", fmt.Errorf("multiple plans found named '%s'. Please specify the absolute path or use --at to target the plan", planName)
	}

	// 4. Provide a clear, actionable error message if not found
	return "", fmt.Errorf("plan '%s' not found.\nHint: run from the project directory, or use --at <plan-name>", planName)
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

// stateDir returns the directory whose ecosystem owns the active-plan state for
// the current command invocation: the process working directory. core/state
// resolves this up to its ecosystem/worktree root, so reads/writes from $HOME
// (no ecosystem) are graceful-empty / refused respectively.
func stateDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

// resolvePlanPathWithActiveJob resolves a plan path, using the active job if no path is provided.
// If no active job is set, it falls back to the rolling plan, creating it if necessary.
func resolvePlanPathWithActiveJob(planName, contextDir string) (string, error) {
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
			planName = plan.RollingPlanName
		}
	}

	// Self-heal the rolling plan whenever it's the resolved plan — whether it
	// arrived via the empty-name fallback above OR was already the active plan
	// (state/branch/registry). EnsureRollingPlan requires a valid workspace, so
	// we never create "rolling/" in a random directory. created==true means we
	// just materialized it for the first time, so notify the user once.
	if planName == plan.RollingPlanName {
		dir, created, err := plan.EnsureRollingPlan(contextDir)
		if err != nil {
			return "", fmt.Errorf("cannot use rolling plan: %w", err)
		}
		if created {
			fmt.Fprintf(os.Stderr, "No active plan set. Using rolling plan at: %s\n", dir)
		}
		return dir, nil
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
	flowCfg, err := orchestration.FlowConfigFrom(coreCfg)
	if err != nil {
		return nil, "", err
	}

	return flowCfg, getRecipeCmd, nil
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
