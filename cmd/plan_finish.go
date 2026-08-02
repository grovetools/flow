package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fatih/color"
	groveplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/state"
	"github.com/spf13/cobra"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/plan_finish"
	"github.com/grovetools/flow/pkg/tui/wizards/finish"
)

// NewPlanFinishCmd creates the `plan finish` command.
func NewPlanFinishCmd() *cobra.Command {
	opts := &plan_finish.Options{}
	cmd := &cobra.Command{
		Use:   "finish [directory]",
		Short: "Finish and clean up a plan and its associated worktree (use: flow finish)",
		Long: `Guides through the process of cleaning up a completed plan.
This can include removing the git worktree, deleting the branch, closing tmux sessions, and archiving the plan.

Plans can be referenced by slug from any directory. Use --dir to specify the workspace context.

Examples:
  flow finish my-feature                     # from any directory
  flow finish my-feature --dir ~/Code/myapp  # explicit workspace`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPlanFinish(cmd, args, opts)
		},
	}
	registerFinishFlags(cmd, opts)
	return cmd
}

// NewFinishCmd creates the top-level `finish` command.
func NewFinishCmd() *cobra.Command {
	opts := &plan_finish.Options{}
	cmd := &cobra.Command{
		Use:   "finish [directory]",
		Short: "Finish and clean up a plan and its associated worktree",
		Long: `Guides through the process of cleaning up a completed plan.
This can include removing the git worktree, deleting the branch, closing tmux sessions, and archiving the plan.

Plans can be referenced by slug from any directory. Use --dir to specify the workspace context.

Examples:
  flow finish my-feature                     # from any directory
  flow finish my-feature --dir ~/Code/myapp  # explicit workspace`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPlanFinish(cmd, args, opts)
		},
	}
	registerFinishFlags(cmd, opts)
	return cmd
}

func registerFinishFlags(cmd *cobra.Command, opts *plan_finish.Options) {
	cmd.Flags().BoolVarP(&opts.Yes, "yes", "y", false, "Automatically confirm all cleanup actions")
	cmd.Flags().BoolVar(&opts.DeleteBranch, "delete-branch", false, "Delete the local git branch")
	cmd.Flags().BoolVar(&opts.DeleteRemote, "delete-remote", false, "Delete the remote git branch")
	cmd.Flags().BoolVar(&opts.PruneWorktree, "prune-worktree", false, "DELETE the git worktree directory. Under --yes this is the only way to delete rather than archive; mutually exclusive with --archive-worktree")
	cmd.Flags().BoolVar(&opts.ArchiveWorktree, "archive-worktree", false, "Move the worktree to the grove worktree archive (detached backup with git bundles) instead of deleting it. This is what --yes does by default; mutually exclusive with --prune-worktree")
	cmd.Flags().BoolVar(&opts.CloseSession, "close-session", false, "Close the associated tmux session")
	cmd.Flags().BoolVar(&opts.CleanDevLinks, "clean-dev-links", false, "Clean up development binary links from the worktree")
	cmd.Flags().BoolVar(&opts.RebuildBinaries, "rebuild-binaries", false, "Rebuild binaries in the main repository")
	cmd.Flags().BoolVar(&opts.Archive, "archive", false, "Archive the plan directory to a local .archive subdirectory")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Force git operations (use with caution)")
	cmd.Flags().BoolVar(&opts.KeepEnv, "keep-env", false, "Skip environment teardown during cleanup")
	cmd.Flags().BoolVar(&opts.KeepWorktree, "keep-worktree", false, "Skip worktree removal during cleanup")
	cmd.Flags().BoolVar(&opts.PruneOrphans, "prune-orphans", false, "Run `grove env prune --worktree <slug> --yes` after env teardown (local orphans only)")
	cmd.Flags().BoolVar(&opts.PruneCloud, "prune-cloud", false, "Additionally pass --include-cloud to the orphan-prune step (requires --prune-orphans)")
	cmd.Flags().BoolVar(&opts.PreserveCloud, "preserve-cloud", false, "Honor skip_destroy during env teardown (preserve cloud resources across plan finish; default is to destroy)")
	cmd.Flags().BoolVar(&opts.KeepNotes, "keep-notes", false, "Skip moving the plan's linked notes to completed/ during finish")
	cmd.Flags().BoolVar(&opts.NoLedger, "no-ledger", false, "Skip writing the plan ledger note (commit ranges, landing receipts, final worktree state) to the notebook")
	cmd.Flags().StringVarP(&planContextDir, "dir", "d", "", "Workspace or plan directory context (defaults to current directory)")
}

