package cmd

import (
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
	cmd.Flags().BoolVar(&opts.PruneWorktree, "prune-worktree", false, "Remove the git worktree directory")
	cmd.Flags().BoolVar(&opts.ArchiveWorktree, "archive-worktree", false, "Move the worktree to the grove worktree archive (detached backup with git bundles) instead of deleting it; mutually exclusive with --prune-worktree")
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
	cmd.Flags().StringVarP(&planContextDir, "dir", "d", "", "Workspace or plan directory context (defaults to current directory)")
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

	// Determine which items to enable based on flags. Item lookup
	// is by stable ID so re-ordering in the factory can't silently
	// rewire CLI flags to the wrong action.
	enable := func(id string, on bool) {
		if it := plan_finish.ItemsByID(items, id); it != nil {
			it.IsEnabled = on && it.IsAvailable
		}
	}
	anyExplicitFlags := opts.DeleteBranch || opts.DeleteRemote || opts.PruneWorktree || opts.ArchiveWorktree || opts.CloseSession || opts.CleanDevLinks || opts.RebuildBinaries || opts.Archive || opts.Force
	if opts.Yes {
		// --yes confirms cleanup actions, but the main-repo binary
		// rebuild is a slow (~30s), non-teardown concern that must stay
		// opt-in. Enable it under --yes ONLY when the user explicitly
		// passed --rebuild-binaries; otherwise plain `flow plan finish
		// --yes` is fast teardown.
		for _, item := range items {
			if item.ID == plan_finish.ItemRebuildBinaries && !opts.RebuildBinaries {
				item.IsEnabled = false
				continue
			}
			// Archiving the worktree is strictly opt-in (like the
			// rebuild), and it replaces pruning: when requested, the
			// prune item must stay off or both would race over the
			// same container.
			if item.ID == plan_finish.ItemArchiveWorktree && !opts.ArchiveWorktree {
				item.IsEnabled = false
				continue
			}
			if item.ID == plan_finish.ItemPruneWorktree && opts.ArchiveWorktree {
				item.IsEnabled = false
				continue
			}
			item.IsEnabled = item.IsAvailable
		}
	} else if anyExplicitFlags {
		// Always enable env teardown, agent cleanup, submodule merge, mark-finished.
		enable(plan_finish.ItemEnvTeardown, true)
		enable(plan_finish.ItemKillBoundAgents, true)
		enable(plan_finish.ItemMergeSubmodules, true)
		enable(plan_finish.ItemMarkFinished, true)
		enable(plan_finish.ItemCloseSession, opts.CloseSession)
		enable(plan_finish.ItemPruneWorktree, opts.PruneWorktree)
		enable(plan_finish.ItemArchiveWorktree, opts.ArchiveWorktree)
		// Pruning the worktree leaves its sessionizer keymap entries
		// dangling (paths that no longer exist), so clear them whenever
		// the worktree is pruned.
		enable(plan_finish.ItemClearNavBindings, opts.PruneWorktree)
		enable(plan_finish.ItemCleanDevLinks, opts.CleanDevLinks)
		enable(plan_finish.ItemDeleteSubmoduleBranches, opts.DeleteBranch)
		enable(plan_finish.ItemDeleteLocalBranch, opts.DeleteBranch)
		enable(plan_finish.ItemDeleteRemoteBranch, opts.DeleteRemote)
		enable(plan_finish.ItemRebuildBinaries, opts.RebuildBinaries)
		enable(plan_finish.ItemArchivePlan, opts.Archive)
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
	plan_finish.RunOnFinishHook(plan, planName)

	// Execute enabled actions. Returns a non-nil error if any enabled
	// action failed; when that happens archive_plan and mark_finished
	// are skipped so the plan stays resolvable by slug for retry.
	actionErr := executeFinishActions(items)
	if actionErr != nil {
		fmt.Fprintf(os.Stderr, "\ncleanup incomplete — plan left in 'review' status, re-run 'flow plan finish %s' after resolving the issue\n", planName)
		return actionErr
	}

	// Check if the finished plan was the active plan and unset it.
	activePlan, err := getActivePlanWithMigration()
	if err == nil && activePlan == planName {
		if err := state.Delete(stateDir(), groveplan.StateKey); err != nil {
			fmt.Printf("Warning: could not unset active plan: %v\n", err)
		} else {
			_ = state.Delete(stateDir(), groveplan.LegacyStateKey)
			fmt.Println("\n* Unset active plan")
		}
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
	terminalItemIDs := map[string]bool{
		plan_finish.ItemArchivePlan:  true,
		plan_finish.ItemMarkFinished: true,
	}
	for _, item := range items {
		if item == nil || !item.IsEnabled || item.Action == nil {
			continue
		}
		if firstErr != nil && terminalItemIDs[item.ID] {
			fmt.Printf("  - %-40s... %s\n", item.Name, color.YellowString("Skipped (previous failure)"))
			continue
		}
		executed = true
		fmt.Printf("  - %-40s... ", item.Name)
		if err := item.Action(); err != nil {
			fmt.Println(color.RedString("Failed"))
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
