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

// Tab order: Jobs and Add Job come first because they're the most
// frequent destinations once a plan is loaded. Plans lives at 3 as
// the entry point for picking a different plan; Add Plan and Finish
// Plan follow.
//
//	1. Jobs          (modeStatus)        — status view for the active plan
//	2. Add Job       (modeAddWizard)     — the add-job wizard
//	3. Plans         (modeBrowser)       — plan list browser
//	4. Add Plan      (modeInitWizard)    — plan-init wizard
//	5. Finish Plan   (modeFinishWizard)  — finish wizard
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

// addWizardReadyMsg is dispatched once a freshly-built add.Model
// (which synchronously loads config, skills service, templates, and
// skill metadata on disk) finishes construction off the bubbletea
// event loop. Routed to the meta-panel's Update, which installs the
// model, sizes it, and fires its Init. generation is set to the
// pager-generation counter at dispatch time so stale builds are
// dropped if the user already navigated away before construction
// finished.
type addWizardReadyMsg struct {
	model      add.Model
	generation uint64
}

// finishWizardReadyMsg is the finish-wizard analog of
// addWizardReadyMsg. Construction runs plan_finish.BuildItems which
// executes all Check closures (git merge / worktree / state file
// probes) synchronously — we push that off the event loop so the
// tab switch is instant.
type finishWizardReadyMsg struct {
	model      finish.Model
	err        error
	generation uint64
}

// initWizardReadyMsg is dispatched once a freshly-built planinit.Model
// finishes construction off the bubbletea event loop. planinit.New
// synchronously enumerates recipes (subprocess call to GetRecipeCmd),
// scans available models, loads config, and probes workspace — all
// disk/subprocess I/O we keep off the event loop so switching to the
// Add Plan tab is instant.
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

	mode               mode
	browserModel       browser.Model
	statusModel        *status.Model
	wizardModel        *add.Model
	finishWizardModel  *finish.Model
	initWizardModel    *planinit.Model

	// pendingInitPlanName is the plan slug captured from the init
	// wizard's Request when the user submits. The meta-panel uses
	// it after the `flow plan init` subprocess returns to locate
	// the freshly-created plan on disk and switch the status view
	// to it.
	pendingInitPlanName string

	// width/height are cached from the last WindowSizeMsg so lazily
	// constructed status models can be sized immediately.
	width  int
	height int

	// finishTransient is a short status line overlayed on top of the
	// active sub-model's View after a finish-wizard run, used to
	// surface action errors (or a build error on wizard launch) that
	// the user would otherwise never see. Cleared on the next
	// keypress so it doesn't linger forever.
	finishTransient string

	// wizardBuildGen is a monotonic counter bumped every time the
	// meta-panel kicks off a wizard construction tea.Cmd. The cmd
	// closes over the generation value at dispatch time; when its
	// *WizardReadyMsg arrives back in Update we compare against the
	// current counter and drop the result if the user already
	// switched away (e.g. hit `1` to go back to Browser) or if a
	// newer build was dispatched in the meantime. Without this
	// guard, a slow skill-service scan could slam a stale wizard
	// into the active slot after the user moved on.
	wizardBuildGen       uint64
	addWizardBuilding    bool
	finishWizardBuilding bool
	initWizardBuilding   bool
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

