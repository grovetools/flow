package cmd

import (
	"fmt"

	"github.com/mattsolo1/grove-core/state"
	"github.com/spf13/cobra"
)

// This file contains top-level versions of plan configuration commands
// that are hoisted from the `plan` subcommand.

// NewSetCmd creates the top-level `set` command.
func NewSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <plan-directory>",
		Short: "Set the active job plan directory",
		Long: `Set the active job plan directory to avoid specifying it in every command.

Examples:
  flow set user-profile-api
  flow set ./plans/feature-x`,
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

// NewCurrentCmd creates the top-level `current` command.
func NewCurrentCmd() *cobra.Command {
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

// NewUnsetCmd creates the top-level `unset` command.
func NewUnsetCmd() *cobra.Command {
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

// NewHoldCmd creates the top-level `hold` command.
func NewHoldCmd() *cobra.Command {
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

// NewUnholdCmd creates the top-level `unhold` command.
func NewUnholdCmd() *cobra.Command {
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

// NewResumeCmd creates the top-level `resume` command.
func NewResumeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resume <job-file>",
		Short: "Resume a completed interactive agent job",
		Long: `Resumes a completed interactive agent session by finding its native agent session ID
and re-launching the agent in a new tmux window.`,
		Args: cobra.ExactArgs(1),
		RunE: runPlanResume,
	}
	return cmd
}

// NewConfigCmd creates the top-level `config` command.
// This delegates to the existing NewPlanConfigCmd from plan_config.go
func NewConfigCmd() *cobra.Command {
	return NewPlanConfigCmd()
}