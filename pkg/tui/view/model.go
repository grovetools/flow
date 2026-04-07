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
	"github.com/grovetools/core/pkg/daemon"
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
		if m.finishWizardModel != nil {
			fUpdated, fc := m.finishWizardModel.Update(msg)
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
		// Intercept `ctrl+f` in status mode to launch the finish
		// wizard. `f` is bound to "view frontmatter" on the status
		// sub-model, `F` is bound to "skills" (ViewSkillPane), and
		// no other status single-letter key is mnemonic for
		// "finish" — ctrl+f is unused across the flow TUI so it
		// avoids shadowing any status binding.
		if m.mode == modeStatus && msg.String() == "ctrl+f" && m.statusModel != nil {
			plan := m.statusModel.Plan
			if plan != nil {
				w, err := m.buildFinishWizard(plan)
				if err != nil {
					// Surface the build error so the user knows
					// the keystroke was not a silent no-op.
					m.finishTransient = fmt.Sprintf("finish wizard: %v", err)
					return m, nil
				}
				m.finishWizardModel = &w
				m.mode = modeFinishWizard
				var cmds []tea.Cmd
				if m.width > 0 && m.height > 0 {
					sized, c := m.finishWizardModel.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
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
			}
		}

		// Intercept `n` in browser mode to launch the plan-init
		// wizard in-process. The browser also binds `n` to "create
		// new plan", which used to shell out via tea.ExecProcess;
		// intercepting here replaces that subprocess path with the
		// embedded wizard.
		if m.mode == modeBrowser && msg.String() == "n" {
			getRecipeCmd, runInitByDefault := planinit.LoadFlowDefaults()
			w := planinit.New(planinit.Config{
				PlansDir:         m.cfg.PlansDir,
				GetRecipeCmd:     getRecipeCmd,
				RunInitByDefault: runInitByDefault,
				DaemonClient:     m.cfg.DaemonClient,
				WorkspaceDir:     m.cfg.WorkspaceDir,
			})
			m.initWizardModel = &w
			m.mode = modeInitWizard
			var cmds []tea.Cmd
			if m.width > 0 && m.height > 0 {
				sized, c := m.initWizardModel.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
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
		}

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

// buildFinishWizard constructs a finish.Model for the given plan by
// invoking the plan_finish factory with conservative default Options
// (no --force, env teardown enabled, nothing pre-selected beyond what
// the user toggles). The factory runs all Check closures to populate
// Status/IsAvailable before the wizard renders.
func (m Model) buildFinishWizard(plan *orchestration.Plan) (finish.Model, error) {
	bctx := plan_finish.NewBuildContext(plan, plan.Directory)
	opts := plan_finish.Options{}
	result, err := plan_finish.BuildItems(bctx, opts)
	if err != nil {
		return finish.Model{}, err
	}
	return finish.New(finish.Config{
		PlanName:       plan.Directory,
		Items:          result.Items,
		BranchIsMerged: result.BranchIsMerged,
		BranchExists:   result.BranchExists,
		Plan:           plan,
		DaemonClient:   m.cfg.DaemonClient,
		WorkspaceDir:   m.cfg.WorkspaceDir,
	}), nil
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

// View renders the active sub-model. A nil status model in status mode
// renders a placeholder (should not normally happen, but guards against
// any race where mode flipped before the lazy init finished).
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
		// Protect against any panic inside the status View so a bad
		// render doesn't kill the host process.
		defer func() { _ = recover() }()
		body = m.statusModel.View()
	default:
		body = m.browserModel.View()
	}
	if m.finishTransient != "" {
		return m.finishTransient + "\n" + body
	}
	return body
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
