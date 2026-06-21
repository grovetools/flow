// Package plan_finish owns the cleanup-action factory that powers
// both the `flow plan finish` CLI command and the in-TUI finish
// wizard launched from the flow view meta-panel.
//
// The factory builds a list of finish.Item values (from
// flow/pkg/tui/wizards/finish) with Check and Action closures bound
// to an explicit BuildContext and Options. Hosts — the CLI wrapper
// and the view meta-panel — construct these dependencies, call
// BuildItems, present the returned items to the user (either
// interactively or not), and then invoke Action on each enabled
// item.
//
// The package intentionally has no package-level globals: everything
// is carried on Options / BuildContext so multiple callers can run
// the factory concurrently with different settings.
package plan_finish

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/fatih/color"
	"github.com/grovetools/core/fs"
	"github.com/grovetools/core/git"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/env"
	"github.com/grovetools/core/pkg/mux"
	"github.com/grovetools/core/pkg/paths"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/pkg/worktreeregistry"
	"github.com/grovetools/core/util/pathutil"
	"github.com/grovetools/core/util/sanitize"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"

	gexec "github.com/grovetools/flow/pkg/exec"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/tui/wizards/finish"
)

// Options carries the cleanup toggles that used to live as CLI flag
// globals in flow/cmd/plan_finish.go. Both the CLI path and the view
// meta-panel populate an Options and pass it to BuildItems; the
// factory consults these values inside the Check and Action closures
// it constructs.
type Options struct {
	// Force causes destructive git operations (worktree remove,
	// branch delete) to use --force equivalents. When false, the
	// factory's Action closures refuse to fall back to destructive
	// commands even if the safe variant fails — callers must re-run
	// with Force=true (or toggle it in the wizard) to confirm.
	Force bool
	// KeepEnv skips environment teardown entirely — the env-teardown
	// item reports "Skipped" and its Action is a no-op.
	KeepEnv bool
	// KeepWorktree is accepted for parity with the CLI --keep-worktree
	// flag. It is not consulted by any closure today; callers that
	// want to suppress the prune-worktree item should leave its
	// IsEnabled at false.
	KeepWorktree bool
	// Yes enables every available item by default. Hosts still
	// choose whether to present a confirmation prompt. Used by the
	// CLI --yes flag.
	Yes bool
	// DeleteBranch enables the "delete local git branch" and
	// "delete submodule branches" items when they are available.
	DeleteBranch bool
	// DeleteRemote enables the "delete remote git branch" item.
	DeleteRemote bool
	// PruneWorktree enables the "prune git worktree" item.
	PruneWorktree bool
	// CloseSession enables the "close tmux session" item.
	CloseSession bool
	// Archive enables the "archive plan directory" item.
	Archive bool
	// CleanDevLinks enables the "clean up dev binaries" item.
	CleanDevLinks bool
	// RebuildBinaries enables the "rebuild main repo binaries" item.
	RebuildBinaries bool
	// PruneOrphans enables the "prune orphan resources" item that
	// shells out to `grove env prune --worktree <slug> --yes` after
	// env teardown. Safe local cleanup; cloud cleanup is gated on
	// PruneCloud below.
	PruneOrphans bool
	// PruneCloud additionally forwards --include-cloud to the prune
	// call, destroying per-worktree cloud resources (Cloud Run, GCE,
	// AR tags, GCS state). Only effective when PruneOrphans is true.
	PruneCloud bool
	// PreserveCloud keeps `skip_destroy=true` semantics during env
	// teardown at plan-finish time. Default is false — plan-finish
	// retires the worktree, so cloud resources that were
	// semi-persistent across iteration should now be destroyed. Set
	// this flag in the rare case that the caller wants to iterate
	// again across finish (e.g. recovery of a legacy plan slug).
	PreserveCloud bool
}

// Stable item identifiers. Hosts look items up by these constants
// via Items.ByID instead of depending on slice indexes.
const (
	ItemEnvTeardown             = "env_teardown"
	ItemMergeSubmodules         = "merge_submodules"
	ItemMarkFinished            = "mark_finished"
	ItemKillBoundAgents         = "kill_bound_agents"
	ItemCloseSession            = "close_session"
	ItemPruneWorktree           = "prune_worktree"
	ItemCleanDevLinks           = "clean_dev_links"
	ItemDeleteSubmoduleBranches = "delete_submodule_branches"
	ItemDeleteLocalBranch       = "delete_local_branch"
	ItemDeleteRemoteBranch      = "delete_remote_branch"
	ItemRebuildBinaries         = "rebuild_binaries"
	ItemArchivePlan             = "archive_plan"
	ItemPruneOrphans            = "prune_orphans"
)

// ItemsByID returns the first item matching the given ID, or nil if
// no such item exists in the slice.
func ItemsByID(items []*finish.Item, id string) *finish.Item {
	for _, it := range items {
		if it != nil && it.ID == id {
			return it
		}
	}
	return nil
}

// BuildContext bundles the explicit dependencies the cleanup closures
// need. runPlanFinish used to build these as local variables inside
// the command function; now callers populate a BuildContext (usually
// via NewBuildContext) and pass it to BuildItems.
type BuildContext struct {
	// PlanPath is the absolute path to the plan directory. Required.
	PlanPath string
	// Plan is the loaded plan. Required for ecosystem-worktree
	// cleanup (reads plan.Config.Repos).
	Plan *orchestration.Plan
	// GitRoot is the git root of the workspace the plan lives in.
	// Empty disables all git-related items.
	GitRoot string
	// WorktreeName is the plan's worktree (== branch) name. Empty
	// disables worktree-related items.
	WorktreeName string
	// BranchName defaults to WorktreeName when built via
	// NewBuildContext. Kept separate for future flexibility.
	BranchName string
	// SessionName is the sanitized tmux session name.
	SessionName string
	// Provider is the workspace provider used for ecosystem
	// submodule discovery. May be nil if discovery failed.
	Provider *workspace.Provider
	// Executor is used to run all git/tmux/make subcommands.
	Executor gexec.CommandExecutor
	// WM is used by the prune-worktree item to enumerate worktrees.
	WM *git.WorktreeManager
}

// Result is what BuildItems returns: the populated item list plus the
// branch-merge metadata the wizard header uses.
type Result struct {
	// Items is the list of cleanup items in display order, with
	// Status populated (from Check) and IsAvailable set. IsEnabled
	// is left at false — callers apply their own enable policy.
	Items []*finish.Item
	// BranchIsMerged is true if WorktreeName is merged into
	// main/master. Used to color the wizard header.
	BranchIsMerged bool
	// BranchExists is true if WorktreeName exists as a local ref.
	BranchExists bool
}

// NewBuildContext constructs a BuildContext by doing the same
// workspace discovery / git-root resolution / session-name
// sanitization runPlanFinish used to do inline. Callers that already
// have these values can populate BuildContext manually instead.
//
// The plan must already be loaded. GitRoot is resolved from the
// current working directory, matching runPlanFinish's behavior.
func NewBuildContext(plan *orchestration.Plan, planPath string) BuildContext {
	bctx := BuildContext{
		PlanPath: planPath,
		Plan:     plan,
		Executor: &gexec.RealCommandExecutor{},
		WM:       git.NewWorktreeManager(),
	}
	if cwd, err := os.Getwd(); err == nil {
		if root, gerr := git.GetGitRoot(cwd); gerr == nil {
			bctx.GitRoot = root
		}
	}
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)
	disc := workspace.NewDiscoveryService(logger)
	if result, err := disc.DiscoverAll(); err == nil && result != nil {
		bctx.Provider = workspace.NewProvider(result)
	}
	if plan != nil && plan.Config != nil {
		bctx.WorktreeName = plan.Config.Worktree
	}
	bctx.BranchName = bctx.WorktreeName
	bctx.SessionName = sanitize.SanitizeForTmuxSession(bctx.WorktreeName)
	return bctx
}