// subSize returns the WindowSizeMsg that sub-models should receive.
// It subtracts the meta-panel's own chrome (horizontal padding +
// tab bar + mode title rows) from the cached terminal size. Used by
// every lazy construction path that needs to size a freshly-built
// sub-model immediately (status on plan select, wizards on tab
// switch, etc.) so they don't briefly think they own the full
// terminal.
func (m Model) subSize() tea.WindowSizeMsg {
	w := m.width - 4
	h := m.height - 2
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return tea.WindowSizeMsg{Width: w, Height: h}
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
		// Deduct the meta-panel's own chrome before forwarding to
		// sub-models: 4 cols (Padding(0, 2)) + 2 rows (tab bar +
		// mode title). Without this deduction the status view's
		// log-pane math thinks it has the full terminal height and
		// overflows when a running job opens its logs pane.
		sub := tea.WindowSizeMsg{
			Width:  msg.Width - 4,
			Height: msg.Height - 2,
		}
		if sub.Width < 1 {
			sub.Width = 1
		}
		if sub.Height < 1 {
			sub.Height = 1
		}
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
		// Focus/blur routes to the active sub-model only. The inactive
		// one does not need to react to focus changes it can't see.
		return m.updateActive(msg)

	case addWizardReadyMsg:
		// Drop stale builds the user navigated away from mid-
		// construction. wizardBuildGen is bumped on every new
		// build dispatch; a ready msg with a lower generation has
		// been superseded.
		if msg.generation != m.wizardBuildGen {
			return m, nil
		}
		m.addWizardBuilding = false
		local := msg.model
		m.wizardModel = &local
		// If the user already switched away from Add Plan while we
		// were building, skip the sizing/init — the stale guard
		// above handles navigation that bumped the generation, but
		// a concurrent mode switch without a new build (e.g.
		// switching to Status) should still install the model so
		// a later return to Add is cheap. Size it either way.
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
			// Surface the build error and fall back to the status
			// view so the user isn't stranded on a "Loading..."
			// placeholder they can't dismiss.
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
		// Auto-switch signal emitted by a sub-panel (wizard submit,
		// browser enter, etc.). The flow meta-panel preserves its
		// existing state machine for lifecycle management, so we
		// translate the tab index back into a mode and run the same
		// bookkeeping the manual key handler would. Invalid requests
		// (e.g. switching to Status without a loaded plan) fall
		// through to a no-op.
		target, ok := modeForTabIndex(msg.TabIndex)
		if !ok {
			return m, nil
		}
		return m.switchToMode(target)

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
		// DoneMsg routing depends on which wizard is live. The add
		// wizard emits a *orchestration.Job on submit; the finish
		// wizard emits a []*finish.Item whose IsEnabled flags the
		// user toggled. Both emit nil on cancel. The init wizard
		// emits a *planinit.Request; on submit the meta-panel
		// launches `flow plan init` as a subprocess and waits for
		// initCompletedMsg.
		switch m.mode {
		case modeInitWizard:
			m.initWizardModel = nil
			if msg.Result == nil {
				// Cancel: return to browser.
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
						// Kick off a refresh so the new job appears.
						return m, func() tea.Msg { return status.RefreshMsg{} }
					}
				}
			}
			return m, nil
		case modeFinishWizard:
			m.finishWizardModel = nil
			// Cancel path: Result is nil, return to status view
			// without touching the plan.
			if msg.Result == nil {
				m.mode = modeStatus
				return m, nil
			}
			items, ok := msg.Result.([]*finish.Item)
			if !ok || items == nil {
				m.mode = modeStatus
				return m, nil
			}
			// Execute each enabled action sequentially, collecting
			// per-item errors instead of dropping them. Errors are
			// surfaced to the user via finishTransient so a failing
			// step is visible rather than swallowed silently.
			var actionErrs []finishActionError
			for _, item := range items {
				if item == nil || !item.IsEnabled || item.Action == nil {
					continue
				}
				if err := item.Action(); err != nil {
					actionErrs = append(actionErrs, finishActionError{itemTitle: item.Name, err: err})
				}
			}
			// Mirror the CLI path: run the user's on_finish hook and
			// clear the active-plan state regardless of whether any
			// actions errored. Hook failures are surfaced alongside
			// action errors.
			plan := (*orchestration.Plan)(nil)
			if m.statusModel != nil {
				plan = m.statusModel.Plan
			}
			if plan != nil {
				// Pass the plan slug (e.g. "my-feature") so hook
				// templates that reference {{.PlanName}} receive the
				// same value as the CLI path (filepath.Base(planPath))
				// rather than the plan's absolute directory.
				plan_finish.RunOnFinishHook(plan, plan.Name)
			}
			if err := state.Delete(groveplan.StateKey); err != nil {
				actionErrs = append(actionErrs, finishActionError{itemTitle: "unset active plan", err: err})
			} else {
				_ = state.Delete(groveplan.LegacyStateKey)
			}
			m.finishTransient = formatFinishErrors(actionErrs)
			// The active plan is either archived or marked finished
			// at this point. Tear down the status model and switch
			// back to the browser so the user sees the fresh plan
			// list without the now-gone plan.
			if m.statusModel != nil {
				_ = m.statusModel.Close()
				m.statusModel = nil
			}
			m.mode = modeBrowser
			// Re-focus the browser and trigger a reload so the plan
			// list reflects the cleanup.
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

		// Numeric tab jumps: 1=Jobs, 2=Add Job, 3=Plans, 4=Add Plan,
		// 5=Finish Plan. Gated when a wizard text input has focus
		// so digits don't get swallowed.
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
		// Intercept `ctrl+f` in status mode to launch the finish
		// wizard. `f` is bound to "view frontmatter" on the status
		// sub-model, `F` is bound to "skills" (ViewSkillPane), and
		// no other status single-letter key is mnemonic for
		// "finish" — ctrl+f is unused across the flow TUI so it
		// avoids shadowing any status binding.
		// Match both lowercase and uppercase reporting of Ctrl+F.
		// Some bubbletea + terminal combos report the chord as
		// "ctrl+F" (the rune the SHIFT modifier produces) even when
		// the user did not press shift; pass-2 testing showed
		// "ctrl+f" being silently ignored as a result. Accept both
		// so the binding is reachable from any terminal.
		ks := msg.String()
		if m.mode == modeStatus && (ks == "ctrl+f" || ks == "ctrl+F") && m.statusModel != nil {
			plan := m.statusModel.Plan
			if plan != nil {
				// Delegate to the shared async switch path so
				// ctrl+f behaves identically to pressing `4`.
				return m.switchToMode(modeFinishWizard)
			}
		}

		// Intercept `n` in browser mode as a shortcut to the Add
		// Plan tab (init wizard). Delegates to switchToMode so it
		// shares the same async-build path as pressing `4`.
		if m.mode == modeBrowser && msg.String() == "n" {
			return m.switchToMode(modeInitWizard)
		}

		// Intercept `a` in status mode to launch the add-job wizard.
		// Delegates to the shared async switch path so `a` behaves
		// identically to pressing `3`.
		if m.mode == modeStatus && msg.String() == "a" && m.statusModel != nil {
			if m.statusModel.Plan != nil {
				return m.switchToMode(modeAddWizard)
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

// formatFinishErrors renders a list of finishActionError entries as
// a single status-bar line: the first entry's item and error, with a
// "(+N more)" suffix if additional errors were collected. Returns an
// empty string when there are no errors.
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

// runInitSubprocess builds a `flow plan init` subprocess invocation
// from the wizard's Request and returns a tea.Cmd that suspends the
// bubbletea program via tea.ExecProcess while the subprocess runs.
// The subprocess is allowed to do all the heavy disk I/O (worktree
// creation, ecosystem bootstrap, editor invocations) without ever
// blocking the TUI event loop.
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
		// Add wizard needs a plan to target. Same fallback as
		// modeStatus: promote the browser's cursor-selected plan
		// if we don't have one loaded yet.
		if m.statusModel == nil {
			_ = m.ensureStatusFromBrowser()
		}
		if m.statusModel == nil || m.statusModel.Plan == nil {
			return m, nil
		}
		m.mode = modeAddWizard
		if m.wizardModel != nil || m.addWizardBuilding {
			// Already built or already building — just switch the
			// mode; the View will render the existing wizard or a
			// "Loading wizard..." placeholder until addWizardReadyMsg
			// lands.
			return m, nil
		}
		return m, m.startAddWizardBuild(m.statusModel.Plan)

	case modeInitWizard:
		// Init wizard is a peer tab. No status model required — it
		// creates a brand new plan. Construction runs orchestration.
		// ListAllRecipes (subprocess!) and scans available models /
		// config / workspace, so we push it off the event loop.
		m.mode = modeInitWizard
		if m.initWizardModel != nil || m.initWizardBuilding {
			return m, nil
		}
		return m, m.startInitWizardBuild()

	case modeFinishWizard:
		// Finish wizard requires a BuildContext populated by running
		// all Check closures (git / worktree / state probes). That is
		// disk I/O so we defer it to a tea.Cmd and render a placeholder
		// until finishWizardReadyMsg arrives.
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

// ensureStatusFromBrowser promotes the browser's cursor-selected
// plan into a freshly-built status model when the meta-panel has
// none yet. This is the fallback path for numeric tab jumps that
// target Status / Add / Finish while the user is still on the
// Browser tab and hasn't pressed Enter. Returns a tea.Cmd carrying
// the new status model's Init + a focused WindowSizeMsg, or nil if
// the browser has no plan under the cursor (list empty / still
// loading). On success, m.statusModel is populated and m.mode is
// NOT changed — the caller decides which mode to end up in.
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
	}
	parts = append(parts, body)
	composed := lipgloss.JoinVertical(lipgloss.Left, parts...)

	if m.finishTransient != "" {
		composed = m.finishTransient + "\n" + composed
	}
	// Horizontal padding only — sub-models supply their own top
	// margins. Matches cx/memory left-edge alignment without
	// stacking redundant blank rows above the tab bar.
	return lipgloss.NewStyle().Padding(0, 2).Render(composed)
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
