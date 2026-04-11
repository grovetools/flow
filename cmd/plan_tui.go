package cmd

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/compositor"
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/git"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/tui/embed"
	"github.com/grovetools/core/util/delegation"
	"github.com/grovetools/flow/pkg/tui/browser"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
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

	model := browser.New(browser.Config{
		PlansDir:     plansDirectory,
		WorkspaceDir: cwdGitRoot,
	})

	host := newStandalonePlanTUIHost(model)
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

// standalonePlanTUIHost wraps a browser.Model for standalone CLI use.
// It behaves like embed.StandaloneHost but additionally intercepts
// browser.BrowserPlanSelectedMsg to launch `flow plan status --tui` as
// a subprocess, reproducing the original TUI's "enter opens status"
// behavior without the browser package itself having to know about
// status navigation.
type standalonePlanTUIHost struct {
	model browser.Model
}

func newStandalonePlanTUIHost(m browser.Model) standalonePlanTUIHost {
	return standalonePlanTUIHost{model: m}
}

func (h standalonePlanTUIHost) Init() tea.Cmd {
	return h.model.Init()
}

func (h standalonePlanTUIHost) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case browser.BrowserPlanSelectedMsg:
		// Reproduce the old in-process behavior: set the active plan
		// and exec the status TUI as a subprocess. We intentionally do
		// NOT dispatch the message to the inner model — the browser
		// has no opinion on selection, and forwarding would be a no-op.
		execCmd := delegation.Command("flow", "plan", "status", "--tui")
		_ = msg // PlanName/PlanPath/Plan available here if we later want to pass them explicitly
		return h, tea.ExecProcess(execCmd, func(err error) tea.Msg { return nil })

	case embed.DoneMsg:
		return h, tea.Quit

	case embed.CloseRequestMsg, embed.CloseConfirmMsg:
		return h, tea.Quit
	}

	updated, cmd := h.model.Update(msg)
	if m, ok := updated.(browser.Model); ok {
		h.model = m
	}
	return h, cmd
}

func (h standalonePlanTUIHost) View() string {
	return h.model.View()
}
