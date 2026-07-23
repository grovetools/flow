package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/planops"
)

var planMergeWorktreeCmd = &cobra.Command{
	Use:   "merge-worktree [plan-name]",
	Short: "Merge plan worktree branch to main",
	Long: `Land every repository in a plan's qualified worktree onto its local
main/master branch. Every repository is preflighted before mutation; execution
uses the exact registry-qualified plan target and never derives scope from CWD.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanMergeWorktree,
}

func init() {
	planMergeWorktreeCmd.Flags().StringVarP(&planContextDir, "dir", "d", "", "Workspace or plan directory context (defaults to current directory)")
	planCmd.AddCommand(planMergeWorktreeCmd)
}

func runPlanMergeWorktree(cmd *cobra.Command, args []string) error {
	planPath, err := resolvePlanOperationPath(cmd, args)
	if err != nil {
		return err
	}
	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}
	return executePlanOperation(cmd.Context(), plan, planops.OperationLand)
}

func resolvePlanOperationPath(cmd *cobra.Command, args []string) (string, error) {
	ref := ""
	if len(args) > 0 {
		ref = args[0]
	}
	contextDir := planContextDir
	if contextDir == "" {
		contextDir = "."
	}
	return resolvePlanPathWithActiveJobCtx(cmd.Context(), ref, contextDir)
}
