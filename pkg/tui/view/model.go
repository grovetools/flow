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
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/grovetools/core/pkg/daemon"
	groveplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/state"
	"github.com/grovetools/core/tui/components/pager"
	"github.com/grovetools/core/tui/embed"
	"github.com/grovetools/core/tui/keymap"
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
	// Hosted is true when running inside groveterm. Passed through to
	// the status sub-model to enable the native agent pane preview.
	Hosted bool
}

// viewState holds the mutable sub-model state that is shared between
// the host Model and its page adapters. Heap-allocated in New() so
// that page adapter pointers survive Model value copies (bubbletea
// copies Model on every Update cycle).
type viewState struct {
	cfg               Config
	browserModel      browser.Model
	statusModel       *status.Model
	wizardModel       *add.Model
	finishWizardModel *finish.Model
	initWizardModel   *planinit.Model
}

// Model is the flow meta-panel. The browser is built eagerly; status
// and wizard sub-models are lazy. Closing the status model on
// navigation prevents daemon SSE goroutine leaks.
type Model struct {
	// s is the shared mutable state. Page adapters hold a pointer to
	// the same struct so state changes made in the host's Update are
	// visible to the adapters' View/Enabled/Ready methods.
	s *viewState

	pager pager.Model
	mode  mode

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
		EmbedMode:    true,
	})
	vs := &viewState{
		cfg:          cfg,
		browserModel: b,
	}
	m := Model{
		s:    vs,
		mode: modeBrowser,
	}
	if cfg.InitialPlan != nil && cfg.InitialGraph != nil {
		sm := status.New(status.Config{
			Plan:         cfg.InitialPlan,
			Graph:        cfg.InitialGraph,
			DaemonClient: cfg.DaemonClient,
			Hosted:       cfg.Hosted,
		})
		vs.statusModel = &sm
		m.mode = modeStatus
	}

	pages := []pager.Page{
		&statusPage{s: vs},
		&addJobPage{s: vs},
		&plansPage{s: vs},
		&addPlanPage{s: vs},
		&finishPlanPage{s: vs},
	}
	startTab := tabPlans
	if m.mode == modeStatus {
		startTab = tabJobs
	}
	pg := pager.NewAt(pages, pager.KeyMapFromBase(keymap.NewBase()), startTab)
	pg.SetConfig(pager.Config{
		OuterPadding: [4]int{1, 2, 0, 2},
		ShowTitleRow: true,
	})
	m.pager = pg

	return m
}