// BuildItems constructs the cleanup-item list. All Check closures are
// invoked before return so Status and IsAvailable are populated. The
// returned Result also carries branchIsMerged/branchExists for the
// wizard header.
func BuildItems(bctx BuildContext, opts Options) (*Result, error) {
	if bctx.Plan == nil {
		return nil, fmt.Errorf("plan_finish: BuildContext.Plan is required")
	}
	if bctx.PlanPath == "" {
		return nil, fmt.Errorf("plan_finish: BuildContext.PlanPath is required")
	}
	if bctx.Executor == nil {
		bctx.Executor = &gexec.RealCommandExecutor{}
	}
	if bctx.WM == nil {
		bctx.WM = git.NewWorktreeManager()
	}

	planPath := bctx.PlanPath
	plan := bctx.Plan
	worktreeName := bctx.WorktreeName
	gitRoot := bctx.GitRoot
	branchName := bctx.BranchName
	sessionName := bctx.SessionName
	provider := bctx.Provider
	executor := bctx.Executor
	wm := bctx.WM

	// Shared variable for the merge item's Check to populate repo
	// details; copied onto mergeItem.Details after Check runs.
	var sharedRepoDetails []finish.RepoStatus

	// Determine worktree path for state lookups. Use the registry-aware
	// resolver so anchored worktrees (under a sub-repo's XDG base) are
	// found. A miss leaves worktreePath empty, which routes state lookups
	// to the legacy plan-local fallback.
	var worktreePath string
	if worktreeName != "" && gitRoot != "" {
		if found, ok := resolveContainerWorktreePath(gitRoot, worktreeName, provider); ok {
			worktreePath = found
		}
	}

	findEnvState := func() (data []byte, statePath, worktreeStateDir string) {
		if worktreePath != "" {
			wsStateDir := filepath.Join(worktreePath, ".grove", "env")
			wsStatePath := filepath.Join(wsStateDir, "state.json")
			if d, err := os.ReadFile(wsStatePath); err == nil {
				return d, wsStatePath, wsStateDir
			}
		}
		legacyPath := filepath.Join(planPath, ".env_state.json")
		if d, err := os.ReadFile(legacyPath); err == nil {
			return d, legacyPath, ""
		}
		return nil, "", ""
	}

	planSlug := filepath.Base(planPath)

	envTeardownItem := &finish.Item{
		ID:   ItemEnvTeardown,
		Name: "Teardown environment resources",
		Check: func() (string, error) {
			if opts.KeepEnv {
				return "Skipped (--keep-env)", nil
			}
			data, _, _ := findEnvState()
			if data == nil {
				return "N/A", nil
			}
			var stateFile env.EnvStateFile
			if err := json.Unmarshal(data, &stateFile); err != nil {
				return "N/A", nil
			}
			expectedOwner := fmt.Sprintf("plan:%s", planSlug)
			if stateFile.ManagedBy == "user" {
				return color.YellowString("Active (user-managed, skipping)"), nil
			}
			if stateFile.ManagedBy != "" && stateFile.ManagedBy != expectedOwner {
				return color.YellowString("Active (managed by %s, skipping)", stateFile.ManagedBy), nil
			}
			return color.YellowString("Active (%s)", stateFile.Provider), nil
		},
		Action: func() error {
			if opts.KeepEnv {
				return nil
			}
			data, statePath, wsStateDir := findEnvState()
			if data == nil {
				return nil
			}
			var stateFile env.EnvStateFile
			if err := json.Unmarshal(data, &stateFile); err != nil {
				return fmt.Errorf("failed to parse env state: %w", err)
			}
			expectedOwner := fmt.Sprintf("plan:%s", planSlug)
			if stateFile.ManagedBy == "user" {
				fmt.Println("    Environment is user-managed, skipping teardown")
				return nil
			}
			if stateFile.ManagedBy != "" && stateFile.ManagedBy != expectedOwner {
				fmt.Printf("    Environment managed by %s, skipping teardown\n", stateFile.ManagedBy)
				return nil
			}
			fmt.Printf("    Tearing down %s environment...\n", stateFile.Provider)
			req := env.EnvRequest{
				Provider:     stateFile.Provider,
				PlanDir:      planPath,
				StateDir:     wsStateDir,
				Config:       make(map[string]interface{}),
				ManagedBy:    stateFile.ManagedBy,
				ForceDestroy: !opts.PreserveCloud,
			}
			if provider != nil && worktreePath != "" {
				if node := provider.FindByPath(worktreePath); node != nil {
					req.Workspace = node
				}
			}
			client := daemon.NewWithAutoStart()
			prov := env.ResolveProvider(stateFile.Provider, client, stateFile.Command)
			if err := prov.Down(context.Background(), req); err != nil {
				return fmt.Errorf("environment teardown failed: %w", err)
			}
			os.Remove(statePath)
			if wsStateDir != "" {
				os.Remove(filepath.Join(wsStateDir, "state.json"))
				os.Remove(filepath.Join(wsStateDir, ".env.local"))
				os.Remove(wsStateDir)
			}
			os.Remove(filepath.Join(planPath, ".env_state.json"))
			if worktreePath != "" {
				os.Remove(filepath.Join(worktreePath, ".env.local"))
			}
			return nil
		},
	}

	mergeItem := &finish.Item{
		ID:   ItemMergeSubmodules,
		Name: "Merge/fast-forward ecosystem repos to main",
		Check: func() (string, error) {
			if worktreeName == "" || gitRoot == "" {
				return "N/A", nil
			}
			// The plan's repo scope is the universal source of truth for
			// which repos participate, loaded in memory via plan.Config —
			// no need to read/parse the worktree's .grove/workspace from
			// disk (which is absent for anchored/XDG worktrees whose
			// container lives outside gitRoot).
			if plan.Config == nil || len(plan.Config.Repos) == 0 {
				return "N/A (not ecosystem)", nil
			}
			if provider == nil {
				return color.YellowString("Available (discovery failed)"), nil
			}
			localWorkspaces := provider.LocalWorkspacesInEcosystem(gitRoot)
			totalRepos := len(plan.Config.Repos)
			needsMerge := 0
			alreadyMerged := 0
			notFound := 0
			needsRebase := 0
			var repoDetails []finish.RepoStatus
			for _, repoName := range plan.Config.Repos {
				repoPath, exists := localWorkspaces[repoName]
				if !exists {
					notFound++
					repoDetails = append(repoDetails, finish.RepoStatus{Name: repoName, Status: "not_found"})
					continue
				}
				branchCheckCmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+worktreeName) //nolint:gosec // worktreeName is internal
				branchCheckCmd.Dir = repoPath
				if err := branchCheckCmd.Run(); err != nil {
					notFound++
					repoDetails = append(repoDetails, finish.RepoStatus{Name: repoName, Status: "not_found"})
					continue
				}
				mainCheckCmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/main")
				mainCheckCmd.Dir = repoPath
				if err := mainCheckCmd.Run(); err != nil {
					notFound++
					repoDetails = append(repoDetails, finish.RepoStatus{Name: repoName, Status: "not_found"})
					continue
				}
				aheadCmd := exec.Command("git", "rev-list", "--count", "main.."+worktreeName) //nolint:gosec // worktreeName is internal
				aheadCmd.Dir = repoPath
				aheadOutput, err := aheadCmd.Output()
				if err != nil {
					repoDetails = append(repoDetails, finish.RepoStatus{Name: repoName, Status: "not_found"})
					continue
				}
				aheadCount := strings.TrimSpace(string(aheadOutput))
				if aheadCount == "0" || aheadCount == "" {
					alreadyMerged++
					repoDetails = append(repoDetails, finish.RepoStatus{Name: repoName, Status: "merged"})
					continue
				}
				mergeBaseCmd := exec.Command("git", "merge-base", "main", worktreeName)
				mergeBaseCmd.Dir = repoPath
				mergeBaseOutput, err := mergeBaseCmd.Output()
				if err != nil {
					repoDetails = append(repoDetails, finish.RepoStatus{Name: repoName, Status: "not_found"})
					continue
				}
				mergeBase := strings.TrimSpace(string(mergeBaseOutput))
				mainRevCmd := exec.Command("git", "rev-parse", "main")
				mainRevCmd.Dir = repoPath
				mainRevOutput, err := mainRevCmd.Output()
				if err != nil {
					repoDetails = append(repoDetails, finish.RepoStatus{Name: repoName, Status: "not_found"})
					continue
				}
				mainRev := strings.TrimSpace(string(mainRevOutput))
				if mergeBase == mainRev {
					needsMerge++
					repoDetails = append(repoDetails, finish.RepoStatus{Name: repoName, Status: "needs_merge"})
				} else {
					needsRebase++
					repoDetails = append(repoDetails, finish.RepoStatus{Name: repoName, Status: "needs_rebase"})
				}
			}
			sharedRepoDetails = repoDetails
			var statusParts []string
			if needsMerge > 0 {
				statusParts = append(statusParts, color.YellowString("%d to merge", needsMerge))
			}
			if alreadyMerged > 0 {
				statusParts = append(statusParts, color.GreenString("%d merged", alreadyMerged))
			}
			if needsRebase > 0 {
				statusParts = append(statusParts, color.RedString("%d need rebase", needsRebase))
			}
			if notFound > 0 {
				statusParts = append(statusParts, color.New(color.Faint).Sprintf("%d skipped", notFound))
			}
			if len(statusParts) == 0 {
				return color.YellowString("Available"), nil
			}
			return fmt.Sprintf("%d repos: %s", totalRepos, strings.Join(statusParts, ", ")), nil
		},
		Action: func() error {
			if plan.Config == nil || len(plan.Config.Repos) == 0 {
				return nil
			}
			fmt.Printf("    Merging/fast-forwarding ecosystem branches to main...\n")
			if provider == nil {
				return fmt.Errorf("cannot merge ecosystem repos; workspace discovery failed")
			}
			localWorkspaces := provider.LocalWorkspacesInEcosystem(gitRoot)
			hasErrors := false
			for _, repoName := range plan.Config.Repos {
				repoPath, exists := localWorkspaces[repoName]
				if !exists {
					fmt.Printf("      Warning: repo '%s' not found in local workspaces, skipping\n", repoName)
					continue
				}
				branchCheckCmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+worktreeName) //nolint:gosec // worktreeName is internal
				branchCheckCmd.Dir = repoPath
				if err := branchCheckCmd.Run(); err != nil {
					continue
				}
				mainCheckCmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/main")
				mainCheckCmd.Dir = repoPath
				if err := mainCheckCmd.Run(); err != nil {
					fmt.Printf("      Warning: main branch not found in %s, skipping\n", repoName)
					continue
				}
				aheadCmd := exec.Command("git", "rev-list", "--count", "main.."+worktreeName) //nolint:gosec // worktreeName is internal
				aheadCmd.Dir = repoPath
				aheadOutput, err := aheadCmd.Output()
				if err != nil {
					fmt.Printf("      Warning: failed to check commits ahead for %s: %v\n", repoName, err)
					continue
				}
				aheadCount := strings.TrimSpace(string(aheadOutput))
				if aheadCount == "0" || aheadCount == "" {
					continue
				}
				fmt.Printf("      • %s: merging %s commits to main\n", repoName, aheadCount)
				checkoutCmd := exec.Command("git", "checkout", "main")
				checkoutCmd.Dir = repoPath
				if output, err := checkoutCmd.CombinedOutput(); err != nil {
					fmt.Printf("        Error: failed to checkout main: %s\n", string(output))
					hasErrors = true
					continue
				}
				mergeCmd := exec.Command("git", "merge", "--ff-only", worktreeName)
				mergeCmd.Dir = repoPath
				if output, err := mergeCmd.CombinedOutput(); err != nil {
					outputStr := string(output)
					if strings.Contains(outputStr, "Not possible to fast-forward") {
						fmt.Printf("        Warning: cannot fast-forward %s (needs rebase), skipping\n", repoName)
					} else {
						fmt.Printf("        Error: failed to merge: %s\n", outputStr)
						hasErrors = true
					}
					continue
				}
				fmt.Printf("        * Merged successfully\n")
			}
			if hasErrors {
				return fmt.Errorf("some ecosystem repos failed to merge")
			}
			return nil
		},
	}

	markFinishedItem := &finish.Item{
		ID:   ItemMarkFinished,
		Name: "Mark plan as finished in .grove-plan.yml",
		Check: func() (string, error) {
			configPath := filepath.Join(planPath, ".grove-plan.yml")
			data, err := os.ReadFile(configPath)
			if err != nil {
				return "Not found", nil
			}
			var config map[string]interface{}
			if err := yaml.Unmarshal(data, &config); err != nil {
				return "Invalid YAML", nil
			}
			if status, ok := config["status"].(string); ok && status == "finished" {
				return color.GreenString("Already finished"), nil
			}
			return color.YellowString("Available"), nil
		},
		Action: func() error {
			configPath := filepath.Join(planPath, ".grove-plan.yml")
			data, err := os.ReadFile(configPath)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			var config map[string]interface{}
			if len(data) > 0 {
				if err := yaml.Unmarshal(data, &config); err != nil {
					return err
				}
			}
			if config == nil {
				config = make(map[string]interface{})
			}
			config["status"] = "finished"
			newData, err := yaml.Marshal(config)
			if err != nil {
				return err
			}
			return os.WriteFile(configPath, newData, 0o600)
		},
	}

	pruneItem := &finish.Item{
		ID:   ItemPruneOrphans,
		Name: "Prune orphan environment resources",
		Check: func() (string, error) {
			if !opts.PruneOrphans {
				return "Skipped (use --prune-orphans)", nil
			}
			if worktreeName == "" {
				return "N/A", nil
			}
			if _, err := exec.LookPath("grove"); err != nil {
				return "N/A (grove not found)", nil
			}
			if opts.PruneCloud {
				return color.YellowString("Available (local + cloud)"), nil
			}
			return color.YellowString("Available (local only)"), nil
		},
		Action: func() error {
			if !opts.PruneOrphans || worktreeName == "" {
				return nil
			}
			args := []string{"env", "prune", "--worktree", worktreeName, "--yes"}
			if opts.PruneCloud {
				args = append(args, "--include-cloud")
			}
			fmt.Printf("    Pruning orphaned resources for %s...\n", worktreeName)
			runCmd := exec.Command("grove", args...)
			runCmd.Dir = gitRoot
			runCmd.Stdout = os.Stdout
			runCmd.Stderr = os.Stderr
			if err := runCmd.Run(); err != nil {
				// Best-effort — a prune failure shouldn't block
				// the rest of plan finish (archive, branch
				// cleanup). Surface as a warning.
				fmt.Printf("    Warning: grove env prune failed: %v\n", err)
			}
			return nil
		},
	}

	items := []*finish.Item{
		envTeardownItem,
		pruneItem,
		mergeItem,
		{
			ID:   ItemKillBoundAgents,
			Name: "Kill bound agents",
			Check: func() (string, error) {
				ctx := context.Background()
				client := daemon.New()
				defer client.Close()
				sessions, err := client.GetSessions(ctx)
				if err != nil {
					return "Daemon unavailable", nil
				}
				planBaseName := filepath.Base(planPath)
				count := 0
				for _, s := range sessions {
					if s.PlanName == planBaseName && (s.Status == "running" || s.Status == "idle" || s.Status == "pending_user") {
						count++
					}
				}
				if count == 0 {
					return "None running", nil
				}
				return color.YellowString("%d running", count), nil
			},
			Action: func() error {
				ctx := context.Background()
				client := daemon.New()
				defer client.Close()
				sessions, err := client.GetSessions(ctx)
				if err != nil {
					return nil // daemon not running; nothing to kill
				}
				planBaseName := filepath.Base(planPath)
				for _, s := range sessions {
					if s.PlanName == planBaseName && (s.Status == "running" || s.Status == "idle" || s.Status == "pending_user") {
						if killErr := client.KillSession(ctx, s.ID); killErr != nil {
							fmt.Printf("    Note: could not kill session %s (%s): %v\n", s.JobTitle, s.ID, killErr)
						}
					}
				}
				return nil
			},
		},
		{
			ID:   ItemCloseSession,
			Name: "Close session",
			Check: func() (string, error) {
				if sessionName == "" {
					return "N/A", nil
				}
				engine, err := mux.DetectMuxEngine(context.Background())
				if err != nil {
					return "Not found", nil
				}
				exists, _ := engine.SessionExists(context.Background(), sessionName)
				if exists {
					return color.YellowString("Running"), nil
				}
				return "Not found", nil
			},
			Action: func() error {
				engine, err := mux.DetectMuxEngine(context.Background())
				if err != nil {
					return err
				}
				return engine.KillSession(context.Background(), sessionName)
			},
		},
		{
			ID:   ItemPruneWorktree,
			Name: "Prune git worktree",
			Check: func() (string, error) {
				if worktreeName == "" || gitRoot == "" {
					return "N/A", nil
				}
				// Container worktrees (every worktree now carries repos: >= 1)
				// are NOT a git worktree of gitRoot — the container dir is a
				// plain dir (synthetic) or an ecosystem worktree, and the only
				// registered worktrees are the per-repo subdirs whose paths end
				// in /<repo>, not /<worktreeName>. So the suffix-match over
				// ListWorktrees below misses them entirely. Detect existence by
				// the container dir on disk instead, so the Action (which routes
				// to cleanupEcosystemWorktree) actually runs and deregisters the
				// child worktrees before the branch-delete step.
				if plan.Config != nil && len(plan.Config.Repos) > 0 {
					containerDir, ok := resolveContainerWorktreePath(gitRoot, worktreeName, provider)
					if !ok {
						return "Not found", nil
					}
					if _, err := os.Stat(containerDir); err != nil {
						return "Not found", nil
					}
					return color.YellowString("Exists"), nil
				}
				worktrees, err := wm.ListWorktrees(context.Background(), gitRoot)
				if err != nil {
					return "Error", err
				}
				for _, wt := range worktrees {
					if strings.HasSuffix(wt.Path, worktreeName) {
						wPath := wt.Path
						statusOutput, statusErr := exec.Command("git", "-C", wPath, "status", "--porcelain", "--ignore-submodules").Output()
						if statusErr != nil {
							return color.YellowString("Exists"), nil
						}
						if strings.TrimSpace(string(statusOutput)) != "" {
							return color.RedString("Has changes (needs --force)"), nil
						}
						return color.YellowString("Exists"), nil
					}
				}
				return "Not found", nil
			},
			Action: func() error {
				wPath, ok := resolveContainerWorktreePath(gitRoot, worktreeName, provider)
				if !ok {
					wPath = workspace.ResolveNewWorktreePath(gitRoot, worktreeName, false)
				}
				if plan.Config != nil && len(plan.Config.Repos) > 0 {
					if err := cleanupEcosystemWorktree(context.Background(), gitRoot, worktreeName, plan.Config.Repos, provider, opts.Force); err != nil {
						return err
					}
					_ = worktreeregistry.Delete(pathutil.WorktreeID(wPath))
					// The container above is the registry-tracked (often anchored
					// XDG) worktree. A stray legacy `<gitRoot>/.grove-worktrees/<name>`
					// superproject stub — left by an older legacy-only worktree-prep
					// path — has no registry entry and would survive the prune.
					// Reap it defensively so orphans don't accumulate.
					reapLegacyStubWorktree(context.Background(), gitRoot, worktreeName, opts.Force)
					return nil
				}
				hasSubmodules := false
				if _, err := os.Stat(filepath.Join(wPath, ".gitmodules")); err == nil {
					hasSubmodules = true
				}
				// Attempt parent worktree removal FIRST. Removing
				// submodule worktrees first causes the parent's
				// tracked submodule checkouts to appear as `deleted:`
				// in git status, so `git worktree remove` refuses
				// without --force even when the user has no
				// uncommitted work. Only prune linked submodule
				// worktrees if git explicitly complains about
				// submodules, then retry the parent.
				runParent := func() error {
					args := []string{"worktree", "remove"}
					if opts.Force {
						args = append(args, "--force")
					}
					args = append(args, wPath)
					return executor.Execute("git", args...)
				}
				err := runParent()
				if err != nil && hasSubmodules && strings.Contains(err.Error(), "working trees containing submodules") {
					if subErr := removeLinkedSubmoduleWorktrees(context.Background(), gitRoot, worktreeName, provider, opts.Force); subErr != nil {
						fmt.Printf("    Warning: failed to remove linked submodule worktrees: %v\n", subErr)
					}
					err = runParent()
				}
				// When the user asked for --force and git bailed out
				// with "Directory not empty" (gitignored/untracked
				// content survived `worktree remove --force`), honor
				// the user's intent by nuking the directory directly.
				// Gated by a strict path check to keep os.RemoveAll
				// from ever escaping the .grove-worktrees boundary.
				if err != nil && opts.Force && isDirNotEmptyErr(err) && pathIsUnderGroveWorktrees(wPath, gitRoot) {
					wPathClean := filepath.Clean(wPath)
					if rmErr := os.RemoveAll(wPathClean); rmErr != nil {
						return fmt.Errorf("force-remove worktree dir: %w", rmErr)
					}
					// Git leaves orphaned metadata in .git/worktrees/<name>
					// when `worktree remove` fails with "Directory not
					// empty"; prune it so `git worktree list` stays clean.
					if pruneErr := executor.Execute("git", "-C", gitRoot, "worktree", "prune"); pruneErr != nil {
						fmt.Printf("    Warning: failed to prune worktree metadata: %v\n", pruneErr)
					}
					err = nil
				}
				if err != nil {
					// Do NOT auto-retry with --force when the user
					// did not explicitly request force. Surfacing
					// the original error lets the user re-run with
					// --force (or toggle the force option in the
					// wizard) if they really want to discard
					// uncommitted work.
					return err
				}
				// After a successful parent removal, still prune any
				// dangling worktree metadata from the submodule
				// source repos so `git worktree list` in those repos
				// stays clean.
				if hasSubmodules {
					if subErr := removeLinkedSubmoduleWorktrees(context.Background(), gitRoot, worktreeName, provider, opts.Force); subErr != nil {
						fmt.Printf("    Warning: failed to prune linked submodule worktree metadata: %v\n", subErr)
					}
				}
				_ = worktreeregistry.Delete(pathutil.WorktreeID(wPath))
				// Reap any stray legacy superproject stub left at the in-repo
				// `<gitRoot>/.grove-worktrees/<name>` location by an older
				// legacy-only worktree-prep path. In the single-repo case the
				// prune above usually already removed it (wPath == that path), so
				// this is a no-op then; it matters when a duplicate stub coexists
				// with a differently-located real worktree.
				reapLegacyStubWorktree(context.Background(), gitRoot, worktreeName, opts.Force)
				return nil
			},
		},
		{
			ID:   ItemCleanDevLinks,
			Name: "Clean up dev binaries from worktree",
			Check: func() (string, error) {
				if _, err := exec.LookPath("grove"); err != nil {
					return "N/A (grove not found)", nil
				}
				return color.YellowString("Available"), nil
			},
			Action: func() error {
				fmt.Printf("    Pruning broken dev links...\n")
				if err := executor.Execute("grove", "dev", "prune"); err != nil {
					fmt.Printf("    Note: grove dev prune failed: %v\n", err)
				}
				return nil
			},
		},
		{
			ID:   ItemDeleteSubmoduleBranches,
			Name: "Delete submodule branches",
			Check: func() (string, error) {
				if branchName == "" || gitRoot == "" {
					return "N/A", nil
				}
				// Modern ecosystem plans track their repos in plan.Config.Repos
				// and route branch deletion through ItemDeleteLocalBranch
				// (Phase 3). This legacy `.gitmodules`-based submodule path
				// would double-delete (or fight) those branches, so skip it.
				if plan.Config != nil && len(plan.Config.Repos) > 0 {
					return "N/A (ecosystem plan)", nil
				}
				if _, err := os.Stat(filepath.Join(gitRoot, ".gitmodules")); os.IsNotExist(err) {
					return "N/A (no submodules)", nil
				}
				return color.YellowString("Available"), nil
			},
			Action: func() error {
				// Select the safe variant (-d) by default; escalate
				// to -D only when the user explicitly asked for
				// --force. Destroying unmerged commits in submodules
				// without opt-in is the same data-loss pattern the
				// local-branch delete item already guards against.
				deleteFlag := "-d"
				if opts.Force {
					deleteFlag = "-D"
				}
				foreachCmd := fmt.Sprintf("git branch %s %s 2>/dev/null || true", deleteFlag, branchName)
				cmd := exec.Command("git", "-C", gitRoot, "submodule", "foreach", foreachCmd)
				_ = cmd.Run()
				var notMergedErr error
				recordNotMerged := func(repoLabel string, err error) {
					if notMergedErr == nil {
						notMergedErr = fmt.Errorf("branch %q not fully merged in %s (re-run with --force to destroy unmerged commits): %w", branchName, repoLabel, err)
					}
				}
				gitmodulesPath := filepath.Join(gitRoot, ".gitmodules")
				if _, err := os.Stat(gitmodulesPath); err == nil {
					submodulePaths, _ := parseGitmodules(gitmodulesPath)
					var localWorkspaces map[string]string
					if provider != nil {
						localWorkspaces = provider.LocalWorkspacesInEcosystem(gitRoot)
					} else {
						localWorkspaces = make(map[string]string)
					}
					ecoWorktreeRoot, ok := workspace.FindWorktreePath(gitRoot, branchName)
					if !ok {
						ecoWorktreeRoot = workspace.ResolveNewWorktreePath(gitRoot, branchName, false)
					}
					for submoduleName, submodulePath := range submodulePaths {
						wtPath := filepath.Join(ecoWorktreeRoot, submodulePath)
						mainSubmodulePath := filepath.Join(gitRoot, submodulePath)
						if _, err := os.Stat(filepath.Join(mainSubmodulePath, ".git")); err == nil {
							removeWorktreeArgs := []string{"-C", mainSubmodulePath, "worktree", "remove"}
							if opts.Force {
								removeWorktreeArgs = append(removeWorktreeArgs, "--force")
							}
							removeWorktreeArgs = append(removeWorktreeArgs, wtPath)
							removeWorktreeCmd := exec.Command("git", removeWorktreeArgs...)
							if output, err := removeWorktreeCmd.CombinedOutput(); err != nil {
								if !strings.Contains(string(output), "not a working tree") && !strings.Contains(string(output), "No such file") {
									fmt.Printf("    Note: could not remove worktree for %s from main checkout: %s\n", submoduleName, string(output))
								}
							}
							deleteCmd := exec.Command("git", "-C", mainSubmodulePath, "branch", deleteFlag, branchName)
							if output, err := deleteCmd.CombinedOutput(); err != nil {
								outputStr := string(output)
								if !strings.Contains(outputStr, "not found") {
									if !opts.Force && strings.Contains(outputStr, "not fully merged") {
										recordNotMerged(submoduleName+" (main checkout)", err)
									}
									fmt.Printf("    Note: could not delete branch '%s' from %s main checkout: %v\n", branchName, submoduleName, err)
								}
							}
						}
						if localRepoPath, hasLocal := localWorkspaces[submoduleName]; hasLocal {
							removeWorktreeArgs := []string{"-C", localRepoPath, "worktree", "remove"}
							if opts.Force {
								removeWorktreeArgs = append(removeWorktreeArgs, "--force")
							}
							removeWorktreeArgs = append(removeWorktreeArgs, wtPath)
							removeWorktreeCmd := exec.Command("git", removeWorktreeArgs...)
							if output, err := removeWorktreeCmd.CombinedOutput(); err != nil {
								if !strings.Contains(string(output), "not a working tree") && !strings.Contains(string(output), "No such file") {
									fmt.Printf("    Note: could not remove worktree for %s from local workspace: %s\n", submoduleName, string(output))
								}
							}
							deleteCmd := exec.Command("git", "-C", localRepoPath, "branch", deleteFlag, branchName)
							if output, err := deleteCmd.CombinedOutput(); err != nil {
								outputStr := string(output)
								if !strings.Contains(outputStr, "not found") {
									if !opts.Force && strings.Contains(outputStr, "not fully merged") {
										recordNotMerged(submoduleName+" (local workspace)", err)
									}
									fmt.Printf("    Warning: failed to delete branch '%s' from %s local workspace: %v\n", branchName, submoduleName, err)
								}
							}
						}
					}
				}
				return notMergedErr
			},
		},
		{
			ID:   ItemDeleteLocalBranch,
			Name: "Delete local git branch(es)",
			Check: func() (string, error) {
				if branchName == "" || gitRoot == "" {
					return "N/A", nil
				}
				// Ecosystem plans: the feature branch lives in each
				// participating sub-repo's git, not in gitRoot (gitRoot is
				// the ecosystem root, which never holds the branch for
				// anchored/XDG worktrees). Probe the source repos.
				if plan.Config != nil && len(plan.Config.Repos) > 0 {
					sources := resolveEcosystemRepoSources(gitRoot, plan.Config.Repos, provider)
					existsIn := 0
					aheadStatus := ""
					for _, repo := range plan.Config.Repos {
						repoPath, ok := sources[repo]
						if !ok {
							continue
						}
						if err := exec.Command("git", "-C", repoPath, "show-ref", "--verify", "--quiet", "refs/heads/"+branchName).Run(); err != nil { //nolint:gosec // branchName is internal
							continue
						}
						existsIn++
						if aheadStatus != "" {
							continue
						}
						// Surface ahead-of-base count from the first repo that
						// still carries the branch, matching the single-repo
						// display so the wizard can color the row.
						for _, baseBranch := range []string{"main", "master"} {
							if exec.Command("git", "-C", repoPath, "show-ref", "--verify", "--quiet", "refs/heads/"+baseBranch).Run() != nil {
								continue
							}
							aheadOutput, aheadErr := exec.Command("git", "-C", repoPath, "rev-list", "--count", baseBranch+".."+branchName).Output() //nolint:gosec // branchName is internal
							if aheadErr == nil {
								aheadCount := strings.TrimSpace(string(aheadOutput))
								if aheadCount != "0" && aheadCount != "" {
									aheadStatus = color.RedString("%s has %s commits ahead of %s", repo, aheadCount, baseBranch)
								}
							}
							break
						}
					}
					if existsIn == 0 {
						return "Not found", nil
					}
					if aheadStatus != "" {
						return aheadStatus, nil
					}
					return color.YellowString("Available"), nil
				}
				output, err := exec.Command("git", "-C", gitRoot, "branch", "--list", branchName).Output()
				if err != nil {
					return "Error", err
				}
				if strings.TrimSpace(string(output)) == "" {
					return "Not found", nil
				}
				baseBranches := []string{"main", "master"}
				for _, baseBranch := range baseBranches {
					_, branchCheckErr := exec.Command("git", "-C", gitRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+baseBranch).Output()
					if branchCheckErr != nil {
						continue
					}
					aheadOutput, aheadErr := exec.Command("git", "-C", gitRoot, "rev-list", "--count", baseBranch+".."+branchName).Output()
					if aheadErr == nil {
						aheadCount := strings.TrimSpace(string(aheadOutput))
						if aheadCount != "0" && aheadCount != "" {
							return color.RedString("Has " + aheadCount + " commits ahead of " + baseBranch), nil
						}
					}
					break
				}
				worktreeList, wtErr := exec.Command("git", "-C", gitRoot, "worktree", "list").Output()
				if wtErr != nil {
					return color.YellowString("Exists"), nil
				}
				worktreeLines := strings.Split(string(worktreeList), "\n")
				for _, line := range worktreeLines {
					if strings.Contains(line, "["+branchName+"]") {
						return color.YellowString("Checked out in worktree"), nil
					}
				}
				return color.YellowString("Exists"), nil
			},
			Action: func() error {
				// Ecosystem plans: delete the feature branch from every
				// participating source repo. The prune-worktree step already
				// removed the linked worktrees (Phase 2 decoupling), so the
				// refs are no longer checked out and `-d` succeeds. Aggregate
				// failures so one unmerged/locked repo doesn't abort the rest.
				if plan.Config != nil && len(plan.Config.Repos) > 0 {
					sources := resolveEcosystemRepoSources(gitRoot, plan.Config.Repos, provider)
					var firstErr error
					for _, repo := range plan.Config.Repos {
						repoPath, ok := sources[repo]
						if !ok {
							continue
						}
						if err := exec.Command("git", "-C", repoPath, "show-ref", "--verify", "--quiet", "refs/heads/"+branchName).Run(); err != nil { //nolint:gosec // branchName is internal
							continue // branch already gone in this repo
						}
						if err := deleteLocalBranch(repoPath, branchName, opts.Force); err != nil {
							fmt.Printf("    Warning: failed to delete branch '%s' from %s: %v\n", branchName, repo, err)
							if firstErr == nil {
								firstErr = err
							}
							continue
						}
						fmt.Printf("    Deleted branch '%s' from %s\n", branchName, repo)
					}
					return firstErr
				}
				err := executor.Execute("git", "-C", gitRoot, "branch", "-d", branchName)
				if err != nil {
					if strings.Contains(err.Error(), "not found") {
						// Idempotent: the branch is already gone.
						return nil
					} else if strings.Contains(err.Error(), "checked out at") {
						// Worktree was pruned earlier in the cleanup
						// sequence; the branch ref just has a stale
						// worktree pointer, so -D here does not lose
						// work.
						fmt.Printf("    Using -D (force) to delete branch that was in worktree...\n")
						return executor.Execute("git", "-C", gitRoot, "branch", "-D", branchName)
					} else if strings.Contains(err.Error(), "not fully merged") {
						// Destroys unmerged commits. Only allowed
						// when the user explicitly asked for force.
						if !opts.Force {
							return err
						}
						fmt.Printf("    Using -D (force) due to unmerged commits...\n")
						return executor.Execute("git", "-C", gitRoot, "branch", "-D", branchName)
					}
				}
				return err
			},
		},
		{
			ID:   ItemDeleteRemoteBranch,
			Name: "Delete remote git branch",
			Check: func() (string, error) {
				if branchName == "" || gitRoot == "" {
					return "N/A", nil
				}
				remoteOutput, remoteErr := exec.Command("git", "-C", gitRoot, "ls-remote", "--heads", "origin", branchName).Output()
				if remoteErr != nil {
					return "N/A (no remote)", nil
				}
				if strings.TrimSpace(string(remoteOutput)) == "" {
					return "Not found", nil
				}
				return color.YellowString("Exists on origin"), nil
			},
			Action: func() error {
				return executor.Execute("git", "-C", gitRoot, "push", "origin", "--delete", branchName)
			},
		},
		{
			ID:   ItemRebuildBinaries,
			Name: "Rebuild main repo binaries",
			Check: func() (string, error) {
				if gitRoot == "" {
					return "N/A", nil
				}
				makefilePath := filepath.Join(gitRoot, "Makefile")
				if _, err := os.Stat(makefilePath); os.IsNotExist(err) {
					return "N/A (no Makefile)", nil
				}
				return color.YellowString("Available"), nil
			},
			Action: func() error {
				if gitRoot == "" {
					return nil
				}
				fmt.Printf("    Building binaries in main repository...\n")
				buildCmd := exec.Command("make", "build")
				buildCmd.Dir = gitRoot
				buildCmd.Stdout = os.Stdout
				buildCmd.Stderr = os.Stderr
				if err := buildCmd.Run(); err != nil {
					fmt.Printf("    Warning: build failed: %v\n", err)
					return nil
				}
				return nil
			},
		},
		markFinishedItem,
		{
			ID:   ItemArchivePlan,
			Name: "Archive plan directory",
			Check: func() (string, error) {
				return color.YellowString("Available"), nil
			},
			Action: func() error {
				plansParentDir := filepath.Dir(planPath)
				planName := filepath.Base(planPath)
				archiveDir := filepath.Join(plansParentDir, ".archive")
				if err := os.MkdirAll(archiveDir, 0o755); err != nil {
					return fmt.Errorf("failed to create archive directory: %w", err)
				}
				archivePath := filepath.Join(archiveDir, planName)
				if _, err := os.Stat(archivePath); err == nil {
					return fmt.Errorf("archive destination already exists: %s", archivePath)
				}
				if err := os.Rename(planPath, archivePath); err != nil {
					if err := fs.CopyDir(planPath, archivePath); err != nil {
						return fmt.Errorf("failed to copy plan to archive: %w", err)
					}
					if err := os.RemoveAll(planPath); err != nil {
						return fmt.Errorf("failed to remove original plan directory: %w", err)
					}
				}
				fmt.Printf("    Archived plan to: %s\n", archivePath)
				return nil
			},
		},
	}

	// Populate status and availability.
	for _, item := range items {
		status, _ := item.Check()
		item.Status = status
		if item == mergeItem && len(sharedRepoDetails) > 0 {
			item.Details = sharedRepoDetails
		}
		if status == color.YellowString("Available") ||
			status == color.YellowString("Active") ||
			status == color.YellowString("Exists") ||
			status == color.YellowString("Exists on origin") ||
			status == color.YellowString("Running") ||
			status == color.YellowString("Running containers found") ||
			status == color.YellowString("Has links") ||
			status == color.YellowString("Checked out in worktree") ||
			status == color.RedString("Has changes (needs --force)") ||
			strings.Contains(status, "commits ahead of") {
			item.IsAvailable = true
		}
	}

	// Check if branch exists and is merged. For ecosystem plans with a
	// resolved worktree container, check branch existence in sub-repo
	// directories inside the container — anchored/XDG ecosystem worktrees
	// don't have the feature branch in gitRoot's own refs.
	branchIsMerged := false
	branchExists := false
	branchCheckDir := gitRoot
	if plan.Config != nil && len(plan.Config.Repos) > 0 && worktreePath != "" {
		for _, repo := range plan.Config.Repos {
			repoDir := filepath.Join(worktreePath, repo)
			if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
				continue
			}
			branchCheckDir = repoDir
			break
		}
	}
	if branchName != "" && branchCheckDir != "" {
		branchCheckCmd := exec.Command("git", "-C", branchCheckDir, "show-ref", "--verify", "--quiet", "refs/heads/"+branchName)
		if branchCheckCmd.Run() == nil {
			branchExists = true
			baseBranches := []string{"main", "master"}
			for _, baseBranch := range baseBranches {
				_, baseCheckErr := exec.Command("git", "-C", branchCheckDir, "show-ref", "--verify", "--quiet", "refs/heads/"+baseBranch).Output()
				if baseCheckErr != nil {
					continue
				}
				aheadOutput, aheadErr := exec.Command("git", "-C", branchCheckDir, "rev-list", "--count", baseBranch+".."+branchName).Output()
				if aheadErr == nil {
					aheadCount := strings.TrimSpace(string(aheadOutput))
					if aheadCount == "0" || aheadCount == "" {
						branchIsMerged = true
					}
				}
				break
			}
		}
	}

	return &Result{
		Items:          items,
		BranchIsMerged: branchIsMerged,
		BranchExists:   branchExists,
	}, nil
}

