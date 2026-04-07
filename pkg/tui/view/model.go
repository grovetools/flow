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
	"github.com/grovetools/flow/pkg/tui/wizards/add"
)

// mode enumerates which sub-model the meta-panel is currently routing
// updates and render calls to.
type mode int

const (
	modeBrowser mode = iota
	modeStatus
	// modeAddWizard is active while the add-job wizard is open on
	// top of the status view. The wizard is constructed lazily when
	// the user presses `a` in status mode and torn down when it
	// emits embed.DoneMsg.
	modeAddWizard
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
	// InitialPlan, if non-nil, causes the meta-panel to start in
	// status mode targeting this plan instead of the default browser
	// mode. Used by the flow plan status CLI wrapper so invoking
	// `flow plan status -t` still lands the user directly on the
	// status view.
	InitialPlan *orchestration.Plan
	// InitialGraph is the dependency graph for InitialPlan. Must be
	// non-nil if InitialPlan is set.
	InitialGraph *orchestration.DependencyGraph
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
	wizardModel  *add.Model

	// width/height are cached from the last WindowSizeMsg so lazily
	// constructed status models can be sized immediately.
	width  int
	height int
}

// New constructs a Model from the given Config. The browser sub-model
// is initialized immediately; the status sub-model is nil until the
// first plan selection — unless Config.InitialPlan is set, in which
// case a status sub-model is built eagerly and the meta-panel starts
// in status mode.
func New(cfg Config) Model {
	b := browser.New(browser.Config{
		PlansDir:     cfg.PlansDir,
		WorkspaceDir: cfg.WorkspaceDir,
		DaemonClient: cfg.DaemonClient,
	})
	m := Model{
		cfg:          cfg,
		mode:         modeBrowser,
		browserModel: b,
	}
	if cfg.InitialPlan != nil && cfg.InitialGraph != nil {
		s := status.New(status.Config{
			Plan:         cfg.InitialPlan,
			Graph:        cfg.InitialGraph,
			DaemonClient: cfg.DaemonClient,
		})
		m.statusModel = &s
		m.mode = modeStatus
	}
	return m
}

// Init forwards to whichever sub-model is currently active.
func (m Model) Init() tea.Cmd {
	if m.mode == modeStatus && m.statusModel != nil {
		// Start both: status for the initial view, plus the browser
		// so its plan list is already loaded when the user hits esc
		// to switch modes.
		return tea.Batch(m.statusModel.Init(), m.browserModel.Init())
	}
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
		if m.wizardModel != nil {
			wUpdated, wc := m.wizardModel.Update(msg)
			if wm, ok := wUpdated.(add.Model); ok {
				*m.wizardModel = wm
			}
			if wc != nil {
				cmds = append(cmds, wc)
			}
		}
		return m, tea.Batch(cmds...)

	case embed.SetWorkspaceMsg:
		// Workspace changed. The browser re-targets via SetWorkspaceMsg;
		// the status model, if alive, is plan-scoped and should be torn
		// down because the active plan is about to change. We drop it
		// here and switch back to the browser so the user sees the new
		// workspace's plan list. Any open wizard is also plan-scoped
		// and must be discarded along with the status model.
		if m.wizardModel != nil {
			_ = m.wizardModel.Close()
			m.wizardModel = nil
		}
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

	case embed.DoneMsg:
		// The add wizard has completed. A non-nil Result means the
		// user submitted a new job; persist it to the active plan and
		// refresh the status view. A nil Result means the user
		// cancelled, so we just return to status mode without
		// touching the plan.
		if m.mode != modeAddWizard {
			// DoneMsg from somewhere else — ignore. We don't forward
			// to sub-models because neither browser nor status emit
			// DoneMsg today.
			return m, nil
		}
		m.wizardModel = nil
		m.mode = modeStatus
		if msg.Result != nil && m.statusModel != nil {
			if job, ok := msg.Result.(*orchestration.Job); ok && job != nil {
				if _, err := orchestration.AddJob(m.statusModel.Plan, job); err == nil {
					// Kick off a refresh so the new job appears.
					return m, func() tea.Msg { return status.RefreshMsg{} }
				}
			}
		}
		return m, nil

	case tea.KeyMsg:
		// Intercept `a` in status mode to launch the add-job wizard.
		if m.mode == modeStatus && msg.String() == "a" && m.statusModel != nil {
			plan := m.statusModel.Plan
			if plan != nil {
				w := add.New(add.Config{
					Plan:         plan,
					DaemonClient: m.cfg.DaemonClient,
					WorkspaceDir: m.cfg.WorkspaceDir,
				})
				m.wizardModel = &w
				m.mode = modeAddWizard
				var cmds []tea.Cmd
				if m.width > 0 && m.height > 0 {
					sized, c := m.wizardModel.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
					if wm, ok := sized.(add.Model); ok {
						*m.wizardModel = wm
					}
					if c != nil {
						cmds = append(cmds, c)
					}
				}
				if c := m.wizardModel.Init(); c != nil {
					cmds = append(cmds, c)
				}
				return m, tea.Batch(cmds...)
			}
		}

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
	case modeAddWizard:
		if m.wizardModel == nil {
			return m, nil
		}
		updated, c := m.wizardModel.Update(msg)
		if wm, ok := updated.(add.Model); ok {
			*m.wizardModel = wm
		}
		return m, c
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
	case modeAddWizard:
		if m.wizardModel == nil {
			return "Loading wizard..."
		}
		defer func() { _ = recover() }()
		return m.wizardModel.View()
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
	if m.wizardModel != nil {
		_ = m.wizardModel.Close()
		m.wizardModel = nil
	}
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
