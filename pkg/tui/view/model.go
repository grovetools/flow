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
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/grovetools/core/pkg/daemon"
	core_theme "github.com/grovetools/core/tui/theme"
	groveplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/state"
	"github.com/grovetools/core/tui/embed"
	"github.com/grovetools/core/util/delegation"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/plan_finish"
	"github.com/grovetools/flow/pkg/tui/browser"
	"github.com/grovetools/flow/pkg/tui/status"
	"github.com/grovetools/flow/pkg/tui/wizards/add"
	"github.com/grovetools/flow/pkg/tui/wizards/finish"
	planinit "github.com/grovetools/flow/pkg/tui/wizards/init"
)

// Tab order: 1=Jobs, 2=Add Job, 3=Plans, 4=Add Plan, 5=Finish Plan.
// Jobs / Add Job lead because they're the most frequent destinations
// once a plan is loaded.
func tabIndexFor(md mode) int {
	switch md {
	case modeStatus:
		return 0
	case modeAddWizard:
		return 1
	case modeBrowser:
		return 2
	case modeInitWizard:
		return 3
	case modeFinishWizard:
		return 4
	default:
		return 2
	}
}

// modeForTabIndex is the inverse of tabIndexFor.
func modeForTabIndex(idx int) (mode, bool) {
	switch idx {
	case 0:
		return modeStatus, true
	case 1:
		return modeAddWizard, true
	case 2:
		return modeBrowser, true
	case 3:
		return modeInitWizard, true
	case 4:
		return modeFinishWizard, true
	default:
		return modeBrowser, false
	}
}

// finishActionError pairs a failed cleanup item with its error so the
// meta-panel can surface which action failed after the wizard closes.
type finishActionError struct {
	itemTitle string
	err       error
}

// initCompletedMsg is dispatched after the `flow plan init`
// subprocess returns. It carries the error from the subprocess (nil
// on success) so the meta-panel can decide whether to transition to
// the new plan's status view or surface the failure.
type initCompletedMsg struct {
	err error
}

// *WizardReadyMsg messages carry a freshly-built wizard model from
// the async build cmd back to the meta-panel. generation matches
// wizardBuildGen at dispatch time so stale builds are dropped.
type addWizardReadyMsg struct {
	model      add.Model
	generation uint64
}

type finishWizardReadyMsg struct {
	model      finish.Model
	err        error
	generation uint64
}

type initWizardReadyMsg struct {
	model      planinit.Model
	generation uint64
}

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
	// modeFinishWizard is active while the plan-finish wizard is
	// open on top of the status view. Constructed lazily when the
	// user presses `f` in status mode and torn down on DoneMsg.
	modeFinishWizard
	// modeInitWizard is active while the plan-init wizard is open
	// on top of the browser view. Constructed lazily when the user
	// presses `n` in browser mode and torn down when it emits
	// embed.DoneMsg. On submit the meta-panel launches the actual
	// plan creation via tea.ExecProcess (mirroring the pre-embed
	// browser's subprocess shellout) so the worktree/disk I/O
	// never runs inside the bubbletea event loop.
	modeInitWizard
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

// Model is the flow meta-panel. The browser is built eagerly; status
// and wizard sub-models are lazy. Closing the status model on
// navigation prevents daemon SSE goroutine leaks.
type Model struct {
	cfg Config

	mode              mode
	browserModel      browser.Model
	statusModel       *status.Model
	wizardModel       *add.Model
	finishWizardModel *finish.Model
	initWizardModel   *planinit.Model

	// Set when the init wizard submits; consulted after the
	// `flow plan init` subprocess returns to locate the new plan.
	pendingInitPlanName string

	width  int
	height int

	// One-shot status line shown over the body after a finish-wizard
	// run; cleared on the next keypress.
	finishTransient string

	// wizardBuildGen + *Building flags guard async wizard
	// construction. Stale ready msgs (after user navigates away or
	// the workspace changes) are dropped by generation mismatch.
	wizardBuildGen       uint64
	addWizardBuilding    bool
	finishWizardBuilding bool
	initWizardBuilding   bool
}

// New constructs a Model. Boots in browser mode unless InitialPlan
// is set, in which case it boots straight into status.
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

