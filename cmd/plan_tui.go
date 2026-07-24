package cmd

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/compositor"
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/git"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/grovetools/flow/pkg/tui/view"
)

// Plan TUI command - interactive version of `flow plan list`.
//
// Historically this file contained the full plan-list TUI implementation
// (~2000 lines). As part of Phase 2.1 of the flow TUI embed, that logic
// moved to flow/pkg/tui/browser so it can be embedded inside the
// terminal meta-panel. This file is now a thin CLI wrapper that
// constructs a browser.Model, runs it via a bubbletea program, and
// translates browser.BrowserPlanSelectedMsg into a subprocess launch of
// `flow plan status --tui` to preserve the standalone CLI behavior
// (Enter in the list opens the status view).
var planTUICmd = &cobra.Command{
	Use:   "tui",
	Short: "Launch interactive TUI for browsing and managing plans",
	Long: `Launch an interactive TUI that provides a navigable view of all plans
in your plans directory, similar to 'flow plan list' but with interactive features.

Features:
- Navigate through all plans with keyboard (↑/↓, j/k)
- View plan status details (Enter key)
- Execute plan finish command (Ctrl+X)
- Real-time plan list display`,
	Args: cobra.NoArgs,
	RunE: runPlanTUI,
}

func runPlanTUI(cmd *cobra.Command, args []string) error {
	if !isatty.IsTerminal(os.Stdout.Fd()) && !isatty.IsCygwinTerminal(os.Stdout.Fd()) {
		return fmt.Errorf("TUI mode requires an interactive terminal")
	}

	cwd, _ := os.Getwd()
	if project, notebookRoot, _ := workspace.GetProjectFromNotebookPath(cwd); notebookRoot != "" {
		workspaceName := workspace.ExtractWorkspaceNameFromNotebookPath(cwd, notebookRoot)
		if project != nil {
			return fmt.Errorf("you are in the notebook directory for '%s'.\n"+
				"Run this command from the project directory instead:\n\n"+
				"  cd %s", workspaceName, project.Path)
		}
		return fmt.Errorf("you are in a notebook directory for '%s'.\n"+
			"Run this command from the associated project directory instead.", workspaceName)
	}

	node, err := workspace.GetProjectByPath(".")
	if err != nil {
		return fmt.Errorf("could not determine current workspace: %w", err)
	}

	coreCfg, err := config.LoadDefault()
	if err != nil {
		coreCfg = &config.Config{}
	}
	locator := workspace.NewNotebookLocator(coreCfg)

	plansDirectory, err := locator.GetPlansDir(node)
	if err != nil {
		return fmt.Errorf("could not resolve plans directory: %w", err)
	}

	cwdGitRoot := node.Path
	if cwdGitRoot == "" {
		cwdGitRoot, _ = git.GetGitRoot(".")
	}

	// Resolve the pre-TUI client from the SAME directory as the reconnect
	// factory below. With mismatched inputs (no-arg here resolves GROVE_SCOPE,
	// the factory resolves cwdGitRoot) the two calls can target two different
	// scoped daemons — and pay two sequential daemon boots before the first
	// row renders.
	daemonClient := daemon.NewWithAutoStart(cwdGitRoot)
	defer daemonClient.Close()
	model := view.New(view.Config{
		PlansDir:     plansDirectory,
		WorkspaceDir: cwdGitRoot,
		DaemonClient: daemonClient,
		DaemonClientFactory: func() daemon.Client {
			return daemon.NewWithAutoStartOpts(cwdGitRoot, daemon.SuppressStartNotice())
		},
	})

	// Use the same coordinator host as `flow plan status --tui`. Enter now
	// opens the highlighted row in-process, independent of selected-plan state,
	// and Esc returns to the preserved portfolio cursor.
	host := newStatusTUIHost(model)
	compModel := compositor.NewModel(host)
	program := tea.NewProgram(compModel, tea.WithAltScreen())
	finalModel, err := program.Run()
	if err != nil {
		return fmt.Errorf("error running plan list TUI: %w", err)
	}
	if cm, ok := finalModel.(*compositor.Model); ok {
		cm.Free()
	}
	return nil
}
