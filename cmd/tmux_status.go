package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/grovetools/core/pkg/mux"
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

			ctx := context.Background()
			engine, err := mux.DetectMuxEngine(ctx)
			if err != nil {
				// Not in a mux session, run the status TUI directly
				statusCmd := &cobra.Command{
					Use: "status",
				}
				return RunPlanStatus(statusCmd, args)
			}

			tuiEngine, ok := engine.(mux.MuxTUIEngine)
			if !ok {
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

			if err := tuiEngine.FocusOrRunCommandInWindow(ctx, command, windowName, -1); err != nil {
				return fmt.Errorf("failed to open in tmux window: %w", err)
			}

			_ = tuiEngine.ClosePopup(ctx)

			return nil
		},
	}

	cmd.Flags().StringVar(&windowName, "window-name", "plan", "Name of the tmux window")

	return cmd
}
