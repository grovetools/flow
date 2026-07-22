// Package planinit is the embeddable "create a new plan" wizard. It renders
// a multi-field form (plan name, recipe, model, worktree options, note
// target) and emits embed.DoneMsg on submit carrying a *Request that
// captures everything needed to execute `flow plan init`. Hosts — the
// flow CLI wrapper and the flow meta-panel at flow/pkg/tui/view — are
// responsible for turning that Request into actual plan creation. The
// wizard itself performs no heavy I/O in its Update loop; it is purely
// a form collector.
package planinit

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/tui/components/help"
	"github.com/grovetools/core/tui/theme"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/plancreate"
)

// Screen represents the current screen in the plan init wizard.
type Screen int

const (
	MainScreen Screen = iota
	AdvancedScreen
	ReviewScreen
)

// Request captures every form field the wizard collects. Hosts map
// this onto their plan-creation code path (cmd.PlanInitCmd for the
// CLI wrapper, or a subprocess invocation for the in-TUI host).
type Request struct {
	Dir               string
	Force             bool
	Model             string
	Worktree          string
	ExtractAllFrom    string
	OpenSession       bool
	EnvProfile        string
	Recipe            string
	RecipeVars        []string
	RecipeCmd         string
	SiblingWorkspaces []string
	NoteRef           string
	FromNote          string
	NoteTargetFile    string
	RunInit           bool
	Anchor            string
	Layout            string
}

// Config carries the dependencies the init wizard needs. Hosts build
// a Config and pass it to New so the wizard doesn't reach out to any
// package-level globals or CLI flag vars.
type Config struct {
	// PlansDir is the directory new plans will be created under.
	// Captured by the wizard but not acted on until a host executes
	// the returned Request.
	PlansDir string
	// Initial, if non-nil, pre-populates form fields (used by the
	// CLI wrapper when flags are passed alongside --tui).
	Initial *Request
	// InitialExact makes false boolean values in Initial authoritative. The
	// coordinator uses it when rebuilding a failed request; CLI flag defaults
	// leave it false so configured defaults are not accidentally overwritten.
	InitialExact bool
	// GetRecipeCmd is the dynamic recipe command from the flow
	// config. Empty is acceptable — the wizard will only show
	// built-in recipes in that case.
	GetRecipeCmd string
	// RunInitByDefault controls the default state of the "Run Init
	// Actions" checkbox. Callers that don't care can leave it
	// false; the CLI wrapper reads flow config to populate it.
	RunInitByDefault bool
	// KeyMap, if non-nil, overrides the default wizard keymap.
	KeyMap *KeyMap
	// DaemonClient is accepted for API consistency with other flow
	// TUI packages. Unused today.
	DaemonClient daemon.Client
	// WorkspaceDir is accepted for API consistency. Unused today.
	WorkspaceDir string
}

// Model is the plan-init wizard state. It owns all form fields and
// the derived values captured on submit. On submit it emits
// embed.DoneMsg with a *Request as Result (or nil on cancel).
type Model struct {
	plansDirectory    string
	workspaceDir      string
	getRecipeCmd      string
	focusIndex        int
	unfocused         bool
	highestFocusIndex int
	err               error
	width, height     int

	// Form inputs
	nameInput   textinput.Model
	recipeList  list.Model
	modelList   list.Model
	openSession bool
	runInit     bool

	// Screen navigation
	currentScreen Screen
	validating    bool
	validation    *plancreate.ValidationReport
	manifest      *plancreate.MutationManifest

	// Advanced options
	withWorktree        bool
	worktreeInput       textinput.Model
	extractFromInput    textinput.Model
	noteTargetFileInput textinput.Model
	anchorInput         textinput.Model
	layoutInput         textinput.Model

	keys KeyMap
	help help.Model
}

// IsTextEntryActive reports whether one of the wizard's text input
// fields currently has focus. Hosts embedding the wizard as a tab
// consult this before intercepting single-character navigation keys
// (e.g. the flow meta-panel's 1-5 tab jumps) so digits the user is
// typing into a field aren't swallowed. Returns false in the
// "unfocused" navigation mode (after ESC).
func (m Model) IsTextEntryActive() bool {
	if m.unfocused {
		return false
	}
	return m.nameInput.Focused() ||
		m.worktreeInput.Focused() ||
		m.extractFromInput.Focused() ||
		m.noteTargetFileInput.Focused() ||
		m.anchorInput.Focused() ||
		m.layoutInput.Focused()
}

