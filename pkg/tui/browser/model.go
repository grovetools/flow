package browser

import (
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/git"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/tui/components/help"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/planutil"
)

// RollingPlanName is the name of the auto-created rolling plan. Duplicated
// here rather than imported from flow/cmd so this package can be imported
// without pulling in the cobra command surface.
const RollingPlanName = "rolling"

// refreshInterval controls how often the browser re-polls the plans
// directory and git log while running.
const refreshInterval = 2 * time.Second

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
	Worktree              string
	GitStatus             *git.StatusInfo
	ReviewStatus          string
	MergeStatus           string
	Notes                 string
	EcosystemRepoStatuses []planutil.EcosystemRepoStatus
}

// Config carries the dependencies a browser Model needs. Hosts (the CLI
// wrapper and the terminal meta-panel) construct a Config and pass it to
// New so daemon clients, keymaps, and workspace paths are injected from
// outside rather than rebuilt inside the TUI.
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
	// KeyMap, if non-nil, overrides the default browser keymap. Leave
	// nil to use NewKeyMap(config.LoadDefault()).
	KeyMap *KeyMap
	// EmbedMode suppresses the inline footer (help + status) in View()
	// so the host can pin it via Footer() instead. When false (the
	// default, standalone CLI use) the footer renders inline at the
	// bottom of the view.
	EmbedMode bool
}

// Model is the browser TUI state. It holds the rendered plan list, the
// navigation cursor, any transient edit/confirm state, and the async
// results from loading plans / fetching git logs.
type Model struct {
	cfg Config

	plans          []PlanListItem
	cursor         int
	width          int
	height         int
	err            error
	loading        bool
	plansDirectory string
	cwdGitRoot     string
	statusMessage  string
	help           help.Model
	keys           KeyMap
	activePlan     string
	editingNotes   bool
	notesInput     textinput.Model
	editPlanIndex  int
	showGitLog     bool
	gitLogContent  string
	gitLogError    error
	showOnHold     bool

	embedMode bool // suppress inline footer; host uses Footer()

	// Ecosystem sub-navigation: when the user toggles git log on an
	// ecosystem plan, we enter a secondary navigation mode that walks
	// through repos in the ecosystem and shows per-repo git log.
	inRepoNavigationMode bool
	repoCursor           int
	repoGitLogContent    string
	repoGitLogError      error
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

// New builds a Model from the given Config. Most fields default to their
// zero values; real data is fetched in Init via async tea.Cmds.
func New(cfg Config) Model {
	km := KeyMap{}
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
		cfg:            cfg,
		plans:          []PlanListItem{},
		cursor:         0,
		loading:        true,
		plansDirectory: cfg.PlansDir,
		cwdGitRoot:     cfg.WorkspaceDir,
		help:           helpModel,
		keys:           km,
		showGitLog:     false,
		embedMode:      cfg.EmbedMode,
	}
}

// Init is the standard bubbletea entry point: kicks off an initial plans
// load, a top-level git log fetch, and the periodic refresh tick.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		loadPlansListCmd(m.plansDirectory, m.cwdGitRoot, m.showOnHold),
		fetchGitLogCmd(m.cwdGitRoot),
		refreshTick(),
	)
}

// Close releases resources owned by the browser. Currently the browser
// holds no long-lived goroutines or channels, so Close is a no-op; the
// method is provided so hosts can call it uniformly against any embedded
// sub-model and so future features (e.g. a daemon subscription) can add
// teardown here without changing the embedding contract.
func (m *Model) Close() error {
	return nil
}
