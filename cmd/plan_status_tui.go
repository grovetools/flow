package cmd

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattsolo1/grove-core/logging"
	"github.com/mattsolo1/grove-core/tui/components/logviewer"
	"github.com/mattsolo1/grove-flow/cmd/status_tui"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
)

// runStatusTUI runs the interactive TUI for plan status
func runStatusTUI(plan *orchestration.Plan, graph *orchestration.DependencyGraph) error {
	// Inject the helper functions into the status_tui package
	status_tui.FindRootJobsFunc = ExportedFindRootJobs
	status_tui.FindAllDependentsFunc = ExportedFindAllDependents
	status_tui.VerifyRunningJobStatusFunc = ExportedVerifyRunningJobStatus
	status_tui.CompleteJobFunc = ExportedCompleteJob

	// Create a TUI log writer that will receive all redirected output
	// We'll set the program reference after creating it
	var streamWriter *logviewer.StreamWriter

	model := status_tui.New(plan, graph)

	// Use alt screen only when not in Neovim (to fix screen duplication)
	// But disable it in Neovim to allow editor functionality
	var opts []tea.ProgramOption
	if os.Getenv("GROVE_NVIM_PLUGIN") != "true" {
		opts = append(opts, tea.WithAltScreen())
	}
	opts = append(opts, tea.WithOutput(os.Stderr))

	program := tea.NewProgram(model, opts...)

	// Create the stream writer with the program reference
	streamWriter = logviewer.NewStreamWriter(program, "System")

	// Redirect all Grove loggers to our TUI writer
	logging.SetGlobalOutput(streamWriter)
	defer logging.SetGlobalOutput(os.Stderr) // Ensure we reset on exit

	// Set the program reference in the package-level variable
	// The model's Init() method will read this and set m.Program
	status_tui.SetProgramRef(program)

	if _, err := program.Run(); err != nil {
		return fmt.Errorf("error running status TUI: %w", err)
	}

	return nil
}