// Init forwards to the active sub-model. When booting in status mode
// also kicks the browser so its plan list is preloaded.
func (m Model) Init() tea.Cmd {
	if m.mode == modeStatus && m.s.statusModel != nil {
		return tea.Batch(m.s.statusModel.Init(), m.s.browserModel.Init())
	}
	return m.s.browserModel.Init()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// The pager's Update handles WindowSizeMsg by calling
		// SubSize and forwarding to all pages via SetSize, then
		// forwarding the adjusted msg to the active page's Update.
		var cmd tea.Cmd
		m.pager, cmd = m.pager.Update(msg)
		return m, cmd

	case embed.SetWorkspaceMsg:
		// Workspace changed: tear down all plan-scoped sub-models
		// and return to browser for the new workspace's plan list.
		if m.s.wizardModel != nil {
			_ = m.s.wizardModel.Close()
			m.s.wizardModel = nil
		}
		if m.s.finishWizardModel != nil {
			_ = m.s.finishWizardModel.Close()
			m.s.finishWizardModel = nil
		}
		if m.s.initWizardModel != nil {
			_ = m.s.initWizardModel.Close()
			m.s.initWizardModel = nil
		}
		m.pendingInitPlanName = ""
		// Invalidate any in-flight async wizard builds — their ready
		// msgs will arrive with a stale generation and be dropped.
		m.wizardBuildGen++
		m.addWizardBuilding = false
		m.finishWizardBuilding = false
		m.initWizardBuilding = false
		if m.s.statusModel != nil {
			_ = m.s.statusModel.Close()
			m.s.statusModel = nil
		}
		m.mode = modeBrowser
		if msg.Node != nil {
			m.s.cfg.WorkspaceDir = msg.Node.Path
		}
		updated, c := m.s.browserModel.Update(msg)
		if bm, ok := updated.(browser.Model); ok {
			m.s.browserModel = bm
		}
		m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabPlans})
		return m, c

	case embed.FocusMsg, embed.BlurMsg:
		var cmd tea.Cmd
		m.pager, cmd = m.pager.Update(msg)
		return m, cmd

	case addWizardReadyMsg:
		if msg.generation != m.wizardBuildGen {
			return m, nil
		}
		m.addWizardBuilding = false
		local := msg.model
		m.s.wizardModel = &local
		var cmds []tea.Cmd
		if m.width > 0 && m.height > 0 {
			sized, c := m.s.wizardModel.Update(m.pager.SubSize(m.width, m.height))
			if wm, ok := sized.(add.Model); ok {
				*m.s.wizardModel = wm
			}
			if c != nil {
				cmds = append(cmds, c)
			}
		}
		if c := m.s.wizardModel.Init(); c != nil {
			cmds = append(cmds, c)
		}
		return m, tea.Batch(cmds...)

	case initWizardReadyMsg:
		if msg.generation != m.wizardBuildGen {
			return m, nil
		}
		m.initWizardBuilding = false
		local := msg.model
		m.s.initWizardModel = &local
		var cmds []tea.Cmd
		if m.width > 0 && m.height > 0 {
			sized, c := m.s.initWizardModel.Update(m.pager.SubSize(m.width, m.height))
			if im, ok := sized.(planinit.Model); ok {
				*m.s.initWizardModel = im
			}
			if c != nil {
				cmds = append(cmds, c)
			}
		}
		if c := m.s.initWizardModel.Init(); c != nil {
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
			if m.s.statusModel != nil {
				m.mode = modeStatus
				m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabJobs})
			} else {
				m.mode = modeBrowser
				m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabPlans})
			}
			return m, nil
		}
		local := msg.model
		m.s.finishWizardModel = &local
		var cmds []tea.Cmd
		if m.width > 0 && m.height > 0 {
			sized, c := m.s.finishWizardModel.Update(m.pager.SubSize(m.width, m.height))
			if fm, ok := sized.(finish.Model); ok {
				*m.s.finishWizardModel = fm
			}
			if c != nil {
				cmds = append(cmds, c)
			}
		}
		if c := m.s.finishWizardModel.Init(); c != nil {
			cmds = append(cmds, c)
		}
		return m, tea.Batch(cmds...)

	case embed.SwitchTabMsg:
		return m.switchToTab(msg.TabIndex)

	case browser.BrowserPlanSelectedMsg:
		// Build a fresh status model for the selected plan and
		// switch to status mode.
		if m.s.statusModel != nil {
			_ = m.s.statusModel.Close()
			m.s.statusModel = nil
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
			DaemonClient: m.s.cfg.DaemonClient,
			Hosted:       m.s.cfg.Hosted,
		})
		m.s.statusModel = &newStatus
		m.mode = modeStatus
		m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabJobs})

		var cmds []tea.Cmd
		if m.width > 0 && m.height > 0 {
			sized, c := m.s.statusModel.Update(m.pager.SubSize(m.width, m.height))
			if sm, ok := sized.(status.Model); ok {
				*m.s.statusModel = sm
			}
			if c != nil {
				cmds = append(cmds, c)
			}
		}
		if c := m.s.statusModel.Init(); c != nil {
			cmds = append(cmds, c)
		}
		// Give the new status model focus so any focus-driven
		// behavior (refresh loops, etc.) arms itself.
		focused, fc := m.s.statusModel.Update(embed.FocusMsg{})
		if sm, ok := focused.(status.Model); ok {
			*m.s.statusModel = sm
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
			return m, m.s.browserModel.Init()
		}
		// Subprocess succeeded. Try to locate and load the new plan
		// directly so we can switch straight to its status view.
		if pendingName != "" {
			planPath := filepath.Join(m.s.cfg.PlansDir, pendingName)
			if plan, err := orchestration.LoadPlan(planPath); err == nil && plan != nil {
				if graph, gerr := orchestration.BuildDependencyGraph(plan); gerr == nil {
					if m.s.statusModel != nil {
						_ = m.s.statusModel.Close()
						m.s.statusModel = nil
					}
					newStatus := status.New(status.Config{
						Plan:         plan,
						Graph:        graph,
						DaemonClient: m.s.cfg.DaemonClient,
						Hosted:       m.s.cfg.Hosted,
					})
					m.s.statusModel = &newStatus
					m.mode = modeStatus
					m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabJobs})
					var cmds []tea.Cmd
					if m.width > 0 && m.height > 0 {
						sized, c := m.s.statusModel.Update(m.pager.SubSize(m.width, m.height))
						if sm, ok := sized.(status.Model); ok {
							*m.s.statusModel = sm
						}
						if c != nil {
							cmds = append(cmds, c)
						}
					}
					if c := m.s.statusModel.Init(); c != nil {
						cmds = append(cmds, c)
					}
					return m, tea.Batch(cmds...)
				}
			}
		}
		// Fallback: refresh browser.
		return m, m.s.browserModel.Init()

	case embed.DoneMsg:
		// Wizard payloads on submit: add → *orchestration.Job,
		// finish → []*finish.Item, init → *planinit.Request.
		// nil on cancel.
		switch m.mode {
		case modeInitWizard:
			m.s.initWizardModel = nil
			if msg.Result == nil {
				m.mode = modeBrowser
				m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabPlans})
				return m, nil
			}
			req, ok := msg.Result.(*planinit.Request)
			if !ok || req == nil || req.Dir == "" {
				m.mode = modeBrowser
				m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabPlans})
				return m, nil
			}
			m.pendingInitPlanName = req.Dir
			return m, runInitSubprocess(req)
		case modeAddWizard:
			m.s.wizardModel = nil
			m.mode = modeStatus
			m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabJobs})
			if msg.Result != nil && m.s.statusModel != nil {
				if job, ok := msg.Result.(*orchestration.Job); ok && job != nil {
					plan := m.s.statusModel.Plan
					// Apply plan config defaults that the wizard doesn't set
					// (model gated to oneshot/chat).
					orchestration.ApplyPlanDefaults(plan, job)
					if _, err := orchestration.AddJob(plan, job); err == nil {
						m.finishTransient = "Added job: " + job.Title
						return m, func() tea.Msg { return status.RefreshMsg{} }
					} else {
						m.finishTransient = "Failed to add job: " + err.Error()
					}
				}
			}
			return m, nil
		case modeFinishWizard:
			m.s.finishWizardModel = nil
			if msg.Result == nil {
				m.mode = modeStatus
				m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabJobs})
				return m, nil
			}
			items, ok := msg.Result.([]*finish.Item)
			if !ok || items == nil {
				m.mode = modeStatus
				m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabJobs})
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
			if m.s.statusModel != nil {
				plan = m.s.statusModel.Plan
			}
			if plan != nil {
				plan_finish.RunOnFinishHook(plan, plan.Name)
			}
			sd := stateDirForView()
			if err := state.Delete(sd, groveplan.StateKey); err != nil {
				actionErrs = append(actionErrs, finishActionError{itemTitle: "unset active plan", err: err})
			} else {
				_ = state.Delete(sd, groveplan.LegacyStateKey)
			}
			m.finishTransient = formatFinishErrors(actionErrs)
			if m.s.statusModel != nil {
				_ = m.s.statusModel.Close()
				m.s.statusModel = nil
			}
			m.mode = modeBrowser
			m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabPlans})
			updated, _ := m.s.browserModel.Update(embed.FocusMsg{})
			if bm, ok := updated.(browser.Model); ok {
				m.s.browserModel = bm
			}
			return m, m.s.browserModel.Init()
		default:
			return m, nil
		}

	case tea.KeyMsg:
		// Clear any lingering finish-wizard transient message on the
		// next keypress so it behaves like a one-shot status line.
		if m.finishTransient != "" {
			m.finishTransient = ""
		}

		// Letter shortcuts (delegate to switchToTab for shared
		// async-build path). ctrl+f reported as both "ctrl+f" and
		// "ctrl+F" depending on terminal — accept both.
		ks := msg.String()

		// Check if text entry is active in the status model
		textEntryActive := m.mode == modeStatus && m.s.statusModel != nil && m.s.statusModel.IsTextEntryActive()

		if !textEntryActive {
			if m.mode == modeStatus && (ks == "ctrl+f" || ks == "ctrl+F") && m.s.statusModel != nil && m.s.statusModel.Plan != nil {
				return m.switchToTab(tabFinishPlan)
			}
			if m.mode == modeBrowser && ks == "n" {
				return m.switchToTab(tabAddPlan)
			}
			if m.mode == modeStatus && ks == "a" && m.s.statusModel != nil && m.s.statusModel.Plan != nil {
				return m.switchToTab(tabAddJob)
			}
		}

		// esc in status mode: if the status model has an active detail
		// pane (or text entry), delegate esc to it so the detail pane
		// is closed properly (including BSP splits). Only pop back to
		// the browser when nothing is open in the status view.
		if m.mode == modeStatus && ks == "esc" {
			if m.s.statusModel != nil && (m.s.statusModel.ActiveDetailPane != status.NoPane || m.s.statusModel.IsTextEntryActive()) {
				// Let the status model handle esc (close detail pane, etc.)
				break // fall through to pager delegation below
			}
			if m.s.statusModel != nil {
				_ = m.s.statusModel.Close()
				m.s.statusModel = nil
			}
			m.mode = modeBrowser
			m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabPlans})
			updated, _ := m.s.browserModel.Update(embed.FocusMsg{})
			if bm, ok := updated.(browser.Model); ok {
				m.s.browserModel = bm
			}
			return m, m.s.browserModel.Init()
		}

		// Delegate numeric tab jumps, [/] cycling, and remaining
		// keys to the pager. The pager handles PageWithTextInput
		// gating, PageWithEnabled skip, and forwards unmatched
		// keys to the active page's Update.
		prevIdx := m.pager.ActiveIndex()
		var pagerCmd tea.Cmd
		m.pager, pagerCmd = m.pager.Update(msg)
		newIdx := m.pager.ActiveIndex()
		if newIdx != prevIdx {
			// The pager switched tabs via numeric jump or [/].
			// Kick the host's lazy-build machinery.
			return m.syncModeFromPager()
		}
		// Pager didn't switch; return whatever cmd the active
		// page produced (quit, refresh, etc.).
		return m, pagerCmd
	}

	// All other messages: forward to the pager (which forwards to
	// the active page adapter, which delegates to the sub-model).
	// Also fan out to the browser model when the Plans tab is NOT
	// active, so background plan-list loads and git-log fetches
	// complete regardless of which tab the user is viewing.
	var cmds []tea.Cmd
	var pagerCmd tea.Cmd
	m.pager, pagerCmd = m.pager.Update(msg)
	if pagerCmd != nil {
		cmds = append(cmds, pagerCmd)
	}
	if m.pager.ActiveIndex() != tabPlans {
		updated, bc := m.s.browserModel.Update(msg)
		if bm, ok := updated.(browser.Model); ok {
			m.s.browserModel = bm
		}
		if bc != nil {
			cmds = append(cmds, bc)
		}
	}
	return m, tea.Batch(cmds...)
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
// NOTE: req.SiblingWorkspaces (--sibling-workspaces) is not forwarded yet.
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

