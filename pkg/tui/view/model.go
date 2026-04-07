// Package view is a meta-panel that hosts flow's browser and status
// sub-TUIs in a single embeddable tea.Model. It starts in "browser" mode
// (showing the plan list), switches to "status" mode when the user
// selects a plan (Enter), and back to "browser" mode on `esc`. Hosts
// embed this package instead of picking one of the sub-TUIs directly so
// the Plan panel can toggle between list and detail views without the
// host knowing about either.
package view

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/tui/embed"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/tui/browser"
	"github.com/grovetools/flow/pkg/tui/status"
)

// mode enumerates which sub-model the meta-panel is currently routing
// updates and render calls to.
type mode int

const (
	modeBrowser mode = iota
	modeStatus
)

// Config carries the dependencies the meta-panel needs to build its
// sub-models. It mirrors the Config shape used by status and browser,
// plus optional fields for deciding the initial plans directory.
type Config struct {
	// WorkspaceDir is the git root for the workspace this meta-panel
	// is scoped to. Passed through to browser as WorkspaceDir and to
	// status via SetWorkspaceMsg on re-targeting.
	WorkspaceDir string
	// PlansDir is the plans directory for the browser view. Required
	// for the browser to enumerate plans.
	PlansDir string
	// DaemonClient is a shared daemon client used by the status
	// sub-model. May be nil, in which case the status model runs in
	// orchestrator-only mode.
	DaemonClient daemon.Client
}

// Model is the meta-panel model. It owns both sub-models and routes
// messages, keys, and render calls to whichever is currently active.
//
// The browser is constructed eagerly in New so it can begin loading the
// plan list. The status model is constructed lazily: only when the user
// picks a plan (and we have a loaded *orchestration.Plan to hand it).
// When the user navigates back to the browser the status model is
// closed and discarded so its daemon SSE subscription and listener
// goroutines don't leak.
type Model struct {
	cfg Config

	mode         mode
	browserModel browser.Model
	statusModel  *status.Model

	// width/height are cached from the last WindowSizeMsg so lazily
	// constructed status models can be sized immediately.
	width  int
	height int
}

// New constructs a Model from the given Config. The browser sub-model
// is initialized immediately; the status sub-model is nil until the
// first plan selection.
func New(cfg Config) Model {
	b := browser.New(browser.Config{
		PlansDir:     cfg.PlansDir,
		WorkspaceDir: cfg.WorkspaceDir,
		DaemonClient: cfg.DaemonClient,
	})
	return Model{
		cfg:          cfg,
		mode:         modeBrowser,
		browserModel: b,
	}
}

// Init forwards to the browser sub-model's Init (the starting mode).
func (m Model) Init() tea.Cmd {
	return m.browserModel.Init()
}

