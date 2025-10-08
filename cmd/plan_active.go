package cmd

import (
	"fmt"

	"github.com/mattsolo1/grove-core/state"
	"github.com/spf13/cobra"
)

// NewPlanSetCmd creates the plan set command.
func NewPlanSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <plan-directory>",
		Short: "Set the active job plan directory",
		Long: `Set the active job plan directory to avoid specifying it in every command.

Examples:
  flow plan set user-profile-api
  flow plan set ./plans/feature-x`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			planDir := args[0]
			if err := state.Set("flow.active_plan", planDir); err != nil {
				return fmt.Errorf("set active job: %w", err)
			}
			fmt.Printf("Set active job to: %s\n", planDir)
			return nil
		},
	}
}

// NewPlanCurrentCmd creates the plan current command.
func NewPlanCurrentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Show the current active job plan directory",
		Long: `Show the current active job plan directory.

If no active job is set, this command will indicate that.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			activeJob, err := getActivePlanWithMigration()
			if err != nil {
				return fmt.Errorf("get active job: %w", err)
			}
			if activeJob == "" {
				fmt.Println("No active job set")
			} else {
				fmt.Printf("Active job: %s\n", activeJob)
			}
			return nil
		},
	}
}

// NewPlanUnsetCmd creates the plan unset command.
func NewPlanUnsetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unset",
		Short: "Clear the active job plan directory",
		Long:  `Clear the active job plan directory.`,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Clear both old and new keys
			if err := state.Delete("flow.active_plan"); err != nil {
				return fmt.Errorf("clear active job: %w", err)
			}
			// Also try to delete old key (ignore errors)
			_ = state.Delete("active_plan")
			fmt.Println("Cleared active job")
			return nil
		},
	}
}
