package add

import (
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/tui/components/help"
	"github.com/grovetools/core/tui/theme"
	skillservice "github.com/grovetools/skills/pkg/service"

	"github.com/grovetools/flow/pkg/orchestration"
)

// Config carries the dependencies the add-job wizard needs. Hosts
// (the CLI wrapper and the flow meta-panel) construct a Config and
// pass it to New so the wizard doesn't reach out to globals.
type Config struct {
	// Plan is the plan the new job will be added to. Required.
	Plan *orchestration.Plan
	// InitialDeps is a list of job filenames to pre-select in the
	// dependency picker (used by the CLI `-d` flag).
	InitialDeps []string
	// KeyMap, if non-nil, overrides the default wizard keymap.
	// Leave nil to use NewKeyMap(config.LoadDefault()).
	KeyMap *KeyMap
	// SkillService is used to populate the skills picker for agent
	// job types. If nil, New attempts to construct one from the
	// default core config; if that fails, the skills picker will
	// show only the built-in "none" entry.
	SkillService *skillservice.Service
	// WorkspaceNode scopes skill discovery to a specific workspace.
	// If nil, New attempts to derive it from Plan.Directory.
	WorkspaceNode *workspace.WorkspaceNode
	// DaemonClient is accepted for API consistency with other flow
	// TUI packages (status, browser, view). The wizard itself does
	// not currently call the daemon.
	DaemonClient daemon.Client
	// WorkspaceDir is accepted for API consistency. Unused today.
	WorkspaceDir string
}

// Model is the add-job wizard state. It owns all form fields and the
// derived values captured on submit. Hosts embed Model as an opaque
// tea.Model; the Result payload it emits via embed.DoneMsg is a
// *orchestration.Job (or nil on cancel).
type Model struct {
	plan       *orchestration.Plan
	keys       KeyMap
	helpModel  help.Model
	focusIndex int
	unfocused  bool // Track if we're in unfocused state

	// Form inputs
	titleInput   textinput.Model
	jobTypeList  list.Model
	depList      list.Model
	selectedDeps map[string]bool // Track selected dependencies
	templateList list.Model
	skillList    list.Model
	promptInput  textarea.Model

	// Slot 2 mode: skills for agent types, templates for chat/oneshot
	slot2IsSkills bool

	// Skills service for loading skill metadata
	skillService  *skillservice.Service
	workspaceNode *workspace.WorkspaceNode

	// All available templates (for filtering)
	allTemplates []*orchestration.JobTemplate

	// Fields to store the final job data
	jobTitle         string
	jobType          string
	jobDependencies  []string
	jobTemplate      string
	jobSkill         string
	jobSkillSequence []string
	jobPrompt        string

	// Claw (channel + autonomous) toggle
	clawEnabled bool

	// Terminal dimensions for width-clamping the View output.
	width, height int
}

// slotID identifies a wizard form slot independently of its position
// in the focus order, so the focus/nav/update logic can key on a
// stable identity rather than a raw index.
type slotID int

const (
	slotTitle slotID = iota
	slotJobType
	slotTemplateOrSkill
	slotDeps
	slotPrompt
)

// slotKind classifies a slot's input widget for focus/keystroke
// routing. slotText slots capture all keys (title input, prompt
// textarea); slotList slots are navigable pickers.
type slotKind int

const (
	slotText slotKind = iota
	slotList
)

// formSlot is one entry in the wizard's ordered slot table. kind and
// visible are functions of the live Model because a slot's widget
// type and applicability can depend on prior selections (Phase 4b);
// in Phase 4a they are all static. The function fields take *Model
// rather than closing over a pointer at construction because
// bubbletea copies the Model by value on every Update — a captured
// pointer would read stale state.
type formSlot struct {
	id      slotID
	kind    func(m *Model) slotKind
	visible func(m *Model) bool
}

// staticText/staticList are the constant slot-kind functions used by
// the current (Phase 4a) slots. alwaysVisible marks a slot as always
// part of the focus order.
func staticText(*Model) slotKind { return slotText }
func staticList(*Model) slotKind { return slotList }
func alwaysVisible(*Model) bool  { return true }