// RunOnFinishHook executes the plan's on_finish hook (if any). It is
// called by the CLI path after the user has confirmed cleanup
// selection and before the enabled actions run. Kept here (rather
// than in cmd) so both CLI and view can opt in to hook execution.
func RunOnFinishHook(plan *orchestration.Plan, planName string) {
	if plan == nil || plan.Config == nil || plan.Config.Hooks == nil {
		return
	}
	hookCmdStr, ok := plan.Config.Hooks["on_finish"]
	if !ok || hookCmdStr == "" {
		return
	}
	var noteRef string
	for _, job := range plan.Jobs {
		if job.NoteRef != "" {
			noteRef = job.NoteRef
			break
		}
	}
	fmt.Println("▶️  Executing on_finish hook...")
	templateData := struct {
		PlanName string
		NoteRef  string
	}{PlanName: planName, NoteRef: noteRef}
	tmpl, err := template.New("hook").Parse(hookCmdStr)
	if err != nil {
		fmt.Printf("Warning: failed to parse on_finish hook template: %v\n", err)
		return
	}
	var renderedCmd bytes.Buffer
	if err := tmpl.Execute(&renderedCmd, templateData); err != nil {
		fmt.Printf("Warning: failed to render on_finish hook command: %v\n", err)
		return
	}
	hookCmd := exec.Command("sh", "-c", renderedCmd.String()) //nolint:gosec // on_finish hook comes from trusted plan config
	hookCmd.Stdout = os.Stdout
	hookCmd.Stderr = os.Stderr
	if err := hookCmd.Run(); err != nil {
		fmt.Printf("Warning: on_finish hook execution failed: %v\n", err)
	} else {
		fmt.Println("* on_finish hook executed successfully.")
	}
}

