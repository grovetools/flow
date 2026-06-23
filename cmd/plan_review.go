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

	if plan.Config != nil && (plan.Config.Status == "review" || plan.Config.Status == "finished") {
		fmt.Printf("* Plan '%s' is already marked as '%s'. No action taken.\n", plan.Name, plan.Config.Status)
		fmt.Println("You can now proceed with final cleanup using 'flow plan finish'.")
		return nil
	}

	// Mark for review: runs the on_review hook, flips status to "review", and
	// persists .grove-plan.yml. Shared with non-CLI callers (e.g. git-viewer).
	if err := orchestration.MarkPlanReview(planPath); err != nil {
		return err
	}

	fmt.Printf("* Plan '%s' marked for review.\n", plan.Name)
	fmt.Println("  You can now verify the results and then run 'flow plan finish' to clean up the worktree and branches.")

	return nil
}
