package browser

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/git"
	"github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/models"
	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/tui/components/help"
	"github.com/grovetools/core/tui/embed"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/planutil"
)

// RollingPlanName is the name of the auto-created rolling plan, sourced from
// core/pkg/plan so the literal lives in exactly one place.
const RollingPlanName = coreplan.RollingPlanName

// refreshInterval controls how often the browser re-polls the plans
// directory and git log while running.
const (
	fallbackRefreshInterval = 45 * time.Second
	daemonReconnectInterval = 5 * time.Second
)

// PlanListItem describes a single row rendered by the browser: the loaded
// plan plus all the derived status fields (worktree, git state, merge
// state, notes, last-updated) that the table needs.
type PlanListItem struct {
	Plan                  *orchestration.Plan
	Name                  string
	JobCount              int
	Status                string
	StatusParts           map[string]int
	LastUpdated           time.Time
	Workspace             string
	WorkspaceRoot         string
	Repositories          []string
	Selected              bool
	Worktree              string
	GitStatus             *git.StatusInfo
	ReviewStatus          string
	MergeStatus           string
	MergeVerdict          string
	Notes                 string
	NoteCount             int
	EcosystemRepoStatuses []planutil.EcosystemRepoStatus
	Key                   coreplan.PlanKey
	Binding               coreplan.PlanBinding
	ActionTarget          coreplan.PlanActionTarget
	// Archived marks a plan loaded from the <plansDir>/.archive scan.
	// Archived rows are read-only in the browser: mutating row-actions
	// are refused and the row renders dimmed.
	Archived bool
}

// Config carries the dependencies a browser Model needs. Hosts (the CLI
// wrapper and the terminal meta-panel) construct a Config and pass it to
// New so daemon clients, keymaps, and workspace paths are injected from
// outside rather than rebuilt inside the TUI.
type DaemonClientFactory func() daemon.Client

type Config struct {
	// PlansDir is the directory that holds the plan subdirectories this
	// browser will list. Required.
	PlansDir string
	// WorkspaceDir is the git root for the workspace the browser is
	// scoped to. Used to resolve worktree paths and to render the
	// top-level "repo log" pane. May be empty, in which case the browser
	// falls back to GetGitRoot(".").
	WorkspaceDir string
	// DaemonClient is an optional pre-constructed daemon client. The
	// browser itself does not currently speak to the daemon, but the
	// field is here so the Config shape matches status.Config and future
	// features (e.g. job counts from live state) can be wired in without
	// changing the embedding contract.
	DaemonClient daemon.Client
	// DaemonClientFactory creates a fresh, auto-start-capable client for each
	// stream connection attempt. Without it reconnecting a dead scoped daemon
	// would keep retrying the stale client captured at startup.
	DaemonClientFactory DaemonClientFactory
	// KeyMap, if non-nil, overrides the default browser keymap. Leave
	// nil to use NewKeyMap(config.LoadDefault()).
	KeyMap *KeyMap
	// EmbedMode suppresses the inline footer (help + status) in View()
	// so the host can pin it via Footer() instead. When false (the
	// default, standalone CLI use) the footer renders inline at the
	// bottom of the view.
	EmbedMode bool
	// Hosted means a workspace-switching host such as treemux owns this
	// browser. Workspace actions emit embed.SwitchWorkspaceRequestMsg instead
	// of spawning an external tmux session.
	Hosted bool
}

// Model is the browser TUI state. It holds the rendered plan list, the
// navigation cursor, any transient edit/confirm state, and the async
// results from loading plans / fetching git logs.
type Model struct {
	cfg Config

	plans        []PlanListItem
	cursor       int
	scrollOffset int
	width        int
	height       int
	err          error
	loading      bool
	// initialLoaded becomes true once the current workspace context has
	// completed at least one load. The "Loading plans..." placeholder is
	// gated on !initialLoaded so background refreshes (every refreshInterval)
	// refresh the list in place instead of flickering back to the placeholder.
	initialLoaded    bool
	focused          bool
	plansDirectory   string
	cwdGitRoot       string
	statusMessage    string
	help             help.Model
	keys             KeyMap
	activePlan       string
	editingNotes     bool
	notesInput       textinput.Model
	editPlanIndex    int
	showGitLog       bool
	gitLogContent    string
	gitLogError      error
	gitLogLoaded     bool
	showOnHold       bool
	showArchived     bool
	columnSelectMode bool
	columnCursor     int
	columnVisibility map[string]bool

	embedMode bool // suppress inline footer; host uses Footer()
	hosted    bool // route workspace opens through the embedding host

	// Ecosystem sub-navigation: when the user toggles git log on an
	// ecosystem plan, we enter a secondary navigation mode that walks
	// through repos in the ecosystem and shows per-repo git log.
	inRepoNavigationMode bool
	repoCursor           int
	repoGitLogContent    string
	repoGitLogError      error

	// Portfolio refresh source. Daemon mode is push-driven; local fallback is
	// explicitly labelled and uses a slow/manual refresh cadence.
	dataSource        string
	planIndexRevision uint64
	planSummaries     map[string]models.PlanSummary
	hasDaemonSnapshot bool
	streamGeneration  uint64
	streamCancel      context.CancelFunc
	streamConnecting  bool
	latestSnapshotAt  time.Time
	renderProbe       *renderLatencyProbe

	// Selected-row detail is the only live hydration allowed in daemon mode.
	detailGeneration uint64
	detailPendingKey string
	loadGeneration   uint64

	// holdPending/actionPending are keyed by qualified PlanKey, never by slug or
	// cursor. actionPending covers the U/M handoff lifetime and is cleared only
	// when the preserved Plans model regains focus and refreshes that exact key.
	holdPending    map[string]bool
	actionPending  map[string]embed.GitOperation
	refreshPlanKey string

	// dismissedPlanDirs are plans retired successfully by this TUI session.
	// Keeping a small tombstone prevents an in-flight stale daemon projection
	// from flashing an archived plan back into the table.
	dismissedPlanDirs map[string]bool
}

