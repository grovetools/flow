// Package view is a meta-panel that hosts flow's browser and status
// sub-TUIs in a single embeddable tea.Model. It starts in "browser" mode
// (showing the plan list), switches to "status" mode when the user
// selects a plan (Enter), and back to "browser" mode on `esc`. Hosts
// embed this package instead of picking one of the sub-TUIs directly so
// the Plan panel can toggle between list and detail views without the
// host knowing about either.
package view

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"
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

type finishActionsCompletedMsg struct {
	errs []finishActionError
	// noteErr is reported separately from errs on purpose: moving the plan's
	// linked notes to completed/ has nothing to do with git teardown, and
	// letting it into errs made a note-move failure skip mark_finished and
	// archive_plan. The CLI has always reported note outcomes separately.
	noteErr error
}

// finishProgressTickMsg re-reads the live finish run so the Finish page can
// repaint while cleanup is running.
type finishProgressTickMsg struct{}

// finishRun is the live state of one embedded finish run: the sink every item's
// chatter (and every subprocess it spawns) is written into, plus which item is
// currently executing.
//
// This is what replaced swapping the process-global os.Stdout/os.Stderr to
// /dev/null for the duration of the run. That swap silenced the chatter, but it
// also (a) raced the render loop, which reads the same global, and (b) broke
// any renderer that re-resolves its output fd per frame — composed frames went
// to /dev/null while the front buffer was committed as painted.
type finishRun struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	current string
	index   int
	total   int
}

func (r *finishRun) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	const maxCapture = 128 * 1024
	n, err := r.buf.Write(p)
	if r.buf.Len() > maxCapture {
		trimmed := r.buf.Bytes()[r.buf.Len()-maxCapture:]
		kept := append([]byte("… earlier output omitted …\n"), trimmed...)
		r.buf.Reset()
		r.buf.Write(kept)
	}
	return n, err
}

// begin records the item about to run, for the progress surface.
func (r *finishRun) begin(index, total int, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.index, r.total, r.current = index, total, name
}