// addFormSlots is the wizard's ordered slot table. The order defines
// the tab/nav cycle. Kinds are static and every slot is visible, so
// Phase 4a behaves identically to the previous hardcoded index logic.
var addFormSlots = []formSlot{
	{id: slotTitle, kind: staticText, visible: alwaysVisible},
	{id: slotJobType, kind: staticList, visible: alwaysVisible},
	{id: slotTemplateOrSkill, kind: staticList, visible: alwaysVisible},
	{id: slotDeps, kind: staticList, visible: alwaysVisible},
	{id: slotPrompt, kind: staticText, visible: alwaysVisible},
}

// currentSlot returns the slot the focus index currently points at.
func (m *Model) currentSlot() formSlot {
	return addFormSlots[m.focusIndex]
}

// currentSlotKind returns the widget kind of the currently focused
// slot.
func (m *Model) currentSlotKind() slotKind {
	return addFormSlots[m.focusIndex].kind(m)
}

// nextVisibleSlot returns the index of the next visible slot after
// from, wrapping around. With every slot visible this is just
// (from+1) mod len.
func (m *Model) nextVisibleSlot(from int) int {
	n := len(addFormSlots)
	for i := 1; i <= n; i++ {
		idx := (from + i) % n
		if addFormSlots[idx].visible(m) {
			return idx
		}
	}
	return from
}

// prevVisibleSlot returns the index of the previous visible slot
// before from, wrapping around.
func (m *Model) prevVisibleSlot(from int) int {
	n := len(addFormSlots)
	for i := 1; i <= n; i++ {
		idx := ((from-i)%n + n) % n
		if addFormSlots[idx].visible(m) {
			return idx
		}
	}
	return from
}

// firstVisibleSlot returns the index of the first visible slot.
func (m *Model) firstVisibleSlot() int {
	for i := range addFormSlots {
		if addFormSlots[i].visible(m) {
			return i
		}
	}
	return 0
}

// lastVisibleSlot returns the index of the last visible slot.
func (m *Model) lastVisibleSlot() int {
	for i := len(addFormSlots) - 1; i >= 0; i-- {
		if addFormSlots[i].visible(m) {
			return i
		}
	}
	return len(addFormSlots) - 1
}

// activeList returns a pointer to the list widget backing the
// currently focused slot, honoring the slot-2 skills/templates mode,
// or nil when the focused slot is not a list. It is the single source
// of truth for "which list is the user interacting with," used by
// filter detection.
func (m *Model) activeList() *list.Model {
	switch m.currentSlot().id {
	case slotJobType:
		return &m.jobTypeList
	case slotTemplateOrSkill:
		if m.slot2IsSkills {
			return &m.skillList
		}
		return &m.templateList
	case slotDeps:
		return &m.depList
	}
	return nil
}

// IsTextEntryActive reports whether the wizard currently has a
// text input, textarea, or list filter input focused. Hosts that
// want to intercept single-character keys (e.g. the flow
// meta-panel's 1-4 tab jumps) consult this so they don't swallow
// characters the user is typing into a form field. Returns false
// when the wizard is in its "unfocused" navigation mode (after
// pressing ESC).
func (m Model) IsTextEntryActive() bool {
	if m.unfocused {
		return false
	}
	if m.currentSlotKind() == slotText {
		return true
	}
	return m.isListFiltering()
}

// isListFiltering reports whether the currently focused list
// component is in active filter mode (the user is typing into the
// list's built-in search input).
func (m Model) isListFiltering() bool {
	if l := m.activeList(); l != nil {
		return l.FilterState() == list.Filtering
	}
	return false
}

