package cmd

import (
	"github.com/spf13/cobra"
)

// NewPlanHoldCmd creates the `plan hold` command.
func NewPlanHoldCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hold [directory]",
		Short: "Set a plan's status to 'hold'",
		Long: `Sets the status of a plan to 'hold' in its .grove-plan.yml file.
On-hold plans are hidden from most views by default to reduce clutter.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var dir string
			if len(args) > 0 {
				dir = args[0]
			}
			configCmd := &PlanConfigCmd{
				Dir: dir,
				Set: []string{"status=hold"},
			}
			return RunPlanConfig(configCmd)
		},
	}
	return cmd
}

// NewPlanUnholdCmd creates the `plan unhold` command.
func NewPlanUnholdCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unhold [directory]",
		Short: "Remove a plan's on-hold status",
		Long: `Resumes an on-hold plan by removing its status from its .grove-plan.yml file.
This makes the plan visible in default views again.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var dir string
			if len(args) > 0 {
				dir = args[0]
			}
			configCmd := &PlanConfigCmd{
				Dir: dir,
				Set: []string{"status="},
			}
			return RunPlanConfig(configCmd)
		},
	}
	return cmd
}