// Update routes the message to the active sub-model, after handling the
// small set of meta-panel concerns: window sizing, embed routing for
// workspace switches, focus/blur fan-out, and the browser↔status mode
// transitions driven by BrowserPlanSelectedMsg and the `esc` key.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Forward to both sub-models so the inactive one is ready to
		// render correctly if we switch into it.
		var cmds []tea.Cmd
		updated, c := m.browserModel.Update(msg)
		if bm, ok := updated.(browser.Model); ok {
			m.browserModel = bm
		}
		if c != nil {
			cmds = append(cmds, c)
		}
		if m.statusModel != nil {
			sUpdated, sc := m.statusModel.Update(msg)
			if sm, ok := sUpdated.(status.Model); ok {
				*m.statusModel = sm
			}
			if sc != nil {
				cmds = append(cmds, sc)
			}
		}
		return m, tea.Batch(cmds...)

	case embed.SetWorkspaceMsg:
		// Workspace changed. The browser re-targets via SetWorkspaceMsg;
		// the status model, if alive, is plan-scoped and should be torn
		// down because the active plan is about to change. We drop it
		// here and switch back to the browser so the user sees the new
		// workspace's plan list.
		if m.statusModel != nil {
			_ = m.statusModel.Close()
			m.statusModel = nil
		}
		m.mode = modeBrowser
		if msg.Node != nil {
			m.cfg.WorkspaceDir = msg.Node.Path
		}
		updated, c := m.browserModel.Update(msg)
		if bm, ok := updated.(browser.Model); ok {
			m.browserModel = bm
		}
		return m, c

	case embed.FocusMsg, embed.BlurMsg:
		// Focus/blur routes to the active sub-model only. The inactive
		// one does not need to react to focus changes it can't see.
		return m.updateActive(msg)

	case browser.BrowserPlanSelectedMsg:
		// User picked a plan in the browser. Tear down any existing
		// status model, build a fresh one for the chosen plan, and
		// switch to status mode. Errors building the status model fall
		// back to staying in browser mode with no transition.
		if m.statusModel != nil {
			_ = m.statusModel.Close()
			m.statusModel = nil
		}

		plan := msg.Plan
		if plan == nil {
			return m, nil
		}
		graph, err := orchestration.BuildDependencyGraph(plan)
		if err != nil {
			return m, nil
		}

		newStatus := status.New(status.Config{
			Plan:         plan,
			Graph:        graph,
			DaemonClient: m.cfg.DaemonClient,
		})
		m.statusModel = &newStatus
		m.mode = modeStatus

		var cmds []tea.Cmd
		if m.width > 0 && m.height > 0 {
			sized, c := m.statusModel.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
			if sm, ok := sized.(status.Model); ok {
				*m.statusModel = sm
			}
			if c != nil {
				cmds = append(cmds, c)
			}
		}
		if c := m.statusModel.Init(); c != nil {
			cmds = append(cmds, c)
		}
		// Give the new status model focus so any focus-driven
		// behavior (refresh loops, etc.) arms itself.
		focused, fc := m.statusModel.Update(embed.FocusMsg{})
		if sm, ok := focused.(status.Model); ok {
			*m.statusModel = sm
		}
		if fc != nil {
			cmds = append(cmds, fc)
		}
		return m, tea.Batch(cmds...)

	case tea.KeyMsg:
		// Intercept `esc` in status mode to pop back to the browser.
		// All other keys route to the active sub-model.
		if m.mode == modeStatus && msg.String() == "esc" {
			if m.statusModel != nil {
				_ = m.statusModel.Close()
				m.statusModel = nil
			}
			m.mode = modeBrowser
			// Re-focus the browser and kick off a refresh so the plan
			// list picks up any changes (status, worktree, etc.) made
			// while the user was in status view.
			updated, _ := m.browserModel.Update(embed.FocusMsg{})
			if bm, ok := updated.(browser.Model); ok {
				m.browserModel = bm
			}
			return m, m.browserModel.Init()
		}
		return m.updateActive(msg)
	}

	return m.updateActive(msg)
}

// updateActive forwards a message to whichever sub-model is currently
// active and returns the updated meta-Model + tea.Cmd.
func (m Model) updateActive(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeStatus:
		if m.statusModel == nil {
			return m, nil
		}
		updated, c := m.statusModel.Update(msg)
		if sm, ok := updated.(status.Model); ok {
			*m.statusModel = sm
		}
		return m, c
	default:
		updated, c := m.browserModel.Update(msg)
		if bm, ok := updated.(browser.Model); ok {
			m.browserModel = bm
		}
		return m, c
	}
}

// View renders the active sub-model. A nil status model in status mode
// renders a placeholder (should not normally happen, but guards against
// any race where mode flipped before the lazy init finished).
func (m Model) View() string {
	switch m.mode {
	case modeStatus:
		if m.statusModel == nil {
			return "Loading plan..."
		}
		// Protect against any panic inside the status View so a bad
		// render doesn't kill the host process.
		defer func() { _ = recover() }()
		return m.statusModel.View()
	default:
		return m.browserModel.View()
	}
}

// Close tears down any live sub-models. The browser's Close is a no-op
// today but is called for symmetry; the status model owns daemon SSE
// subscription goroutines that must be drained on shutdown.
func (m *Model) Close() error {
	var firstErr error
	if m.statusModel != nil {
		if err := m.statusModel.Close(); err != nil {
			firstErr = err
		}
		m.statusModel = nil
	}
	if err := m.browserModel.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// compile-time guard that Model satisfies tea.Model.
var _ tea.Model = Model{}

// unused import guard for fmt in case future debugging needs it.
var _ = fmt.Sprintf
