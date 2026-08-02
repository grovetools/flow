package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/grovetools/flow/pkg/orchestration"
)

var planReviewCmd = &cobra.Command{
	Use:   "review [directory]",
	Short: "Mark a plan as ready for review and execute completion hooks (use: flow review)",
	Long: `Marks a plan as ready for review, executes on-review hooks, and prepares it for final cleanup.
This is the intermediary step before using 'flow plan finish'.

Plans can be referenced by slug from any directory. Use --dir to specify the workspace context.

Examples:
  flow plan review my-feature                     # from any directory
  flow plan review my-feature --dir ~/Code/myapp  # explicit workspace`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanReview,
}

// NewReviewCmd creates the top-level `review` command.
func NewReviewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review [directory]",
		Short: "Mark a plan as ready for review and execute completion hooks",
		Long: `Marks a plan as ready for review, executes on-review hooks, and prepares it for final cleanup.
This is the intermediary step before using 'flow finish'.

Plans can be referenced by slug from any directory. Use --dir to specify the workspace context.

Examples:
  flow review my-feature                     # from any directory
  flow review my-feature --dir ~/Code/myapp  # explicit workspace`,
		Args: cobra.MaximumNArgs(1),
		RunE: runPlanReview,
	}
	cmd.Flags().StringVarP(&planContextDir, "dir", "d", "", "Workspace or plan directory context (defaults to current directory)")
	return cmd
}

// runPlanReview implements the review command.
func runPlanReview(cmd *cobra.Command, args []string) error {
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

	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	alreadyReview := plan.Config != nil && (plan.Config.Status == "review" || plan.Config.Status == "finished")

	// Mark for review: runs the on_review hook, flips status to "review",
	// persists .grove-plan.yml, and creates/refreshes the durable review packet
	// note. The flip is idempotent (shared with non-CLI callers such as the
	// git-viewer roll-up); the packet refresh is NOT skipped on a plan that is
	// already in review, because re-running this verb after marking more files
	// reviewed is exactly how a user asks for a fresh checkpoint.
	outcome, err := orchestration.MarkPlanReviewWithPacket(planPath)
	if err != nil {
		return err
	}

	if alreadyReview {
		fmt.Printf("* Plan '%s' is already marked as '%s'.\n", plan.Name, plan.Config.Status)
	} else {
		fmt.Printf("* Plan '%s' marked for review.\n", plan.Name)
	}
	reportReviewPacket(outcome)
	if alreadyReview {
		fmt.Println("You can now proceed with final cleanup using 'flow plan finish'.")
	} else {
		fmt.Println("  You can now verify the results and then run 'flow plan finish' to clean up the worktree and branches.")
	}

	// A packet failure is reported loudly (non-zero exit) but never un-flips
	// the plan — the status change already persisted above.
	return outcome.PacketErr
}

// reportReviewPacket prints where the review packet landed, plus any non-fatal
// warnings. A failure is printed here too so the message is adjacent to the
// plan's status line, then returned by the caller as the command's error.
func reportReviewPacket(outcome orchestration.PlanReviewOutcome) {
	for _, warning := range outcome.Packet.Warnings {
		fmt.Printf("  Warning: %s\n", warning)
	}
	if outcome.PacketErr != nil {
		fmt.Printf("  %v\n", outcome.PacketErr)
		return
	}
	fmt.Printf("  %s\n", outcome.Packet.Summary())
}