// New constructs a Model from the given Config. Form defaults mirror
// the legacy flow/cmd/plan_init_tui.go behavior.
func New(cfg Config) Model {
	workspaceDir := cfg.WorkspaceDir
	if workspaceDir == "" {
		workspaceDir, _ = os.Getwd()
	}
	m := Model{
		plansDirectory: cfg.PlansDir,
		workspaceDir:   workspaceDir,
		getRecipeCmd:   cfg.GetRecipeCmd,
		runInit:        cfg.RunInitByDefault,
		// Start in navigation mode so switching to the Add Plan tab
		// doesn't drop the user straight into text input. They
		// press "i" to enter insert mode when ready.
		unfocused: true,
	}

	m.nameInput = textinput.New()
	m.nameInput.Placeholder = "new-feature-plan"
	m.nameInput.CharLimit = 156
	m.nameInput.Width = 50

	// Recipes list.
	recipes, _ := orchestration.ListAllRecipes(cfg.GetRecipeCmd)
	recipeItems := make([]list.Item, len(recipes)+1)
	recipeItems[0] = item("none")
	defaultRecipeIndex := 0
	for i, r := range recipes {
		recipeItems[i+1] = item(r.Name)
		if r.Name == "standard-feature" {
			defaultRecipeIndex = i + 1
		}
	}
	m.recipeList = newList(recipeItems, 35, 6)
	m.recipeList.Select(defaultRecipeIndex)

	// Models list.
	models := getAvailableModels()
	modelItems := make([]list.Item, len(models)+1)
	modelItems[0] = modelItem{modelInfo{ID: "(default)"}}
	defaultModelIndex := 0
	for i, model := range models {
		modelItems[i+1] = modelItem{model}
		if model.ID == "gemini-2.5-pro" {
			defaultModelIndex = i + 1
		}
	}
	m.modelList = newList(modelItems, 35, 6)
	m.modelList.Select(defaultModelIndex)

	m.worktreeInput = textinput.New()
	m.worktreeInput.Placeholder = "feature/branch-name"
	m.worktreeInput.Width = 41

	m.extractFromInput = textinput.New()
	m.extractFromInput.Placeholder = "/path/to/spec.md"
	m.extractFromInput.Width = 41

	m.noteTargetFileInput = textinput.New()
	defaultRecipeName := ""
	if it, ok := m.recipeList.SelectedItem().(item); ok {
		defaultRecipeName = string(it)
	}
	initialNoteTarget := getDefaultNoteTargetFile(defaultRecipeName, cfg.GetRecipeCmd)
	m.noteTargetFileInput.Placeholder = initialNoteTarget
	m.noteTargetFileInput.SetValue(initialNoteTarget)
	m.noteTargetFileInput.Width = 41

	m.anchorInput = textinput.New()
	m.anchorInput.Placeholder = "repo name (auto-inferred when empty)"
	m.anchorInput.Width = 41
	m.layoutInput = textinput.New()
	m.layoutInput.Placeholder = "xdg or legacy (default when empty)"
	m.layoutInput.Width = 41

	// Defaults
	m.withWorktree = true
	m.openSession = false

	m.currentScreen = MainScreen

	// Keymap + help.
	if cfg.KeyMap != nil {
		m.keys = *cfg.KeyMap
	} else {
		coreCfg, _ := config.LoadDefault()
		m.keys = NewKeyMap(coreCfg)
	}
	m.help = help.NewBuilder().
		WithKeys(m.keys).
		WithTitle("󰠡 Create New Plan - Help").
		Build()

	// Apply pre-populated values (may override defaults).
	m.prePopulate(cfg.Initial, cfg.InitialExact)

	// Auto-detect sub-project worktree context and inherit the
	// parent ecosystem worktree name.
	currentNode, err := workspace.GetProjectByPath(".")
	if err == nil && currentNode.Kind == workspace.KindEcosystemWorktreeSubProjectWorktree {
		parentWorktreeName := filepath.Base(currentNode.ParentEcosystemPath)
		m.worktreeInput.SetValue(parentWorktreeName)
		m.withWorktree = false
	}

	return m
}

// prePopulate sets the initial wizard state from a partially-filled
// Request (e.g. from CLI flags).
func (m *Model) prePopulate(initial *Request, exact bool) {
	if initial == nil {
		return
	}

	if initial.Dir != "" {
		m.nameInput.SetValue(initial.Dir)
	}

	if initial.Recipe != "" && initial.Recipe != "chat-workflow" {
		for i, listItem := range m.recipeList.Items() {
			if recipeItem, ok := listItem.(item); ok && string(recipeItem) == initial.Recipe {
				m.recipeList.Select(i)
				break
			}
		}
	}

	if initial.Model != "" {
		for i, listItem := range m.modelList.Items() {
			if mi, ok := listItem.(modelItem); ok && mi.ID == initial.Model {
				m.modelList.Select(i)
				break
			}
		}
	}

	switch initial.Worktree {
	case "__AUTO__":
		m.withWorktree = true
	case "":
		if exact {
			m.withWorktree = false
		}
	default:
		m.withWorktree = false
		m.worktreeInput.SetValue(initial.Worktree)
	}

	if initial.FromNote != "" {
		m.extractFromInput.SetValue(initial.FromNote)
	}
	if initial.NoteTargetFile != "" {
		m.noteTargetFileInput.SetValue(initial.NoteTargetFile)
	}
	m.anchorInput.SetValue(initial.Anchor)
	m.layoutInput.SetValue(initial.Layout)

	m.openSession = initial.OpenSession
	if initial.RunInit || exact {
		m.runInit = initial.RunInit
	}
}

