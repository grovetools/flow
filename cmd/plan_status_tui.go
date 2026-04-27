package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/compositor"
	"github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/tui/components/logviewer"
	"github.com/grovetools/core/tui/embed"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/tui/browser"
	"github.com/grovetools/flow/pkg/tui/view"
)

// runStatusTUI runs the interactive TUI for plan status.
//
// As part of Phase 2.1 of the flow TUI embed, this now drives the
// view meta-panel (flow/pkg/tui/view) instead of flow/pkg/tui/status
// directly. That gives the standalone CLI the same browser/status
// toggle as the terminal host: `flow plan status --tui` lands the
// user on the status view for the active plan (via InitialPlan), and
// `esc` pops back to the plan browser. Enter on a plan in the
// browser re-targets the status view without launching a subprocess.
//
// Embedding hosts (terminal) bypass this file entirely and instantiate
// view.New themselves with their own Config.
func runStatusTUI(plan *orchestration.Plan, graph *orchestration.DependencyGraph) error {
	daemonClient := daemon.NewWithAutoStart()
	defer func() {
		if daemonClient != nil {
			daemonClient.Close()
		}
	}()

	// PlansDir is the parent directory of the active plan's directory.
	// Used by the browser view to enumerate sibling plans.
	plansDir := filepath.Dir(plan.Directory)
	workspaceDir := filepath.Dir(plansDir)

	metaModel := view.New(view.Config{
		WorkspaceDir: workspaceDir,
		PlansDir:     plansDir,
		DaemonClient: daemonClient,
		InitialPlan:  plan,
		InitialGraph: graph,
	})

	host := newStatusTUIHost(metaModel)

	var opts []tea.ProgramOption
	if os.Getenv("GROVE_NVIM_PLUGIN") != "true" {
		opts = append(opts, tea.WithAltScreen())
	}
	opts = append(opts, tea.WithOutput(os.Stderr))

	compModel := compositor.NewModel(host)
	program := tea.NewProgram(compModel, opts...)

	streamWriter := logviewer.NewStreamWriter(program, "System")
	logging.SetGlobalOutput(streamWriter)
	defer logging.SetGlobalOutput(os.Stderr)

	finalModel, err := program.Run()
	if err != nil {
		return fmt.Errorf("error running status TUI: %w", err)
	}
	if cm, ok := finalModel.(*compositor.Model); ok {
		cm.Free()
	}
	return nil
}

// statusTUIHost wraps a view.Model for standalone CLI use. It intercepts
// the embed lifecycle messages (DoneMsg, CloseRequestMsg) to quit and
// otherwise forwards everything through. Kept here rather than in the
// view package so the package stays free of CLI-specific concerns.
type statusTUIHost struct {
	model view.Model
}

func newStatusTUIHost(m view.Model) statusTUIHost {
	return statusTUIHost{model: m}
}

func (h statusTUIHost) Init() tea.Cmd {
	return h.model.Init()
}

func (h statusTUIHost) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case embed.CloseRequestMsg, embed.CloseConfirmMsg:
		return h, tea.Quit

	case embed.DoneMsg:
		// Forward DoneMsg to the inner model — it handles wizard
		// completion (add-job, finish-plan) internally. Only quit
		// if the inner model re-emits a close request.
		updated, cmd := h.model.Update(msg)
		if vm, ok := updated.(view.Model); ok {
			h.model = vm
		}
		return h, cmd

	case embed.EditRequestMsg:
		// Standalone CLI: translate to tea.ExecProcess so $EDITOR
		// runs in the user's terminal. In groveterm this message is
		// caught by the terminal host's WrapPanelCmd instead.
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		c := exec.Command(editor, msg.Path)
		return h, tea.ExecProcess(c, func(err error) tea.Msg {
			return embed.EditFinishedMsg{Err: err}
		})
	}
	// BrowserPlanSelectedMsg is handled inside view.Model
	// already; nothing else to intercept here.
	updated, cmd := h.model.Update(msg)
	if vm, ok := updated.(view.Model); ok {
		h.model = vm
	}
	return h, cmd
}

func (h statusTUIHost) View() string {
	return h.model.View()
}

// unused import guard — browser is referenced indirectly via view.
var _ = browser.BrowserPlanSelectedMsg{}