// modeForTab maps a tab index to the host's mode enum.
func modeForTab(idx int) mode {
	switch idx {
	case tabJobs:
		return modeStatus
	case tabAddJob:
		return modeAddWizard
	case tabPlans:
		return modeBrowser
	case tabAddPlan:
		return modeInitWizard
	case tabFinishPlan:
		return modeFinishWizard
	default:
		return modeBrowser
	}
}

// switchToTab implements the tab jump / auto-switch logic for the
// flow meta-panel. It respects the invariants the lazy state machine
// relies on (Status/Add/Finish need a loaded plan) and builds any
// missing sub-model on demand. Requests that can't be satisfied
// (e.g. Jobs without a plan) are no-ops.
func (m Model) switchToTab(idx int) (tea.Model, tea.Cmd) {
	target := modeForTab(idx)
	if target == m.mode {
		return m, nil
	}
	switch target {
	case modeBrowser:
		// Tear down any wizard overlays but KEEP the status model
		// alive — switching back to status should not cost another
		// BuildDependencyGraph pass.
		if m.s.wizardModel != nil {
			_ = m.s.wizardModel.Close()
			m.s.wizardModel = nil
		}
		if m.s.finishWizardModel != nil {
			_ = m.s.finishWizardModel.Close()
			m.s.finishWizardModel = nil
		}
		m.mode = modeBrowser
		m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabPlans})
		updated, c := m.s.browserModel.Update(embed.FocusMsg{})
		if bm, ok := updated.(browser.Model); ok {
			m.s.browserModel = bm
		}
		return m, c

	case modeStatus:
		var ensureCmd tea.Cmd
		if m.s.statusModel == nil {
			ensureCmd = m.ensureStatusFromBrowser()
			if m.s.statusModel == nil {
				return m, nil
			}
		}
		if m.s.wizardModel != nil {
			_ = m.s.wizardModel.Close()
			m.s.wizardModel = nil
		}
		if m.s.finishWizardModel != nil {
			_ = m.s.finishWizardModel.Close()
			m.s.finishWizardModel = nil
		}
		m.mode = modeStatus
		m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabJobs})
		focused, c := m.s.statusModel.Update(embed.FocusMsg{})
		if sm, ok := focused.(status.Model); ok {
			*m.s.statusModel = sm
		}
		return m, tea.Batch(ensureCmd, c)

	case modeAddWizard:
		if m.s.statusModel == nil {
			_ = m.ensureStatusFromBrowser()
		}
		if m.s.statusModel == nil || m.s.statusModel.Plan == nil {
			return m, nil
		}
		m.mode = modeAddWizard
		m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabAddJob})
		if m.s.wizardModel != nil || m.addWizardBuilding {
			return m, nil
		}
		return m, m.startAddWizardBuild(m.s.statusModel.Plan)

	case modeInitWizard:
		m.mode = modeInitWizard
		m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabAddPlan})
		if m.s.initWizardModel != nil || m.initWizardBuilding {
			return m, nil
		}
		return m, m.startInitWizardBuild()

	case modeFinishWizard:
		if m.s.statusModel == nil {
			_ = m.ensureStatusFromBrowser()
		}
		if m.s.statusModel == nil || m.s.statusModel.Plan == nil {
			return m, nil
		}
		m.mode = modeFinishWizard
		m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabFinishPlan})
		if m.s.finishWizardModel != nil || m.finishWizardBuilding {
			return m, nil
		}
		return m, m.startFinishWizardBuild(m.s.statusModel.Plan)
	}
	return m, nil
}