// hasExplicitFinishFlags reports whether the user named any individual
// cleanup action on the command line. When they did (and --yes was not
// passed) the finish runs exactly those actions instead of opening the
// interactive checklist.
func hasExplicitFinishFlags(opts plan_finish.Options) bool {
	return opts.DeleteBranch || opts.DeleteRemote || opts.PruneWorktree ||
		opts.ArchiveWorktree || opts.CloseSession || opts.CleanDevLinks ||
		opts.RebuildBinaries || opts.Archive || opts.Force
}

// applyFinishSelection decides which cleanup items run on the two
// non-interactive paths: --yes (confirm everything) and explicit-flag mode
// (run only what was named). It is a pure function over the item slice so the
// policy — in particular which of the two mutually exclusive worktree
// retirements runs by default — is testable without a plan on disk.
//
// The interactive TUI path never comes here: it starts with nothing ticked and
// the user chooses.
func applyFinishSelection(items []*finish.Item, opts plan_finish.Options) {
	// Item lookup is by stable ID so re-ordering in the factory can't
	// silently rewire CLI flags to the wrong action.
	enable := func(id string, on bool) {
		if it := plan_finish.ItemsByID(items, id); it != nil {
			it.IsEnabled = on && it.IsAvailable
		}
	}
	if opts.Yes {
		// --yes is the ONLY path that retires a worktree unattended, so
		// its default retirement is the RECOVERABLE one: archive_worktree
		// moves the container under the grove worktree archive, detached
		// from its owner repos and with a per-repo git bundle for any
		// unpushed history. prune_worktree, which `git worktree remove`s
		// and then os.RemoveAll's the container, is destruction — and
		// destruction has to be asked for.
		//
		// The two are mutually exclusive (they retire the same container),
		// so exactly one of them is enabled here, chosen by --prune-worktree.
		// Note what does NOT happen: when the archive item is unavailable,
		// prune is NOT promoted to take its place. "Could not archive" must
		// never silently become "deleted the worktree".
		pruneRequested := opts.PruneWorktree
		for _, item := range items {
			if item == nil {
				continue
			}
			switch item.ID {
			case plan_finish.ItemRebuildBinaries:
				// --yes confirms cleanup actions, but the main-repo binary
				// rebuild is a slow (~30s), non-teardown concern that must
				// stay opt-in. Enable it under --yes ONLY when the user
				// explicitly passed --rebuild-binaries; otherwise plain
				// `flow plan finish --yes` is fast teardown.
				if !opts.RebuildBinaries {
					item.IsEnabled = false
					continue
				}
			case plan_finish.ItemArchiveWorktree:
				item.IsEnabled = !pruneRequested && item.IsAvailable
				continue
			case plan_finish.ItemPruneWorktree:
				item.IsEnabled = pruneRequested && item.IsAvailable
				continue
			}
			item.IsEnabled = item.IsAvailable
		}
		return
	}
	// Always enable env teardown, agent cleanup, submodule merge, mark-finished.
	enable(plan_finish.ItemEnvTeardown, true)
	enable(plan_finish.ItemKillBoundAgents, true)
	enable(plan_finish.ItemMergeSubmodules, true)
	enable(plan_finish.ItemMarkFinished, true)
	// Provenance promotion is always on for the same reason mark_finished is:
	// it is not one of the destructive actions the explicit flags select
	// between, it is the record of what the finish is about to retire. Both
	// items are self-gating (the ledger reports Skipped under --no-ledger, the
	// tombstone reports Already finished / Not found), so "always enable"
	// means "never silently omitted", not "always writes".
	enable(plan_finish.ItemLedgerNote, true)
	enable(plan_finish.ItemTombstoneRegistry, true)
	enable(plan_finish.ItemCloseSession, opts.CloseSession)
	enable(plan_finish.ItemPruneWorktree, opts.PruneWorktree)
	enable(plan_finish.ItemArchiveWorktree, opts.ArchiveWorktree)
	// Retiring the worktree leaves its sessionizer keymap entries
	// dangling, so clear them whenever it is retired — by EITHER route.
	// Pruning deletes the container; archiving moves it out from under its
	// owner repos into the worktree archive. Both leave every keymap entry
	// pointing at a path that no longer exists.
	enable(plan_finish.ItemClearNavBindings, opts.PruneWorktree || opts.ArchiveWorktree)
	enable(plan_finish.ItemCleanDevLinks, opts.CleanDevLinks)
	enable(plan_finish.ItemDeleteSubmoduleBranches, opts.DeleteBranch)
	enable(plan_finish.ItemDeleteLocalBranch, opts.DeleteBranch)
	enable(plan_finish.ItemDeleteRemoteBranch, opts.DeleteRemote)
	enable(plan_finish.ItemRebuildBinaries, opts.RebuildBinaries)
	enable(plan_finish.ItemArchivePlan, opts.Archive)
}