// parseGitmodules reads and parses the .gitmodules file.
func parseGitmodules(gitmodulesPath string) (map[string]string, error) {
	file, err := os.Open(gitmodulesPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	submodules := make(map[string]string)
	var currentName string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[submodule") {
			start := strings.Index(line, "\"")
			end := strings.LastIndex(line, "\"")
			if start != -1 && end != -1 && start < end {
				currentName = line[start+1 : end]
			}
		} else if strings.HasPrefix(line, "path =") && currentName != "" {
			path := strings.TrimSpace(strings.TrimPrefix(line, "path ="))
			submodules[currentName] = path
		}
	}
	return submodules, scanner.Err()
}

// removeLinkedSubmoduleWorktrees removes linked worktrees from submodule source repositories.
// When force is false, `git worktree remove` is run without --force so that
// uncommitted submodule work is preserved (the failure surfaces to the caller).
func removeLinkedSubmoduleWorktrees(ctx context.Context, gitRoot, worktreeName string, provider *workspace.Provider, force bool) error {
	// Registry-first resolution so a worktree created with `--anchor <sub-repo>`
	// (living under the anchor repo's XDG base, not gitRoot's) is found; only
	// then does the .gitmodules under it resolve correctly.
	worktreePath, ok := resolveContainerWorktreePath(gitRoot, worktreeName, provider)
	if !ok {
		worktreePath = workspace.ResolveNewWorktreePath(gitRoot, worktreeName, false)
	}
	gitmodulesPath := filepath.Join(worktreePath, ".gitmodules")
	if _, err := os.Stat(gitmodulesPath); os.IsNotExist(err) {
		return nil
	}
	submodulePaths, err := parseGitmodules(gitmodulesPath)
	if err != nil {
		return fmt.Errorf("failed to parse .gitmodules: %w", err)
	}
	var localWorkspaces map[string]string
	if provider != nil {
		localWorkspaces = provider.LocalWorkspacesInEcosystem(gitRoot)
	} else {
		return nil
	}
	for submoduleName, submodulePath := range submodulePaths {
		submoduleWorktreePath := filepath.Join(worktreePath, submodulePath)
		mainSubmodulePath := filepath.Join(gitRoot, submodulePath)
		if _, err := os.Stat(filepath.Join(mainSubmodulePath, ".git")); err == nil {
			cmd := exec.CommandContext(ctx, "git", "worktree", "list", "--porcelain")
			cmd.Dir = mainSubmodulePath
			output, err := cmd.Output()
			if err == nil && strings.Contains(string(output), submoduleWorktreePath) {
				fmt.Printf("    Removing linked worktree for %s\n", submoduleName)
				removeArgs := []string{"worktree", "remove"}
				if force {
					removeArgs = append(removeArgs, "--force")
				}
				removeArgs = append(removeArgs, submoduleWorktreePath)
				removeCmd := exec.CommandContext(ctx, "git", removeArgs...)
				removeCmd.Dir = mainSubmodulePath
				if err := removeCmd.Run(); err != nil {
					fmt.Printf("      Warning: failed to remove worktree from main checkout: %v\n", err)
				} else {
					continue
				}
			}
		}
		localRepoPath, hasLocal := localWorkspaces[submoduleName]
		if !hasLocal {
			continue
		}
		cmd := exec.CommandContext(ctx, "git", "worktree", "list", "--porcelain")
		cmd.Dir = localRepoPath
		output, err := cmd.Output()
		if err != nil {
			continue
		}
		if strings.Contains(string(output), submoduleWorktreePath) {
			fmt.Printf("    Removing linked worktree for %s\n", submoduleName)
			removeArgs := []string{"worktree", "remove"}
			if force {
				removeArgs = append(removeArgs, "--force")
			}
			removeArgs = append(removeArgs, submoduleWorktreePath)
			removeCmd := exec.CommandContext(ctx, "git", removeArgs...)
			removeCmd.Dir = localRepoPath
			if err := removeCmd.Run(); err != nil {
				fmt.Printf("      Warning: failed to remove worktree: %v\n", err)
			}
		}
	}
	return nil
}