// New constructs a Model from the given Config. It initializes all
// form fields and attempts to derive SkillService / WorkspaceNode
// from the default core config when Config leaves them nil.
func New(cfg Config) Model {
	_ = theme.DefaultTheme // ensure theme package is referenced for style

	// Keymap (with overrides)
	var keys KeyMap
	if cfg.KeyMap != nil {
		keys = *cfg.KeyMap
	} else {
		coreCfg, _ := config.LoadDefault()
		keys = NewKeyMap(coreCfg)
	}

	m := Model{
		plan: cfg.Plan,
		keys: keys,
		// Start in unfocused (navigation) mode so switching to the
		// Add Job tab doesn't drop the user straight into text input
		// — they press "i" to enter insert mode when ready.
		unfocused: true,
		helpModel: help.NewBuilder().
			WithKeys(keys).
			WithTitle("󰝒 Add New Job - Help").
			Build(),
	}

	// Resolve skill service + workspace node, either from config or
	// by falling back to defaults derived from the plan directory.
	m.skillService = cfg.SkillService
	m.workspaceNode = cfg.WorkspaceNode
	if m.workspaceNode == nil && cfg.Plan != nil {
		if node, err := orchestration.ResolveProjectForSessionNaming(cfg.Plan.Directory); err == nil {
			m.workspaceNode = node
		}
	}
	if m.skillService == nil {
		if coreCfg, err := config.LoadDefault(); err == nil && coreCfg != nil {
			if svc, err := skillservice.New(nil, coreCfg, nil); err == nil {
				m.skillService = svc
			}
		}
	}

	// 1. Title Input (textinput) — deliberately not focused; the
	// wizard starts in navigation mode (see unfocused above).
	m.titleInput = textinput.New()
	m.titleInput.Placeholder = "New job title here"
	m.titleInput.CharLimit = 156
	m.titleInput.Width = 50

	// 2. Job Type Input (list)
	jobTypes := []list.Item{
		item("interactive_agent"),
		item("isolated_agent"),
		item("headless_agent"),
		item("oneshot"),
		item("shell"),
		item("chat"),
		item("file"),
	}
	m.jobTypeList = list.New(jobTypes, itemDelegate{}, 20, 7)
	m.jobTypeList.Title = ""
	m.jobTypeList.SetShowTitle(false)
	m.jobTypeList.SetShowStatusBar(false)
	m.jobTypeList.SetFilteringEnabled(true)
	m.jobTypeList.SetShowHelp(false)
	m.jobTypeList.SetShowPagination(true)
	m.jobTypeList.FilterInput.Prompt = " "
	m.jobTypeList.FilterInput.PromptStyle = theme.DefaultTheme.Bold
	m.jobTypeList.FilterInput.TextStyle = theme.DefaultTheme.Selected

	// 3. Dependencies Input (List with checkboxes)
	m.selectedDeps = make(map[string]bool)

	// Create a set for efficient lookup of initial dependencies.
	initialDepsSet := make(map[string]bool)
	for _, dep := range cfg.InitialDeps {
		initialDepsSet[dep] = true
	}

	var depItems []list.Item
	if cfg.Plan != nil {
		depItems = make([]list.Item, 0, len(cfg.Plan.Jobs))
		for _, job := range cfg.Plan.Jobs {
			depItems = append(depItems, dependencyItem{job: job})
			// Pre-select the job if its filename is in the initial dependencies set.
			if initialDepsSet[job.Filename] {
				m.selectedDeps[job.ID] = true
			}
		}
	}
	m.depList = list.New(depItems, dependencyDelegate{selectedDeps: &m.selectedDeps}, 45, 7)
	m.depList.Title = ""
	m.depList.SetShowTitle(false)
	m.depList.SetShowStatusBar(false)
	m.depList.SetFilteringEnabled(true)
	m.depList.SetShowHelp(false)
	m.depList.SetShowPagination(true)
	m.depList.FilterInput.Prompt = " "
	m.depList.FilterInput.PromptStyle = theme.DefaultTheme.Bold
	m.depList.FilterInput.TextStyle = theme.DefaultTheme.Selected

	// 4. Template Input (list)
	templateManager := orchestration.NewTemplateManager()
	templates, _ := templateManager.ListTemplates() // Ignore error for now
	m.allTemplates = templates

	// Initially show all templates (no job type selected yet)
	m.templateList = m.buildTemplateList("")

	// 4b. Skills list (shown for agent types instead of templates)
	m.skillList = buildSkillList(m.skillService, m.workspaceNode)
	m.slot2IsSkills = true // Default job type is interactive_agent, which uses skills

	// 5. Prompt Input (textarea)
	m.promptInput = textarea.New()
	m.promptInput.Placeholder = "Enter prompt here..."
	m.promptInput.SetWidth(41)
	m.promptInput.SetHeight(7)

	return m
}

// Init returns the initial tea.Cmd for the wizard. It used to call
// tea.ClearScreen here, but that wiped the host's whole terminal on
// every tab-switch in the groveterm embedded view and caused a
// visible flash/sluggish redraw. Bubbletea already clears its own
// render region when the program starts, so the standalone CLI
// path doesn't actually need the explicit clear either.
func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

// Close releases resources owned by the wizard. The add wizard is
// purely stateful and holds no long-lived goroutines, so Close is a
// no-op today. The method is provided for symmetry with other
// embeddable flow TUI packages.
func (m *Model) Close() error {
	return nil
}

// compile-time guard that Model satisfies tea.Model.
var _ tea.Model = Model{}
