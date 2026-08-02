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
	"errors"
	"fmt"
	"io"
	stdfs "io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/fatih/color"
	"github.com/grovetools/core/fs"
	"github.com/grovetools/core/git"
	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/env"
	"github.com/grovetools/core/pkg/models"
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
// RetainedWorktreeError reports a worktree that was deliberately kept rather
// than retired — most often because per-repo checkouts still hold uncommitted
// work, but also when the retirement itself could not be carried out safely.
//
// It is a PARTIAL SUCCESS, not a teardown failure. Everything not contingent on
// those repos already ran, and the plan must still be marked finished, archived
// and cleared from the active-plan key — a stray dirty file in one repo is not
// a reason to leave a plan listed forever with no way to retry it. Hosts use
// IsRetainedWorktree to tell this apart from a genuine failure when deciding
// whether to skip the terminal items.
type RetainedWorktreeError struct {
	// Details holds one "<repo>: <git's own message>" line per retained repo.
	Details []string
	// Reason, when non-empty, replaces the per-repo dirty-work explanation.
	// It exists for retentions that have nothing to do with uncommitted work
	// — e.g. the archive item finding no grove data dir to archive INTO.
	// Those still have to be partial successes: archiving is the default
	// retirement under `flow plan finish --yes`, so a hard failure there
	// would strand plans in review over an environment problem, and the only
	// other "recovery" available to the operator would be --prune-worktree,
	// i.e. deleting the code the archive existed to keep.
	Reason string
}

func (e *RetainedWorktreeError) Error() string {
	if e.Reason != "" {
		return "worktree retained: " + e.Reason
	}
	return fmt.Sprintf("worktree retained for %d repo(s): %s (enable Force to discard uncommitted work)",
		len(e.Details), strings.Join(e.Details, "; "))
}

// IsRetainedWorktree reports whether err (or anything it wraps) is a
// retained-worktree partial success.
func IsRetainedWorktree(err error) bool {
	var target *RetainedWorktreeError
	return errors.As(err, &target)
}

// ForceSwitch is a late-bound force toggle. The hosted finish wizard builds
// its items once (the Check closures are expensive) and only learns whether
// the user wants --force when the checklist is submitted, so it hands the
// factory a switch instead of a value and flips it before running the actions.
//
// Safe to flip from a different goroutine than the one running the actions.
type ForceSwitch struct {
	mu sync.Mutex
	on bool
}

// Set records whether destructive git operations may use their --force forms.
func (f *ForceSwitch) Set(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.on = on
}

// Enabled reports the current setting. A nil switch is never enabled.
func (f *ForceSwitch) Enabled() bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.on
}

type Options struct {
	// Force causes destructive git operations (worktree remove,
	// branch delete) to use --force equivalents. When false, the
	// factory's Action closures refuse to fall back to destructive
	// commands even if the safe variant fails — callers must re-run
	// with Force=true (or flip the wizard's Force toggle, which the
	// hosted TUI wires through ForceSwitch) to confirm.
	Force bool
	// ForceSwitch, when non-nil, supersedes Force: every Action reads it
	// at run time. Hosts that build items before the user has chosen
	// (the hosted wizard) pass one of these and Set it on submit.
	ForceSwitch *ForceSwitch
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
	// ArchiveWorktree enables the "archive git worktree" item, which
	// moves the worktree container under paths.WorktreeArchiveDir()
	// (detached from its owner repos, with per-repo git bundles for
	// unpushed history) instead of deleting it. Mutually exclusive
	// with PruneWorktree — the archive item's Check and Action refuse
	// to run when both are set.
	ArchiveWorktree bool
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
	// KeepNotes skips the native note lifecycle at finish — the plan's
	// linked notes are NOT moved to completed/. Default is false:
	// every finish moves all of the plan's notes to completed.
	KeepNotes bool
	// NoLedger skips the plan-ledger note — the ledger item reports
	// "Skipped" and its Action is a no-op. Default is false: every finish
	// promotes the plan's commit ranges, landing receipts and final
	// worktree state into a notebook note before the teardown items run.
	NoLedger bool
}