// Init returns the initial tea.Cmd for the wizard. Dropped the
// prior tea.ClearScreen because, when embedded as a peer tab in
// the flow meta-panel, it caused a visible flash + full host
// redraw on every tab switch. Bubbletea's own render setup handles
// the standalone CLI path fine without an explicit clear.
func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

// Close is a no-op; the wizard holds no long-lived resources.
func (m *Model) Close() error {
	return nil
}

// toRequest converts the final wizard state into a *Request.
func (m Model) toRequest() *Request {
	req := &Request{
		Dir:            m.nameInput.Value(),
		FromNote:       m.extractFromInput.Value(),
		NoteTargetFile: m.noteTargetFileInput.Value(),
		OpenSession:    m.openSession,
		RunInit:        m.runInit,
		Anchor:         m.anchorInput.Value(),
		Layout:         m.layoutInput.Value(),
	}
	if selected := m.recipeList.SelectedItem(); selected != nil {
		if recipeItem, ok := selected.(item); ok && string(recipeItem) != "none" {
			req.Recipe = string(recipeItem)
		}
	}
	if selected := m.modelList.SelectedItem(); selected != nil {
		if mi, ok := selected.(modelItem); ok && mi.ID != "(default)" {
			req.Model = mi.ID
		}
	}
	if m.withWorktree {
		req.Worktree = "__AUTO__"
	} else if m.worktreeInput.Value() != "" {
		req.Worktree = m.worktreeInput.Value()
	}
	return req
}

// getMaxFocusIndex returns the maximum focus index for the current screen.
func (m Model) getMaxFocusIndex() int {
	switch m.currentScreen {
	case MainScreen:
		return 4
	case AdvancedScreen:
		return 5
	case ReviewScreen:
		return 0
	}
	return 4
}

// updateFocus updates focus state for all components based on
// the current focusIndex and unfocused flag.
func (m Model) updateFocus() Model {
	m.nameInput.Blur()
	m.worktreeInput.Blur()
	m.extractFromInput.Blur()
	m.noteTargetFileInput.Blur()
	m.anchorInput.Blur()
	m.layoutInput.Blur()

	if !m.unfocused {
		switch m.currentScreen {
		case MainScreen:
			if m.focusIndex == 0 {
				m.nameInput.Focus()
			}
		case AdvancedScreen:
			switch m.focusIndex {
			case 1:
				if !m.withWorktree {
					m.worktreeInput.Focus()
				}
			case 2:
				m.extractFromInput.Focus()
			case 3:
				m.noteTargetFileInput.Focus()
			case 4:
				m.anchorInput.Focus()
			case 5:
				m.layoutInput.Focus()
			}
		}
	}
	return m
}

// getDefaultNoteTargetFile returns the appropriate default note
// target file for a given recipe. It consults the recipe's
// DefaultNoteTarget field, falling back to the first job file
// alphabetically.
func getDefaultNoteTargetFile(recipeName, getRecipeCmd string) string {
	if recipeName == "none" || recipeName == "" {
		return ""
	}
	recipe, err := orchestration.GetRecipe(recipeName, getRecipeCmd)
	if err != nil || recipe == nil {
		return ""
	}
	if recipe.DefaultNoteTarget != "" {
		return recipe.DefaultNoteTarget
	}
	if len(recipe.Jobs) == 0 {
		return ""
	}
	var jobFiles []string
	for filename := range recipe.Jobs {
		jobFiles = append(jobFiles, filename)
	}
	sort.Strings(jobFiles)
	if len(jobFiles) > 0 {
		return jobFiles[0]
	}
	return ""
}

// newList builds a list.Model with the settings used by every list
// in the init wizard.
func newList(items []list.Item, width, height int) list.Model {
	l := list.New(items, itemDelegate{}, width, height)
	l.Title = ""
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	l.SetShowPagination(true)
	l.FilterInput.Prompt = " "
	l.FilterInput.PromptStyle = theme.DefaultTheme.Bold
	l.FilterInput.TextStyle = theme.DefaultTheme.Selected
	return l
}

// compile-time guard that Model satisfies tea.Model.
var _ tea.Model = Model{}
