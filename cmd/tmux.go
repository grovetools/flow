package cmd

import (
	"github.com/spf13/cobra"
)

// NewTmuxCmd returns the tmux command with all subcommands configured.
func NewTmuxCmd() *cobra.Command {
	tmuxCmd := &cobra.Command{
		Use:   "tmux",
		Short: "Tmux window management commands",
		Long:  `Commands for managing flow in dedicated tmux windows.`,
	}

	tmuxCmd.AddCommand(NewTmuxStatusCmd())

	return tmuxCmd
}