type renderLatencyProbe struct {
	once        sync.Once
	snapshotAt  time.Time
	projectedAt time.Time
	revision    uint64
}

func (p *renderLatencyProbe) record() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		now := time.Now()
		logging.NewUnifiedLogger("flow.tui.plans").Debug("Plan snapshot rendered").
			Field("revision", p.revision).
			Field("snapshot_to_render", now.Sub(p.snapshotAt)).
			Field("projection_to_render", now.Sub(p.projectedAt)).
			Log(context.Background())
	})
}

func (m *Model) armRenderProbe(snapshotAt time.Time, revision uint64) {
	if snapshotAt.IsZero() {
		snapshotAt = time.Now()
	}
	m.latestSnapshotAt = snapshotAt
	m.renderProbe = &renderLatencyProbe{snapshotAt: snapshotAt, projectedAt: time.Now(), revision: revision}
}

// PlanCount returns the number of plans in the browser list.
func (m Model) PlanCount() int { return len(m.plans) }

// DismissPlan immediately removes a successfully retired plan from the
// portfolio and tombstones its old directory for the rest of this TUI session.
// This avoids waiting for daemon refresh/index propagation after Finish.
func (m *Model) DismissPlan(planDir string) {
	if planDir == "" {
		return
	}
	planDir = filepath.Clean(planDir)
	if m.dismissedPlanDirs == nil {
		m.dismissedPlanDirs = make(map[string]bool)
	}
	m.dismissedPlanDirs[planDir] = true
	filtered := m.plans[:0]
	for _, item := range m.plans {
		if item.Plan != nil && filepath.Clean(item.Plan.Directory) == planDir {
			continue
		}
		filtered = append(filtered, item)
	}
	m.plans = filtered
	delete(m.planSummaries, planDir)
	m.ensureCursorVisible()
}

