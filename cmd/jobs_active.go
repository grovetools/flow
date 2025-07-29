package cmd

import (
	"fmt"

	"github.com/grovepm/grove-flow/pkg/state"
	"github.com/spf13/cobra"
)

// NewJobsSetCmd creates the jobs set command.
func NewJobsSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <plan-directory>",
		Short: "Set the active job plan directory",
		Long: `Set the active job plan directory to avoid specifying it in every command.

Examples:
  grove jobs set user-profile-api
  grove jobs set ./plans/feature-x`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			planDir := args[0]
			if err := state.SetActiveJob(planDir); err != nil {
				return fmt.Errorf("set active job: %w", err)
			}
			fmt.Printf("Set active job to: %s\n", planDir)
			return nil
		},
	}
}

// NewJobsCurrentCmd creates the jobs current command.
func NewJobsCurrentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Show the current active job plan directory",
		Long: `Show the current active job plan directory.

If no active job is set, this command will indicate that.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			activeJob, err := state.GetActiveJob()
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

// NewJobsUnsetCmd creates the jobs unset command.
func NewJobsUnsetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unset",
		Short: "Clear the active job plan directory",
		Long:  `Clear the active job plan directory.`,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := state.ClearActiveJob(); err != nil {
				return fmt.Errorf("clear active job: %w", err)
			}
			fmt.Println("Cleared active job")
			return nil
		},
	}
}