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
	"github.com/grovetools/flow/pkg/orchestration"
	skillservice "github.com/grovetools/skills/pkg/service"
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
	err        error

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
		plan:      cfg.Plan,
		keys:      keys,
		unfocused: false, // Start in insert mode (focused)
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
		if node, err := workspace.GetProjectByPath(cfg.Plan.Directory); err == nil {
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

	// 1. Title Input (textinput)
	m.titleInput = textinput.New()
	m.titleInput.Placeholder = "New job title here"
	m.titleInput.Focus()
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

// Init returns the initial tea.Cmd for the wizard. It clears the
// screen and blinks the focused text input.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		tea.ClearScreen,
		textinput.Blink,
	)
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
