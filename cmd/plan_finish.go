package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fatih/color"
	groveplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/state"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/plan_finish"
	"github.com/grovetools/flow/pkg/tui/wizards/finish"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	planFinishYes             bool
	planFinishDeleteBranch    bool
	planFinishDeleteRemote    bool
	planFinishPruneWorktree   bool
	planFinishCloseSession    bool
	planFinishArchive         bool
	planFinishCleanDevLinks   bool
	planFinishRebuildBinaries bool
	planFinishForce           bool
	planFinishKeepEnv         bool
	planFinishKeepWorktree    bool
)

// NewPlanFinishCmd creates the `plan finish` command.
func NewPlanFinishCmd() *cobra.Command {
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
		RunE: runPlanFinish,
	}
	registerFinishFlags(cmd)
	return cmd
}

// NewFinishCmd creates the top-level `finish` command.
func NewFinishCmd() *cobra.Command {
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
		RunE: runPlanFinish,
	}
	registerFinishFlags(cmd)
	return cmd
}

func registerFinishFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVarP(&planFinishYes, "yes", "y", false, "Automatically confirm all cleanup actions")
	cmd.Flags().BoolVar(&planFinishDeleteBranch, "delete-branch", false, "Delete the local git branch")
	cmd.Flags().BoolVar(&planFinishDeleteRemote, "delete-remote", false, "Delete the remote git branch")
	cmd.Flags().BoolVar(&planFinishPruneWorktree, "prune-worktree", false, "Remove the git worktree directory")
	cmd.Flags().BoolVar(&planFinishCloseSession, "close-session", false, "Close the associated tmux session")
	cmd.Flags().BoolVar(&planFinishCleanDevLinks, "clean-dev-links", false, "Clean up development binary links from the worktree")
	cmd.Flags().BoolVar(&planFinishRebuildBinaries, "rebuild-binaries", false, "Rebuild binaries in the main repository")
	cmd.Flags().BoolVar(&planFinishArchive, "archive", false, "Archive the plan directory to a local .archive subdirectory")
	cmd.Flags().BoolVar(&planFinishForce, "force", false, "Force git operations (use with caution)")
	cmd.Flags().BoolVar(&planFinishKeepEnv, "keep-env", false, "Skip environment teardown during cleanup")
	cmd.Flags().BoolVar(&planFinishKeepWorktree, "keep-worktree", false, "Skip worktree removal during cleanup")
	cmd.Flags().StringVarP(&planContextDir, "dir", "d", "", "Workspace or plan directory context (defaults to current directory)")
}

func runPlanFinish(cmd *cobra.Command, args []string) error {
	var dir string
	if len(args) > 0 {
		dir = args[0]
	}

	contextDir := planContextDir
	if contextDir == "" {
		contextDir = "."
	}
	planPath, err := resolvePlanPathWithActiveJob(dir, contextDir)
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
	opts := plan_finish.Options{
		Force:        planFinishForce,
		KeepEnv:      planFinishKeepEnv,
		KeepWorktree: planFinishKeepWorktree,
	}
	result, err := plan_finish.BuildItems(bctx, opts)
	if err != nil {
		return err
	}
	items := result.Items

	// Determine which items to enable based on flags.
	anyExplicitFlags := planFinishDeleteBranch || planFinishDeleteRemote || planFinishPruneWorktree || planFinishCloseSession || planFinishCleanDevLinks || planFinishRebuildBinaries || planFinishArchive || planFinishForce
	if planFinishYes {
		for _, item := range items {
			item.IsEnabled = item.IsAvailable
		}
	} else if anyExplicitFlags {
		// Always enable env teardown, merging submodules, marking as finished, and closing tmux.
		items[0].IsEnabled = items[0].IsAvailable                             // Teardown environment resources
		items[1].IsEnabled = items[1].IsAvailable                             // Merge/fast-forward submodules to main
		items[2].IsEnabled = items[2].IsAvailable                             // Mark plan as finished
		items[3].IsEnabled = planFinishCloseSession && items[3].IsAvailable   // Close tmux session
		items[4].IsEnabled = planFinishPruneWorktree && items[4].IsAvailable  // Prune git worktree
		items[5].IsEnabled = planFinishCleanDevLinks && items[5].IsAvailable  // Clean up dev binaries
		items[6].IsEnabled = planFinishDeleteBranch && items[6].IsAvailable   // Delete submodule branches
		items[7].IsEnabled = planFinishDeleteBranch && items[7].IsAvailable   // Delete local git branch
		items[8].IsEnabled = planFinishDeleteRemote && items[8].IsAvailable   // Delete remote git branch
		items[9].IsEnabled = planFinishRebuildBinaries && items[9].IsAvailable // Rebuild main repo binaries
		items[10].IsEnabled = planFinishArchive && items[10].IsAvailable      // Archive plan directory
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

	// Execute on_finish hook before marking as finished.
	if plan.Config != nil && plan.Config.Status == "review" {
		plan_finish.RunOnFinishHook(plan, planName)

		// Now mark plan as finished.
		plan.Config.Status = "finished"
		configPath := filepath.Join(planPath, ".grove-plan.yml")
		if data, err := yaml.Marshal(plan.Config); err == nil {
			os.WriteFile(configPath, data, 0644)
			fmt.Println("  - Marked plan as finished... Done")
		}
	}

	// Execute enabled actions.
	executeFinishActions(items)

	// Check if the finished plan was the active plan and unset it.
	activePlan, err := getActivePlanWithMigration()
	if err == nil && activePlan == planName {
		if err := state.Delete(groveplan.StateKey); err != nil {
			fmt.Printf("Warning: could not unset active plan: %v\n", err)
		} else {
			_ = state.Delete(groveplan.LegacyStateKey)
			fmt.Println("\n* Unset active plan")
		}
	}

	fmt.Println("\nPlan cleanup finished.")
	return nil
}

// executeFinishActions runs the Action closure on every enabled item,
// printing per-item success/failure. It is shared between the CLI
// path and any other host that wants to run cleanup with the same
// console output format.
func executeFinishActions(items []*finish.Item) {
	fmt.Println("\nPerforming selected actions...")
	executed := false
	for _, item := range items {
		if item == nil || !item.IsEnabled || item.Action == nil {
			continue
		}
		executed = true
		fmt.Printf("  - %-40s... ", item.Name)
		if err := item.Action(); err != nil {
			fmt.Println(color.RedString("Failed"))
			fmt.Printf("    %s\n", err)
		} else {
			fmt.Println(color.GreenString("Done"))
		}
	}
	if !executed {
		fmt.Println("No actions selected.")
	}
}
