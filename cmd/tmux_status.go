package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/mattsolo1/grove-core/pkg/tmux"
	"github.com/spf13/cobra"
)

// NewTmuxStatusCmd returns the command for opening plan status in a tmux window.
func NewTmuxStatusCmd() *cobra.Command {
	var windowName string

	cmd := &cobra.Command{
		Use:   "status [directory]",
		Short: "Open plan status TUI in a dedicated tmux window",
		Long: `Opens the flow plan status TUI in a dedicated tmux window.
If the window already exists, it focuses it without disrupting the session.
If not in a tmux session, falls back to running the TUI directly.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var dir string
			if len(args) > 0 {
				dir = args[0]
			}

			client, err := tmux.NewClient()
			if err != nil {
				// Not in a tmux session, run the status TUI directly
				statusCmd := &cobra.Command{
					Use: "status",
				}
				return RunPlanStatus(statusCmd, args)
			}

			// Build the command to run in the tmux window
			flowBin, err := exec.LookPath("flow")
			if err != nil {
				flowBin = os.Args[0] // Fall back to current executable
			}

			command := fmt.Sprintf("%s plan status", flowBin)
			if dir != "" {
				command += fmt.Sprintf(" %s", dir)
			}

			// Use the tmux client to manage the window with error handling
			ctx := context.Background()
			if err := client.FocusOrRunTUIWithErrorHandling(ctx, command, windowName, -1); err != nil {
				return fmt.Errorf("failed to open in tmux window: %w", err)
			}

			// Close any popup that might have launched this command
			if err := client.ClosePopup(ctx); err != nil {
				// Ignore errors - we might not be in a popup
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&windowName, "window-name", "plan", "Name of the tmux window")

	return cmd
}