// Stable item identifiers. Hosts look items up by these constants
// via Items.ByID instead of depending on slice indexes.
const (
	ItemEnvTeardown             = "env_teardown"
	ItemMergeSubmodules         = "merge_submodules"
	ItemMarkFinished            = "mark_finished"
	ItemKillBoundAgents         = "kill_bound_agents"
	ItemCloseSession            = "close_session"
	ItemLedgerNote              = "ledger_note"
	ItemTombstoneRegistry       = "tombstone_registry"
	ItemArchiveWorktree         = "archive_worktree"
	ItemPruneWorktree           = "prune_worktree"
	ItemCleanDevLinks           = "clean_dev_links"
	ItemDeleteSubmoduleBranches = "delete_submodule_branches"
	ItemDeleteLocalBranch       = "delete_local_branch"
	ItemDeleteRemoteBranch      = "delete_remote_branch"
	ItemRebuildBinaries         = "rebuild_binaries"
	ItemArchivePlan             = "archive_plan"
	ItemPruneOrphans            = "prune_orphans"
	ItemClearNavBindings        = "clear_nav_bindings"
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

// ansiRe matches SGR escape sequences so availability classification never
// depends on whether the status was built with a color helper (and therefore
// on whether stdout happened to be a terminal).
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// unavailableStatusPrefixes is the CLOSED vocabulary of "nothing to do here"
// statuses. Anything an item's Check reports that is not one of these is
// treated as actionable.
//
// This list is deliberately a deny-list. The previous gate was an allow-list of
// exact equality comparisons against colourised display strings, which meant
// any copy-edit to any status text silently deleted that item from the product
// on every host — no test failure, no log line. `60add16` appended
// " (opt-in)" to one status and killed `--rebuild-binaries` outright; the same
// mechanism had already killed the env-teardown, kill-bound-agents,
// prune-orphans and merge-repos items for their non-trivial statuses. With a
// deny-list the failure direction inverts: editing an actionable status is
// safe, and only a NEW kind of "nothing to do" status needs registering here —
// which shows up as an item offering itself with an obviously inert status
// rather than as a feature disappearing.
//
// TestEveryNonNAItemIsReachable pins the agreement between this vocabulary and
// what the item Checks actually return.
var unavailableStatusPrefixes = []string{
	"N/A",
	"Not found",
	"None",
	"Skipped",
	"Error",
	"Invalid",
	"Daemon unavailable",
	"Already finished",
	"Conflicts with",
	"Unknown",
}

// statusIsAvailable reports whether a Check status means the item can run.
func statusIsAvailable(status string) bool {
	plain := strings.TrimSpace(ansiRe.ReplaceAllString(status, ""))
	if plain == "" {
		return false
	}
	for _, prefix := range unavailableStatusPrefixes {
		if strings.HasPrefix(plain, prefix) {
			return false
		}
	}
	return true
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
	// Output receives the human-readable chatter the Check and Action
	// closures produce, and is wired to the Stdout/Stderr of every
	// subprocess they spawn. nil means os.Stdout (the CLI default).
	//
	// A TUI host MUST supply its own writer. The alternative it used
	// before this field existed was to swap the process-global
	// os.Stdout/os.Stderr to /dev/null for the duration of the run,
	// which (a) is an unsynchronized write to a global that the render
	// loop reads concurrently, and (b) breaks any renderer that
	// re-resolves its output fd from os.Stdout per frame — frames get
	// composed into /dev/null while the front buffer is marked painted.
	Output io.Writer
	// LedgerWriter creates the plan-ledger note. nil means the real nb
	// shell-out (writeLedgerNoteViaNb); tests substitute their own so no
	// test can ever write into the owner's notebook.
	LedgerWriter LedgerNoteWriter
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
	if root, err := orchestration.GetProjectGitRoot(planPath); err == nil {
		bctx.GitRoot = root
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
	if bctx.Output == nil {
		bctx.Output = os.Stdout
	}

	// forceEnabled is read at Action time, not captured at build time. The
	// hosted wizard is built ONCE (its Checks are expensive), so a force toggle
	// the user flips in the checklist has to reach closures that already exist;
	// re-running BuildItems to apply it would cost a full workspace rescan.
	forceEnabled := func() bool {
		if opts.ForceSwitch != nil {
			return opts.ForceSwitch.Enabled()
		}
		return opts.Force
	}

	out := bctx.Output
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
				fmt.Fprintln(out, "    Environment is user-managed, skipping teardown")
				return nil
			}
			if stateFile.ManagedBy != "" && stateFile.ManagedBy != expectedOwner {
				fmt.Fprintf(out, "    Environment managed by %s, skipping teardown\n", stateFile.ManagedBy)
				return nil
			}
			fmt.Fprintf(out, "    Tearing down %s environment...\n", stateFile.Provider)
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
			// Resolve repos the same way ItemDeleteLocalBranch does so an
			// --anchor <sub-repo> worktree (whose repos aren't in the bare
			// provider map keyed off gitRoot) is found, not mis-flagged
			// not_found. A weaker lookup here hid the merge item entirely.
			sources := resolveEcosystemRepoSources(gitRoot, plan.Config.Repos, provider)
			totalRepos := len(plan.Config.Repos)
			needsMerge := 0
			alreadyMerged := 0
			notFound := 0
			needsRebase := 0
			var repoDetails []finish.RepoStatus
			for _, repoName := range plan.Config.Repos {
				repoPath, exists := sources[repoName]
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
			fmt.Fprintf(out, "    Merging/fast-forwarding ecosystem branches to main...\n")
			if provider == nil {
				return fmt.Errorf("cannot merge ecosystem repos; workspace discovery failed")
			}
			sources := resolveEcosystemRepoSources(gitRoot, plan.Config.Repos, provider)
			hasErrors := false
			for _, repoName := range plan.Config.Repos {
				repoPath, exists := sources[repoName]
				if !exists {
					fmt.Fprintf(out, "      Warning: repo '%s' not found in local workspaces, skipping\n", repoName)
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
					fmt.Fprintf(out, "      Warning: main branch not found in %s, skipping\n", repoName)
					continue
				}
				aheadCmd := exec.Command("git", "rev-list", "--count", "main.."+worktreeName) //nolint:gosec // worktreeName is internal
				aheadCmd.Dir = repoPath
				aheadOutput, err := aheadCmd.Output()
				if err != nil {
					fmt.Fprintf(out, "      Warning: failed to check commits ahead for %s: %v\n", repoName, err)
					continue
				}
				aheadCount := strings.TrimSpace(string(aheadOutput))
				if aheadCount == "0" || aheadCount == "" {
					continue
				}
				fmt.Fprintf(out, "      • %s: merging %s commits to main\n", repoName, aheadCount)
				checkoutCmd := exec.Command("git", "checkout", "main")
				checkoutCmd.Dir = repoPath
				if output, err := checkoutCmd.CombinedOutput(); err != nil {
					fmt.Fprintf(out, "        Error: failed to checkout main: %s\n", string(output))
					hasErrors = true
					continue
				}
				mergeCmd := exec.Command("git", "merge", "--ff-only", worktreeName)
				mergeCmd.Dir = repoPath
				if output, err := mergeCmd.CombinedOutput(); err != nil {
					outputStr := string(output)
					if strings.Contains(outputStr, "Not possible to fast-forward") {
						fmt.Fprintf(out, "        Warning: cannot fast-forward %s (needs rebase), skipping\n", repoName)
					} else {
						fmt.Fprintf(out, "        Error: failed to merge: %s\n", outputStr)
						hasErrors = true
					}
					continue
				}
				fmt.Fprintf(out, "        * Merged successfully\n")
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
			if err := os.WriteFile(configPath, newData, 0o600); err != nil {
				return err
			}
			entry := grovelogging.NewUnifiedLogger("grove-flow").Info("Plan finished").
				Field("event", "plan.finished").
				Field("plan", filepath.Base(planPath)).
				Field("plan_dir", planPath)
			if worktreeName != "" {
				entry = entry.Field("worktree", worktreeName)
			}
			entry.StructuredOnly().Emit()
			return nil
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
			fmt.Fprintf(out, "    Pruning orphaned resources for %s...\n", worktreeName)
			runCmd := exec.Command("grove", args...)
			runCmd.Dir = gitRoot
			runCmd.Stdout = out
			runCmd.Stderr = out
			if err := runCmd.Run(); err != nil {
				// Best-effort — a prune failure shouldn't block
				// the rest of plan finish (archive, branch
				// cleanup). Surface as a warning.
				fmt.Fprintf(out, "    Warning: grove env prune failed: %v\n", err)
			}
			return nil
		},
	}

	// The two provenance items below share ONE snapshot of the plan's
	// commit ranges, receipts and final git state. Collecting it is a git
	// read over every repo in the worktree, and — more importantly — both
	// items must describe the SAME moment: a ledger that disagrees with the
	// tombstone's final SHAs is worse than either alone. Collection is lazy
	// (Action time, not Check time) so building the checklist stays cheap,
	// and happens before any teardown item has removed anything.
	var (
		ledgerOnce sync.Once
		ledgerSnap Ledger
	)
	sharedLedger := func() Ledger {
		ledgerOnce.Do(func() {
			ledgerSnap = CollectLedger(plan, planPath, worktreePath, time.Now())
		})
		return ledgerSnap
	}

	ledgerNoteItem := &finish.Item{
		ID:   ItemLedgerNote,
		Name: "Write plan ledger note to notebook",
		Check: func() (string, error) {
			if opts.NoLedger {
				return "Skipped (--no-ledger)", nil
			}
			if _, err := exec.LookPath("nb"); err != nil {
				return "N/A (nb not found)", nil
			}
			return color.YellowString("Available"), nil
		},
		Action: func() error {
			if opts.NoLedger {
				return nil
			}
			planName := filepath.Base(planPath)
			body := RenderLedger(sharedLedger())
			write := bctx.LedgerWriter
			if write == nil {
				write = writeLedgerNoteViaNb
			}
			// A failure here fails the item, which skips mark_finished /
			// archive_plan and leaves the plan re-runnable. That is
			// deliberate: this item exists to stop finish destroying the
			// plan's story, so it must not be the step that shrugs. The
			// cost of a retry is a second ledger note (nb never overwrites
			// a note), which is an accurate extra snapshot, not corruption.
			result, err := write(planPath, planName, LedgerTitle(planName), body)
			if err != nil {
				return fmt.Errorf("write plan ledger note: %w", err)
			}
			for _, warning := range result.Warnings {
				fmt.Fprintf(out, "    Warning: %s\n", warning)
			}
			if result.Path != "" {
				fmt.Fprintf(out, "    Wrote plan ledger: %s\n", result.Path)
			}
			return nil
		},
	}

	tombstoneItem := &finish.Item{
		ID:   ItemTombstoneRegistry,
		Name: "Tombstone worktree registry entry",
		Check: func() (string, error) {
			if worktreePath == "" {
				return "N/A", nil
			}
			entry, err := worktreeregistry.Load(pathutil.WorktreeID(worktreePath))
			if err != nil {
				return "Not found", nil
			}
			if entry.IsFinished() {
				return color.GreenString("Already finished"), nil
			}
			return color.YellowString("Available"), nil
		},
		Action: func() error {
			if worktreePath == "" {
				return nil
			}
			id := pathutil.WorktreeID(worktreePath)
			entry, err := worktreeregistry.Tombstone(id, sharedLedger().FinalStates())
			if err != nil {
				if os.IsNotExist(err) {
					// Nothing was ever registered for this worktree, so
					// there is no record to lose. Not a failure.
					fmt.Fprintf(out, "    No registry entry for %s; nothing to tombstone\n", worktreePath)
					return nil
				}
				// Everything after this point retires the worktree. A lost
				// record is precisely what this item exists to prevent, so
				// teardown must not proceed past a failed write.
				return fmt.Errorf("tombstone registry entry %s: %w", id, err)
			}
			fmt.Fprintf(out, "    Recorded %d repo final SHA(s) on the registry tombstone\n", len(entry.FinalSHAs))
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
							fmt.Fprintf(out, "    Note: could not kill session %s (%s): %v\n", s.JobTitle, s.ID, killErr)
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
		// Both provenance items run BEFORE every retirement item (archive,
		// prune, branch deletes). They read the branches, checkouts and
		// commit ranges those items are about to remove, so promoting the
		// worktree's story has to happen while the story still exists.
		ledgerNoteItem,
		tombstoneItem,
		{
			// Archive runs BEFORE prune_worktree (and before the
			// branch-deletion items) so the per-repo git bundles it
			// creates still see every ref. It is mutually exclusive
			// with prune_worktree: both retire the same container.
			ID:   ItemArchiveWorktree,
			Name: "Archive git worktree",
			// Paired with prune_worktree so hosts that let the user pick
			// items freely (the wizard's toggle and select-all) can enforce
			// the exclusion themselves. The factory's own opts-based guard
			// below only sees CLI flags, which the wizard never sets — it
			// cannot catch a selection made in the UI. Archive is listed
			// FIRST, which is how "select all" resolves the group in favour
			// of the recoverable retirement.
			ExclusiveGroup: finish.GroupWorktreeRetirement,
			Check: func() (string, error) {
				if opts.ArchiveWorktree && opts.PruneWorktree {
					return color.RedString("Conflicts with prune_worktree"),
						fmt.Errorf("archive_worktree is mutually exclusive with prune_worktree")
				}
				if worktreeName == "" || gitRoot == "" {
					return "N/A", nil
				}
				containerDir, ok := resolveContainerWorktreePath(gitRoot, worktreeName, provider)
				if !ok {
					containerDir = workspace.ResolveNewWorktreePath(gitRoot, worktreeName, false)
				}
				if _, err := os.Stat(containerDir); err != nil {
					return "Not found", nil
				}
				return color.YellowString("Exists"), nil
			},
			Action: func() error {
				// The wizard applies no CLI-level flag exclusion, so
				// the factory is the last line of defense: refuse to
				// archive when the prune item is also requested.
				if opts.ArchiveWorktree && opts.PruneWorktree {
					return fmt.Errorf("archive_worktree is mutually exclusive with prune_worktree — enable only one")
				}
				if worktreeName == "" || gitRoot == "" {
					return fmt.Errorf("cannot archive worktree: worktree name or git root unknown")
				}
				containerPath, ok := resolveContainerWorktreePath(gitRoot, worktreeName, provider)
				if !ok {
					containerPath = workspace.ResolveNewWorktreePath(gitRoot, worktreeName, false)
				}
				if _, err := os.Stat(containerPath); err != nil {
					return fmt.Errorf("worktree not found at %s: %w", containerPath, err)
				}
				archiveBase := paths.WorktreeArchiveDir()
				if archiveBase == "" {
					// There is nowhere to archive TO. Since `--yes` now
					// archives by default, a hard error here would fail a
					// finish that used to succeed, and the plan would sit in
					// review forever over an environment problem. Retain the
					// container where it is and report it: the finish is a
					// partial success, mark_finished / archive_plan still run,
					// and — critically — nothing falls back to deleting it.
					return &RetainedWorktreeError{
						Reason: fmt.Sprintf("cannot resolve worktree archive directory (no grove data dir); "+
							"the worktree was left untouched at %s", containerPath),
					}
				}
				// A name collision is disambiguated rather than refused; see
				// uniqueArchiveDest for why.
				destPath, err := uniqueArchiveDest(filepath.Join(archiveBase, workspace.DirIdentifier(gitRoot), worktreeName))
				if err != nil {
					return err
				}
				if !pathIsUnderWorktreeArchive(destPath) {
					return fmt.Errorf("refusing to archive outside the worktree-archive boundary: %s", destPath)
				}
				var repos []string
				if plan.Config != nil {
					repos = plan.Config.Repos
				}
				if err := archiveWorktreeContainer(context.Background(), out, containerPath, destPath, gitRoot, repos); err != nil {
					return err
				}
				// Re-key the registry entry onto the archive location
				// (sets ArchivedAt/OriginalPath, deletes the old ID) so
				// Reconcile neither prunes nor re-adopts it.
				if err := worktreeregistry.Archive(containerPath, destPath); err != nil {
					return fmt.Errorf("worktree moved to %s but registry update failed: %w", destPath, err)
				}
				// Same defensive reap as prune_worktree: a stray legacy
				// `<gitRoot>/.grove-worktrees/<name>` stub has no registry
				// entry and would otherwise survive.
				reapLegacyStubWorktree(context.Background(), out, gitRoot, worktreeName, forceEnabled())
				fmt.Fprintf(out, "    Archived worktree to: %s\n", destPath)
				return nil
			},
		},
		{
			ID:   ItemPruneWorktree,
			Name: "Prune git worktree",
			// See archive_worktree above: same container, one or the other.
			ExclusiveGroup: finish.GroupWorktreeRetirement,
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
					// Pre-flight dirty detection, mirroring the non-ecosystem
					// branch below. Without it exactly the plan shape that hits
					// the dirty-repo failure got no warning at all: the
					// checklist said "Exists" and the obstacle only surfaced
					// after the destructive run had already half-completed.
					if dirty := dirtyEcosystemRepos(containerDir, plan.Config.Repos); len(dirty) > 0 {
						return color.RedString("Has changes in %s (needs force)", strings.Join(dirty, ", ")), nil
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
					// The deregistration steps below are NOT contingent on every
					// repo coming clean. A registry entry describing a container
					// that is being torn down must not survive because one repo
					// inside it was dirty — that is what left plans listed
					// forever with no way to retry them.
					cleanupErr := cleanupEcosystemWorktree(context.Background(), out, gitRoot, worktreeName, plan.Config.Repos, provider, forceEnabled())
					// DeleteUnlessFinished, not Delete: when the tombstone item
					// has already recorded this worktree's story, pruning the
					// container must not then erase the record of it. An entry
					// that was never tombstoned is deleted exactly as before.
					_, _ = worktreeregistry.DeleteUnlessFinished(pathutil.WorktreeID(wPath))
					// The container above is the registry-tracked (often anchored
					// XDG) worktree. A stray legacy `<gitRoot>/.grove-worktrees/<name>`
					// superproject stub — left by an older legacy-only worktree-prep
					// path — has no registry entry and would survive the prune.
					// Reap it defensively so orphans don't accumulate.
					reapLegacyStubWorktree(context.Background(), out, gitRoot, worktreeName, forceEnabled())
					return cleanupErr
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
					if forceEnabled() {
						args = append(args, "--force")
					}
					args = append(args, wPath)
					return executor.Execute("git", args...)
				}
				err := runParent()
				if err != nil && hasSubmodules && strings.Contains(err.Error(), "working trees containing submodules") {
					if subErr := removeLinkedSubmoduleWorktrees(context.Background(), out, gitRoot, worktreeName, provider, forceEnabled()); subErr != nil {
						fmt.Fprintf(out, "    Warning: failed to remove linked submodule worktrees: %v\n", subErr)
					}
					err = runParent()
				}
				// When the user asked for --force and git bailed out
				// with "Directory not empty" (gitignored/untracked
				// content survived `worktree remove --force`), honor
				// the user's intent by nuking the directory directly.
				// Gated by a strict path check to keep os.RemoveAll
				// from ever escaping the .grove-worktrees boundary.
				if err != nil && forceEnabled() && isDirNotEmptyErr(err) && pathIsUnderGroveWorktrees(wPath, gitRoot, planRepos(plan)) {
					wPathClean := filepath.Clean(wPath)
					if rmErr := os.RemoveAll(wPathClean); rmErr != nil {
						return fmt.Errorf("force-remove worktree dir: %w", rmErr)
					}
					// Git leaves orphaned metadata in .git/worktrees/<name>
					// when `worktree remove` fails with "Directory not
					// empty"; prune it so `git worktree list` stays clean.
					if pruneErr := executor.Execute("git", "-C", gitRoot, "worktree", "prune"); pruneErr != nil {
						fmt.Fprintf(out, "    Warning: failed to prune worktree metadata: %v\n", pruneErr)
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
					if subErr := removeLinkedSubmoduleWorktrees(context.Background(), out, gitRoot, worktreeName, provider, forceEnabled()); subErr != nil {
						fmt.Fprintf(out, "    Warning: failed to prune linked submodule worktree metadata: %v\n", subErr)
					}
				}
				// DeleteUnlessFinished, not Delete: see the ecosystem branch
				// above — a tombstone recorded by the provenance item must
				// survive the prune that follows it.
				_, _ = worktreeregistry.DeleteUnlessFinished(pathutil.WorktreeID(wPath))
				// Reap any stray legacy superproject stub left at the in-repo
				// `<gitRoot>/.grove-worktrees/<name>` location by an older
				// legacy-only worktree-prep path. In the single-repo case the
				// prune above usually already removed it (wPath == that path), so
				// this is a no-op then; it matters when a duplicate stub coexists
				// with a differently-located real worktree.
				reapLegacyStubWorktree(context.Background(), out, gitRoot, worktreeName, forceEnabled())
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
				fmt.Fprintf(out, "    Pruning broken dev links...\n")
				if err := executor.Execute("grove", "dev", "prune"); err != nil {
					fmt.Fprintf(out, "    Note: grove dev prune failed: %v\n", err)
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
				if forceEnabled() {
					deleteFlag = "-D"
				}
				foreachCmd := fmt.Sprintf("git branch %s %s 2>/dev/null || true", deleteFlag, branchName)
				cmd := exec.Command("git", "-C", gitRoot, "submodule", "foreach", foreachCmd)
				_ = cmd.Run()
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
							if forceEnabled() {
								removeWorktreeArgs = append(removeWorktreeArgs, "--force")
							}
							removeWorktreeArgs = append(removeWorktreeArgs, wtPath)
							removeWorktreeCmd := exec.Command("git", removeWorktreeArgs...)
							if output, err := removeWorktreeCmd.CombinedOutput(); err != nil {
								if !strings.Contains(string(output), "not a working tree") && !strings.Contains(string(output), "No such file") {
									fmt.Fprintf(out, "    Note: could not remove worktree for %s from main checkout: %s\n", submoduleName, string(output))
								}
							}
							deleteCmd := exec.Command("git", "-C", mainSubmodulePath, "branch", deleteFlag, branchName)
							if output, err := deleteCmd.CombinedOutput(); err != nil {
								outputStr := string(output)
								if !strings.Contains(outputStr, "not found") {
									fmt.Fprintf(out, "    Warning: could not delete branch '%s' from %s main checkout: %v\n", branchName, submoduleName, err)
								}
							}
						}
						if localRepoPath, hasLocal := localWorkspaces[submoduleName]; hasLocal {
							removeWorktreeArgs := []string{"-C", localRepoPath, "worktree", "remove"}
							if forceEnabled() {
								removeWorktreeArgs = append(removeWorktreeArgs, "--force")
							}
							removeWorktreeArgs = append(removeWorktreeArgs, wtPath)
							removeWorktreeCmd := exec.Command("git", removeWorktreeArgs...)
							if output, err := removeWorktreeCmd.CombinedOutput(); err != nil {
								if !strings.Contains(string(output), "not a working tree") && !strings.Contains(string(output), "No such file") {
									fmt.Fprintf(out, "    Note: could not remove worktree for %s from local workspace: %s\n", submoduleName, string(output))
								}
							}
							deleteCmd := exec.Command("git", "-C", localRepoPath, "branch", deleteFlag, branchName)
							if output, err := deleteCmd.CombinedOutput(); err != nil {
								outputStr := string(output)
								if !strings.Contains(outputStr, "not found") {
									fmt.Fprintf(out, "    Warning: failed to delete branch '%s' from %s local workspace: %v\n", branchName, submoduleName, err)
								}
							}
						}
					}
				}
				// Branch-delete failures degrade to warnings above: the worktree is
				// already pruned, so they must not abort mark_finished + archive_plan.
				return nil
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
					for _, repo := range plan.Config.Repos {
						repoPath, ok := sources[repo]
						if !ok {
							continue
						}
						if err := exec.Command("git", "-C", repoPath, "show-ref", "--verify", "--quiet", "refs/heads/"+branchName).Run(); err != nil { //nolint:gosec // branchName is internal
							continue // branch already gone in this repo
						}
						if err := deleteLocalBranch(repoPath, branchName, forceEnabled()); err != nil {
							// Degrade to a warning: the worktree has already been
							// pruned by this point, so a branch-delete failure must
							// NOT abort the finish (which would skip mark_finished +
							// archive_plan and strand the plan in 'review').
							fmt.Fprintf(out, "    Warning: failed to delete branch '%s' from %s: %v\n", branchName, repo, err)
							continue
						}
						fmt.Fprintf(out, "    Deleted branch '%s' from %s\n", branchName, repo)
					}
					return nil
				}
				// Authoritative merged-check independent of gitRoot's ambient
				// HEAD (Bug 2): `git branch -d` treats an ancestor-of-main branch
				// as "not fully merged" when HEAD is parked elsewhere. Consult
				// main/master directly and escalate to -D when provably merged.
				delFlag := "-d"
				if forceEnabled() {
					delFlag = "-D"
				} else {
					for _, baseBranch := range []string{"main", "master"} {
						if exec.Command("git", "-C", gitRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+baseBranch).Run() != nil { //nolint:gosec // baseBranch is a literal
							continue
						}
						if exec.Command("git", "-C", gitRoot, "merge-base", "--is-ancestor", branchName, baseBranch).Run() == nil { //nolint:gosec // refs are internal
							delFlag = "-D" // provably merged; -D is safe
							break
						}
					}
				}
				err := executor.Execute("git", "-C", gitRoot, "branch", delFlag, branchName)
				if err != nil {
					if strings.Contains(err.Error(), "not found") {
						// Idempotent: the branch is already gone.
						return nil
					}
					if strings.Contains(err.Error(), "checked out at") {
						// Worktree was pruned earlier in the cleanup
						// sequence; the branch ref just has a stale
						// worktree pointer, so -D here does not lose
						// work.
						fmt.Fprintf(out, "    Using -D (force) to delete branch that was in worktree...\n")
						if derr := executor.Execute("git", "-C", gitRoot, "branch", "-D", branchName); derr != nil {
							fmt.Fprintf(out, "    Warning: failed to delete branch '%s': %v\n", branchName, derr)
						}
						return nil
					}
					// Any other failure (including a genuinely-unmerged branch
					// without --force) degrades to a warning so archive +
					// mark_finished still run; the branch is left intact, so no
					// unmerged work is destroyed.
					fmt.Fprintf(out, "    Warning: failed to delete branch '%s': %v\n", branchName, err)
				}
				return nil
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
			ID:       ItemRebuildBinaries,
			Name:     "Rebuild main repo binaries via grove build",
			Advanced: true,
			Check: func() (string, error) {
				if gitRoot == "" {
					return "N/A", nil
				}
				if _, err := exec.LookPath("grove"); err != nil {
					return "N/A (grove not found)", nil
				}
				// "(opt-in)" is honest labelling, not a gate: the item is never
				// auto-enabled (the wizard enables nothing by itself, and the
				// CLI's --yes loop skips it unless --rebuild-binaries was
				// passed). Availability only decides whether the user is
				// ALLOWED to ask for it.
				return color.YellowString("Available (opt-in)"), nil
			},
			Action: func() error {
				if gitRoot == "" {
					return nil
				}
				fmt.Fprintf(out, "    Building binaries through grove build...\n")
				buildCmd := exec.Command("grove", "build")
				buildCmd.Dir = gitRoot
				buildCmd.Stdout = out
				buildCmd.Stderr = out
				// A rebuild is not teardown. Returning an error here would make
				// a red build the run's firstErr, which skips archive_plan and
				// mark_finished and leaves the plan un-archived — a build
				// failure must never be able to do that.
				if err := buildCmd.Run(); err != nil {
					fmt.Fprintf(out, "    Warning: grove build failed: %v\n", err)
				}
				return nil
			},
		},
		{
			ID:   ItemClearNavBindings,
			Name: "Clear sessionizer keymap entries",
			Check: func() (string, error) {
				if worktreePath == "" {
					return "N/A", nil
				}
				ctx := context.Background()
				client := daemon.New()
				defer client.Close()
				bindings, err := client.GetNavBindings(ctx)
				if err != nil {
					return "Daemon unavailable", nil
				}
				if n := countNavBindingsUnderPath(bindings, worktreePath); n > 0 {
					return color.YellowString("%d stale key(s)", n), nil
				}
				return "None", nil
			},
			Action: func() error {
				if worktreePath == "" {
					return nil
				}
				ctx := context.Background()
				client := daemon.New()
				defer client.Close()
				bindings, err := client.GetNavBindings(ctx)
				if err != nil {
					// Daemon not running; nothing to clear.
					return nil
				}
				removed := pruneNavBindingsUnderPath(bindings, worktreePath)
				if removed == 0 {
					return nil
				}
				// Persist the default (top-level) group and every named
				// group we touched. UpdateNavGroup with "default" replaces
				// the top-level Sessions map; a named group replaces that
				// group's Sessions map.
				if err := client.UpdateNavGroup(ctx, "default", models.NavGroupState{
					Sessions:   bindings.Sessions,
					LockedKeys: bindings.LockedKeys,
				}); err != nil {
					return fmt.Errorf("failed to update default nav bindings: %w", err)
				}
				for name, group := range bindings.Groups {
					if err := client.UpdateNavGroup(ctx, name, group); err != nil {
						return fmt.Errorf("failed to update nav group %q: %w", name, err)
					}
				}
				fmt.Fprintf(out, "    Cleared %d stale sessionizer keymap entr(ies)\n", removed)
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
				fmt.Fprintf(out, "    Archived plan to: %s\n", archivePath)
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
		item.IsAvailable = statusIsAvailable(status)
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
func RunOnFinishHook(plan *orchestration.Plan, planName string, out io.Writer) {
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
	fmt.Fprintln(out, "▶️  Executing on_finish hook...")
	templateData := struct {
		PlanName string
		NoteRef  string
	}{PlanName: planName, NoteRef: noteRef}
	tmpl, err := template.New("hook").Parse(hookCmdStr)
	if err != nil {
		fmt.Fprintf(out, "Warning: failed to parse on_finish hook template: %v\n", err)
		return
	}
	var renderedCmd bytes.Buffer
	if err := tmpl.Execute(&renderedCmd, templateData); err != nil {
		fmt.Fprintf(out, "Warning: failed to render on_finish hook command: %v\n", err)
		return
	}
	hookCmd := exec.Command("sh", "-c", renderedCmd.String()) //nolint:gosec // on_finish hook comes from trusted plan config
	hookCmd.Stdout = out
	hookCmd.Stderr = out
	if err := hookCmd.Run(); err != nil {
		fmt.Fprintf(out, "Warning: on_finish hook execution failed: %v\n", err)
	} else {
		fmt.Fprintln(out, "* on_finish hook executed successfully.")
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
func removeLinkedSubmoduleWorktrees(ctx context.Context, out io.Writer, gitRoot, worktreeName string, provider *workspace.Provider, force bool) error {
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
				fmt.Fprintf(out, "    Removing linked worktree for %s\n", submoduleName)
				removeArgs := []string{"worktree", "remove"}
				if force {
					removeArgs = append(removeArgs, "--force")
				}
				removeArgs = append(removeArgs, submoduleWorktreePath)
				removeCmd := exec.CommandContext(ctx, "git", removeArgs...)
				removeCmd.Dir = mainSubmodulePath
				if err := removeCmd.Run(); err != nil {
					fmt.Fprintf(out, "      Warning: failed to remove worktree from main checkout: %v\n", err)
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
			fmt.Fprintf(out, "    Removing linked worktree for %s\n", submoduleName)
			removeArgs := []string{"worktree", "remove"}
			if force {
				removeArgs = append(removeArgs, "--force")
			}
			removeArgs = append(removeArgs, submoduleWorktreePath)
			removeCmd := exec.CommandContext(ctx, "git", removeArgs...)
			removeCmd.Dir = localRepoPath
			if err := removeCmd.Run(); err != nil {
				fmt.Fprintf(out, "      Warning: failed to remove worktree: %v\n", err)
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

// navBindingPathStale reports whether a nav session path points at (or
// inside) the worktree being finished, so its keymap entry should be
// removed. The worktree container is removed by the prune step, so any
// binding at that path or a sub-repo path beneath it is now dead.
func navBindingPathStale(sessionPath, worktreePath string) bool {
	if sessionPath == "" || worktreePath == "" {
		return false
	}
	sp := filepath.Clean(sessionPath)
	wp := filepath.Clean(worktreePath)
	if sp == wp {
		return true
	}
	return strings.HasPrefix(sp, wp+string(filepath.Separator))
}

// countNavBindingsUnderPath counts nav session keys (across the default
// group and every named group) whose path is at or under worktreePath.
func countNavBindingsUnderPath(bindings *models.NavSessionsFile, worktreePath string) int {
	if bindings == nil {
		return 0
	}
	count := 0
	for _, cfg := range bindings.Sessions {
		if navBindingPathStale(cfg.Path, worktreePath) {
			count++
		}
	}
	for _, group := range bindings.Groups {
		for _, cfg := range group.Sessions {
			if navBindingPathStale(cfg.Path, worktreePath) {
				count++
			}
		}
	}
	return count
}

// pruneNavBindingsUnderPath removes, in place, every nav session key
// whose path is at or under worktreePath — from the default (top-level)
// group and every named group. Returns the number of entries removed so
// the caller can skip the daemon write when nothing changed.
func pruneNavBindingsUnderPath(bindings *models.NavSessionsFile, worktreePath string) int {
	if bindings == nil {
		return 0
	}
	removed := 0
	for key, cfg := range bindings.Sessions {
		if navBindingPathStale(cfg.Path, worktreePath) {
			delete(bindings.Sessions, key)
			removed++
		}
	}
	for _, group := range bindings.Groups {
		for key, cfg := range group.Sessions {
			if navBindingPathStale(cfg.Path, worktreePath) {
				delete(group.Sessions, key)
				removed++
			}
		}
	}
	return removed
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
//
// Before the safe delete it runs an authoritative merged-check. `git branch -d`
// evaluates "merged" against the repo's ambient HEAD (or the branch's upstream),
// so a branch that IS an ancestor of main is falsely reported "not fully merged"
// whenever HEAD is parked on some other branch — the common case after a
// worktree prune, where the source repo's checkout is on its own feature branch
// or a stale ref. We instead consult main/master directly with
// `git merge-base --is-ancestor <branch> <base>`; when the branch is provably an
// ancestor of a base, we escalate to `-D` so the delete still succeeds WITHOUT
// destroying any unmerged work (there is none — it's already in the base).
func deleteLocalBranch(repoPath, branchName string, force bool) error {
	if !force {
		for _, baseBranch := range []string{"main", "master"} {
			if exec.Command("git", "-C", repoPath, "show-ref", "--verify", "--quiet", "refs/heads/"+baseBranch).Run() != nil { //nolint:gosec // baseBranch is a literal
				continue
			}
			if exec.Command("git", "-C", repoPath, "merge-base", "--is-ancestor", branchName, baseBranch).Run() == nil { //nolint:gosec // refs are internal
				// Provably merged into a base branch: -D is safe.
				force = true
				break
			}
		}
	}

	delFlag := "-d"
	if force {
		delFlag = "-D"
	}
	out, err := exec.Command("git", "-C", repoPath, "branch", delFlag, branchName).CombinedOutput() //nolint:gosec // branchName is internal
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

// dirtyEcosystemRepos returns the names of the container's per-repo worktrees
// that hold uncommitted work, in the plan's repo order. Repos whose subdir is
// absent or whose status cannot be read are reported as clean: this is a
// pre-flight warning, not a gate, and a false alarm is worse than a miss.
func dirtyEcosystemRepos(containerDir string, repos []string) []string {
	var dirty []string
	for _, repo := range repos {
		repoDir := filepath.Join(containerDir, repo)
		if _, err := os.Stat(repoDir); err != nil {
			continue
		}
		statusOutput, err := exec.Command("git", "-C", repoDir, "status", "--porcelain", "--ignore-submodules").Output()
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(statusOutput)) != "" {
			dirty = append(dirty, repo)
		}
	}
	return dirty
}

func cleanupEcosystemWorktree(ctx context.Context, out io.Writer, gitRoot, worktreeName string, repos []string, provider *workspace.Provider, force bool) error {
	ecosystemDir, ok := resolveContainerWorktreePath(gitRoot, worktreeName, provider)
	if !ok {
		ecosystemDir = workspace.ResolveNewWorktreePath(gitRoot, worktreeName, false)
	}
	fmt.Fprintf(out, "    Cleaning up ecosystem worktree at %s\n", ecosystemDir)
	var localWorkspaces map[string]string
	if provider != nil {
		localWorkspaces = provider.LocalWorkspacesInEcosystem(gitRoot)
	} else {
		fmt.Fprintf(out, "    Warning: workspace discovery failed, cannot clean up submodule branches\n")
		localWorkspaces = make(map[string]string)
	}
	// retained collects one entry per repo whose worktree survived, with the
	// reason. A repo holding uncommitted work is a fact about THAT repo: it
	// must not veto the teardown of anything unrelated to it, and the reason
	// must reach the caller intact (the raw git message is the only thing that
	// tells the user what to do next).
	var retained []string
	recordErr := func(err error) {
		if err != nil {
			retained = append(retained, err.Error())
		}
	}
	for _, repo := range repos {
		repoWorktreePath := filepath.Join(ecosystemDir, repo)
		fmt.Fprintf(out, "    • %s: removing worktree\n", repo)
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
			fmt.Fprintf(out, "      Warning: repo '%s' not found in local workspaces, skipping branch cleanup\n", repo)
			if force {
				if !pathIsUnderGroveWorktrees(repoWorktreePath, gitRoot, repos) {
					fmt.Fprintf(out, "      Warning: refusing to remove %s (outside worktree boundary)\n", repoWorktreePath)
				} else if err := os.RemoveAll(repoWorktreePath); err != nil {
					fmt.Fprintf(out, "      Warning: failed to remove directory %s: %v\n", repoWorktreePath, err)
				}
			} else if _, err := os.Stat(repoWorktreePath); err == nil {
				// Without force we don't know if the orphaned
				// directory holds uncommitted work, so refuse to
				// blow it away.
				fmt.Fprintf(out, "      Refusing to remove %s without --force (repo not in workspaces, cannot verify clean state)\n", repoWorktreePath)
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
				fmt.Fprintf(out, "      Warning: git worktree remove failed, removing directory manually: %s\n", outputStr)
				if !pathIsUnderGroveWorktrees(repoWorktreePath, gitRoot, repos) {
					fmt.Fprintf(out, "      Warning: refusing to remove %s (outside worktree boundary)\n", repoWorktreePath)
				} else if err := os.RemoveAll(repoWorktreePath); err != nil {
					fmt.Fprintf(out, "      Warning: failed to remove directory %s: %v\n", repoWorktreePath, err)
				}
			} else {
				fmt.Fprintf(out, "      Error: git worktree remove failed for %s: %s\n", repo, outputStr)
				// Carry git's own message: "contains modified or untracked
				// files, use --force to delete it" is the only text that tells
				// the user what actually blocked the removal, and the TUI has
				// no other channel to it.
				recordErr(fmt.Errorf("%s: %s", repo, firstLine(outputStr)))
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
				fmt.Fprintf(out, "      Warning: failed to prune stale worktree registration in %s: %s\n", repoPath, string(output))
			}
		}
	}
	// A retained repo means the CONTAINER must stay (it still holds work), but
	// nothing else about the teardown is contingent on it. The ecosystem-level
	// prune below still runs, and the caller still deletes the registry entry,
	// marks the plan finished and archives it — a dirty file in one repo is not
	// a reason to leave the plan listed forever.
	if len(retained) == 0 {
		if !pathIsUnderGroveWorktrees(ecosystemDir, gitRoot, repos) {
			return fmt.Errorf("refusing to remove ecosystem directory outside worktree boundary: %s", ecosystemDir)
		}
		if err := os.RemoveAll(ecosystemDir); err != nil {
			return fmt.Errorf("failed to remove ecosystem directory: %w", err)
		}
	} else {
		fmt.Fprintf(out, "    Keeping %s: %d repo(s) still hold work\n", ecosystemDir, len(retained))
	}
	// The ecosystem worktree dir is removed with os.RemoveAll (not `git
	// worktree remove`), so the ecosystem repo still carries a stale worktree
	// registration pointing at the now-deleted dir. Prune it, otherwise a
	// later `git branch -d <worktree>` (the delete-branch finish step) is
	// refused with "cannot delete branch ... used by worktree".
	pruneCmd := exec.CommandContext(ctx, "git", "-C", gitRoot, "worktree", "prune")
	if output, err := pruneCmd.CombinedOutput(); err != nil {
		fmt.Fprintf(out, "    Warning: failed to prune stale worktree registration in %s: %s\n", gitRoot, string(output))
	}
	if len(retained) > 0 {
		return &RetainedWorktreeError{Details: retained}
	}
	fmt.Fprintf(out, "    * Ecosystem worktree removed successfully\n")
	return nil
}

// firstLine returns the first non-empty line of s, trimmed. git's failure
// messages are one useful line plus noise; the useful line is what the user
// needs to see in a status bar.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return strings.TrimSpace(s)
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
func reapLegacyStubWorktree(ctx context.Context, out io.Writer, gitRoot, worktreeName string, force bool) {
	if gitRoot == "" || worktreeName == "" {
		return
	}
	// The legacy (non-XDG) location for this ecosystem's worktree.
	legacyPath := workspace.ResolveNewWorktreePath(gitRoot, worktreeName, false)
	if !pathIsUnderGroveWorktrees(legacyPath, gitRoot, nil) {
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
	listOut, err := listCmd.Output()
	if err != nil || !worktreeListContainsPath(string(listOut), legacyPath) {
		return
	}
	fmt.Fprintf(out, "    Reaping stray legacy worktree stub at %s\n", legacyPath)
	args := []string{"-C", gitRoot, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, legacyPath)
	if err := exec.CommandContext(ctx, "git", args...).Run(); err != nil {
		fmt.Fprintf(out, "      Warning: failed to remove legacy worktree stub: %v\n", err)
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

// planRepos returns the participating repo names of plan, or nil when the plan
// carries no repo list. It exists so callers can pass the anchor-scope to
// pathIsUnderGroveWorktrees without repeating the nil-guard.
func planRepos(plan *orchestration.Plan) []string {
	if plan == nil || plan.Config == nil {
		return nil
	}
	return plan.Config.Repos
}

// pathIsUnderGroveWorktrees reports whether wPath is a safe target for
// force-removal: it must live strictly beneath one of the worktree bases
// (the identifier-level container directories returned by
// workspace.WorktreeBases) of gitRoot OR of one of this ecosystem's anchor
// repos. Returns false if gitRoot is empty, if wPath is a base container
// itself, or if wPath escapes every base (after cleaning). Guards os.RemoveAll
// against catastrophic misuse.
//
// repos is the plan's participating repo names. An anchored worktree
// (`--anchor <sub-repo>`) lives under the ANCHOR repo's OWN identifier dir
// (WorktreeBases(<gitRoot>/<repo>)), not gitRoot's — so the guard must accept
// those too. Crucially it does so by matching each anchor repo's *specific*
// identifier dir, NOT by blanket-accepting anything under the shared XDG
// worktrees tree: a different clone's worktrees live under a different
// identifier and must stay out of bounds (cross-clone protection).
func pathIsUnderGroveWorktrees(wPath, gitRoot string, repos []string) bool {
	if gitRoot == "" || wPath == "" {
		return false
	}
	wPathClean := filepath.Clean(wPath)

	// underBasesOf reports true when wPath is strictly beneath one of root's
	// worktree bases, and false when it equals a base container (never a
	// removal target). The second return says whether a decision was made.
	underBasesOf := func(root string) (bool, bool) {
		for _, base := range workspace.WorktreeBases(root) {
			container := filepath.Clean(base)
			if wPathClean == container {
				return false, true
			}
			if strings.HasPrefix(wPathClean, container+string(filepath.Separator)) {
				return true, true
			}
		}
		return false, false
	}

	if ok, decided := underBasesOf(gitRoot); decided {
		return ok
	}
	// Anchor repos: an `--anchor <sub-repo>` worktree lives under the sub-repo's
	// identifier dir. Scope acceptance to THIS ecosystem's repos only.
	for _, r := range repos {
		if ok, decided := underBasesOf(filepath.Join(gitRoot, r)); decided {
			return ok
		}
	}
	return false
}

// canonicalizePathForBoundary returns an absolute, symlink-resolved form of p
// suitable for boundary comparisons. Unlike a bare EvalSymlinks, p need not
// exist yet: the longest EXISTING ancestor is resolved and the non-existent
// suffix re-joined, so a not-yet-created /var/... destination still compares
// equal to its /private/var/... archive root on macOS.
func canonicalizePathForBoundary(p string) string {
	abs := p
	if a, err := filepath.Abs(p); err == nil {
		abs = a
	}
	abs = filepath.Clean(abs)
	prefix := abs
	suffix := ""
	for {
		if resolved, err := filepath.EvalSymlinks(prefix); err == nil {
			return filepath.Clean(filepath.Join(resolved, suffix))
		}
		parent := filepath.Dir(prefix)
		if parent == prefix {
			return abs
		}
		suffix = filepath.Join(filepath.Base(prefix), suffix)
		prefix = parent
	}
}

// pathIsUnderWorktreeArchive reports whether destPath lies STRICTLY beneath
// paths.WorktreeArchiveDir(). It is the archive-side counterpart of
// pathIsUnderGroveWorktrees, which guards removal of LIVE worktrees and
// rejects the archive base by design — so the archive move needs its own
// destination guard. The archive root itself is never a valid destination.
// Comparisons are symlink-resolved (see worktreeListContainsPath) so macOS
// /var vs /private/var spellings still match.
func pathIsUnderWorktreeArchive(destPath string) bool {
	base := paths.WorktreeArchiveDir()
	if base == "" || destPath == "" {
		return false
	}
	root := canonicalizePathForBoundary(base)
	dest := canonicalizePathForBoundary(destPath)
	return dest != root && strings.HasPrefix(dest, root+string(filepath.Separator))
}

// archiveDestAttempts bounds the disambiguation search below. A hundred
// archives of the same worktree name under the same owner is far past the point
// where an operator should be told to go and tidy up by hand.
const archiveDestAttempts = 100

// uniqueArchiveDest returns base when nothing occupies it, and otherwise the
// first free "<base>-<n>" for n in 2..archiveDestAttempts.
//
// A destination collision means a worktree of this name was archived before —
// ordinary when a plan slug is reused. It is a naming accident, not a safety
// signal, so it must not fail the archive: archiving is now the DEFAULT
// retirement under `flow plan finish --yes`, and refusing there would break a
// finish that used to succeed over something only a manual `mv` can fix, while
// nudging the operator toward `--prune-worktree` — deleting the very code the
// archive exists to keep. Suffixing keeps BOTH archives; nothing is overwritten
// and no history is lost.
//
// A non-NotExist stat error (permissions, a dangling symlink) counts as
// occupied: the archive move must never land on a path it cannot inspect.
func uniqueArchiveDest(base string) (string, error) {
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return base, nil
	}
	for n := 2; n <= archiveDestAttempts; n++ {
		candidate := fmt.Sprintf("%s-%d", base, n)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("archive destination %s already exists and the first %d disambiguated names are taken too; "+
		"clear out %s before finishing again", base, archiveDestAttempts-1, filepath.Dir(base))
}

// ownerFromWorktreeGitPointer parses the `.git` FILE of the linked worktree
// checkout at worktreeDir ("gitdir: <owner>/.git/worktrees/<name>", or
// "<bare>/worktrees/<name>" for bare owners) and returns the owning repo
// root. It mirrors core/pkg/workspace's ownerFromGitdir (layout.go), which is
// unexported; parsing the pointer directly is exact and — unlike the
// provider-based resolution in cleanupEcosystemWorktree — needs no workspace
// discovery, which suits the archive path (read the pointer, then delete it).
// Returns ok=false when .git is missing, a directory, or not a pointer file.
func ownerFromWorktreeGitPointer(worktreeDir string) (string, bool) {
	content, err := os.ReadFile(filepath.Join(worktreeDir, ".git"))
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(content))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if gitdir == "" {
		return "", false
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(worktreeDir, gitdir)
	}
	gitdir = filepath.Clean(gitdir)
	sep := string(filepath.Separator)
	// Normal owners: <owner>/.git/worktrees/<name>
	if i := strings.LastIndex(gitdir, sep+".git"+sep+"worktrees"+sep); i >= 0 {
		return gitdir[:i], true
	}
	// Bare owners: <bare>/worktrees/<name>
	if i := strings.LastIndex(gitdir, sep+"worktrees"+sep); i >= 0 {
		return gitdir[:i], true
	}
	return "", false
}

// archiveWorktreeContainer detaches every git checkout inside containerPath
// from its owner repo and moves the whole container to destPath (under the
// worktree archive). The caller has already validated destPath (exists-check
// + pathIsUnderWorktreeArchive) and updates the worktree registry afterwards.
//
// Sequence, per checkout (repo subdirs for ecosystem containers — from the
// plan's repos list, like cleanupEcosystemWorktree — plus the container
// itself; single-repo worktrees ARE the checkout):
//
//  1. `git bundle create <container>/<name>.bundle --all` captures every ref
//     (including unpushed commits) as standalone history BEFORE the
//     branch-deletion finish items delete the refs. Bundles refs, not the
//     index, so a dirty tree is fine — and dirty/untracked files are
//     preserved by the move itself.
//  2. The `.git` gitdir POINTER FILE (never a directory) and the
//     `.grove/workspace` owner marker are deleted so the archived copy can
//     no longer resolve (or write through to) an owner repo.
//  3. Each owner repo gets a `git worktree prune` (raw exec, matching
//     cleanupEcosystemWorktree) to scrub the now-dangling registration.
//  4. The container is moved with os.Rename, falling back to fs.CopyDir +
//     os.RemoveAll of the source — the RemoveAll is gated by
//     pathIsUnderGroveWorktrees, the same guard the prune item uses.
//
// Failures on individual repos (not a git checkout, bundle failure) are
// logged warnings, not fatal: archiving a partially-degraded container is
// still strictly better than losing it.
func archiveWorktreeContainer(ctx context.Context, out io.Writer, containerPath, destPath, gitRoot string, repos []string) error {
	type detachTarget struct {
		dir        string
		bundleName string
	}
	var targets []detachTarget
	if len(repos) > 0 {
		for _, repo := range repos {
			targets = append(targets, detachTarget{
				dir:        filepath.Join(containerPath, repo),
				bundleName: repo + ".bundle",
			})
		}
		// The container itself may be a linked worktree of the ecosystem
		// root (rather than a plain synthetic dir); detach it too. The
		// fixed bundle name cannot collide with a repo subdir's bundle.
		targets = append(targets, detachTarget{dir: containerPath, bundleName: "ecosystem.bundle"})
	} else {
		targets = append(targets, detachTarget{
			dir:        containerPath,
			bundleName: filepath.Base(gitRoot) + ".bundle",
		})
	}

	var owners []string
	seenOwner := map[string]bool{}
	addOwner := func(p string) {
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		if !seenOwner[p] {
			seenOwner[p] = true
			owners = append(owners, p)
		}
	}

	for _, tgt := range targets {
		if _, err := os.Stat(tgt.dir); err != nil {
			if tgt.dir != containerPath {
				fmt.Fprintf(out, "    Warning: repo dir %s missing, skipping\n", tgt.dir)
			}
			continue
		}
		gitPtr := filepath.Join(tgt.dir, ".git")
		fi, err := os.Lstat(gitPtr)
		if err == nil {
			// Archives preserve source, commits, and untracked work, not dependency
			// caches or generated files. Git's ignored set is the safest generic
			// definition of disposable content and handles target/dist/.cache in
			// addition to language-specific dependency trees.
			cleanCmd := exec.CommandContext(ctx, "git", "-C", tgt.dir, "clean", "-ffdX")
			if output, cleanErr := cleanCmd.CombinedOutput(); cleanErr != nil {
				fmt.Fprintf(out, "    Warning: failed to prune ignored files in %s: %s\n", tgt.dir, strings.TrimSpace(string(output)))
			}
		}
		switch {
		case err != nil:
			// No .git at all. Expected for a synthetic ecosystem
			// container; suspicious for a repo subdir.
			if tgt.dir != containerPath {
				fmt.Fprintf(out, "    Warning: %s is not a git checkout, skipping bundle\n", tgt.dir)
			}
		case fi.Mode().IsRegular():
			if owner, ok := ownerFromWorktreeGitPointer(tgt.dir); ok {
				addOwner(owner)
			}
			bundlePath := filepath.Join(containerPath, tgt.bundleName)
			bundleCmd := exec.CommandContext(ctx, "git", "-C", tgt.dir, "bundle", "create", bundlePath, "--all")
			if output, bundleErr := bundleCmd.CombinedOutput(); bundleErr != nil {
				fmt.Fprintf(out, "    Warning: git bundle failed for %s: %s\n", tgt.dir, strings.TrimSpace(string(output)))
			}
			// Delete the gitdir pointer FILE — never a directory — so
			// the archived copy cannot resolve an owner.
			if rmErr := os.Remove(gitPtr); rmErr != nil {
				return fmt.Errorf("failed to remove .git pointer %s: %w", gitPtr, rmErr)
			}
		default:
			// A full .git DIRECTORY means a standalone clone, not a
			// linked worktree: it owns its own object store, needs no
			// bundle for safety and no detaching. Leave it intact.
			fmt.Fprintf(out, "    Note: %s has a full .git directory (standalone clone); left intact\n", tgt.dir)
		}
		marker := filepath.Join(tgt.dir, ".grove", "workspace")
		if mfi, mErr := os.Lstat(marker); mErr == nil && mfi.Mode().IsRegular() {
			if rmErr := os.Remove(marker); rmErr != nil {
				fmt.Fprintf(out, "    Warning: failed to remove workspace marker %s: %v\n", marker, rmErr)
			}
		}
	}

	// node_modules is always reproducible and is sometimes missing from a
	// repository's ignore rules. Remove it (and package-manager stores) by
	// name throughout the container before paying archive copy/storage costs.
	if err := pruneArchiveDependencyDirs(containerPath); err != nil {
		fmt.Fprintf(out, "    Warning: failed to prune dependency directories: %v\n", err)
	}

	// Scrub the now-dangling worktree registrations in each owner repo
	// (raw exec, matching the pattern in cleanupEcosystemWorktree).
	for _, owner := range owners {
		pruneCmd := exec.CommandContext(ctx, "git", "-C", owner, "worktree", "prune")
		if output, pruneErr := pruneCmd.CombinedOutput(); pruneErr != nil {
			fmt.Fprintf(out, "    Warning: failed to prune stale worktree registration in %s: %s\n", owner, string(output))
		}
	}

	// Move the container into the archive.
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("failed to create archive directory: %w", err)
	}
	if err := os.Rename(containerPath, destPath); err != nil {
		if err := fs.CopyDir(containerPath, destPath); err != nil {
			return fmt.Errorf("failed to copy worktree to archive: %w", err)
		}
		if !pathIsUnderGroveWorktrees(containerPath, gitRoot, repos) {
			return fmt.Errorf("worktree copied to %s but refusing to remove source outside worktree boundary: %s", destPath, containerPath)
		}
		if err := os.RemoveAll(filepath.Clean(containerPath)); err != nil {
			return fmt.Errorf("failed to remove original worktree directory: %w", err)
		}
	}
	return nil
}

// pruneArchiveDependencyDirs removes dependency caches that are never source.
// WalkDir does not follow symlinked directories, and .git is skipped so a
// standalone clone's object database is never traversed or altered.
func pruneArchiveDependencyDirs(root string) error {
	removable := map[string]bool{"node_modules": true, ".pnpm-store": true}
	return filepath.WalkDir(root, func(path string, entry stdfs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		if entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if path != root && removable[entry.Name()] {
			if err := os.RemoveAll(path); err != nil {
				return err
			}
			return filepath.SkipDir
		}
		return nil
	})
}