// cleanupEcosystemWorktree removes an ecosystem worktree by cleaning up individual repo worktrees.
// When force is false, destructive git operations (worktree remove --force, branch -D)
// are downgraded to their safe variants so that uncommitted work and unmerged commits
// survive; failures surface to the caller instead of being papered over with os.RemoveAll.
// resolveContainerWorktreePath locates the on-disk container directory for the
// worktree named worktreeName belonging to the ecosystem at gitRoot.
//
// FindWorktreePath only probes gitRoot's OWN worktree bases, so it misses
// worktrees created with `--anchor <sub-repo>` — those live under the anchor
// repo's XDG base (paths.WorktreesDir()/DirIdentifier(anchor)/<name>), not the
// ecosystem's. When the direct probe misses, this falls back to the
// per-worktree registry: it returns the AbsPath of a registry entry whose
// basename matches worktreeName AND whose Owner is one of this ecosystem's
// local workspaces (the only repos an --anchor in this ecosystem could name),
// which keeps the match scoped and unambiguous. Returns ("", false) when no
// container can be resolved.
func resolveContainerWorktreePath(gitRoot, worktreeName string, provider *workspace.Provider) (string, bool) {
	// Delegate to the shared, registry-first resolver in core so every consumer
	// (create-time provisioning, add-worktrees, and this prune path) agrees on
	// where the worktree named worktreeName lives — including anchored worktrees
	// under a sub-repo's XDG base. Owners accepted: the ecosystem root plus every
	// local workspace, any of which could be an --anchor target.
	owners := []string{gitRoot}
	if provider != nil {
		for _, p := range provider.LocalWorkspacesInEcosystem(gitRoot) {
			owners = append(owners, p)
		}
	}
	return workspace.ResolveWorktreePathByName(gitRoot, worktreeName, owners)
}