// syncModeFromPager syncs the host mode enum after the pager's
// built-in tab switching (numeric jumps, [/] cycling). It kicks the
// host's lazy-build machinery for wizard tabs.
func (m Model) syncModeFromPager() (tea.Model, tea.Cmd) {
	return m.switchToTab(m.pager.ActiveIndex())
}

// ensureStatusFromBrowser builds a status model from the browser's
// cursor-selected plan. Used when numeric jumps target Status/Add/
// Finish before the user pressed Enter. m.mode is unchanged; the
// caller decides which mode to end up in.
func (m *Model) ensureStatusFromBrowser() tea.Cmd {
	if m.s.statusModel != nil {
		return nil
	}
	plan := m.s.browserModel.CurrentPlan()
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
		DaemonClient: m.s.cfg.DaemonClient,
	})
	m.s.statusModel = &newStatus
	var cmds []tea.Cmd
	if m.width > 0 && m.height > 0 {
		sized, c := m.s.statusModel.Update(m.pager.SubSize(m.width, m.height))
		if sm, ok := sized.(status.Model); ok {
			*m.s.statusModel = sm
		}
		if c != nil {
			cmds = append(cmds, c)
		}
	}
	if c := m.s.statusModel.Init(); c != nil {
		cmds = append(cmds, c)
	}
	focused, fc := m.s.statusModel.Update(embed.FocusMsg{})
	if sm, ok := focused.(status.Model); ok {
		*m.s.statusModel = sm
	}
	if fc != nil {
		cmds = append(cmds, fc)
	}
	if len(cmds) == 0 {
		// Return a no-op cmd so callers can distinguish "status
		// model built" (non-nil return or populated m.s.statusModel)
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
		DaemonClient: m.s.cfg.DaemonClient,
		WorkspaceDir: m.s.cfg.WorkspaceDir,
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
		PlansDir:         m.s.cfg.PlansDir,
		GetRecipeCmd:     getRecipeCmd,
		RunInitByDefault: runInitByDefault,
		DaemonClient:     m.s.cfg.DaemonClient,
		WorkspaceDir:     m.s.cfg.WorkspaceDir,
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
			DaemonClient:   m.s.cfg.DaemonClient,
			WorkspaceDir:   m.s.cfg.WorkspaceDir,
		})
		return finishWizardReadyMsg{model: fm, generation: gen}
	}
}