// snapshot returns the current progress line and the captured output so far.
func (r *finishRun) snapshot() (string, string) {
	if r == nil {
		return "", ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	progress := "Finishing plan…"
	if r.current != "" {
		progress = fmt.Sprintf("Finishing plan… (%d/%d: %s)", r.index, r.total, r.current)
	}
	return progress, r.buf.String()
}

// finishPlanNotes is a seam so the note lifecycle can be exercised
// independently of the `nb` binary in tests.
var finishPlanNotes = orchestration.FinishPlanNotes

// InitExecutionReport is the durable account of one delegated Add Plan
// attempt. Command is an unambiguous JSON rendering of Argv (not shell text),
// and Executable is the path actually selected by the delegation boundary.
type InitExecutionReport struct {
	AttemptID          string    `json:"attempt_id"`
	Phase              string    `json:"phase"`
	Executable         string    `json:"executable"`
	Argv               []string  `json:"argv"`
	Command            string    `json:"command"`
	WorkingDirectory   string    `json:"working_directory"`
	PlansDir           string    `json:"plans_dir"`
	PlanDir            string    `json:"plan_dir"`
	StartedAt          time.Time `json:"started_at"`
	FinishedAt         time.Time `json:"finished_at,omitempty"`
	ExitCause          string    `json:"exit_cause,omitempty"`
	ExitCode           *int      `json:"exit_code,omitempty"`
	Signal             string    `json:"signal,omitempty"`
	Stdout             string    `json:"stdout,omitempty"`
	Stderr             string    `json:"stderr,omitempty"`
	JournalPath        string    `json:"journal_path"`
	JournalWriteErrors []string  `json:"journal_write_errors,omitempty"`
	Residue            []string  `json:"residue,omitempty"`

	Err error `json:"-"`
}

// initCompletedMsg is dispatched only after the terminal report has been
// atomically persisted (or the persistence error has been recorded).
type initCompletedMsg struct {
	report InitExecutionReport
}

type initOutputTickMsg struct {
	path    string
	content string
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

type statusReadyMsg struct {
	plan       *orchestration.Plan
	graph      *orchestration.DependencyGraph
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
	// plan creation via an asynchronous command with captured output so
	// the worktree/disk I/O
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
	// DaemonClientFactory is passed to the Plans browser so reconnects can
	// auto-start a daemon instead of retrying a dead startup client.
	DaemonClientFactory browser.DaemonClientFactory
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
	// initProgress is non-empty only while the confirmed plan-init subprocess
	// is running. It lets the Add Plan page distinguish creation from its
	// separate asynchronous wizard-construction loading state.
	initProgress      string
	initOutputPath    string
	initOutput        string
	statusLoading     bool
	statusLoadError   string
	statusLoadingPlan string
	statusLoadGen     uint64
	// finishExecuting is non-zero exactly while the cleanup actions run. The
	// Finish page keeps rendering during that window (the wizard model is
	// discarded on submit), so without this the page fell back to its
	// wizard-loading placeholder and showed a static "Loading wizard…" for the
	// entire run — the "Finish Plan freezes" the user reported.
	finishExecuting bool
	finishProgress  string
	finishOutput    string
	// finishRun is the output sink + progress tracker shared by the item
	// closures (bound at build time) and the execution cmd.
	finishRun *finishRun
	// finishForce is flipped from the wizard's Force toggle at submit time.
	// The items are built once, so the switch has to be late-bound.
	finishForce *plan_finish.ForceSwitch
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
	// lastInitRequest preserves all entered identity/options across a failed
	// creation so retrying the Add Plan tab never starts from a blank form.
	lastInitRequest *planinit.Request
	// initFailure is intentionally durable (unlike finishTransient): it stays
	// visible until the user retries, cancels, changes workspace, or succeeds.
	initFailure string

	width  int
	height int

	// One-shot status line shown over the body after a finish-wizard
	// run; cleared on the next keypress.
	finishTransient string
	// finishFailure is the durable counterpart (cf. initFailure): the full
	// per-item account of a failed finish, including which repos were retained
	// and git's own reason. The one-shot line alone was erased by the very next
	// keypress, and the detail behind it used to go to /dev/null.
	finishFailure string
	// finishAfterStatusLoad deep-links a browser selection into Finish once its
	// plan and dependency graph have loaded off the event loop.
	finishAfterStatusLoad bool

	// wizardBuildGen + *Building flags guard async wizard
	// construction. Stale ready msgs (after user navigates away or
	// the workspace changes) are dropped by generation mismatch.
	wizardBuildGen       uint64
	addWizardBuilding    bool
	finishWizardBuilding bool
	initWizardBuilding   bool
}

func loadStatusCmd(planPath string, generation uint64) tea.Cmd {
	return func() tea.Msg {
		plan, problems := orchestration.LoadPlanLenient(planPath)
		if plan == nil {
			return statusReadyMsg{err: fmt.Errorf("load plan %q", planPath), generation: generation}
		}
		graph, err := orchestration.BuildDependencyGraph(plan)
		if err != nil {
			return statusReadyMsg{err: fmt.Errorf("open plan %q: %w", plan.Name, err), generation: generation}
		}
		// Lenient loading keeps valid jobs visible when one file is malformed.
		// The status model already surfaces per-job failures; only a wholly
		// unreadable plan blocks navigation.
		_ = problems
		return statusReadyMsg{plan: plan, graph: graph, generation: generation}
	}
}

// New constructs a Model. Boots in browser mode unless InitialPlan
// is set, in which case it boots straight into status.
func New(cfg Config) Model {
	b := browser.New(browser.Config{
		PlansDir:            cfg.PlansDir,
		WorkspaceDir:        cfg.WorkspaceDir,
		DaemonClient:        cfg.DaemonClient,
		DaemonClientFactory: cfg.DaemonClientFactory,
		EmbedMode:           true,
		Hosted:              cfg.Hosted,
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
		// Plans can render help + source plus a blank separator and action
		// status. Reserve that real maximum footer chrome in SubSize so the
		// selected row and range cannot be clipped when a status is present.
		FooterHeight: 4,
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
		m.lastInitRequest = nil
		m.initFailure = ""
		removeInitOutput(m.s.initOutputPath)
		m.s.initProgress = ""
		m.s.initOutputPath = ""
		m.s.initOutput = ""
		// Invalidate any in-flight async wizard builds — their ready
		// msgs will arrive with a stale generation and be dropped.
		m.wizardBuildGen++
		m.s.statusLoadGen++
		m.s.statusLoading = false
		m.s.statusLoadError = ""
		m.s.statusLoadingPlan = ""
		m.finishAfterStatusLoad = false
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
			// Keep the pair in lockstep. Leaving PlansDir at its launch-time
			// value reintroduces "validated one directory, wrote another" on
			// every host workspace switch.
			if plansDir := groveplan.ResolvePlansDir(msg.Node.Path); plansDir != "" {
				m.s.cfg.PlansDir = plansDir
			}
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

	case browser.BrowserPlanFinishRequestedMsg:
		if m.s.statusModel != nil {
			_ = m.s.statusModel.Close()
			m.s.statusModel = nil
		}
		if msg.PlanPath == "" {
			return m, nil
		}
		m.s.statusLoadGen++
		generation := m.s.statusLoadGen
		m.s.statusLoading = true
		m.s.statusLoadError = ""
		m.s.statusLoadingPlan = msg.PlanName
		m.finishAfterStatusLoad = true
		m.mode = modeFinishWizard
		m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabFinishPlan})
		return m, loadStatusCmd(msg.PlanPath, generation)

	case browser.BrowserPlanSelectedMsg:
		// Daemon portfolio rows intentionally contain only summary data. Load the
		// selected plan off the event loop and expose the Jobs page's loading gate
		// rather than briefly constructing a truthful-looking empty status model.
		if m.s.statusModel != nil {
			_ = m.s.statusModel.Close()
			m.s.statusModel = nil
		}
		if msg.PlanPath == "" {
			return m, nil
		}
		m.s.statusLoadGen++
		generation := m.s.statusLoadGen
		m.s.statusLoading = true
		m.s.statusLoadError = ""
		m.s.statusLoadingPlan = msg.PlanName
		m.finishAfterStatusLoad = false
		m.mode = modeStatus
		m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabJobs})
		return m, loadStatusCmd(msg.PlanPath, generation)

	case statusReadyMsg:
		if msg.generation != m.s.statusLoadGen {
			return m, nil
		}
		m.s.statusLoading = false
		if msg.err != nil {
			m.s.statusLoadError = msg.err.Error()
			m.finishAfterStatusLoad = false
			return m, nil
		}
		newStatus := status.New(status.Config{
			Plan: msg.plan, Graph: msg.graph,
			DaemonClient: m.s.cfg.DaemonClient, Hosted: m.s.cfg.Hosted,
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
		if m.finishAfterStatusLoad {
			m.finishAfterStatusLoad = false
			m.mode = modeFinishWizard
			m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabFinishPlan})
			if c := m.startFinishWizardBuild(msg.plan); c != nil {
				cmds = append(cmds, c)
			}
		}
		return m, tea.Batch(cmds...)

	case finishProgressTickMsg:
		if !m.s.finishExecuting {
			return m, nil
		}
		m.s.finishProgress, m.s.finishOutput = m.s.finishRun.snapshot()
		return m, pollFinishProgressCmd()

	case finishActionsCompletedMsg:
		m.s.finishExecuting = false
		m.s.finishProgress = ""
		m.finishTransient = formatFinishErrors(msg.errs)
		if msg.noteErr != nil {
			// Reported, never gating: this cannot skip mark_finished /
			// archive_plan the way it used to.
			note := "finish: linked notes: " + msg.noteErr.Error()
			if m.finishTransient == "" {
				m.finishTransient = note
			} else {
				m.finishTransient += " • " + note
			}
		}
		if len(msg.errs) > 0 {
			// Durable, multi-line, and carrying the captured subprocess output:
			// the one-shot line was erased by the next keypress and the detail
			// behind it used to be written to /dev/null.
			m.finishFailure = formatFinishFailures(msg.errs, m.s.finishOutput)
			// Keep the plan loaded and resolvable so cleanup can be retried.
			m.mode = modeStatus
			m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabJobs})
			return m, nil
		}
		m.finishFailure = ""
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
		cmds := []tea.Cmd{m.s.browserModel.Init()}
		if m.s.cfg.DaemonClient != nil {
			cmds = append(cmds, refreshDaemonCmd(m.s.cfg.DaemonClient))
		}
		return m, tea.Batch(cmds...)

	case initOutputTickMsg:
		if msg.path != m.s.initOutputPath || m.s.initProgress == "" {
			return m, nil
		}
		m.s.initOutput = msg.content
		return m, pollInitOutputCmd(msg.path)

	case initCompletedMsg:
		// Clear pending name/progress regardless of success/failure. Keep the
		// submitted request long enough to decide whether success should change
		// the host workspace's active plan.
		pendingName := m.pendingInitPlanName
		initRequest := m.lastInitRequest
		m.pendingInitPlanName = ""
		removeInitOutput(m.s.initOutputPath)
		m.s.initProgress = ""
		m.s.initOutputPath = ""
		m.s.initOutput = ""
		if msg.report.Err != nil {
			m.initFailure = formatInitFailure(msg.report)
			// Rebuild immediately from lastInitRequest. This keeps the Add Plan
			// page usable instead of leaving a nil model behind its loading gate.
			m.mode = modeInitWizard
			m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabAddPlan})
			cmds := []tea.Cmd{m.startInitWizardBuild(), m.s.browserModel.Init()}
			if m.s.cfg.DaemonClient != nil {
				cmds = append(cmds, refreshDaemonCmd(m.s.cfg.DaemonClient))
			}
			return m, tea.Batch(cmds...)
		}
		m.initFailure = ""
		m.lastInitRequest = nil
		if len(msg.report.JournalWriteErrors) > 0 {
			m.finishTransient = "plan created; journal finalization warning: " + strings.Join(msg.report.JournalWriteErrors, "; ")
		}
		// Subprocess succeeded. Try to locate and load the new plan
		// directly so we can switch straight to its status view.
		if pendingName != "" {
			// Look where the subprocess actually wrote, keeping the host's
			// configured location as a fallback so a host that already supplies
			// a correctly-anchored PlansDir is unaffected.
			planPath := filepath.Join(m.initPlansDir(), pendingName)
			if _, statErr := os.Stat(planPath); statErr != nil {
				planPath = filepath.Join(m.s.cfg.PlansDir, pendingName)
			}
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
					// Match `flow plan init`: a plan with its own worktree is activated
					// inside that worktree by the init subprocess. Do not also activate
					// it in the host/main ecosystem, which should retain rolling (or
					// whatever was active before creation). An opened session likewise
					// owns its activation.
					if shouldActivateCreatedPlanInHost(initRequest) {
						_ = state.Set(m.s.cfg.WorkspaceDir, groveplan.StateKey, plan.Name)
					}
					if m.s.cfg.DaemonClient != nil {
						cmds = append(cmds, refreshDaemonCmd(m.s.cfg.DaemonClient))
					}
					return m, tea.Batch(cmds...)
				}
			}
		}
		// Fallback: creation succeeded but the new plan could not be loaded.
		// Never leave the pager on Add Plan with a nil wizard.
		m.mode = modeBrowser
		m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabPlans})
		m.finishTransient = "plan created, but its workspace could not be loaded"
		// The live log has already been deleted above, so report.Stdout is the
		// last surviving copy of what the subprocess did. Keeping a short tail
		// stops a successful-but-unloadable creation from being a total
		// information loss.
		if tail := lastOutputLines(msg.report.Stdout, 3); tail != "" {
			m.finishTransient += " — " + tail
		}
		cmds := []tea.Cmd{m.s.browserModel.Init()}
		if m.s.cfg.DaemonClient != nil {
			cmds = append(cmds, refreshDaemonCmd(m.s.cfg.DaemonClient))
		}
		return m, tea.Batch(cmds...)

	case embed.DoneMsg:
		// Wizard payloads on submit: add → *orchestration.Job,
		// finish → []*finish.Item, init → *planinit.Request.
		// nil on cancel.
		switch m.mode {
		case modeInitWizard:
			m.s.initWizardModel = nil
			if msg.Result == nil {
				m.lastInitRequest = nil
				m.initFailure = ""
				removeInitOutput(m.s.initOutputPath)
				m.s.initProgress = ""
				m.s.initOutputPath = ""
				m.s.initOutput = ""
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
			requestCopy := *req
			m.lastInitRequest = &requestCopy
			m.initFailure = ""
			// Resolve once and reuse: the live-output log the poller reads and
			// the plans dir the subprocess reports into must be the same
			// directory, or the progress surface tails a file nobody writes.
			plansDir := m.initPlansDir()
			m.s.initProgress = "Creating plan " + req.Dir + "…"
			m.s.initOutputPath = initOutputPath(plansDir, req.Dir)
			m.s.initOutput = ""
			return m, tea.Batch(
				runInitSubprocess(req, plansDir, planinit.ResolveTargetWorkspace(m.s.cfg.WorkspaceDir), m.s.initOutputPath),
				pollInitOutputCmd(m.s.initOutputPath),
			)
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
			// Read the force toggle before discarding the wizard. It rides the
			// model rather than embed.DoneMsg so the DoneMsg Result stays
			// []*finish.Item for the standalone CLI wizard.
			force := m.s.finishWizardModel != nil && m.s.finishWizardModel.Force()
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
			var plan *orchestration.Plan
			if m.s.statusModel != nil {
				plan = m.s.statusModel.Plan
			}
			if m.s.finishForce != nil {
				m.s.finishForce.Set(force)
			}
			stateDir := m.finishStateDir()
			m.finishFailure = ""
			m.finishTransient = "Finishing plan…"
			m.s.finishExecuting = true
			m.s.finishProgress = "Finishing plan…"
			m.s.finishOutput = ""
			return m, tea.Batch(
				runEmbeddedFinishActions(finishRunRequest{
					items: items,
					plan:  plan,
					// Resolved here, on the event loop: the actions are about
					// to delete the directory this names.
					stateDir:   stateDir,
					activePlan: groveplan.ActivePlanForPath(stateDir),
					run:        m.s.finishRun,
				}),
				pollFinishProgressCmd(),
			)
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
		// A pending chord in the status model owns its continuation keys. Without
		// this, the host `a`→add-job shortcut steals the "a" that completes the
		// "va" chord (preview agent pane — what flat "p" used to do), opening the
		// "flow plan add" dialog instead. Same interception hazard the esc guard
		// below handles: host letter-shortcuts must stand down while a chord arms.
		chordPending := m.mode == modeStatus && m.s.statusModel != nil && m.s.statusModel.IsChordPending()

		if !textEntryActive && !chordPending {
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
		// pane (or text entry), or an armed chord / which-key popup, delegate
		// esc to it so the detail pane is closed / the chord is cancelled
		// properly (including BSP splits). Only pop back to the browser when
		// nothing is open in the status view. The chord check is what keeps an
		// esc pressed to dismiss the which-key popup from accidentally exiting
		// the whole status TUI (jobs 43/46 goal) — the status seam consumes it
		// via SequenceCancel.
		if m.mode == modeStatus && ks == "esc" {
			if m.s.statusModel != nil && (m.s.statusModel.ActiveDetailPane != status.NoPane || m.s.statusModel.IsTextEntryActive() || m.s.statusModel.IsChordPending()) {
				// Let the status model handle esc (close detail pane, cancel chord, etc.)
				break // fall through to pager delegation below
			}
			if m.s.statusModel != nil {
				_ = m.s.statusModel.Close()
				m.s.statusModel = nil
			}
			if m.s.statusLoading {
				m.s.statusLoadGen++
				m.s.statusLoading = false
			}
			m.s.statusLoadError = ""
			m.s.statusLoadingPlan = ""
			m.finishFailure = ""
			m.mode = modeBrowser
			m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabPlans})
			updated, _ := m.s.browserModel.Update(embed.FocusMsg{})
			if bm, ok := updated.(browser.Model); ok {
				m.s.browserModel = bm
			}
			return m, m.s.browserModel.Init()
		}

		// esc in the finish wizard cancels it, the same way `q` does. This arm
		// sits AFTER the text-entry / chord guards and after the status-mode
		// handler above so it can neither shadow that handler nor steal the esc
		// that dismisses a which-key popup.
		//
		// Scoped to the finish wizard on purpose: the add and init wizards
		// already bind esc themselves (unfocus a field, step back from the
		// review screen), so a host-level cancel would shadow real in-wizard
		// behaviour there. See the report for that deviation.
		if m.mode == modeFinishWizard && ks == "esc" && !m.s.finishExecuting {
			m.s.finishWizardModel = nil
			m.mode = modeStatus
			m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabJobs})
			return m, nil
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
	// And the same for the status model when the Jobs tab is NOT active. It
	// owns three self-rearming background loops — the 2s plan-refresh tick,
	// the daemon SSE listener, and the MsgCh stream listener — each of which
	// re-arms itself ONLY by handling the message it just produced. Starving
	// it for the duration of a wizard/browser detour kills all three for the
	// lifetime of the model, so the job table silently stops updating once
	// the user comes back to Jobs. Guarded on the active index so the pager's
	// own delivery is never doubled.
	if m.pager.ActiveIndex() != tabJobs && m.s.statusModel != nil {
		updated, sc := m.s.statusModel.Update(msg)
		if sm, ok := updated.(status.Model); ok {
			*m.s.statusModel = sm
		}
		if sc != nil {
			cmds = append(cmds, sc)
		}
	}
	return m, tea.Batch(cmds...)
}