// resolveEcosystemRepoSources maps each plan repo name to its on-disk SOURCE
// checkout (not a worktree), mirroring the resolution order used by
// cleanupEcosystemWorktree so branch operations target the same repos the prune
// step deregistered worktrees from. For anchored/XDG worktrees the feature
// branch lives in these source repos, never in gitRoot's own refs. Repos that
// cannot be resolved are omitted from the result.
func resolveEcosystemRepoSources(gitRoot string, repos []string, provider *workspace.Provider) map[string]string {
	var localWorkspaces map[string]string
	if provider != nil {
		localWorkspaces = provider.LocalWorkspacesInEcosystem(gitRoot)
	} else {
		localWorkspaces = make(map[string]string)
	}
	out := make(map[string]string, len(repos))
	for _, repo := range repos {
		if p, ok := localWorkspaces[repo]; ok {
			out[repo] = p
			continue
		}
		// Sibling repo checked out directly under the ecosystem root (e.g. a
		// non-grove `workspaces=["*"]` sibling the provider can't discover).
		candidate := filepath.Join(gitRoot, repo)
		if _, err := os.Stat(filepath.Join(candidate, ".git")); err == nil {
			out[repo] = candidate
			continue
		}
		// Single-repo container owned by the repo itself: gitRoot IS the source.
		if filepath.Base(gitRoot) == repo {
			if _, err := os.Stat(filepath.Join(gitRoot, ".git")); err == nil {
				out[repo] = gitRoot
				continue
			}
		}
		// Repo linked in from a DIFFERENT ecosystem: resolve its source node
		// (the non-worktree node carrying this name) via the provider.
		if provider != nil {
			for _, node := range provider.All() {
				if node.Name != repo || node.IsWorktree() {
					continue
				}
				if _, err := os.Stat(filepath.Join(node.Path, ".git")); err == nil {
					out[repo] = node.Path
					break
				}
			}
		}
	}
	return out
}