// View delegates to the pager, prepending the transient finish
// message when present.
func (m Model) View() string {
	content := m.pager.View()
	if m.finishTransient != "" {
		content = m.finishTransient + "\n" + content
	}
	if m.width > 0 && m.height > 0 {
		return lipgloss.Place(m.width, m.height, lipgloss.Left, lipgloss.Top, content)
	}
	return content
}

// Close tears down any live sub-models. The browser's Close is a no-op
// today but is called for symmetry; the status model owns daemon SSE
// subscription goroutines that must be drained on shutdown.
func (m *Model) Close() error {
	var firstErr error
	if m.s.initWizardModel != nil {
		_ = m.s.initWizardModel.Close()
		m.s.initWizardModel = nil
	}
	if m.s.finishWizardModel != nil {
		_ = m.s.finishWizardModel.Close()
		m.s.finishWizardModel = nil
	}
	if m.s.wizardModel != nil {
		_ = m.s.wizardModel.Close()
		m.s.wizardModel = nil
	}
	if m.s.statusModel != nil {
		if err := m.s.statusModel.Close(); err != nil {
			firstErr = err
		}
		m.s.statusModel = nil
	}
	if err := m.s.browserModel.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// TestState returns a snapshot of internal state for the debug API.
func (m Model) TestState() map[string]interface{} {
	state := map[string]interface{}{}

	switch m.mode {
	case modeBrowser:
		state["mode"] = "browser"
	case modeStatus:
		state["mode"] = "status"
	case modeAddWizard:
		state["mode"] = "add_wizard"
	case modeFinishWizard:
		state["mode"] = "finish_wizard"
	case modeInitWizard:
		state["mode"] = "init_wizard"
	}

	state["plan_count"] = m.s.browserModel.PlanCount()

	if m.s.statusModel != nil {
		state["job_count"] = len(m.s.statusModel.Jobs)
		state["selected_job_index"] = m.s.statusModel.Cursor
		if m.s.statusModel.Cursor >= 0 && m.s.statusModel.Cursor < len(m.s.statusModel.Jobs) {
			state["selected_job_title"] = m.s.statusModel.Jobs[m.s.statusModel.Cursor].Title
		}
		if m.s.statusModel.Plan != nil {
			state["plan_name"] = m.s.statusModel.Plan.Name
		}
	}

	return state
}

// stateDirForView returns the directory whose ecosystem owns the active-plan
// state. core/state resolves it to its ecosystem/worktree root, so a process
// outside any ecosystem refuses the write rather than touching a home-global
// state file.
func stateDirForView() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

// compile-time guard that Model satisfies tea.Model.
var _ tea.Model = Model{}