func runPlanFinish(cmd *cobra.Command, args []string, opts *plan_finish.Options) error {
	if opts.ArchiveWorktree && opts.PruneWorktree {
		return fmt.Errorf("--archive-worktree and --prune-worktree are mutually exclusive")
	}
	var dir string
	if len(args) > 0 {
		dir = args[0]
	}

	contextDir := planContextDir
	if contextDir == "" {
		contextDir = "."
	}
	planPath, err := resolvePlanPathWithActiveJobCtx(cmd.Context(), dir, contextDir)
	if err != nil {
		return err
	}
	planName := filepath.Base(planPath)

	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	// Check if plan is ready for cleanup (must be in review or finished status)
	if plan.Config == nil || (plan.Config.Status != "review" && plan.Config.Status != "finished") {
		return fmt.Errorf("plan is not ready for cleanup. Please run 'flow plan review %s' first.", planName)
	}

	// If status is finished, it's a legacy plan or already processed, so we allow cleanup but warn.
	if plan.Config != nil && plan.Config.Status == "finished" {
		fmt.Println("WARNING:  Warning: This plan is already 'finished'. The new workflow uses a 'review' step.")
		fmt.Println("   Running cleanup directly. In the future, please run 'flow plan review' first.")
	}

	bctx := plan_finish.NewBuildContext(plan, planPath)
	result, err := plan_finish.BuildItems(bctx, *opts)
	if err != nil {
		return err
	}
	items := result.Items

	// Determine which items to enable based on flags.
	if opts.Yes || hasExplicitFinishFlags(*opts) {
		applyFinishSelection(items, *opts)
	} else {
		// Interactive TUI mode
		err := runFinishTUI(planName, items, result.BranchIsMerged, result.BranchExists)
		if err != nil {
			if err.Error() == "user aborted" {
				fmt.Println("\nCleanup aborted.")
				return nil
			}
			return err
		}
	}

	// Note lifecycle + on_finish hook run on EVERY finish (no longer gated on
	// status == "review"). Ordering is deliberate:
	//
	//  1. Native note handling runs FIRST and is authoritative — it queries nb
	//     for the plan's notes (by plan_ref) and moves each to completed/,
	//     wherever they currently live. Notes go straight to completed; they no
	//     longer pass through review/.
	//  2. The (legacy) on_finish hook runs SECOND. Old frozen hooks do
	//     `nb move "$OLD_PATH" completed`; by the time they run the note has
	//     already been relocated, so their move is a harmless no-op/failure.
	//     Combined with MoveNoteToGroup's idempotency (already-in-completed →
	//     already-completed, not an error) and RunOnFinishHook's non-fatal
	//     warnings, a stale frozen hook cannot corrupt the end state.
	//
	// This runs before the cleanup actions: ItemMarkFinished only flips
	// .grove-plan.yml status AFTER the destructive steps succeed, so writing it
	// here would orphan the plan slug if a later action failed.
	if !opts.KeepNotes {
		outcomes, err := orchestration.FinishPlanNotes(planName)
		if err != nil {
			fmt.Printf("Warning: could not query plan notes for finish: %v\n", err)
		} else {
			reportNoteOutcomes(outcomes)
		}
	}
	plan_finish.RunOnFinishHook(plan, planName, os.Stdout)

	// Resolve the active plan BEFORE the actions run. getActivePlanWithMigration
	// resolves from the process cwd, which for a finish launched inside the
	// plan's own worktree is a directory the actions are about to delete: after
	// the fact the lookup returns "" and the unset below is skipped in silence,
	// leaving the key set with no warning at all.
	activePlan, activePlanErr := getActivePlanWithMigration()
	activeStateDir := stateDir()

	// Execute enabled actions. Returns a non-nil error if any enabled
	// action failed; when that happens archive_plan and mark_finished
	// are skipped so the plan stays resolvable by slug for retry — except
	// for a retained worktree, which is a partial success and does not
	// stop the plan being finished.
	actionErr := executeFinishActions(items)
	if actionErr != nil && !plan_finish.IsRetainedWorktree(actionErr) {
		fmt.Fprintf(os.Stderr, "\ncleanup incomplete — plan left in 'review' status, re-run 'flow plan finish %s' after resolving the issue\n", planName)
		return actionErr
	}

	// Check if the finished plan was the active plan and unset it.
	if activePlanErr == nil && activePlan == planName {
		if err := state.Delete(activeStateDir, groveplan.StateKey); err != nil {
			if !errors.Is(err, state.ErrNoEcosystemRoot) {
				fmt.Printf("Warning: could not unset active plan: %v\n", err)
			}
		} else {
			_ = state.Delete(activeStateDir, groveplan.LegacyStateKey)
			fmt.Println("\n* Unset active plan")
		}
	}

	if actionErr != nil {
		// Retained worktree: the plan IS finished and archived, but the
		// operator still needs to know work survived on disk.
		fmt.Fprintf(os.Stderr, "\n%v\n", actionErr)
		return actionErr
	}

	fmt.Println("\nPlan cleanup finished.")
	return nil
}

