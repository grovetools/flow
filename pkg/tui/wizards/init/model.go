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
	"io"
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
	"github.com/grovetools/core/tui/keymap"
	"github.com/grovetools/core/tui/theme"
	"github.com/sirupsen/logrus"

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
	// DefaultModel is flow.oneshot_model from the target ecosystem config.
	// The default picker entry leaves Request.Model empty so plan init resolves
	// that configured value normally.
	DefaultModel string
	// AnchorRepositories, when non-nil, supplies the canonical repositories in
	// the target ecosystem. A nil slice asks New to discover them.
	AnchorRepositories []string
	// KeyMap, if non-nil, overrides the default wizard keymap.
	KeyMap *KeyMap
	// DaemonClient is accepted for API consistency with other flow
	// TUI packages. Unused today.
	DaemonClient daemon.Client
	// WorkspaceDir is accepted for API consistency. Unused today.
	WorkspaceDir string
	// InvocationDir is the directory the user was actually browsing when the
	// wizard was opened, BEFORE any hoisting to the ecosystem root. It decides
	// anchor behavior: the Anchor Repository picker is shown only when this is
	// an ecosystem node itself, and a member repo of a directory ecosystem
	// (grove.toml workspaces, no git root) becomes the implicit anchor.
	// Empty falls back to WorkspaceDir / cwd.
	InvocationDir string
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
	nameInput  textinput.Model
	recipeList list.Model
	modelList  list.Model
	anchorList list.Model
	// showAnchor gates the Anchor Repository picker: it is only offered when
	// the wizard was invoked at an ecosystem node (root or worktree
	// container). autoAnchor carries the implicit anchor used instead when
	// the wizard was opened from a member repo of a directory ecosystem.
	showAnchor bool
	autoAnchor string
	// openSession is retained only to preserve an explicit CLI
	// --open-session value through the standalone --tui round trip. The
	// embedded wizard no longer offers it: its host already enters the created
	// plan, so spawning a second tmux status session is redundant.
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
	layoutInput         textinput.Model

	keys KeyMap
	help help.Model
	// whichKey is the shared chord/which-key mixin: it arms the single-member
	// t… namespace declared by KeyMap.Namespaces() and renders its popup.
	whichKey keymap.WhichKeyHost
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
		m.layoutInput.Focused()
}

// New constructs a Model from the given Config. Form defaults mirror
// the legacy flow/cmd/plan_init_tui.go behavior.
func New(cfg Config) Model {
	workspaceDir := ResolveTargetWorkspace(cfg.WorkspaceDir)
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
	for i, r := range recipes {
		recipeItems[i+1] = item(r.Name)
	}
	m.recipeList = newList(recipeItems, 35, 6)
	m.recipeList.Select(0)

	// Models list.
	models := getAvailableModels()
	modelItems := make([]list.Item, len(models)+1)
	modelItems[0] = defaultModelItem(cfg.DefaultModel)
	for i, model := range models {
		modelItems[i+1] = modelItem{model}
	}
	m.modelList = newList(modelItems, 35, 6)
	m.modelList.Select(0)

	// Anchor context is decided by where the wizard was actually invoked,
	// before any hoisting to the ecosystem root: the picker is only shown at
	// an ecosystem node, and a member repo of a directory ecosystem (no git
	// root to check a full-ecosystem worktree out from) anchors implicitly
	// to itself.
	invocationDir := cfg.InvocationDir
	if invocationDir == "" {
		invocationDir = cfg.WorkspaceDir
	}
	if invocationDir == "" {
		invocationDir, _ = os.Getwd()
	}
	m.showAnchor, m.autoAnchor = anchorContext(invocationDir)
	if cfg.AnchorRepositories != nil {
		// An explicitly supplied roster always shows the picker.
		m.showAnchor = true
	}

	anchorRepos := cfg.AnchorRepositories
	if anchorRepos == nil && m.showAnchor {
		anchorRepos = discoverAnchorRepositories(workspaceDir)
	}
	anchorRepos = sortedUnique(anchorRepos)
	anchorItems := make([]list.Item, len(anchorRepos)+1)
	anchorItems[0] = item("(auto-infer)")
	for i, repo := range anchorRepos {
		anchorItems[i+1] = item(repo)
	}
	m.anchorList = newList(anchorItems, 35, 6)
	m.anchorList.Select(0)

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

	m.layoutInput = textinput.New()
	m.layoutInput.Placeholder = "xdg or legacy (default when empty)"
	m.layoutInput.Width = 41

	// Defaults
	m.withWorktree = true
	m.openSession = false

	m.currentScreen = MainScreen

	// Keymap + help.
	coreCfg, _ := config.LoadDefault()
	if cfg.KeyMap != nil {
		m.keys = *cfg.KeyMap
	} else {
		m.keys = NewKeyMap(coreCfg)
	}
	m.whichKey = keymap.NewWhichKeyHost(coreCfg, m.keys.Namespaces()...)
	m.help = help.NewBuilder().
		WithKeys(m.keys).
		WithTitle("󰠡 Create New Plan - Help").
		Build()

	// Apply pre-populated values (may override defaults).
	m.prePopulate(cfg.Initial, cfg.InitialExact)

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
	if initial.Anchor != "" {
		if !m.showAnchor {
			// The picker is hidden; honor the explicit anchor verbatim so a
			// rebuilt/CLI-seeded request survives the round trip.
			m.autoAnchor = initial.Anchor
		}
		for i, listItem := range m.anchorList.Items() {
			if anchorItem, ok := listItem.(item); ok && string(anchorItem) == initial.Anchor {
				m.anchorList.Select(i)
				break
			}
		}
	}
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
		Layout:         m.layoutInput.Value(),
	}
	if selected := m.recipeList.SelectedItem(); selected != nil {
		if recipeItem, ok := selected.(item); ok && string(recipeItem) != "none" {
			req.Recipe = string(recipeItem)
		}
	}
	if selected := m.modelList.SelectedItem(); selected != nil {
		if mi, ok := selected.(modelItem); ok && !mi.IsDefault {
			req.Model = mi.ID
		}
	}
	if m.showAnchor {
		if selected := m.anchorList.SelectedItem(); selected != nil {
			if anchor, ok := selected.(item); ok && string(anchor) != "(auto-infer)" {
				req.Anchor = string(anchor)
			}
		}
	} else if m.autoAnchor != "" {
		req.Anchor = m.autoAnchor
	}
	if m.withWorktree {
		req.Worktree = "__AUTO__"
	} else if m.worktreeInput.Value() != "" {
		req.Worktree = m.worktreeInput.Value()
	}
	return req
}

