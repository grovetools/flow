package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/grovetools/core/config"
	"github.com/grovetools/core/pkg/coderoot"
	"github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/sirupsen/logrus"
)

var defaultNotebookEnsureTimeout = 15 * time.Second

// ensureDefaultNotebookFn runs nb's lazy default-notebook materialization
// (W1.11). Package-level var so tests can stub the exec.
var ensureDefaultNotebookFn = runNbEnsureNotebook

// notebookMaterializationResult is Flow's copy of nb's stable JSON protocol.
// Keeping every currently-defined field and rejecting unknown fields prevents
// human logger output or a silently changed protocol from being mistaken for
// a successful materialization.
type notebookMaterializationResult struct {
	NotebookName  string `json:"notebook_name"`
	RootDir       string `json:"root_dir"`
	Created       bool   `json:"created"`
	MarkerCreated bool   `json:"marker_created"`
	Recorded      bool   `json:"recorded"`
	ConfigPath    string `json:"config_path,omitempty"`
}

// ensureDefaultNotebook materializes + records the default notebook ahead of
// a note-writing action by delegating to `nb internal ensure-notebook`. Any
// execution or protocol failure is fatal: continuing would let plan lookup
// guess a legacy location after materialization failed.
func ensureDefaultNotebook(ctx context.Context) error {
	stdout, stderr, err := ensureDefaultNotebookFn(ctx)
	if err != nil {
		detail := strings.TrimSpace(string(stderr))
		if detail != "" {
			return fmt.Errorf("ensure default notebook with nb: %w; stderr: %s", err, detail)
		}
		return fmt.Errorf("ensure default notebook with nb: %w", err)
	}

	res, err := decodeNotebookMaterialization(stdout)
	if err != nil {
		detail := strings.TrimSpace(string(stderr))
		if detail != "" {
			return fmt.Errorf("invalid nb ensure-notebook protocol: %w; stderr: %s", err, detail)
		}
		return fmt.Errorf("invalid nb ensure-notebook protocol: %w", err)
	}

	if res != nil {
		if !res.Created && !res.MarkerCreated && !res.Recorded {
			return errors.New("invalid nb ensure-notebook protocol: non-null result reports no materialization action")
		}
		if strings.TrimSpace(res.RootDir) == "" {
			return errors.New("invalid nb ensure-notebook protocol: materialization result has an empty root_dir")
		}
	}

	// nb's --json contract reserves stdout exclusively for the JSON document.
	// Successful warning/log output remains on stderr so the parent process's
	// log stream can retain its attention semantics.
	if len(stderr) > 0 {
		_, _ = os.Stderr.Write(stderr)
	}
	if res != nil {
		// The recording changed on-disk config after this process may have
		// already memoized a load; drop the cache so the locator resolves
		// against the recorded root.
		config.ResetLoadCache()
		fmt.Fprintf(os.Stderr, "Materialized default notebook at %s\n", res.RootDir)
	}
	return nil
}

func decodeNotebookMaterialization(stdout []byte) (*notebookMaterializationResult, error) {
	if len(bytes.TrimSpace(stdout)) == 0 {
		return nil, errors.New("empty stdout (expected one JSON document)")
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout))
	decoder.DisallowUnknownFields()
	var res *notebookMaterializationResult
	if err := decoder.Decode(&res); err != nil {
		return nil, fmt.Errorf("decode stdout %q: %w", strings.TrimSpace(string(stdout)), err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("stdout contains more than one JSON document: %q", strings.TrimSpace(string(stdout)))
		}
		return nil, fmt.Errorf("trailing stdout after JSON document: %w", err)
	}
	return res, nil
}

func runNbEnsureNotebook(parent context.Context) ([]byte, []byte, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, defaultNotebookEnsureTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "nb", "internal", "ensure-notebook", "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return stdout.Bytes(), stderr.Bytes(), fmt.Errorf("nb ensure-notebook timed out after %s: %w", defaultNotebookEnsureTimeout, ctxErr)
		}
		return stdout.Bytes(), stderr.Bytes(), fmt.Errorf("nb ensure-notebook canceled: %w", ctxErr)
	}
	if err != nil {
		return stdout.Bytes(), stderr.Bytes(), fmt.Errorf("nb ensure-notebook exited unsuccessfully: %w", err)
	}
	return stdout.Bytes(), stderr.Bytes(), nil
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

// loadPlanNotebookConfig reloads both the layered config and the authoritative
// recorded notebook table. Keeping this projection here is necessary on the
// W1.11 baseline, before core's later compatibility-view compile cutover.
func loadPlanNotebookConfig() (*config.Config, error) {
	cfg, err := config.LoadDefault()
	if err != nil {
		return nil, err
	}
	table, err := coderoot.Load()
	if err != nil {
		return nil, err
	}
	return applyRecordedNotebooks(cfg, table), nil
}

func applyRecordedNotebooks(cfg *config.Config, table coderoot.Table) *config.Config {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if table.NotebooksFilePath == "" {
		return cfg
	}
	definitions := make(map[string]*config.Notebook, len(table.Notebooks))
	for _, name := range table.SortedNotebookNames() {
		definitions[name] = &config.Notebook{RootDir: table.NotebookRoot(name)}
	}
	cfg.Notebooks = &config.NotebooksConfig{
		Definitions: definitions,
		Rules:       &config.NotebookRules{Default: table.Default},
	}
	return cfg
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

	// Load config and explicitly project the authoritative notebooks.toml
	// table. The lazy-materialization baseline predates core's compatibility
	// compile cutover, so relying on Config.LoadDefault alone could route this
	// first write through a legacy fallback even after nb recorded the root.
	coreCfg, err := loadPlanNotebookConfig()
	if err != nil {
		return "", fmt.Errorf("could not reload recorded notebook routing: %w", err)
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
