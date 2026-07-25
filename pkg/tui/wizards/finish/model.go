package finish

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/tui/components/help"

	"github.com/grovetools/flow/pkg/orchestration"
)

// Config carries the dependencies the plan-finish wizard needs. Hosts
// (the CLI wrapper and the flow meta-panel) construct a Config and
// pass it to New so the wizard doesn't reach out to globals.
type Config struct {
	// PlanName is the human-readable plan name rendered in the
	// header. Required.
	PlanName string
	// Items is the cleanup-action list. The wizard toggles the
	// IsEnabled field on each item as the user interacts; hosts
	// receive the same slice back via DoneMsg on submit and are
	// responsible for executing any Action closures.
	Items []*Item
	// BranchIsMerged indicates whether the plan's worktree branch is
	// already merged into main, used to color the header.
	BranchIsMerged bool
	// BranchExists indicates whether the worktree branch still
	// exists, used to hide merged/unmerged state when there's no
	// branch left to report on.
	BranchExists bool
	// KeyMap, if non-nil, overrides the default wizard keymap.
	// Leave nil to use NewKeyMap(config.LoadDefault()).
	KeyMap *KeyMap
	// Plan is accepted for API consistency with other flow TUI
	// packages. The wizard itself does not currently read from it.
	Plan *orchestration.Plan
	// DaemonClient is accepted for API consistency. Unused today.
	DaemonClient daemon.Client
	// WorkspaceDir is accepted for API consistency. Unused today.
	WorkspaceDir string
	// ShowForceToggle offers the in-checklist Force toggle. Only hosts that
	// actually act on Model.Force() should set it: the standalone CLI wizard
	// takes force from the --force flag and would render a control that does
	// nothing.
	ShowForceToggle bool
}

// Model is the plan-finish wizard state. It owns the item slice and
// cursor position; on submit it emits embed.DoneMsg with the
// (possibly mutated) item slice as its Result.
type Model struct {
	planName       string
	items          []*Item
	cursor         int
	branchIsMerged bool
	branchExists   bool
	keys           KeyMap
	helpModel      help.Model
	width          int
	showForce      bool
	force          bool
}

// New constructs a Model from the given Config. The cursor is placed
// on the first available item (if any); the help model is built from
// the final keymap so key.Matches lookups and help rendering stay in
// sync.
func New(cfg Config) Model {
	var keys KeyMap
	if cfg.KeyMap != nil {
		keys = *cfg.KeyMap
	} else {
		coreCfg, _ := config.LoadDefault()
		keys = NewKeyMap(coreCfg)
	}

	// Find first available item for initial cursor position.
	cursor := 0
	for i, item := range cfg.Items {
		if item != nil && item.IsAvailable {
			cursor = i
			break
		}
	}

	return Model{
		planName:       cfg.PlanName,
		items:          cfg.Items,
		cursor:         cursor,
		branchIsMerged: cfg.BranchIsMerged,
		branchExists:   cfg.BranchExists,
		keys:           keys,
		helpModel:      help.New(keys),
		showForce:      cfg.ShowForceToggle,
	}
}

// Force reports whether the user asked for destructive git operations to use
// their --force forms. Hosts read this when the wizard submits; it is
// deliberately NOT carried on embed.DoneMsg, whose Result stays []*Item so the
// standalone CLI wizard keeps working unchanged.
//
// Always false unless the host offered the toggle (Config.ShowForceToggle).
func (m Model) Force() bool {
	return m.showForce && m.force
}

// Init is a no-op. The wizard is purely synchronous and owns no
// timers or subscriptions.
func (m Model) Init() tea.Cmd {
	return nil
}

// Close releases resources owned by the wizard. The finish wizard is
// purely stateful and holds no long-lived goroutines, so Close is a
// no-op today. The method is provided for symmetry with other
// embeddable flow TUI packages.
func (m *Model) Close() error {
	return nil
}

// compile-time guard that Model satisfies tea.Model.
var _ tea.Model = Model{}