// deleteLocalBranch deletes branchName from the repo at repoPath. It uses the
// safe `-d` by default, escalating to `-D` only when the branch is merely
// pinned by a stale (already-pruned) worktree pointer, or when force is set to
// destroy unmerged commits. Returns an error when the branch is unmerged and
// force is false so the caller can surface the data-loss risk; a missing branch
// is treated as success (idempotent).
func deleteLocalBranch(repoPath, branchName string, force bool) error {
	out, err := exec.Command("git", "-C", repoPath, "branch", "-d", branchName).CombinedOutput() //nolint:gosec // branchName is internal
	if err == nil {
		return nil
	}
	outStr := string(out)
	switch {
	case strings.Contains(outStr, "not found"):
		return nil
	case strings.Contains(outStr, "checked out at") || strings.Contains(outStr, "used by worktree"):
		// Worktree was pruned earlier in the cleanup sequence; the ref just
		// carries a stale worktree pointer, so -D here loses no work.
		return exec.Command("git", "-C", repoPath, "branch", "-D", branchName).Run() //nolint:gosec // branchName is internal
	case strings.Contains(outStr, "not fully merged"):
		if !force {
			return fmt.Errorf("branch %q not fully merged (re-run with --force to destroy unmerged commits)", branchName)
		}
		return exec.Command("git", "-C", repoPath, "branch", "-D", branchName).Run() //nolint:gosec // branchName is internal
	default:
		return fmt.Errorf("%s", strings.TrimSpace(outStr))
	}
}

func cleanupEcosystemWorktree(ctx context.Context, gitRoot, worktreeName string, repos []string, provider *workspace.Provider, force bool) error {
	ecosystemDir, ok := resolveContainerWorktreePath(gitRoot, worktreeName, provider)
	if !ok {
		ecosystemDir = workspace.ResolveNewWorktreePath(gitRoot, worktreeName, false)
	}
	fmt.Printf("    Cleaning up ecosystem worktree at %s\n", ecosystemDir)
	var localWorkspaces map[string]string
	if provider != nil {
		localWorkspaces = provider.LocalWorkspacesInEcosystem(gitRoot)
	} else {
		fmt.Printf("    Warning: workspace discovery failed, cannot clean up submodule branches\n")
		localWorkspaces = make(map[string]string)
	}
	var firstErr error
	recordErr := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	for _, repo := range repos {
		repoWorktreePath := filepath.Join(ecosystemDir, repo)
		fmt.Printf("    • %s: removing worktree\n", repo)
		repoPath, exists := localWorkspaces[repo]
		// fellBackToGitRoot tracks the case where the provider missed the repo
		// but we located its source checkout as a direct child of gitRoot. Such
		// repos (e.g. `workspaces=["*"]` siblings without a grove.toml) aren't
		// discoverable, but their worktree/branch still need cleaning up. We mark
		// this so the post-remove prune below only runs for the fallback path
		// where a force os.RemoveAll may have left a dangling registration.
		fellBackToGitRoot := false
		if !exists {
			// Fallback: a non-grove sibling repo won't be in local workspaces,
			// but its source checkout is a direct child of the ecosystem git
			// root. If that checkout exists, treat it as the source repo and
			// fall through to the normal remove+branch-delete path instead of
			// skipping (which previously orphaned the worktree and branch).
			candidate := filepath.Join(gitRoot, repo)
			if _, statErr := os.Stat(filepath.Join(candidate, ".git")); statErr == nil {
				repoPath = candidate
				exists = true
				fellBackToGitRoot = true
			}
		}
		if !exists && filepath.Base(gitRoot) == repo {
			// Single-repo container owned by the repo itself: gitRoot IS the
			// repo's source checkout (there is no gitRoot/<repo> subdir), and
			// the provider does not list a repo under its own ecosystem path.
			// Without this branch the repo fell into the skip path below, which
			// os.RemoveAll'd the child worktree dir WITHOUT `git worktree
			// remove`, leaving a stale registration in gitRoot. The subsequent
			// `git branch -d <name>` (delete-local-branch finish step) was then
			// refused with "cannot delete branch ... used by worktree at
			// <container>/<repo>". Routing through the normal path deregisters
			// the child worktree (git worktree remove) BEFORE any branch delete.
			if _, statErr := os.Stat(filepath.Join(gitRoot, ".git")); statErr == nil {
				repoPath = gitRoot
				exists = true
				fellBackToGitRoot = true
			}
		}
		if !exists && provider != nil {
			// A repo linked in from a DIFFERENT ecosystem (e.g. `core` grown
			// into a `notify` container via `flow plan add-worktrees`) is not
			// returned by LocalWorkspacesInEcosystem(gitRoot) — its
			// RootEcosystemPath points at its own ecosystem, not gitRoot — and
			// it has no gitRoot/<repo> checkout. Resolve its real source via the
			// provider so it too goes through `git worktree remove` (deregister)
			// before any branch delete, instead of the os.RemoveAll-only skip
			// path that orphans the registration and branch.
			//
			// FindByName returns the FIRST node with the name, which is often a
			// worktree (a repo has one source + many worktree children all
			// sharing its name). Scan All() for the non-worktree SOURCE node so
			// we deregister against the source repo, not a worktree subdir.
			for _, node := range provider.All() {
				if node.Name != repo || node.IsWorktree() {
					continue
				}
				if _, statErr := os.Stat(filepath.Join(node.Path, ".git")); statErr == nil {
					repoPath = node.Path
					exists = true
					fellBackToGitRoot = true
					break
				}
			}
		}
		if !exists {
			fmt.Printf("      Warning: repo '%s' not found in local workspaces, skipping branch cleanup\n", repo)
			if force {
				if !pathIsUnderGroveWorktrees(repoWorktreePath, gitRoot) {
					fmt.Printf("      Warning: refusing to remove %s (outside worktree boundary)\n", repoWorktreePath)
				} else if err := os.RemoveAll(repoWorktreePath); err != nil {
					fmt.Printf("      Warning: failed to remove directory %s: %v\n", repoWorktreePath, err)
				}
			} else if _, err := os.Stat(repoWorktreePath); err == nil {
				// Without force we don't know if the orphaned
				// directory holds uncommitted work, so refuse to
				// blow it away.
				fmt.Printf("      Refusing to remove %s without --force (repo not in workspaces, cannot verify clean state)\n", repoWorktreePath)
				recordErr(fmt.Errorf("%s: orphaned worktree directory retained (re-run with --force to discard)", repo))
			}
			continue
		}
		removeWorktreeArgs := []string{"worktree", "remove"}
		if force {
			removeWorktreeArgs = append(removeWorktreeArgs, "--force")
		}
		removeWorktreeArgs = append(removeWorktreeArgs, repoWorktreePath)
		removeWorktreeCmd := exec.CommandContext(ctx, "git", removeWorktreeArgs...)
		removeWorktreeCmd.Dir = repoPath
		if output, err := removeWorktreeCmd.CombinedOutput(); err != nil {
			outputStr := string(output)
			if strings.Contains(outputStr, "not a working tree") || strings.Contains(outputStr, "No such file") {
				// Already gone; nothing to do.
			} else if force {
				fmt.Printf("      Warning: git worktree remove failed, removing directory manually: %s\n", outputStr)
				if !pathIsUnderGroveWorktrees(repoWorktreePath, gitRoot) {
					fmt.Printf("      Warning: refusing to remove %s (outside worktree boundary)\n", repoWorktreePath)
				} else if err := os.RemoveAll(repoWorktreePath); err != nil {
					fmt.Printf("      Warning: failed to remove directory %s: %v\n", repoWorktreePath, err)
				}
			} else {
				fmt.Printf("      Error: git worktree remove failed for %s: %s\n", repo, outputStr)
				recordErr(fmt.Errorf("%s: worktree remove failed (re-run with --force to discard uncommitted work): %w", repo, err))
				continue
			}
		}
		// Branch deletion is intentionally NOT done here. Pruning the
		// worktree only deregisters/removes the per-repo linked worktrees
		// and the container dir; the feature branches in the source repos
		// are deleted by the separate "Delete local git branch(es)" item
		// (ItemDeleteLocalBranch), which owns the unmerged-commit safety
		// gate and surfaces the deletion in the wizard checklist. Keeping
		// them decoupled avoids the prune step silently destroying branches
		// behind the user's back.

		// In the fallback path the worktree dir may have been force-removed via
		// os.RemoveAll (rather than `git worktree remove`), leaving a dangling
		// 'prunable' registration in the source repo. Scrub it so a subsequent
		// `git branch -d` isn't refused with "used by worktree". Only the
		// fallback path needs this; provider-discovered repos use `git worktree
		// remove`, which deregisters cleanly.
		if fellBackToGitRoot {
			prune := exec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "prune")
			if output, err := prune.CombinedOutput(); err != nil {
				fmt.Printf("      Warning: failed to prune stale worktree registration in %s: %s\n", repoPath, string(output))
			}
		}
	}
	if firstErr != nil {
		// Leave the ecosystem directory in place so the user can
		// recover any preserved work.
		return firstErr
	}
	if !pathIsUnderGroveWorktrees(ecosystemDir, gitRoot) {
		return fmt.Errorf("refusing to remove ecosystem directory outside worktree boundary: %s", ecosystemDir)
	}
	if err := os.RemoveAll(ecosystemDir); err != nil {
		return fmt.Errorf("failed to remove ecosystem directory: %w", err)
	}
	// The ecosystem worktree dir is removed with os.RemoveAll (not `git
	// worktree remove`), so the ecosystem repo still carries a stale worktree
	// registration pointing at the now-deleted dir. Prune it, otherwise a
	// later `git branch -d <worktree>` (the delete-branch finish step) is
	// refused with "cannot delete branch ... used by worktree".
	pruneCmd := exec.CommandContext(ctx, "git", "-C", gitRoot, "worktree", "prune")
	if output, err := pruneCmd.CombinedOutput(); err != nil {
		fmt.Printf("    Warning: failed to prune stale worktree registration in %s: %s\n", gitRoot, string(output))
	}
	fmt.Printf("    * Ecosystem worktree removed successfully\n")
	return nil
}

