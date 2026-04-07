package cmd

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/tui/components/logviewer"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/tui/status"
)

// runStatusTUI runs the interactive TUI for plan status.
// This is a thin CLI wrapper around flow/pkg/tui/status: it constructs a
// status.Config with the CLI's daemon client, creates a *tea.Program, pipes
// global logging output into the program via logviewer.StreamWriter, and
// hands off to bubbletea. Embedding hosts (e.g., terminal) bypass this file
// entirely and instantiate status.New themselves with their own Config.
func runStatusTUI(plan *orchestration.Plan, graph *orchestration.DependencyGraph) error {
	// Construct a daemon client owned by the CLI for the lifetime of this
	// status session. The embedded case will pass its own multiplexed client
	// via status.Config instead.
	daemonClient := daemon.NewWithAutoStart()
	defer func() {
		if daemonClient != nil {
			daemonClient.Close()
		}
	}()

	model := status.New(status.Config{
		Plan:         plan,
		Graph:        graph,
		DaemonClient: daemonClient,
	})

	// Use alt screen only when not in Neovim (to fix screen duplication)
	// But disable it in Neovim to allow editor functionality
	var opts []tea.ProgramOption
	if os.Getenv("GROVE_NVIM_PLUGIN") != "true" {
		opts = append(opts, tea.WithAltScreen())
	}
	opts = append(opts, tea.WithOutput(os.Stderr))

	program := tea.NewProgram(model, opts...)

	// Redirect all Grove loggers into the program so the logviewer pane
	// captures them. The CLI owns the *tea.Program here, so using
	// logviewer.StreamWriter directly is fine.
	streamWriter := logviewer.NewStreamWriter(program, "System")
	logging.SetGlobalOutput(streamWriter)
	defer logging.SetGlobalOutput(os.Stderr) // Ensure we reset on exit

	if _, err := program.Run(); err != nil {
		return fmt.Errorf("error running status TUI: %w", err)
	}

	return nil
}