type initJournal struct {
	Schema   string                `json:"schema"`
	PlanName string                `json:"plan_name"`
	Attempts []InitExecutionReport `json:"attempts"`
}

// legacyInitJournal is read only to preserve evidence written by versions that
// predate attempt IDs. A malformed journal is never overwritten.
type legacyInitJournal struct {
	PlanName   string    `json:"plan_name"`
	Phase      string    `json:"phase"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Error      string    `json:"error,omitempty"`
	Output     string    `json:"output,omitempty"`
}

func initJournalPath(plansDir, planName string) string {
	return filepath.Join(plansDir, ".init-"+filepath.Base(planName)+".journal.json")
}

func atomicWriteInitJournal(path string, journal initJournal) error {
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".init-journal-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readInitJournal(path, planName string) (initJournal, error) {
	journal := initJournal{Schema: "flow.plan-init-journal/v2", PlanName: planName}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return journal, nil
	}
	if err != nil {
		return journal, err
	}
	if err := json.Unmarshal(data, &journal); err == nil && journal.Schema != "" {
		return journal, nil
	}
	var legacy legacyInitJournal
	if err := json.Unmarshal(data, &legacy); err != nil || legacy.PlanName == "" {
		return initJournal{}, fmt.Errorf("read existing journal: invalid JSON")
	}
	legacyAttempt := InitExecutionReport{
		AttemptID:  "legacy-" + legacy.StartedAt.UTC().Format("20060102T150405.000000000Z"),
		Phase:      legacy.Phase,
		StartedAt:  legacy.StartedAt,
		FinishedAt: legacy.FinishedAt,
		ExitCause:  legacy.Error,
		Stdout:     legacy.Output,
	}
	journal.PlanName = legacy.PlanName
	journal.Attempts = append(journal.Attempts, legacyAttempt)
	return journal, nil
}

func refreshDaemonCmd(client daemon.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = client.Refresh(ctx)
		return nil
	}
}

// finishRunRequest is everything one embedded finish run needs. It is a struct
// rather than a parameter list because the state-dir/active-plan pair must be
// resolved on the event loop, BEFORE the actions delete the directory they name.
type finishRunRequest struct {
	items []*finish.Item
	plan  *orchestration.Plan
	// stateDir owns the active-plan key. It is the host workspace, not the
	// process cwd: the cwd may be inside the worktree the run is about to
	// delete, and it is the workspace that plan creation writes the key to.
	stateDir string
	// activePlan is stateDir's active plan as read before the run started.
	// Mirrors the CLI: only unset the key when it names the plan being
	// finished, so finishing a worktree plan cannot clear the host's own
	// active plan.
	activePlan string
	run        *finishRun
}

// runEmbeddedFinishActions keeps slow cleanup off the Bubble Tea event loop.
// Action chatter is captured into req.run (bound into the item closures at
// build time) rather than silenced by swapping the process-global stdout, and
// is surfaced to the user instead of discarded.
func runEmbeddedFinishActions(req finishRunRequest) tea.Cmd {
	return func() tea.Msg {
		sink := io.Writer(io.Discard)
		if req.run != nil {
			sink = req.run
		}

		var errs []finishActionError
		var noteErr error
		if req.plan != nil {
			// A note-move failure is reported, never gated on: it has nothing
			// to do with git teardown, and letting it into errs silently
			// skipped mark_finished and archive_plan.
			if _, err := finishPlanNotes(req.plan.Name); err != nil {
				noteErr = err
			}
			plan_finish.RunOnFinishHook(req.plan, req.plan.Name, sink)
		}
		terminal := map[string]bool{
			plan_finish.ItemArchivePlan:  true,
			plan_finish.ItemMarkFinished: true,
		}
		total := 0
		for _, item := range req.items {
			if item != nil && item.IsEnabled && item.Action != nil {
				total++
			}
		}
		index := 0
		blocking := 0
		for _, item := range req.items {
			if item == nil || !item.IsEnabled || item.Action == nil {
				continue
			}
			// Only a genuine failure skips the terminal items. A worktree
			// retained because one repo still holds uncommitted work is a
			// partial success: the plan is still marked finished and archived,
			// and the surviving directory is self-describing and recoverable.
			if blocking > 0 && terminal[item.ID] {
				continue
			}
			index++
			if req.run != nil {
				req.run.begin(index, total, item.Name)
			}
			if err := item.Action(); err != nil {
				errs = append(errs, finishActionError{itemTitle: item.Name, err: err})
				if !plan_finish.IsRetainedWorktree(err) {
					blocking++
				}
			}
		}
		if blocking == 0 && req.activePlan != "" && req.plan != nil && req.activePlan == req.plan.Name {
			// ErrNoEcosystemRoot is not a failure: when the worktree that owned
			// the state has been deleted, its .grove/state.yml went with it and
			// there is nothing left to unset. Escalating it made EVERY
			// successful finish report an error.
			if err := state.Delete(req.stateDir, groveplan.StateKey); err != nil && !errors.Is(err, state.ErrNoEcosystemRoot) {
				errs = append(errs, finishActionError{itemTitle: "unset active plan", err: err})
			} else if err == nil {
				_ = state.Delete(req.stateDir, groveplan.LegacyStateKey)
			}
		}
		return finishActionsCompletedMsg{errs: errs, noteErr: noteErr}
	}
}

// pollFinishProgressCmd re-arms a tick while cleanup runs so the Finish page
// repaints. Without it nothing changed on screen between submit and completion.
func pollFinishProgressCmd() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg {
		return finishProgressTickMsg{}
	})
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

// formatFinishFailures renders the durable failure account: every failed item
// with its full error (which now carries git's own message for a retained
// worktree), plus a tail of the captured action output.
func formatFinishFailures(errs []finishActionError, output string) string {
	if len(errs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Finish incomplete:")
	for _, e := range errs {
		b.WriteString("\n  • " + e.itemTitle + ": " + e.err.Error())
	}
	if tail := lastOutputLines(output, 5); tail != "" {
		b.WriteString("\n  " + tail)
	}
	return b.String()
}

// finishStateDir is the directory whose ecosystem owns the active-plan key for
// a finish run: the host workspace, falling back to the process cwd.
//
// Deliberately NOT the raw cwd. A TUI launched inside the plan's own worktree
// has a cwd that the finish run is about to delete, after which core/state
// cannot resolve an ecosystem root for it at all. The workspace is also where
// plan creation writes the key (state.Set(cfg.WorkspaceDir, …)), so the set and
// the delete finally name the same directory.
func (m Model) finishStateDir() string {
	if m.s.cfg.WorkspaceDir != "" {
		return m.s.cfg.WorkspaceDir
	}
	return stateDirForView()
}

// shouldActivateCreatedPlanInHost mirrors the activation rule in
// cmd.executePlanInit. Worktree/session plans own their active state outside the
// host workspace, so successful creation must not overwrite the host's state.
func shouldActivateCreatedPlanInHost(req *planinit.Request) bool {
	return req != nil && req.Worktree == "" && !req.OpenSession
}

type initExecutionDeps struct {
	command      func(string, ...string) *exec.Cmd
	writeJournal func(string, initJournal) error
	now          func() time.Time
	attemptID    func() string
	liveOutput   io.Writer
}

func defaultInitExecutionDeps() initExecutionDeps {
	return initExecutionDeps{
		command:      delegation.Command,
		writeJournal: atomicWriteInitJournal,
		now:          time.Now,
		attemptID:    func() string { return uuid.NewString() },
	}
}

// runInitSubprocess shells out to `flow plan init` from a tea.Cmd so
// worktree creation / ecosystem bootstrap doesn't block the event loop.
func runInitSubprocess(req *planinit.Request, plansDir, workspaceDir, outputPath string) tea.Cmd {
	return func() tea.Msg {
		deps := defaultInitExecutionDeps()
		if outputPath != "" {
			if output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
				defer output.Close()
				deps.liveOutput = &lockedWriter{writer: output}
			}
		}
		return initCompletedMsg{report: executeInitSubprocess(req, plansDir, workspaceDir, deps)}
	}
}

type lockedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}

func initOutputPath(plansDir, planName string) string {
	return filepath.Join(absolutePath(plansDir), ".init-"+filepath.Base(planName)+".output.log")
}

func pollInitOutputCmd(path string) tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		data, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return initOutputTickMsg{path: path, content: "Unable to read live output: " + err.Error()}
		}
		const maxLiveOutput = 128 * 1024
		if len(data) > maxLiveOutput {
			data = append([]byte("… earlier output omitted …\n"), data[len(data)-maxLiveOutput:]...)
		}
		return initOutputTickMsg{path: path, content: string(data)}
	})
}

func removeInitOutput(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

func executeInitSubprocess(req *planinit.Request, plansDir, workspaceDir string, deps initExecutionDeps) InitExecutionReport {
	plansDir = absolutePath(plansDir)
	workspaceDir = absolutePath(workspaceDir)
	if workspaceDir == "" {
		workspaceDir, _ = os.Getwd()
		workspaceDir = absolutePath(workspaceDir)
	}
	planDir := filepath.Join(plansDir, req.Dir)
	args := initSubprocessArgs(req)
	cmd := deps.command("flow", args...)
	cmd.Dir = workspaceDir

	report := InitExecutionReport{
		AttemptID:        deps.attemptID(),
		Phase:            "running",
		Executable:       cmd.Path,
		Argv:             append([]string(nil), cmd.Args...),
		WorkingDirectory: workspaceDir,
		PlansDir:         plansDir,
		PlanDir:          planDir,
		StartedAt:        deps.now(),
	}
	report.Command = safeArgv(report.Argv)
	siblingPath := initJournalPath(plansDir, req.Dir)
	report.JournalPath = siblingPath

	journal, readErr := readInitJournal(siblingPath, req.Dir)
	if readErr != nil {
		// Preserve unreadable evidence and give this attempt its own journal.
		siblingPath = filepath.Join(plansDir, ".init-"+filepath.Base(req.Dir)+"-"+report.AttemptID+".journal.json")
		report.JournalPath = siblingPath
		report.JournalWriteErrors = append(report.JournalWriteErrors, readErr.Error())
		journal = initJournal{Schema: "flow.plan-init-journal/v2", PlanName: req.Dir}
	}
	journal.Attempts = append(journal.Attempts, report)
	attemptIndex := len(journal.Attempts) - 1
	if err := deps.writeJournal(siblingPath, journal); err != nil {
		report.JournalWriteErrors = append(report.JournalWriteErrors, "write running report: "+err.Error())
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if deps.liveOutput != nil {
		cmd.Stdout = io.MultiWriter(&stdout, deps.liveOutput)
		cmd.Stderr = io.MultiWriter(&stderr, deps.liveOutput)
	}
	err := cmd.Run()
	report.Stdout = stdout.String()
	report.Stderr = stderr.String()
	report.FinishedAt = deps.now()
	report.Err = err
	setInitExitCause(&report, err)
	if err != nil {
		report.Phase = "failed"
	} else {
		report.Phase = "completed"
	}

	// Persist the terminal state at the sibling location first. If moving the
	// finalized journal fails, this still leaves complete evidence to discover.
	journal.Attempts[attemptIndex] = report
	if writeErr := deps.writeJournal(siblingPath, journal); writeErr != nil {
		report.JournalWriteErrors = append(report.JournalWriteErrors, "write terminal report: "+writeErr.Error())
	}
	// A transient sibling write failure must not prevent successful creation
	// from finalizing evidence inside the plan directory.
	if info, statErr := os.Stat(planDir); statErr == nil && info.IsDir() {
		finalPath := filepath.Join(planDir, ".init-journal.json")
		report.JournalPath = finalPath
		journal.Attempts[attemptIndex] = report
		if writeErr := deps.writeJournal(finalPath, journal); writeErr != nil {
			report.JournalPath = siblingPath
			report.JournalWriteErrors = append(report.JournalWriteErrors, "finalize journal: "+writeErr.Error())
		} else if removeErr := os.Remove(siblingPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			report.JournalWriteErrors = append(report.JournalWriteErrors, "remove sibling journal: "+removeErr.Error())
		}
	}
	report.Residue = discoverInitResidue(plansDir, req.Dir, planDir)
	// Persist the fully-populated report, including final journal location,
	// write/finalization errors, and post-finalization residue, before Bubble Tea
	// receives it. A failure here is still returned independently to the UI.
	journal.Attempts[attemptIndex] = report
	if writeErr := deps.writeJournal(report.JournalPath, journal); writeErr != nil {
		report.JournalWriteErrors = append(report.JournalWriteErrors, "persist completed report: "+writeErr.Error())
	}
	return report
}

func initSubprocessArgs(req *planinit.Request) []string {
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
		args = append(args, "--worktree="+req.Worktree)
	}
	if req.ExtractAllFrom != "" {
		args = append(args, "--extract-all-from", req.ExtractAllFrom)
	}
	for _, recipeVar := range req.RecipeVars {
		args = append(args, "--recipe-vars", recipeVar)
	}
	if req.RecipeCmd != "" {
		args = append(args, "--recipe-cmd", req.RecipeCmd)
	}
	if len(req.SiblingWorkspaces) > 0 {
		args = append(args, "--sibling-workspaces="+strings.Join(req.SiblingWorkspaces, ","))
	}
	if req.NoteRef != "" {
		args = append(args, "--note-ref", req.NoteRef)
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
		args = append(args, "--init=false")
	}
	if req.EnvProfile != "" {
		args = append(args, "--env", req.EnvProfile)
	}
	if req.Anchor != "" {
		args = append(args, "--anchor", req.Anchor)
	}
	if req.Layout != "" {
		args = append(args, "--layout", req.Layout)
	}
	return args
}

func safeArgv(argv []string) string {
	data, _ := json.Marshal(argv)
	return string(data)
}

func absolutePath(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}

func setInitExitCause(report *InitExecutionReport, err error) {
	if err == nil {
		report.ExitCause = "completed successfully"
		code := 0
		report.ExitCode = &code
		return
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		report.ExitCause = "could not start: " + err.Error()
		return
	}
	code := exitErr.ExitCode()
	report.ExitCode = &code
	report.ExitCause = fmt.Sprintf("exited with status %d", code)
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		report.Signal = status.Signal().String()
		report.ExitCause = "terminated by signal " + report.Signal
	}
}

func discoverInitResidue(plansDir, planName, planDir string) []string {
	seen := make(map[string]bool)
	var residue []string
	add := func(path string) {
		if path == "" || seen[path] {
			return
		}
		if _, err := os.Lstat(path); err == nil {
			seen[path] = true
			residue = append(residue, path)
		}
	}
	add(planDir)
	matches, _ := filepath.Glob(filepath.Join(plansDir, ".init-"+filepath.Base(planName)+"*.journal.json"))
	for _, match := range matches {
		add(match)
	}
	add(filepath.Join(planDir, ".init-journal.json"))
	return residue
}

// lastOutputLines returns the final n non-blank lines of output joined into a
// single line, for embedding in the one-line transient status bar.
func lastOutputLines(output string, n int) string {
	var lines []string
	for _, line := range strings.Split(output, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " · ")
}

func formatInitFailure(report InitExecutionReport) string {
	var b strings.Builder
	b.WriteString("Plan creation failed (edit and submit to retry, or return to Plans):\n")
	cause := report.ExitCause
	if cause == "" && report.Err != nil {
		cause = report.Err.Error()
	}
	fmt.Fprintf(&b, "Cause: %s\nCommand: %s\nExecutable: %s\nTarget: %s\nPlans directory: %s\n", cause, report.Command, report.Executable, report.WorkingDirectory, report.PlansDir)
	if report.Stdout == "" && report.Stderr == "" {
		b.WriteString("Output: no stdout or stderr was captured\n")
	} else {
		if report.Stdout != "" {
			b.WriteString("Stdout:\n" + report.Stdout)
			if !strings.HasSuffix(report.Stdout, "\n") {
				b.WriteByte('\n')
			}
		}
		if report.Stderr != "" {
			b.WriteString("Stderr:\n" + report.Stderr)
			if !strings.HasSuffix(report.Stderr, "\n") {
				b.WriteByte('\n')
			}
		}
	}
	b.WriteString("Journal: " + report.JournalPath + "\n")
	for _, writeErr := range report.JournalWriteErrors {
		b.WriteString("Journal error: " + writeErr + "\n")
	}
	for _, path := range report.Residue {
		b.WriteString("Residue: " + path + "\n")
	}
	return strings.TrimSpace(b.String())
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
	cfg := m.addWizardConfig(plan)
	return func() tea.Msg {
		return addWizardReadyMsg{
			model:      add.New(cfg),
			generation: gen,
		}
	}
}

// addWizardConfig builds the add.Config the wizard tab is constructed
// from. The status view's space-selection seeds the wizard's dependency
// picker, so selecting jobs and pressing A still creates a job that
// depends on them — the behaviour the inline create-job form provided
// before A started routing to this tab.
func (m *Model) addWizardConfig(plan *orchestration.Plan) add.Config {
	var initialDeps []string
	if m.s.statusModel != nil && m.s.statusModel.Plan == plan {
		initialDeps = m.s.statusModel.SelectedJobFilenames()
	}
	return add.Config{
		Plan:         plan,
		InitialDeps:  initialDeps,
		DaemonClient: m.s.cfg.DaemonClient,
		WorkspaceDir: m.s.cfg.WorkspaceDir,
	}
}

// initPlansDir resolves the plans directory belonging to the SAME workspace the
// init subprocess will run in, so validation, the journal, the live-output log
// and the post-create load all name one directory. `flow plan init` receives a
// bare plan name and re-resolves the plans dir from its own cwd
// (cmd/plan_init.go), which ResolveTargetWorkspace deliberately sets to the root
// ecosystem; anchoring the parent's bookkeeping on the host-supplied PlansDir
// instead made the two disagree for any non-root workspace.
//
// Falls back to the host's configured PlansDir whenever the ecosystem root
// cannot be resolved, or is the configured workspace itself. Both guards are
// load-bearing: ResolveTargetWorkspace("") falls through to os.Getwd(), which
// would silently retarget unit tests at the developer's real ecosystem.
//
// Does disk I/O (workspace discovery + config load); call it from Update
// handlers only, never from View or a per-tick path.
func (m Model) initPlansDir() string {
	if m.s.cfg.WorkspaceDir == "" {
		return m.s.cfg.PlansDir
	}
	ws := planinit.ResolveTargetWorkspace(m.s.cfg.WorkspaceDir)
	if ws == "" || ws == m.s.cfg.WorkspaceDir {
		return m.s.cfg.PlansDir
	}
	if resolved := groveplan.ResolvePlansDir(ws); resolved != "" {
		return resolved
	}
	return m.s.cfg.PlansDir
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
	targetWorkspace := planinit.ResolveTargetWorkspace(m.s.cfg.WorkspaceDir)
	getRecipeCmd, runInitByDefault, defaultModel := planinit.LoadFlowDefaultsAt(targetWorkspace)
	cfg := planinit.Config{
		// Validate the directory that will actually be written: the review
		// screen's collision/permission checks are built from this value.
		PlansDir:         m.initPlansDir(),
		GetRecipeCmd:     getRecipeCmd,
		RunInitByDefault: runInitByDefault,
		DefaultModel:     defaultModel,
		DaemonClient:     m.s.cfg.DaemonClient,
		WorkspaceDir:     targetWorkspace,
		Initial:          m.lastInitRequest,
		InitialExact:     m.lastInitRequest != nil,
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
	m.finishFailure = ""
	// The sink and the force switch are bound into the item closures HERE,
	// because the items are built once and executed later: the closures must be
	// able to see a force toggle the user has not flipped yet, and their output
	// must never reach the process-global stdout the renderer is using.
	run := &finishRun{}
	force := &plan_finish.ForceSwitch{}
	m.s.finishRun = run
	m.s.finishForce = force
	return func() tea.Msg {
		bctx := plan_finish.NewBuildContext(plan, plan.Directory)
		bctx.Output = run
		opts := plan_finish.Options{ForceSwitch: force}
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
			// This host reads Model.Force() on submit and applies it through
			// the ForceSwitch above, so the toggle is real here.
			ShowForceToggle: true,
		})
		return finishWizardReadyMsg{model: fm, generation: gen}
	}
}

// View delegates to the pager, prepending the transient finish
// message when present.
func (m Model) View() string {
	content := m.pager.View()
	if m.initFailure != "" && m.mode == modeInitWizard {
		content = m.initFailure + "\n\n" + content
	}
	if m.finishFailure != "" {
		content = m.finishFailure + "\n\n" + content
	}
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
