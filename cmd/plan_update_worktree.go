package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/planops"
)

var planUpdateWorktreeCmd = &cobra.Command{
	Use:   "update-worktree [plan-name]",
	Short: "Update plan worktree by rebasing on main",
	Long: `Update every repository in a plan's qualified worktree by rebasing it
onto its local main/master branch. Every repository is preflighted before
mutation and execution never derives repository scope from CWD.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanUpdateWorktree,
}

func init() {
	planUpdateWorktreeCmd.Flags().StringVarP(&planContextDir, "dir", "d", "", "Workspace or plan directory context (defaults to current directory)")
	planCmd.AddCommand(planUpdateWorktreeCmd)
}

func runPlanUpdateWorktree(cmd *cobra.Command, args []string) error {
	planPath, err := resolvePlanOperationPath(cmd, args)
	if err != nil {
		return err
	}
	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}
	return executePlanOperation(cmd.Context(), plan, planops.OperationUpdateOnly)
}