func (m Model) visiblePlans(plans []PlanListItem) []PlanListItem {
	fixedPlan, locked := fixedWorktreePlan(m.cwdGitRoot)
	filtered := make([]PlanListItem, 0, len(plans))
	for _, item := range plans {
		if item.Plan != nil && m.dismissedPlanDirs[filepath.Clean(item.Plan.Directory)] {
			continue
		}
		// A registered plan worktree has one immutable plan identity. Showing
		// unrelated plans here makes it look as if mutable active-plan state can
		// retarget the worktree, so lock this browser to its bound plan.
		if locked && item.Name != fixedPlan {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

// SelectedPlanName returns the display identity under the cursor. Hosts use it
// for the legacy single-workspace navigation contract; portfolio refreshes use
// selectedPlanKey so duplicate slugs in different workspaces stay distinct.
func (m Model) SelectedPlanName() string {
	if m.cursor < 0 || m.cursor >= len(m.plans) {
		return ""
	}
	return m.plans[m.cursor].Name
}

// SelectPlan relocates the cursor by stable plan identity. It deliberately
// leaves the cursor unchanged when the plan is absent (for example, filtered
// or archived between views).
func (m *Model) SelectPlan(name string) bool {
	for i := range m.plans {
		if m.plans[i].Name == name {
			m.cursor = i
			return true
		}
	}
	return false
}

func planItemKey(item PlanListItem) string {
	if key := item.Key.String(); key != "" {
		return key
	}
	if item.Plan != nil && item.Plan.Directory != "" {
		return coreplan.NewPlanKey(item.Plan.Directory).String()
	}
	return ""
}

func (m Model) selectedPlanKey() string {
	if m.cursor < 0 || m.cursor >= len(m.plans) {
		return ""
	}
	return planItemKey(m.plans[m.cursor])
}

func (m *Model) selectPlanKey(key string) bool {
	for i := range m.plans {
		if planItemKey(m.plans[i]) == key {
			m.cursor = i
			m.ensureCursorVisible()
			return true
		}
	}
	return false
}

func (m Model) visibleRowCount() int {
	// Table borders/header consume four rows, followed by the range indicator;
	// the browser view itself adds one row of top padding. Embedded host chrome
	// is already deducted by pager.SubSize.
	rows := m.height - 6
	if !m.embedMode {
		rows -= 3 // blank separator plus the two-line inline footer
	}
	if m.showGitLog {
		rows -= 13
	}
	if rows < 1 {
		rows = 1
	}
	if len(m.plans) > 0 && rows > len(m.plans) {
		rows = len(m.plans)
	}
	return rows
}

func (m *Model) ensureCursorVisible() {
	if len(m.plans) == 0 {
		m.cursor, m.scrollOffset = 0, 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.plans) {
		m.cursor = len(m.plans) - 1
	}
	visible := m.visibleRowCount()
	if m.cursor < m.scrollOffset {
		m.scrollOffset = m.cursor
	}
	if m.cursor >= m.scrollOffset+visible {
		m.scrollOffset = m.cursor - visible + 1
	}
	maxOffset := len(m.plans) - visible
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.scrollOffset > maxOffset {
		m.scrollOffset = maxOffset
	}
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}
}

// CurrentPlan returns the *orchestration.Plan currently under the
// browser's cursor, or nil if the list is empty or the cursor is
// out of range. Hosts call this when they need to promote the
// cursor-selected row without synthesizing a BrowserPlanSelectedMsg
// — e.g. the flow meta-panel turning a "jump to Status tab" key
// press into a plan activation when the user hasn't pressed Enter
// yet.
func (m Model) CurrentPlan() *orchestration.Plan {
	if m.cursor < 0 || m.cursor >= len(m.plans) {
		return nil
	}
	return m.plans[m.cursor].Plan
}

// BrowserPlanSelectedMsg is emitted when the user presses Enter on a plan
// row. Hosts catch this message to decide how to open the plan. Standalone
// CLI shims launch `flow plan status --tui`; the flow meta-panel at
// flow/pkg/tui/view instantiates a status.Model for the selected plan.
type BrowserPlanSelectedMsg struct {
	PlanName string
	PlanPath string
	Plan     *orchestration.Plan
}

// BrowserPlanFinishRequestedMsg asks the embedding flow meta-panel to open the
// selected plan directly on its Finish page. Keeping this in-process avoids
// tea.ExecProcess, which would suspend (and under treemux, exit) the host TUI.
type BrowserPlanFinishRequestedMsg struct {
	PlanName string
	PlanPath string
	Plan     *orchestration.Plan
}

// New builds a Model from the given Config. Most fields default to their
// zero values; real data is fetched in Init via async tea.Cmds.
func New(cfg Config) Model {
	var km KeyMap
	if cfg.KeyMap != nil {
		km = *cfg.KeyMap
	} else {
		cliCfg, _ := config.LoadDefault()
		km = NewKeyMap(cliCfg)
	}

	helpModel := help.NewBuilder().
		WithKeys(km).
		WithTitle("Plan List - Help").
		Build()

	return Model{
		cfg:               cfg,
		plans:             []PlanListItem{},
		cursor:            0,
		loading:           true,
		focused:           !cfg.EmbedMode,
		plansDirectory:    cfg.PlansDir,
		cwdGitRoot:        cfg.WorkspaceDir,
		help:              helpModel,
		keys:              km,
		showGitLog:        false,
		embedMode:         cfg.EmbedMode,
		hosted:            cfg.Hosted,
		dataSource:        "connecting",
		planSummaries:     make(map[string]models.PlanSummary),
		streamGeneration:  1,
		streamConnecting:  true,
		loadGeneration:    1,
		holdPending:       make(map[string]bool),
		actionPending:     make(map[string]embed.GitOperation),
		dismissedPlanDirs: make(map[string]bool),
		columnVisibility:  defaultBrowserColumnVisibility(),
	}
}

// Init starts plan loading only. Git log is intentionally lazy and is fetched
// when the detail pane is first opened.
func (m Model) Init() tea.Cmd {
	if factory := m.daemonClientFactory(); factory != nil {
		return connectPlanIndexCmd(factory, m.streamGeneration, m.plansDirectory, m.showOnHold, m.showArchived)
	}
	return tea.Batch(
		loadPlansListCmd(m.plansDirectory, m.cwdGitRoot, m.showOnHold, m.showArchived, m.loadGeneration),
		fallbackRefreshTick(),
	)
}

func (m Model) daemonClientFactory() DaemonClientFactory {
	if m.cfg.DaemonClientFactory != nil {
		return m.cfg.DaemonClientFactory
	}
	if m.cfg.DaemonClient != nil {
		return func() daemon.Client { return m.cfg.DaemonClient }
	}
	return nil
}

// Close releases resources owned by the browser. Currently the browser
// holds no long-lived goroutines or channels, so Close is a no-op; the
// method is provided so hosts can call it uniformly against any embedded
// sub-model and so future features (e.g. a daemon subscription) can add
// teardown here without changing the embedding contract.
func (m *Model) Close() error {
	if m.streamCancel != nil {
		m.streamCancel()
		m.streamCancel = nil
	}
	return nil
}
