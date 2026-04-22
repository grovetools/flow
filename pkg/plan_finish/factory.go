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
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/util/sanitize"
	gexec "github.com/grovetools/flow/pkg/exec"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/tui/wizards/finish"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
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
}

// Stable item identifiers. Hosts look items up by these constants
// via Items.ByID instead of depending on slice indexes.
const (
	ItemEnvTeardown            = "env_teardown"
	ItemMergeSubmodules        = "merge_submodules"
	ItemMarkFinished           = "mark_finished"
	ItemCloseSession           = "close_session"
	ItemPruneWorktree          = "prune_worktree"
	ItemCleanDevLinks          = "clean_dev_links"
	ItemDeleteSubmoduleBranches = "delete_submodule_branches"
	ItemDeleteLocalBranch      = "delete_local_branch"
	ItemDeleteRemoteBranch     = "delete_remote_branch"
	ItemRebuildBinaries        = "rebuild_binaries"
	ItemArchivePlan            = "archive_plan"
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

	// Determine worktree path for state lookups.
	var worktreePath string
	if worktreeName != "" && gitRoot != "" {
		worktreePath = filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
	}

	findEnvState := func() (data []byte, statePath string, worktreeStateDir string) {
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
				Provider:  stateFile.Provider,
				PlanDir:   planPath,
				StateDir:  wsStateDir,
				Config:    make(map[string]interface{}),
				ManagedBy: stateFile.ManagedBy,
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
		Name: "Merge/fast-forward submodules to main",
		Check: func() (string, error) {
			if worktreeName == "" || gitRoot == "" {
				return "N/A", nil
			}
			workspaceFile := filepath.Join(gitRoot, ".grove", "workspace")
			if _, err := os.Stat(workspaceFile); os.IsNotExist(err) {
				return "N/A (not ecosystem)", nil
			}
			data, err := os.ReadFile(workspaceFile)
			if err != nil {
				return "N/A (read error)", nil
			}
			var workspaceConfig struct {
				Branch    string   `yaml:"branch"`
				Plan      string   `yaml:"plan"`
				CreatedAt string   `yaml:"created_at"`
				Ecosystem bool     `yaml:"ecosystem"`
				Repos     []string `yaml:"repos,omitempty"`
			}
			if err := yaml.Unmarshal(data, &workspaceConfig); err != nil {
				return "N/A (parse error)", nil
			}
			if !workspaceConfig.Ecosystem || len(workspaceConfig.Repos) == 0 {
				return "N/A (not ecosystem)", nil
			}
			if provider == nil {
				return color.YellowString("Available (discovery failed)"), nil
			}
			localWorkspaces := provider.LocalWorkspaces()
			totalRepos := len(workspaceConfig.Repos)
			needsMerge := 0
			alreadyMerged := 0
			notFound := 0
			needsRebase := 0
			var repoDetails []finish.RepoStatus
			for _, repoName := range workspaceConfig.Repos {
				repoPath, exists := localWorkspaces[repoName]
				if !exists {
					notFound++
					repoDetails = append(repoDetails, finish.RepoStatus{Name: repoName, Status: "not_found"})
					continue
				}
				branchCheckCmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+worktreeName)
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
				aheadCmd := exec.Command("git", "rev-list", "--count", "main.."+worktreeName)
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
			workspaceFile := filepath.Join(gitRoot, ".grove", "workspace")
			data, err := os.ReadFile(workspaceFile)
			if err != nil {
				return fmt.Errorf("failed to read workspace file: %w", err)
			}
			var workspaceConfig struct {
				Branch    string   `yaml:"branch"`
				Plan      string   `yaml:"plan"`
				CreatedAt string   `yaml:"created_at"`
				Ecosystem bool     `yaml:"ecosystem"`
				Repos     []string `yaml:"repos,omitempty"`
			}
			if err := yaml.Unmarshal(data, &workspaceConfig); err != nil {
				return fmt.Errorf("failed to parse workspace file: %w", err)
			}
			if !workspaceConfig.Ecosystem || len(workspaceConfig.Repos) == 0 {
				return nil
			}
			fmt.Printf("    Merging/fast-forwarding submodule branches to main...\n")
			if provider == nil {
				return fmt.Errorf("cannot merge submodules; workspace discovery failed")
			}
			localWorkspaces := provider.LocalWorkspaces()
			hasErrors := false
			for _, repoName := range workspaceConfig.Repos {
				repoPath, exists := localWorkspaces[repoName]
				if !exists {
					fmt.Printf("      Warning: repo '%s' not found in local workspaces, skipping\n", repoName)
					continue
				}
				branchCheckCmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+worktreeName)
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
				aheadCmd := exec.Command("git", "rev-list", "--count", "main.."+worktreeName)
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
				return fmt.Errorf("some submodules failed to merge")
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
			return os.WriteFile(configPath, newData, 0644)
		},
	}

	items := []*finish.Item{
		envTeardownItem,
		mergeItem,
		{
			ID:   ItemCloseSession,
			Name: "Close tmux session",
			Check: func() (string, error) {
				if sessionName == "" {
					return "N/A", nil
				}
				err := executor.Execute("tmux", "has-session", "-t", sessionName)
				if err == nil {
					return color.YellowString("Running"), nil
				}
				return "Not found", nil
			},
			Action: func() error {
				return executor.Execute("tmux", "kill-session", "-t", sessionName)
			},
		},
		{
			ID:   ItemPruneWorktree,
			Name: "Prune git worktree",
			Check: func() (string, error) {
				if worktreeName == "" || gitRoot == "" {
					return "N/A", nil
				}
				worktrees, err := wm.ListWorktrees(context.Background(), gitRoot)
				if err != nil {
					return "Error", err
				}
				for _, wt := range worktrees {
					if strings.HasSuffix(wt.Path, worktreeName) {
						wPath := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
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
				wPath := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
				if plan.Config != nil && len(plan.Config.Repos) > 0 {
					return cleanupEcosystemWorktree(context.Background(), gitRoot, worktreeName, plan.Config.Repos, provider, opts.Force)
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
						localWorkspaces = provider.LocalWorkspaces()
					} else {
						localWorkspaces = make(map[string]string)
					}
					for submoduleName, submodulePath := range submodulePaths {
						wtPath := filepath.Join(gitRoot, ".grove-worktrees", branchName, submodulePath)
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
			Name: "Delete local git branch",
			Check: func() (string, error) {
				if branchName == "" || gitRoot == "" {
					return "N/A", nil
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
				err := executor.Execute("git", "-C", gitRoot, "branch", "-d", branchName)
				if err != nil {
					if strings.Contains(err.Error(), "checked out at") {
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
				if err := os.MkdirAll(archiveDir, 0755); err != nil {
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

	// Check if branch exists and is merged.
	branchIsMerged := false
	branchExists := false
	if branchName != "" && gitRoot != "" {
		branchCheckCmd := exec.Command("git", "-C", gitRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branchName)
		if branchCheckCmd.Run() == nil {
			branchExists = true
			baseBranches := []string{"main", "master"}
			for _, baseBranch := range baseBranches {
				_, baseCheckErr := exec.Command("git", "-C", gitRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+baseBranch).Output()
				if baseCheckErr != nil {
					continue
				}
				aheadOutput, aheadErr := exec.Command("git", "-C", gitRoot, "rev-list", "--count", baseBranch+".."+branchName).Output()
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
	hookCmd := exec.Command("sh", "-c", renderedCmd.String())
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
	worktreePath := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
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
		localWorkspaces = provider.LocalWorkspaces()
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
func cleanupEcosystemWorktree(ctx context.Context, gitRoot, worktreeName string, repos []string, provider *workspace.Provider, force bool) error {
	ecosystemDir := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
	fmt.Printf("    Cleaning up ecosystem worktree at %s\n", ecosystemDir)
	var localWorkspaces map[string]string
	if provider != nil {
		localWorkspaces = provider.LocalWorkspaces()
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
	branchDeleteFlag := "-d"
	if force {
		branchDeleteFlag = "-D"
	}
	for _, repo := range repos {
		repoWorktreePath := filepath.Join(ecosystemDir, repo)
		fmt.Printf("    • %s: removing worktree and branch\n", repo)
		repoPath, exists := localWorkspaces[repo]
		if !exists {
			fmt.Printf("      Warning: repo '%s' not found in local workspaces, skipping branch cleanup\n", repo)
			if force {
				if err := os.RemoveAll(repoWorktreePath); err != nil {
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
				if err := os.RemoveAll(repoWorktreePath); err != nil {
					fmt.Printf("      Warning: failed to remove directory %s: %v\n", repoWorktreePath, err)
				}
			} else {
				fmt.Printf("      Error: git worktree remove failed for %s: %s\n", repo, outputStr)
				recordErr(fmt.Errorf("%s: worktree remove failed (re-run with --force to discard uncommitted work): %w", repo, err))
				continue
			}
		}
		deleteBranchCmd := exec.CommandContext(ctx, "git", "branch", branchDeleteFlag, worktreeName)
		deleteBranchCmd.Dir = repoPath
		if output, err := deleteBranchCmd.CombinedOutput(); err != nil {
			outputStr := string(output)
			if strings.Contains(outputStr, "not found") {
				// Idempotent: nothing to delete.
			} else if !force && strings.Contains(outputStr, "not fully merged") {
				fmt.Printf("      Error: branch '%s' in %s has unmerged commits: %s\n", worktreeName, repo, outputStr)
				recordErr(fmt.Errorf("%s: branch %q not fully merged (re-run with --force to destroy unmerged commits)", repo, worktreeName))
			} else {
				fmt.Printf("      Warning: failed to delete branch '%s' from %s: %s\n", worktreeName, repo, outputStr)
			}
		} else {
			fmt.Printf("      * Deleted branch '%s'\n", worktreeName)
		}
	}
	if firstErr != nil {
		// Leave the ecosystem directory in place so the user can
		// recover any preserved work.
		return firstErr
	}
	if err := os.RemoveAll(ecosystemDir); err != nil {
		return fmt.Errorf("failed to remove ecosystem directory: %w", err)
	}
	fmt.Printf("    * Ecosystem worktree removed successfully\n")
	return nil
}