// isDirNotEmptyErr reports whether err is git's specific
// "Directory not empty" failure from `worktree remove --force` when
// gitignored/untracked content survives the remove.
func isDirNotEmptyErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Directory not empty")
}

// reapLegacyStubWorktree removes a stray legacy
// `<gitRoot>/.grove-worktrees/<worktreeName>` superproject git worktree if one
// exists and is registered with gitRoot.
//
// The registry-first prune that runs before this removes the real container
// (including anchored worktrees under a sub-repo's XDG base, which carry a
// registry entry). But a legacy stub created by an older legacy-only
// worktree-prep path (a bare `git worktree add` of the superproject) has NO
// registry entry, so it is invisible to the registry-driven prune and would be
// left orphaned on disk. This reaps it defensively.
//
// The target is strictly confined to gitRoot's own `.grove-worktrees` boundary
// (via pathIsUnderGroveWorktrees) and removed only after confirming gitRoot
// actually registers it as a linked worktree, so nothing outside the
// ecosystem's legacy worktree dir is ever touched. Best-effort: failures are
// logged, never fatal.
func reapLegacyStubWorktree(ctx context.Context, gitRoot, worktreeName string, force bool) {
	if gitRoot == "" || worktreeName == "" {
		return
	}
	// The legacy (non-XDG) location for this ecosystem's worktree.
	legacyPath := workspace.ResolveNewWorktreePath(gitRoot, worktreeName, false)
	if !pathIsUnderGroveWorktrees(legacyPath, gitRoot) {
		return
	}
	if _, err := os.Stat(legacyPath); err != nil {
		return // no stray stub on disk
	}
	// Confirm gitRoot registers this path as a linked worktree before removing,
	// so a directory that merely sits at the legacy location (but isn't a git
	// worktree of gitRoot) is never force-removed here. Compare symlink-resolved
	// paths: `git worktree list` emits realpaths (e.g. /private/var/... on
	// macOS) which need not match the lexically-joined legacyPath.
	listCmd := exec.CommandContext(ctx, "git", "-C", gitRoot, "worktree", "list", "--porcelain")
	out, err := listCmd.Output()
	if err != nil || !worktreeListContainsPath(string(out), legacyPath) {
		return
	}
	fmt.Printf("    Reaping stray legacy worktree stub at %s\n", legacyPath)
	args := []string{"-C", gitRoot, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, legacyPath)
	if err := exec.CommandContext(ctx, "git", args...).Run(); err != nil {
		fmt.Printf("      Warning: failed to remove legacy worktree stub: %v\n", err)
	}
}

// worktreeListContainsPath reports whether the `git worktree list --porcelain`
// output registers a worktree at target, comparing symlink-resolved absolute
// paths so a realpath in the output (macOS /private/var/...) still matches a
// lexically-joined target.
func worktreeListContainsPath(porcelain, target string) bool {
	canon := func(p string) string {
		abs := p
		if a, err := filepath.Abs(p); err == nil {
			abs = a
		}
		if r, err := filepath.EvalSymlinks(abs); err == nil {
			abs = r
		}
		return filepath.Clean(abs)
	}
	want := canon(target)
	for _, line := range strings.Split(porcelain, "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		p := strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		if canon(p) == want {
			return true
		}
	}
	return false
}

// pathIsUnderGroveWorktrees reports whether wPath is a safe target
// for force-removal: it must live strictly beneath one of gitRoot's
// worktree bases (the identifier-level container directories returned
// by workspace.WorktreeBases). Returns false if gitRoot is empty, if
// wPath is a base container itself, or if wPath escapes every base
// (after cleaning). Guards os.RemoveAll against catastrophic misuse.
func pathIsUnderGroveWorktrees(wPath, gitRoot string) bool {
	if gitRoot == "" || wPath == "" {
		return false
	}
	wPathClean := filepath.Clean(wPath)
	for _, base := range workspace.WorktreeBases(gitRoot) {
		container := filepath.Clean(base)
		if wPathClean == container {
			// The container itself is never a removal target.
			return false
		}
		if strings.HasPrefix(wPathClean, container+string(filepath.Separator)) {
			return true
		}
	}
	// Anchored worktrees (`--anchor <sub-repo>`) live under the anchor repo's
	// XDG base, not gitRoot's, so the gitRoot-scoped check above misses them.
	// They still live inside the global XDG worktrees tree
	// (WorktreesDir()/<identifier>/<name>), which IS the grove-managed boundary
	// this guard exists to enforce. Accept a path that is a LEAF (>= 2 levels
	// below the root: an identifier dir and the worktree name), never the root
	// or a bare identifier dir.
	if wtd := paths.WorktreesDir(); wtd != "" {
		root := filepath.Clean(wtd)
		if strings.HasPrefix(wPathClean, root+string(filepath.Separator)) {
			rel, err := filepath.Rel(root, wPathClean)
			if err == nil {
				depth := len(strings.Split(rel, string(filepath.Separator)))
				if depth >= 2 {
					return true
				}
			}
		}
	}
	return false
}