// reportNoteOutcomes prints the per-note results of the native finish note
// handling. Every note is reported (moved / already-completed / failed) so a
// note that fails to move is visible rather than silently skipped.
func reportNoteOutcomes(outcomes []orchestration.NoteOutcome) {
	if len(outcomes) == 0 {
		return
	}
	fmt.Println("\nNote lifecycle (moving linked notes to completed):")
	for _, o := range outcomes {
		switch o.State {
		case orchestration.NoteFailed:
			fmt.Printf("  - %-50s %s\n", o.Path, color.RedString(o.String()))
		case orchestration.NoteAlreadyCompleted:
			fmt.Printf("  - %-50s %s\n", o.Path, color.YellowString(o.String()))
		default:
			fmt.Printf("  - %-50s %s\n", o.Path, color.GreenString(o.String()))
		}
	}
}

// executeFinishActions runs the Action closure on every enabled item,
// printing per-item success/failure. If an action fails, later items
// whose IDs are in terminalItemIDs (archive_plan, mark_finished) are
// skipped so a partial failure cannot orphan the plan slug. The first
// error encountered is returned; other non-terminal items continue to
// run. It is shared between the CLI path and any other host that
// wants to run cleanup with the same console output format.
func executeFinishActions(items []*finish.Item) error {
	fmt.Println("\nPerforming selected actions...")
	executed := false
	var firstErr error
	// blocking is what gates the terminal items. A retained worktree (one repo
	// still holding uncommitted work) is reported and returned, but it is a
	// partial success: it must not leave the plan un-archived and listed
	// forever, which is indistinguishable from "Finish Plan is broken".
	blocking := 0
	terminalItemIDs := map[string]bool{
		plan_finish.ItemArchivePlan:  true,
		plan_finish.ItemMarkFinished: true,
	}
	for _, item := range items {
		if item == nil || !item.IsEnabled || item.Action == nil {
			continue
		}
		if blocking > 0 && terminalItemIDs[item.ID] {
			fmt.Printf("  - %-40s... %s\n", item.Name, color.YellowString("Skipped (previous failure)"))
			continue
		}
		executed = true
		fmt.Printf("  - %-40s... ", item.Name)
		if err := item.Action(); err != nil {
			if plan_finish.IsRetainedWorktree(err) {
				fmt.Println(color.YellowString("Retained"))
			} else {
				fmt.Println(color.RedString("Failed"))
				blocking++
			}
			fmt.Printf("    %s\n", err)
			if firstErr == nil {
				firstErr = err
			}
		} else {
			fmt.Println(color.GreenString("Done"))
		}
	}
	if !executed {
		fmt.Println("No actions selected.")
	}
	return firstErr
}