// stepFocus moves focus by delta with wrap-around, skipping fields that are
// not rendered (the anchor picker at main-screen index 3 when showAnchor is
// false), and maintains highestFocusIndex bookkeeping.
func (m Model) stepFocus(delta int) Model {
	maxIndex := m.getMaxFocusIndex()
	for {
		m.focusIndex += delta
		if m.focusIndex > maxIndex {
			m.focusIndex = 0
		} else if m.focusIndex < 0 {
			m.focusIndex = maxIndex
		}
		if m.currentScreen == MainScreen && m.focusIndex == 3 && !m.showAnchor {
			continue
		}
		break
	}
	if m.currentScreen == MainScreen && m.focusIndex > m.highestFocusIndex {
		m.highestFocusIndex = m.focusIndex
	}
	return m
}

// getMaxFocusIndex returns the maximum focus index for the current screen.
func (m Model) getMaxFocusIndex() int {
	switch m.currentScreen {
	case MainScreen:
		return 4
	case AdvancedScreen:
		return 4
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

// ResolveTargetWorkspace returns the canonical ecosystem root for plan
// creation. Plans launched from any ecosystem worktree are deliberately rooted
// at the main ecosystem rather than the caller's transient checkout.
func ResolveTargetWorkspace(dir string) string {
	if dir == "" {
		dir, _ = os.Getwd()
	}
	abs, err := filepath.Abs(dir)
	if err == nil {
		dir = abs
	}
	node, err := workspace.GetProjectByPath(dir)
	if err != nil || node == nil {
		return dir
	}
	if node.RootEcosystemPath != "" {
		return node.RootEcosystemPath
	}
	if node.Kind == workspace.KindEcosystemRoot {
		return node.Path
	}
	return dir
}

// anchorContext decides anchor behavior from the directory the wizard was
// actually invoked in (before hoisting to the ecosystem root): the Anchor
// Repository picker is offered only at an ecosystem node itself, and a member
// repo of a directory ecosystem — one whose root has no git repo to check a
// full-ecosystem worktree out from — anchors implicitly to itself.
func anchorContext(invocationDir string) (showPicker bool, autoAnchor string) {
	node, err := workspace.GetProjectByPath(invocationDir)
	if err != nil || node == nil {
		return false, ""
	}
	if node.IsEcosystem() {
		return true, ""
	}
	if node.RootEcosystemPath != "" {
		if _, statErr := os.Stat(filepath.Join(node.RootEcosystemPath, ".git")); statErr != nil {
			return false, filepath.Base(node.Path)
		}
	}
	return false, ""
}

func discoverAnchorRepositories(ecosystemRoot string) []string {
	node, err := workspace.GetProjectByPath(ecosystemRoot)
	if err != nil || node == nil || (node.Kind != workspace.KindEcosystemRoot && node.RootEcosystemPath == "") {
		return nil
	}
	root := ResolveTargetWorkspace(ecosystemRoot)
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	result, err := workspace.NewDiscoveryService(logger).DiscoverAll()
	if err != nil {
		return nil
	}
	provider := workspace.NewProvider(result)
	var names []string
	for _, candidate := range provider.All() {
		if candidate != nil && provider.FindSubProjectByName(candidate.Name, root) != nil {
			names = append(names, candidate.Name)
		}
	}
	return sortedUnique(names)
}

// compile-time guard that Model satisfies tea.Model.
var _ tea.Model = Model{}