// subSize returns the WindowSizeMsg sub-models should receive after
// subtracting meta-panel chrome: 4 cols (Padding(1,2,0,2)) + 3 rows
// (1 top pad + 1 tab bar + 1 title-or-blank). Used by every lazy
// construction site so freshly-built sub-models don't briefly think
// they own the full terminal.
func (m Model) subSize() tea.WindowSizeMsg {
	w := m.width - 4
	h := m.height - 3
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return tea.WindowSizeMsg{Width: w, Height: h}
}

// Init forwards to the active sub-model. When booting in status mode
// also kicks the browser so its plan list is preloaded.
func (m Model) Init() tea.Cmd {
	if m.mode == modeStatus && m.statusModel != nil {
		return tea.Batch(m.statusModel.Init(), m.browserModel.Init())
	}
	return m.browserModel.Init()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		sub := m.subSize()
		var cmds []tea.Cmd
		updated, c := m.browserModel.Update(sub)
		if bm, ok := updated.(browser.Model); ok {
			m.browserModel = bm
		}
		if c != nil {
			cmds = append(cmds, c)
		}
		if m.statusModel != nil {
			sUpdated, sc := m.statusModel.Update(sub)
			if sm, ok := sUpdated.(status.Model); ok {
				*m.statusModel = sm
			}
			if sc != nil {
				cmds = append(cmds, sc)
			}
		}
		if m.wizardModel != nil {
			wUpdated, wc := m.wizardModel.Update(sub)
			if wm, ok := wUpdated.(add.Model); ok {
				*m.wizardModel = wm
			}
			if wc != nil {
				cmds = append(cmds, wc)
			}
		}
		if m.finishWizardModel != nil {
			fUpdated, fc := m.finishWizardModel.Update(sub)
			if fm, ok := fUpdated.(finish.Model); ok {
				*m.finishWizardModel = fm
			}
			if fc != nil {
				cmds = append(cmds, fc)
			}
		}
		if m.initWizardModel != nil {
			iUpdated, ic := m.initWizardModel.Update(msg)
			if im, ok := iUpdated.(planinit.Model); ok {
				*m.initWizardModel = im
			}
			if ic != nil {
				cmds = append(cmds, ic)
			}
		}
		return m, tea.Batch(cmds...)

	case embed.SetWorkspaceMsg:
		// Workspace changed: tear down all plan-scoped sub-models
		// and return to browser for the new workspace's plan list.
		if m.wizardModel != nil {
			_ = m.wizardModel.Close()
			m.wizardModel = nil
		}
		if m.finishWizardModel != nil {
			_ = m.finishWizardModel.Close()
			m.finishWizardModel = nil
		}
		if m.initWizardModel != nil {
			_ = m.initWizardModel.Close()
			m.initWizardModel = nil
		}
		m.pendingInitPlanName = ""
		// Invalidate any in-flight async wizard builds — their ready
		// msgs will arrive with a stale generation and be dropped.
		m.wizardBuildGen++
		m.addWizardBuilding = false
		m.finishWizardBuilding = false
		m.initWizardBuilding = false
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
		return m.updateActive(msg)

	case addWizardReadyMsg:
		if msg.generation != m.wizardBuildGen {
			return m, nil
		}
		m.addWizardBuilding = false
		local := msg.model
		m.wizardModel = &local
		var cmds []tea.Cmd
		if m.width > 0 && m.height > 0 {
			sized, c := m.wizardModel.Update(m.subSize())
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

	case initWizardReadyMsg:
		if msg.generation != m.wizardBuildGen {
			return m, nil
		}
		m.initWizardBuilding = false
		local := msg.model
		m.initWizardModel = &local
		var cmds []tea.Cmd
		if m.width > 0 && m.height > 0 {
			sized, c := m.initWizardModel.Update(m.subSize())
			if im, ok := sized.(planinit.Model); ok {
				*m.initWizardModel = im
			}
			if c != nil {
				cmds = append(cmds, c)
			}
		}
		if c := m.initWizardModel.Init(); c != nil {
			cmds = append(cmds, c)
		}
		return m, tea.Batch(cmds...)

	case finishWizardReadyMsg:
		if msg.generation != m.wizardBuildGen {
			return m, nil
		}
		m.finishWizardBuilding = false
		if msg.err != nil {
			// Surface the error and bail out of the loading
			// placeholder so the user isn't stranded.
			m.finishTransient = fmt.Sprintf("finish wizard: %v", msg.err)
			if m.statusModel != nil {
				m.mode = modeStatus
			} else {
				m.mode = modeBrowser
			}
			return m, nil
		}
		local := msg.model
		m.finishWizardModel = &local
		var cmds []tea.Cmd
		if m.width > 0 && m.height > 0 {
			sized, c := m.finishWizardModel.Update(m.subSize())
			if fm, ok := sized.(finish.Model); ok {
				*m.finishWizardModel = fm
			}
			if c != nil {
				cmds = append(cmds, c)
			}
		}
		if c := m.finishWizardModel.Init(); c != nil {
			cmds = append(cmds, c)
		}
		return m, tea.Batch(cmds...)

	case embed.SwitchTabMsg:
		target, ok := modeForTabIndex(msg.TabIndex)
		if !ok {
			return m, nil
		}
		return m.switchToMode(target)

	case browser.BrowserPlanSelectedMsg:
		// Build a fresh status model for the selected plan and
		// switch to status mode.
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
			sized, c := m.statusModel.Update(m.subSize())
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

	case initCompletedMsg:
		// Clear pending name regardless of success/failure.
		pendingName := m.pendingInitPlanName
		m.pendingInitPlanName = ""
		if msg.err != nil {
			m.finishTransient = fmt.Sprintf("plan init: %v", msg.err)
			// Stay in browser mode; refresh so the user sees any
			// partial state.
			return m, m.browserModel.Init()
		}
		// Subprocess succeeded. Try to locate and load the new plan
		// directly so we can switch straight to its status view.
		if pendingName != "" {
			planPath := filepath.Join(m.cfg.PlansDir, pendingName)
			if plan, err := orchestration.LoadPlan(planPath); err == nil && plan != nil {
				if graph, gerr := orchestration.BuildDependencyGraph(plan); gerr == nil {
					if m.statusModel != nil {
						_ = m.statusModel.Close()
						m.statusModel = nil
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
						sized, c := m.statusModel.Update(m.subSize())
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
					return m, tea.Batch(cmds...)
				}
			}
		}
		// Fallback: refresh browser.
		return m, m.browserModel.Init()

	case embed.DoneMsg:
		// Wizard payloads on submit: add → *orchestration.Job,
		// finish → []*finish.Item, init → *planinit.Request.
		// nil on cancel.
		switch m.mode {
		case modeInitWizard:
			m.initWizardModel = nil
			if msg.Result == nil {
				m.mode = modeBrowser
				return m, nil
			}
			req, ok := msg.Result.(*planinit.Request)
			if !ok || req == nil || req.Dir == "" {
				m.mode = modeBrowser
				return m, nil
			}
			m.pendingInitPlanName = req.Dir
			return m, runInitSubprocess(req)
		case modeAddWizard:
			m.wizardModel = nil
			m.mode = modeStatus
			if msg.Result != nil && m.statusModel != nil {
				if job, ok := msg.Result.(*orchestration.Job); ok && job != nil {
					if _, err := orchestration.AddJob(m.statusModel.Plan, job); err == nil {
						return m, func() tea.Msg { return status.RefreshMsg{} }
					}
				}
			}
			return m, nil
		case modeFinishWizard:
			m.finishWizardModel = nil
			if msg.Result == nil {
				m.mode = modeStatus
				return m, nil
			}
			items, ok := msg.Result.([]*finish.Item)
			if !ok || items == nil {
				m.mode = modeStatus
				return m, nil
			}
			// Execute enabled actions sequentially, collecting errors.
			var actionErrs []finishActionError
			for _, item := range items {
				if item == nil || !item.IsEnabled || item.Action == nil {
					continue
				}
				if err := item.Action(); err != nil {
					actionErrs = append(actionErrs, finishActionError{itemTitle: item.Name, err: err})
				}
			}
			// Run on_finish hook + clear active-plan state regardless
			// of action errors.
			plan := (*orchestration.Plan)(nil)
			if m.statusModel != nil {
				plan = m.statusModel.Plan
			}
			if plan != nil {
				plan_finish.RunOnFinishHook(plan, plan.Name)
			}
			if err := state.Delete(groveplan.StateKey); err != nil {
				actionErrs = append(actionErrs, finishActionError{itemTitle: "unset active plan", err: err})
			} else {
				_ = state.Delete(groveplan.LegacyStateKey)
			}
			m.finishTransient = formatFinishErrors(actionErrs)
			if m.statusModel != nil {
				_ = m.statusModel.Close()
				m.statusModel = nil
			}
			m.mode = modeBrowser
			updated, _ := m.browserModel.Update(embed.FocusMsg{})
			if bm, ok := updated.(browser.Model); ok {
				m.browserModel = bm
			}
			return m, m.browserModel.Init()
		default:
			return m, nil
		}

	case tea.KeyMsg:
		// Clear any lingering finish-wizard transient message on the
		// next keypress so it behaves like a one-shot status line.
		if m.finishTransient != "" {
			m.finishTransient = ""
		}

		// Numeric tab jumps. Gated when a wizard text input has
		// focus so typed digits aren't swallowed.
		allowTabJump := true
		switch m.mode {
		case modeAddWizard:
			if m.wizardModel != nil && m.wizardModel.IsTextEntryActive() {
				allowTabJump = false
			}
		case modeInitWizard:
			if m.initWizardModel != nil && m.initWizardModel.IsTextEntryActive() {
				allowTabJump = false
			}
		}
		if allowTabJump {
			switch msg.String() {
			case "1":
				return m.switchToMode(modeStatus)
			case "2":
				return m.switchToMode(modeAddWizard)
			case "3":
				return m.switchToMode(modeBrowser)
			case "4":
				return m.switchToMode(modeInitWizard)
			case "5":
				return m.switchToMode(modeFinishWizard)
			}
		}
		// Letter shortcuts (delegate to switchToMode for shared
		// async-build path). ctrl+f reported as both "ctrl+f" and
		// "ctrl+F" depending on terminal — accept both.
		ks := msg.String()
		if m.mode == modeStatus && (ks == "ctrl+f" || ks == "ctrl+F") && m.statusModel != nil && m.statusModel.Plan != nil {
			return m.switchToMode(modeFinishWizard)
		}
		if m.mode == modeBrowser && ks == "n" {
			return m.switchToMode(modeInitWizard)
		}
		if m.mode == modeStatus && ks == "a" && m.statusModel != nil && m.statusModel.Plan != nil {
			return m.switchToMode(modeAddWizard)
		}

		// esc in status mode pops back to the browser.
		if m.mode == modeStatus && ks == "esc" {
			if m.statusModel != nil {
				_ = m.statusModel.Close()
				m.statusModel = nil
			}
			m.mode = modeBrowser
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

// formatFinishErrors renders a list of action errors as one
// status-bar line: first entry + "(+N more)" suffix if any.
func formatFinishErrors(errs []finishActionError) string {
	if len(errs) == 0 {
		return ""
	}
	first := errs[0]
	if len(errs) == 1 {
		return fmt.Sprintf("finish: %s: %v", first.itemTitle, first.err)
	}
	return fmt.Sprintf("finish: %s: %v (+%d more)", first.itemTitle, first.err, len(errs)-1)
}

// runInitSubprocess shells out to `flow plan init` via tea.ExecProcess
// so worktree creation / ecosystem bootstrap doesn't block the loop.
func runInitSubprocess(req *planinit.Request) tea.Cmd {
	args := []string{"plan", "init", req.Dir}
	if req.Recipe != "" {
		args = append(args, "--recipe", req.Recipe)
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Worktree == "__AUTO__" {
		args = append(args, "--worktree")
	} else if req.Worktree != "" {
		args = append(args, "--worktree", req.Worktree)
	}
	if req.FromNote != "" {
		args = append(args, "--from-note", req.FromNote)
	}
	if req.NoteTargetFile != "" {
		args = append(args, "--note-target-file", req.NoteTargetFile)
	}
	if req.OpenSession {
		args = append(args, "--open-session")
	}
	if req.Force {
		args = append(args, "--force")
	}
	if !req.RunInit {
		// RunInit defaults to true; only pass the override when
		// the user turned it off in the wizard.
		args = append(args, "--init=false")
	}
	if req.EnvProfile != "" {
		args = append(args, "--env", req.EnvProfile)
	}
	cmd := delegation.Command("flow", args...)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return initCompletedMsg{err: err}
	})
}

// switchToMode implements the numeric tab jump / auto-switch logic
// for the flow meta-panel. It respects the invariants the lazy
// state machine relies on (Status/Add/Finish need a loaded plan)
// and builds any missing sub-model on demand. For transitions that
// would require user input beyond "which tab is active" (e.g. the
// finish wizard, which needs a full plan_finish.BuildContext), the
// method falls back to reusing the existing launch path.
//
// Requests that can't be satisfied (e.g. Status without a plan)
// are no-ops so the caller can blindly forward numeric keys or
// embed.SwitchTabMsg without pre-flight checks.
func (m Model) switchToMode(target mode) (tea.Model, tea.Cmd) {
	if target == m.mode {
		return m, nil
	}
	switch target {
	case modeBrowser:
		// Tear down any wizard overlays but KEEP the status model
		// alive — switching back to status should not cost another
		// BuildDependencyGraph pass.
		if m.wizardModel != nil {
			_ = m.wizardModel.Close()
			m.wizardModel = nil
		}
		if m.finishWizardModel != nil {
			_ = m.finishWizardModel.Close()
			m.finishWizardModel = nil
		}
		m.mode = modeBrowser
		updated, c := m.browserModel.Update(embed.FocusMsg{})
		if bm, ok := updated.(browser.Model); ok {
			m.browserModel = bm
		}
		return m, c

	case modeStatus:
		// Status requires a loaded plan. If we don't have one yet
		// (user is still in browser mode and hasn't pressed Enter),
		// try to promote whatever is under the browser's cursor so
		// tab jumps actually go somewhere. Falls through to no-op
		// only when the plan list is empty.
		var ensureCmd tea.Cmd
		if m.statusModel == nil {
			ensureCmd = m.ensureStatusFromBrowser()
			if m.statusModel == nil {
				return m, nil
			}
		}
		if m.wizardModel != nil {
			_ = m.wizardModel.Close()
			m.wizardModel = nil
		}
		if m.finishWizardModel != nil {
			_ = m.finishWizardModel.Close()
			m.finishWizardModel = nil
		}
		m.mode = modeStatus
		focused, c := m.statusModel.Update(embed.FocusMsg{})
		if sm, ok := focused.(status.Model); ok {
			*m.statusModel = sm
		}
		return m, tea.Batch(ensureCmd, c)

	case modeAddWizard:
		if m.statusModel == nil {
			_ = m.ensureStatusFromBrowser()
		}
		if m.statusModel == nil || m.statusModel.Plan == nil {
			return m, nil
		}
		m.mode = modeAddWizard
		if m.wizardModel != nil || m.addWizardBuilding {
			return m, nil
		}
		return m, m.startAddWizardBuild(m.statusModel.Plan)

	case modeInitWizard:
		// Peer tab; no plan required.
		m.mode = modeInitWizard
		if m.initWizardModel != nil || m.initWizardBuilding {
			return m, nil
		}
		return m, m.startInitWizardBuild()

	case modeFinishWizard:
		if m.statusModel == nil {
			_ = m.ensureStatusFromBrowser()
		}
		if m.statusModel == nil || m.statusModel.Plan == nil {
			return m, nil
		}
		m.mode = modeFinishWizard
		if m.finishWizardModel != nil || m.finishWizardBuilding {
			return m, nil
		}
		return m, m.startFinishWizardBuild(m.statusModel.Plan)
	}
	return m, nil
}

// ensureStatusFromBrowser builds a status model from the browser's
// cursor-selected plan. Used when numeric jumps target Status/Add/
// Finish before the user pressed Enter. m.mode is unchanged; the
// caller decides which mode to end up in.
func (m *Model) ensureStatusFromBrowser() tea.Cmd {
	if m.statusModel != nil {
		return nil
	}
	plan := m.browserModel.CurrentPlan()
	if plan == nil {
		return nil
	}
	graph, err := orchestration.BuildDependencyGraph(plan)
	if err != nil {
		return nil
	}
	newStatus := status.New(status.Config{
		Plan:         plan,
		Graph:        graph,
		DaemonClient: m.cfg.DaemonClient,
	})
	m.statusModel = &newStatus
	var cmds []tea.Cmd
	if m.width > 0 && m.height > 0 {
		sized, c := m.statusModel.Update(m.subSize())
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
	focused, fc := m.statusModel.Update(embed.FocusMsg{})
	if sm, ok := focused.(status.Model); ok {
		*m.statusModel = sm
	}
	if fc != nil {
		cmds = append(cmds, fc)
	}
	if len(cmds) == 0 {
		// Return a no-op cmd so callers can distinguish "status
		// model built" (non-nil return or populated m.statusModel)
		// from "browser had nothing to offer" (nil + nil statusModel).
		return func() tea.Msg { return nil }
	}
	return tea.Batch(cmds...)
}

// startAddWizardBuild dispatches an async tea.Cmd that constructs a
// fresh add.Model off the bubbletea event loop. add.New() runs a
// pile of synchronous disk I/O (config load, skill service init,
// template enumeration, per-skill metadata loads), so constructing
// it inline made tab-switches to Add Plan feel sluggish. Running it
// as a Cmd lets the "Loading wizard..." placeholder render instantly
// and the real wizard fade in once the work completes.
func (m *Model) startAddWizardBuild(plan *orchestration.Plan) tea.Cmd {
	m.wizardBuildGen++
	gen := m.wizardBuildGen
	m.addWizardBuilding = true
	cfg := add.Config{
		Plan:         plan,
		DaemonClient: m.cfg.DaemonClient,
		WorkspaceDir: m.cfg.WorkspaceDir,
	}
	return func() tea.Msg {
		return addWizardReadyMsg{
			model:      add.New(cfg),
			generation: gen,
		}
	}
}

// startInitWizardBuild dispatches an async tea.Cmd that constructs
// a fresh planinit.Model off the bubbletea event loop. planinit.New
// runs orchestration.ListAllRecipes (a subprocess invocation!) plus
// model/config/workspace scans; doing it inline made the Add Plan
// tab switch feel sluggish for the same reasons as add/finish.
func (m *Model) startInitWizardBuild() tea.Cmd {
	m.wizardBuildGen++
	gen := m.wizardBuildGen
	m.initWizardBuilding = true
	getRecipeCmd, runInitByDefault := planinit.LoadFlowDefaults()
	cfg := planinit.Config{
		PlansDir:         m.cfg.PlansDir,
		GetRecipeCmd:     getRecipeCmd,
		RunInitByDefault: runInitByDefault,
		DaemonClient:     m.cfg.DaemonClient,
		WorkspaceDir:     m.cfg.WorkspaceDir,
	}
	return func() tea.Msg {
		return initWizardReadyMsg{
			model:      planinit.New(cfg),
			generation: gen,
		}
	}
}

// startFinishWizardBuild mirrors startAddWizardBuild for the finish
// wizard. plan_finish.BuildItems runs all Check closures and the
// build error (if any) is surfaced via finishTransient on the next
// render instead of blocking the tab switch.
func (m *Model) startFinishWizardBuild(plan *orchestration.Plan) tea.Cmd {
	m.wizardBuildGen++
	gen := m.wizardBuildGen
	m.finishWizardBuilding = true
	return func() tea.Msg {
		bctx := plan_finish.NewBuildContext(plan, plan.Directory)
		opts := plan_finish.Options{}
		result, err := plan_finish.BuildItems(bctx, opts)
		if err != nil {
			return finishWizardReadyMsg{err: err, generation: gen}
		}
		fm := finish.New(finish.Config{
			PlanName:       plan.Directory,
			Items:          result.Items,
			BranchIsMerged: result.BranchIsMerged,
			BranchExists:   result.BranchExists,
			Plan:           plan,
			DaemonClient:   m.cfg.DaemonClient,
			WorkspaceDir:   m.cfg.WorkspaceDir,
		})
		return finishWizardReadyMsg{model: fm, generation: gen}
	}
}

// updateActive forwards a message to whichever sub-model is currently
// active and returns the updated meta-Model + tea.Cmd.
func (m Model) updateActive(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeInitWizard:
		if m.initWizardModel == nil {
			return m, nil
		}
		updated, c := m.initWizardModel.Update(msg)
		if im, ok := updated.(planinit.Model); ok {
			*m.initWizardModel = im
		}
		return m, c
	case modeFinishWizard:
		if m.finishWizardModel == nil {
			return m, nil
		}
		updated, c := m.finishWizardModel.Update(msg)
		if fm, ok := updated.(finish.Model); ok {
			*m.finishWizardModel = fm
		}
		return m, c
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

// View renders the active sub-model with a tab bar + optional
// mode title above it. Uses tight vertical chrome (no outer top
// pad, no blank row between bar and body) because several sub-
// models (status, add wizard, etc.) already carry their own top
// margin — stacking everything produced 3 blank rows between tab
// bar and content.
func (m Model) View() string {
	var body string
	switch m.mode {
	case modeInitWizard:
		if m.initWizardModel == nil {
			body = "Loading init wizard..."
			break
		}
		defer func() { _ = recover() }()
		body = m.initWizardModel.View()
	case modeFinishWizard:
		if m.finishWizardModel == nil {
			body = "Loading finish wizard..."
			break
		}
		defer func() { _ = recover() }()
		body = m.finishWizardModel.View()
	case modeAddWizard:
		if m.wizardModel == nil {
			body = "Loading wizard..."
			break
		}
		defer func() { _ = recover() }()
		body = m.wizardModel.View()
	case modeStatus:
		if m.statusModel == nil {
			body = "Loading plan..."
			break
		}
		defer func() { _ = recover() }()
		body = m.statusModel.View()
	default:
		body = m.browserModel.View()
	}

	bar := m.renderTabBar()
	title := m.renderModeTitle()

	var parts []string
	if bar != "" {
		parts = append(parts, bar)
	}
	if title != "" {
		parts = append(parts, title)
	} else if bar != "" {
		// Modes without a dedicated title row (Jobs, whose status
		// sub-model renders its own "Plan Status: X" header) still
		// need a blank row between the tab bar and the body so the
		// header isn't cramped right against the nav.
		parts = append(parts, "")
	}
	parts = append(parts, body)
	composed := lipgloss.JoinVertical(lipgloss.Left, parts...)

	if m.finishTransient != "" {
		composed = m.finishTransient + "\n" + composed
	}
	// 1 row top margin, 0 bottom, 2 cols horizontal.
	return lipgloss.NewStyle().Padding(1, 2, 0, 2).Render(composed)
}

// renderModeTitle returns a short heading rendered above the active
// sub-model's view. Modes whose sub-models already render their own
// title (e.g. status' "Plan Status: ..." header) return "". Others
// get a consistent bold heading so each tab feels labeled.
func (m Model) renderModeTitle() string {
	th := core_theme.DefaultTheme
	heading := func(s string) string {
		return th.Bold.Render(s) + "\n"
	}
	switch m.mode {
	case modeAddWizard:
		return heading(" Add Job")
	case modeInitWizard:
		return heading("󰠡 Add Plan")
	case modeFinishWizard:
		return heading("󰄬 Finish Plan")
	case modeBrowser:
		ws := filepath.Base(m.cfg.WorkspaceDir)
		if ws == "" || ws == "." || ws == "/" {
			return heading("󰠡 Plans")
		}
		return heading(fmt.Sprintf("󰠡 Plans in the %s workspace", ws))
	}
	return ""
}

// renderTabBar draws the 5-tab navigation header above the active
// sub-model. It mirrors the nav / pager visual style: numeric circle
// icons in violet/muted, tab labels in light/muted, separated by a
// bullet. Tabs whose prerequisites aren't met (e.g. Jobs without
// a loaded plan) are still rendered but styled as disabled so the
// user sees the eventual navigation layout.
//
// Tab 4 "Add Plan" is always enabled — it launches the plan-init
// wizard which creates a brand new plan and doesn't need anything
// pre-selected. Tabs 2/3/5 require a loaded plan.
func (m Model) renderTabBar() string {
	entries := []struct {
		name    string
		enabled bool
	}{
		{"Jobs", m.statusModel != nil},
		{"Add Job", m.statusModel != nil},
		{"Plans", true},
		{"Add Plan", true},
		{"Finish Plan", m.statusModel != nil},
	}
	icons := []string{
		coreThemeIconNumeric(1),
		coreThemeIconNumeric(2),
		coreThemeIconNumeric(3),
		coreThemeIconNumeric(4),
		coreThemeIconNumeric(5),
	}
	active := tabIndexFor(m.mode)
	var parts []string
	for i, e := range entries {
		state := tabSegmentInactive
		switch {
		case i == active:
			state = tabSegmentActive
		case !e.enabled:
			state = tabSegmentDisabled
		}
		parts = append(parts, renderTabSegment(icons[i], e.name, state))
	}
	return joinTabs(parts)
}

// Close tears down any live sub-models. The browser's Close is a no-op
// today but is called for symmetry; the status model owns daemon SSE
// subscription goroutines that must be drained on shutdown.
func (m *Model) Close() error {
	var firstErr error
	if m.initWizardModel != nil {
		_ = m.initWizardModel.Close()
		m.initWizardModel = nil
	}
	if m.finishWizardModel != nil {
		_ = m.finishWizardModel.Close()
		m.finishWizardModel = nil
	}
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
