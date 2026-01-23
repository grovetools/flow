package cmd

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/tui/components/help"
	"github.com/grovetools/core/tui/keymap"
	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/flow/pkg/orchestration"
)

var ErrTUIQuit = errors.New("quit")

// planInitScreen represents the current screen in the plan init TUI
type planInitScreen int

const (
	MainScreen planInitScreen = iota
	AdvancedScreen
)

// runPlanInitTUI is the main entry point to launch the plan initialization TUI.
// It returns a fully configured PlanInitCmd or an error if the user quits.
func runPlanInitTUI(plansDir string, initialCmd *PlanInitCmd) (*PlanInitCmd, error) {
	// Check for TTY
	// if !isatty.IsTerminal(os.Stdout.Fd()) && !isatty.IsCygwinTerminal(os.Stdout.Fd()) {
	// 	return nil, fmt.Errorf("TUI mode requires an interactive terminal")
	// }

	model := newPlanInitTUIModel(plansDir, initialCmd)
	model.standalone = true // Mark as running standalone
	p := tea.NewProgram(model)

	finalModel, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("error running plan init TUI: %w", err)
	}

	// Check if the model is our planInitTUIModel or if user navigated away
	m, ok := finalModel.(planInitTUIModel)
	if !ok {
		// User navigated to a different view (e.g., plan list)
		return nil, ErrTUIQuit
	}

	if m.quitting {
		return nil, ErrTUIQuit
	}

	// If we get here, the user submitted the form.
	// Convert the final TUI state to a PlanInitCmd struct.
	finalCmd := m.toPlanInitCmd()
	if finalCmd.Dir == "" {
		return nil, ErrTUIQuit // Treat empty name as quitting
	}
	return finalCmd, nil
}

type planInitTUIKeyMap struct {
	keymap.Base
	Toggle         key.Binding
	ToggleAdvanced key.Binding
	NextField      key.Binding
	PrevField      key.Binding
	Submit         key.Binding
	Back           key.Binding
	Escape         key.Binding
	Insert         key.Binding
	Help           key.Binding
}

func newPlanInitTUIKeyMap() planInitTUIKeyMap {
	return planInitTUIKeyMap{
		Base: keymap.NewBase(),
		Toggle: key.NewBinding(
			key.WithKeys(" "),
			key.WithHelp("space", "toggle checkbox"),
		),
		ToggleAdvanced: key.NewBinding(
			key.WithKeys("a"),
			key.WithHelp("a", "toggle advanced"),
		),
		NextField: key.NewBinding(
			key.WithKeys("tab", "j"),
			key.WithHelp("tab/j", "next field"),
		),
		PrevField: key.NewBinding(
			key.WithKeys("shift+tab", "k"),
			key.WithHelp("shift+tab/k", "prev field"),
		),
		Submit: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "submit form"),
		),
		Back: key.NewBinding(
			key.WithKeys("q"),
			key.WithHelp("q", "back to plan list"),
		),
		Escape: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "normal mode"),
		),
		Insert: key.NewBinding(
			key.WithKeys("i"),
			key.WithHelp("i", "insert mode"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "show help"),
		),
	}
}

func (k planInitTUIKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Help, k.Base.Quit}
}

func (k planInitTUIKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{
			key.NewBinding(key.WithKeys(""), key.WithHelp("", "Navigation")),
			k.NextField,
			k.PrevField,
			key.NewBinding(key.WithKeys("j/k, ↑/↓"), key.WithHelp("j/k, ↑/↓", "navigate list items")),
		},
		{
			key.NewBinding(key.WithKeys(""), key.WithHelp("", "Actions")),
			k.Toggle,
			k.Submit,
			k.ToggleAdvanced,
			k.Insert,
			k.Escape,
		},
		{
			key.NewBinding(key.WithKeys(""), key.WithHelp("", "General")),
			k.Help,
			k.Back,
			k.Base.Quit,
		},
	}
}

// getDefaultNoteTargetFile returns the appropriate default note target file for a given recipe.
// It first checks the recipe's DefaultNoteTarget field, then falls back to the first job file alphabetically.
func getDefaultNoteTargetFile(recipeName string) string {
	if recipeName == "none" || recipeName == "" {
		return ""
	}

	_, getRecipeCmd, _ := loadFlowConfigWithDynamicRecipes()
	recipe, err := orchestration.GetRecipe(recipeName, getRecipeCmd)
	if err != nil || recipe == nil {
		return ""
	}

	// 1. Prefer the explicitly defined default target from the recipe.
	if recipe.DefaultNoteTarget != "" {
		return recipe.DefaultNoteTarget
	}

	// 2. Fallback to the first job file alphabetically if no default is specified.
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

// planInitTUIModel represents the state of the new plan creation TUI.
type planInitTUIModel struct {
	plansDirectory    string
	focusIndex        int
	unfocused         bool // Track if we're in unfocused (normal) state
	highestFocusIndex int  // Track user's progress through the form
	err               error
	width, height     int

	standalone bool
	quitting   bool
	// Form inputs
	nameInput   textinput.Model
	recipeList  list.Model
	modelList   list.Model
	openSession bool
	runInit     bool // Run init actions from recipe

	// Screen navigation
	currentScreen planInitScreen

	// Advanced options
	withWorktree        bool
	worktreeInput       textinput.Model
	extractFromInput    textinput.Model
	noteTargetFileInput textinput.Model

	keyMap planInitTUIKeyMap
	help   help.Model
}

// newPlanInitTUIModel creates a new model for the plan initialization form.
func newPlanInitTUIModel(plansDir string, initialCmd *PlanInitCmd) planInitTUIModel {
	m := planInitTUIModel{
		plansDirectory: plansDir,
	}

	m.nameInput = textinput.New()
	m.nameInput.Placeholder = "new-feature-plan"
	m.nameInput.Focus()
	m.nameInput.CharLimit = 156
	m.nameInput.Width = 50

	// Recipes List
	// Load flow config to get dynamic recipe command
	_, getRecipeCmd, _ := loadFlowConfigWithDynamicRecipes() // Ignore error for TUI
	recipes, _ := orchestration.ListAllRecipes(getRecipeCmd) // Ignore error for TUI
	recipeItems := make([]list.Item, len(recipes)+1)
	recipeItems[0] = item("none")
	defaultRecipeIndex := 0
	for i, r := range recipes {
		recipeItems[i+1] = item(r.Name)
		if r.Name == "standard-feature" {
			defaultRecipeIndex = i + 1
		}
	}
	m.recipeList = list.New(recipeItems, itemDelegate{}, 35, 6)
	m.recipeList.Title = ""
	m.recipeList.SetShowTitle(false)
	m.recipeList.SetShowStatusBar(false)
	m.recipeList.SetFilteringEnabled(true)
	m.recipeList.SetShowHelp(false)
	m.recipeList.SetShowPagination(true)
	m.recipeList.FilterInput.Prompt = " "
	m.recipeList.FilterInput.PromptStyle = theme.DefaultTheme.Bold
	m.recipeList.FilterInput.TextStyle = theme.DefaultTheme.Selected
	m.recipeList.Select(defaultRecipeIndex) // Default to standard-feature

	// Models List
	models := getAvailableModels()
	modelItems := make([]list.Item, len(models)+1)
	modelItems[0] = modelItem{Model: Model{ID: "(default)"}}
	defaultModelIndex := 0
	for i, model := range models {
		modelItems[i+1] = modelItem{model}
		if model.ID == "gemini-2.5-pro" {
			defaultModelIndex = i + 1
		}
	}
	m.modelList = list.New(modelItems, itemDelegate{}, 35, 6)
	m.modelList.Title = ""
	m.modelList.SetShowTitle(false)
	m.modelList.SetShowStatusBar(false)
	m.modelList.SetFilteringEnabled(true)
	m.modelList.SetShowHelp(false)
	m.modelList.SetShowPagination(true)
	m.modelList.FilterInput.Prompt = " "
	m.modelList.FilterInput.PromptStyle = theme.DefaultTheme.Bold
	m.modelList.FilterInput.TextStyle = theme.DefaultTheme.Selected
	m.modelList.Select(defaultModelIndex)

	m.worktreeInput = textinput.New()
	m.worktreeInput.Placeholder = "feature/branch-name"
	m.worktreeInput.Width = 41

	m.extractFromInput = textinput.New()
	m.extractFromInput.Placeholder = "/path/to/spec.md"
	m.extractFromInput.Width = 41

	m.noteTargetFileInput = textinput.New()
	// Get the default recipe name to determine the initial note target file
	defaultRecipeName := ""
	if item, ok := m.recipeList.SelectedItem().(item); ok {
		defaultRecipeName = string(item)
	}
	initialNoteTarget := getDefaultNoteTargetFile(defaultRecipeName)
	m.noteTargetFileInput.Placeholder = initialNoteTarget
	m.noteTargetFileInput.SetValue(initialNoteTarget)
	m.noteTargetFileInput.Width = 41

	// Set default values for checkboxes
	m.withWorktree = true
	m.openSession = false // Changed default to false since it's now in advanced

	// Run init actions by default, unless configured otherwise in grove.yml
	m.runInit = true
	if flowCfg, err := loadFlowConfig(); err == nil && flowCfg.RunInitByDefault != nil {
		m.runInit = *flowCfg.RunInitByDefault
	}

	// Start on main screen
	m.currentScreen = MainScreen

	// Initialize keymap and help
	m.keyMap = newPlanInitTUIKeyMap()
	m.help = help.NewBuilder().
		WithKeys(m.keyMap).
		WithTitle("󰠡 Create New Plan - Help").
		Build()

	// Apply pre-populated values from CLI flags (this may override defaults)
	m.prePopulate(initialCmd)

	// Auto-detect if we're in a sub-project worktree and pre-configure the form.
	currentNode, err := workspace.GetProjectByPath(".")
	if err == nil && currentNode.Kind == workspace.KindEcosystemWorktreeSubProjectWorktree {
		parentWorktreeName := filepath.Base(currentNode.ParentEcosystemPath)
		m.worktreeInput.SetValue(parentWorktreeName)
		m.withWorktree = false // Disable auto-creation; we are inheriting.
	}

	return m
}

// prePopulate sets the initial TUI state from provided CLI flags.
func (m *planInitTUIModel) prePopulate(initialCmd *PlanInitCmd) {
	if initialCmd == nil {
		return
	}

	if initialCmd.Dir != "" {
		m.nameInput.SetValue(initialCmd.Dir)
	}

	if initialCmd.Recipe != "" && initialCmd.Recipe != "chat-workflow" {
		for i, listItem := range m.recipeList.Items() {
			if recipeItem, ok := listItem.(item); ok && string(recipeItem) == initialCmd.Recipe {
				m.recipeList.Select(i)
				break
			}
		}
	}

	if initialCmd.Model != "" {
		for i, listItem := range m.modelList.Items() {
			if model, ok := listItem.(modelItem); ok && model.ID == initialCmd.Model {
				m.modelList.Select(i)
				break
			}
		}
	}

	// Pre-populate the extract-from input (from-note) if provided via CLI flag.
	// This is critical for the note promotion flow where --from-note is passed.
	if initialCmd.FromNote != "" {
		m.extractFromInput.SetValue(initialCmd.FromNote)
	}

	// Pre-populate the note target file if provided via CLI flag.
	if initialCmd.NoteTargetFile != "" {
		m.noteTargetFileInput.SetValue(initialCmd.NoteTargetFile)
	}

	// For boolean flags, if they were set on the command line, their value will be passed in.
	// Cobra handles the default values.
	m.openSession = initialCmd.OpenSession

	// If --init flag was explicitly set (true), respect it
	if initialCmd.RunInit {
		m.runInit = true
	}
}

func (m planInitTUIModel) Init() tea.Cmd {
	return tea.Batch(
		tea.ClearScreen,
		textinput.Blink,
	)
}

func (m planInitTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// Forward to help model so it knows the viewport size
		m.help, _ = m.help.Update(msg)

	case tea.KeyMsg:
		// Let help model handle its own keys first
		if m.help.ShowAll {
			var helpCmd tea.Cmd
			m.help, helpCmd = m.help.Update(msg)
			return m, helpCmd
		}

		// Check if we're in a text input field that should capture all keys
		// MainScreen: 0=name
		// AdvancedScreen: 0=runInit checkbox, 1=worktree, 2=extractFrom, 3=noteTargetFile
		inTextInput := false
		if !m.unfocused {
			if m.currentScreen == MainScreen && m.focusIndex == 0 {
				inTextInput = true
			} else if m.currentScreen == AdvancedScreen && m.focusIndex >= 1 {
				inTextInput = true // Advanced fields 1-3 are text inputs (0 is checkbox)
			}
		}
		// Check if we're in a list that needs arrow keys
		// MainScreen: 1=recipe, 2=model
		inList := !m.unfocused && m.currentScreen == MainScreen && (m.focusIndex == 1 || m.focusIndex == 2)

		switch msg.String() {
		case "esc", "escape":
			if m.currentScreen == AdvancedScreen {
				// Return to main screen from advanced
				m.currentScreen = MainScreen
				m.focusIndex = 0
				m.unfocused = false
				return m.updateFocus(), nil
			}
			// ESC unfocuses any focused field (enters normal mode)
			m.unfocused = true
			m.nameInput.Blur()
			m.worktreeInput.Blur()
			m.extractFromInput.Blur()
			m.noteTargetFileInput.Blur()
			return m, nil

		case "i":
			// Insert mode - refocus current field (like vim)
			if m.unfocused {
				m.unfocused = false
				return m.updateFocus(), nil
			}

		case "?":
			// Toggle help - must call Toggle() to trigger setViewportContent()
			m.help.Toggle()
			return m, nil

		case "a":
			// Navigate to advanced screen (only from main screen and when not in text input or in normal mode)
			if m.currentScreen == MainScreen && (!inTextInput || m.unfocused) {
				m.currentScreen = AdvancedScreen
				m.focusIndex = 0 // Reset focus to first field on advanced screen
				m.unfocused = false
				return m.updateFocus(), nil
			}

		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit

		case "b":
			// Back to main screen from advanced (when unfocused)
			if m.currentScreen == AdvancedScreen && m.unfocused {
				m.currentScreen = MainScreen
				m.focusIndex = 0
				return m.updateFocus(), nil
			}

		case "q":
			// Only quit on 'q' if not in text input or if in normal mode
			if !inTextInput || m.unfocused {
				// Go back to the plan list view
				// Note: cwdGitRoot will be determined by the list model's Init function
				listModel := newPlanListTUIModel(m.plansDirectory, "")
				return listModel, loadPlansListCmd(m.plansDirectory, "", false)
			}

		case "tab":
			// Tab always moves to next field
			m.focusIndex++
			maxIndex := m.getMaxFocusIndex()
			if m.focusIndex > maxIndex {
				m.focusIndex = 0
			}
			// Update highest focus index for progress tracking (only on main screen)
			if m.currentScreen == MainScreen && m.focusIndex > m.highestFocusIndex {
				m.highestFocusIndex = m.focusIndex
			}
			return m.updateFocus(), nil

		case "shift+tab":
			// Shift+tab always moves to previous field
			m.focusIndex--
			if m.focusIndex < 0 {
				m.focusIndex = m.getMaxFocusIndex()
			}
			// Update highest focus index for progress tracking (only on main screen)
			if m.currentScreen == MainScreen && m.focusIndex > m.highestFocusIndex {
				m.highestFocusIndex = m.focusIndex
			}
			return m.updateFocus(), nil

		case "h":
			// h moves to previous field when in normal mode
			if m.unfocused && !inTextInput {
				m.focusIndex--
				if m.focusIndex < 0 {
					m.focusIndex = m.getMaxFocusIndex()
				}
				if m.currentScreen == MainScreen && m.focusIndex > m.highestFocusIndex {
					m.highestFocusIndex = m.focusIndex
				}
				return m.updateFocus(), nil
			}

		case "l":
			// l moves to next field when in normal mode
			if m.unfocused && !inTextInput {
				m.focusIndex++
				if m.focusIndex > m.getMaxFocusIndex() {
					m.focusIndex = 0
				}
				if m.currentScreen == MainScreen && m.focusIndex > m.highestFocusIndex {
					m.highestFocusIndex = m.focusIndex
				}
				return m.updateFocus(), nil
			}

		case " ":
			// Space toggles checkboxes
			if m.currentScreen == MainScreen {
				switch m.focusIndex {
				case 3: // Auto-create Worktree
					m.withWorktree = !m.withWorktree
					if m.withWorktree {
						m.worktreeInput.SetValue("")
					}
					return m, nil
				case 4: // Open Session on Create
					m.openSession = !m.openSession
					return m, nil
				}
			} else if m.currentScreen == AdvancedScreen {
				switch m.focusIndex {
				case 0: // Run Init Actions
					m.runInit = !m.runInit
					return m, nil
				}
			}

		case "enter":
			// Enter submits the form or confirms selection
			if inList {
				// For lists, enter confirms selection and moves to next field
				m.unfocused = false
				m.focusIndex++
				if m.focusIndex > m.getMaxFocusIndex() {
					m.focusIndex = 0
				}
				if m.currentScreen == MainScreen && m.focusIndex > m.highestFocusIndex {
					m.highestFocusIndex = m.focusIndex
				}
				return m.updateFocus(), nil
			} else if m.unfocused {
				// If unfocused, enter refocuses current field
				m.unfocused = false
				return m.updateFocus(), nil
			} else {
				// In insert mode, enter submits the form
				// Validate input
				if m.nameInput.Value() == "" {
					m.err = fmt.Errorf("plan name cannot be empty")
					return m, nil
				}
				return m, tea.Quit
			}
		}
	}

	// Delegate to the focused component only if in insert mode
	if !m.unfocused {
		switch m.currentScreen {
		case MainScreen:
			switch m.focusIndex {
			case 0:
				m.nameInput, cmd = m.nameInput.Update(msg)
			case 1: // Recipe list
				prevSelection := m.recipeList.SelectedItem()
				m.recipeList, cmd = m.recipeList.Update(msg)
				newSelection := m.recipeList.SelectedItem()

				if prevSelection != newSelection && newSelection != nil {
					selectedRecipeName := string(newSelection.(item))
					defaultTarget := getDefaultNoteTargetFile(selectedRecipeName)
					m.noteTargetFileInput.SetValue(defaultTarget)

					if defaultTarget != "" {
						m.noteTargetFileInput.Placeholder = defaultTarget
					} else {
						m.noteTargetFileInput.Placeholder = "job-filename.md"
					}
				}
			case 2:
				m.modelList, cmd = m.modelList.Update(msg)
			}
		case AdvancedScreen:
			switch m.focusIndex {
			case 0:
				// Run Init Actions checkbox - no text input
			case 1:
				if !m.withWorktree {
					m.worktreeInput, cmd = m.worktreeInput.Update(msg)
				}
			case 2:
				m.extractFromInput, cmd = m.extractFromInput.Update(msg)
			case 3:
				m.noteTargetFileInput, cmd = m.noteTargetFileInput.Update(msg)
			}
		}
	}

	return m, cmd
}

// getMaxFocusIndex returns the maximum focus index for the current screen
func (m planInitTUIModel) getMaxFocusIndex() int {
	switch m.currentScreen {
	case MainScreen:
		return 4 // 0-4: name, recipe, model, worktree checkbox, session checkbox
	case AdvancedScreen:
		return 3 // 0-3: run-init checkbox, worktree name, extract-from, note-target
	}
	return 4
}

// updateFocus updates focus state for all components
func (m planInitTUIModel) updateFocus() planInitTUIModel {
	// Blur all inputs
	m.nameInput.Blur()
	m.worktreeInput.Blur()
	m.extractFromInput.Blur()
	m.noteTargetFileInput.Blur()

	// Only focus if not in unfocused state
	if !m.unfocused {
		switch m.currentScreen {
		case MainScreen:
			switch m.focusIndex {
			case 0:
				m.nameInput.Focus()
				// cases 1, 2 are lists (recipe, model) - no text focus needed
				// cases 3, 4 are checkboxes - no text focus needed
			}
		case AdvancedScreen:
			switch m.focusIndex {
			case 0:
				// Run Init Actions checkbox - no text focus
			case 1:
				if !m.withWorktree {
					m.worktreeInput.Focus()
				}
			case 2:
				m.extractFromInput.Focus()
			case 3:
				m.noteTargetFileInput.Focus()
			}
		}
	}
	return m
}

func (m planInitTUIModel) View() string {
	if m.help.ShowAll {
		return m.help.View()
	}

	var b strings.Builder

	// Render based on current screen
	switch m.currentScreen {
	case MainScreen:
		b.WriteString(m.renderMainScreen())
	case AdvancedScreen:
		b.WriteString(m.renderAdvancedScreen())
	}

	// Error message
	if m.err != nil {
		b.WriteString("\n")
		b.WriteString(theme.DefaultTheme.Error.Render(m.err.Error()))
		b.WriteString("\n")
	}

	// Footer with mode indicator and default help
	b.WriteString("\n")
	if m.unfocused {
		b.WriteString(theme.DefaultTheme.Muted.Render("[NORMAL]"))
	} else {
		b.WriteString(theme.DefaultTheme.Muted.Render("[INSERT]"))
	}

	// Add default help (? for help, q to quit)
	helpText := m.help.View()
	if helpText != "" {
		b.WriteString(" • ")
		b.WriteString(helpText)
	}

	// Wrap entire view with left margin
	container := lipgloss.NewStyle().PaddingLeft(2)
	return container.Render(b.String())
}

// renderMainScreen renders the main configuration screen
func (m planInitTUIModel) renderMainScreen() string {
	var b strings.Builder

	b.WriteString(theme.DefaultTheme.Header.Bold(true).Render("󰠡 Create New Plan"))
	b.WriteString("\n")

	// Define border styles for 2-column layout
	borderStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultColors.Border).
		Padding(0, 1).
		Width(40) // Narrower for 2-column layout

	borderStyleWide := borderStyle.Copy().
		Width(85) // Full width for single fields

	unfocusedBorderStyle := borderStyle.Copy().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")) // Dimmed gray

	unfocusedBorderStyleWide := borderStyleWide.Copy().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")) // Dimmed gray

	focusedBorderStyle := borderStyle.Copy().
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(theme.DefaultColors.Orange)

	focusedBorderStyleWide := borderStyleWide.Copy().
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(theme.DefaultColors.Orange)

	// renderField helper function with width option
	renderField := func(index int, title string, content string, wide bool) string {
		var fieldBuilder strings.Builder

		// Add checkmark if field has been visited (only for main screen)
		titlePrefix := "  "
		if index <= m.highestFocusIndex {
			titlePrefix = theme.DefaultTheme.Success.Render("* ")
		}

		fieldBuilder.WriteString(titlePrefix + theme.DefaultTheme.Bold.Render(title))
		fieldBuilder.WriteString("\n")
		fieldBuilder.WriteString(content)

		// Apply appropriate border style based on width and focus
		var style lipgloss.Style
		if wide {
			if m.focusIndex == index && !m.unfocused {
				style = focusedBorderStyleWide
			} else if m.focusIndex == index && m.unfocused {
				style = unfocusedBorderStyleWide
			} else {
				style = borderStyleWide
			}
		} else {
			if m.focusIndex == index && !m.unfocused {
				style = focusedBorderStyle
			} else if m.focusIndex == index && m.unfocused {
				style = unfocusedBorderStyle
			} else {
				style = borderStyle
			}
		}

		return style.Render(fieldBuilder.String())
	}

	// Row 1: Plan Name (full width)
	b.WriteString(renderField(0, "Plan Name", m.nameInput.View(), true))
	b.WriteString("\n")

	// Row 2: Recipe | Default Model For API (rendered as lists with visible options)
	recipeField := renderField(1, "Recipe", m.recipeList.View(), false)
	modelField := renderField(2, "Default Model For API", m.modelList.View(), false)
	row2 := lipgloss.JoinHorizontal(lipgloss.Top, recipeField, "  ", modelField)
	b.WriteString(row2)
	b.WriteString("\n")

	// Row 3: Auto-create Worktree | Open Session
	// Check if we're in an inherited context for worktree
	isInheritedContext := false
	currentNode, err := workspace.GetProjectByPath(".")
	if err == nil && currentNode.Kind == workspace.KindEcosystemWorktreeSubProjectWorktree {
		isInheritedContext = true
	}

	autoWorktreeDisplay := "[ ]"
	if m.withWorktree {
		autoWorktreeDisplay = "[x]"
	}
	if isInheritedContext {
		autoWorktreeDisplay = theme.DefaultTheme.Muted.Render("[ ] (Inherited)")
	}

	openSessionDisplay := "[ ]"
	if m.openSession {
		openSessionDisplay = "[x]"
	}

	autoWorktreeField := renderField(3, "Auto-create Worktree", autoWorktreeDisplay, false)
	openSessionField := renderField(4, "Open Session on Create", openSessionDisplay, false)
	row3 := lipgloss.JoinHorizontal(lipgloss.Top, autoWorktreeField, "  ", openSessionField)
	b.WriteString(row3)
	b.WriteString("\n")

	return b.String()
}

// renderAdvancedScreen renders the advanced options screen
func (m planInitTUIModel) renderAdvancedScreen() string {
	var b strings.Builder

	b.WriteString(theme.DefaultTheme.Header.Bold(true).Render("󰠡 Advanced Options"))
	b.WriteString("\n\n")

	// Define border styles for 2-column layout
	borderStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultColors.Border).
		Padding(0, 1).
		Width(40) // Narrower for 2-column layout

	borderStyleWide := borderStyle.Copy().
		Width(85) // Full width for single fields

	unfocusedBorderStyle := borderStyle.Copy().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")) // Dimmed gray

	unfocusedBorderStyleWide := borderStyleWide.Copy().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")) // Dimmed gray

	focusedBorderStyle := borderStyle.Copy().
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(theme.DefaultColors.Orange)

	focusedBorderStyleWide := borderStyleWide.Copy().
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(theme.DefaultColors.Orange)

	// renderField helper function with width option
	renderField := func(index int, title string, content string, wide bool) string {
		var fieldBuilder strings.Builder

		fieldBuilder.WriteString("  " + theme.DefaultTheme.Bold.Render(title))
		fieldBuilder.WriteString("\n")
		fieldBuilder.WriteString(content)

		// Apply appropriate border style based on width and focus
		var style lipgloss.Style
		if wide {
			if m.focusIndex == index && !m.unfocused {
				style = focusedBorderStyleWide
			} else if m.focusIndex == index && m.unfocused {
				style = unfocusedBorderStyleWide
			} else {
				style = borderStyleWide
			}
		} else {
			if m.focusIndex == index && !m.unfocused {
				style = focusedBorderStyle
			} else if m.focusIndex == index && m.unfocused {
				style = unfocusedBorderStyle
			} else {
				style = borderStyle
			}
		}

		return style.Render(fieldBuilder.String())
	}

	// Run Init Actions checkbox
	runInitDisplay := "[ ]"
	if m.runInit {
		runInitDisplay = "[x]"
	}
	b.WriteString(renderField(0, "Run Init Actions", runInitDisplay, true))
	b.WriteString("\n")

	// Check if we're in an inherited context for worktree
	isInheritedContext := false
	currentNode, err := workspace.GetProjectByPath(".")
	if err == nil && currentNode.Kind == workspace.KindEcosystemWorktreeSubProjectWorktree {
		isInheritedContext = true
	}

	// Worktree Name field
	var worktreeDisplay string
	if m.withWorktree {
		worktreeDisplay = theme.DefaultTheme.Muted.Render("(matches plan name)")
	} else if isInheritedContext {
		worktreeDisplay = theme.DefaultTheme.Info.Render(m.worktreeInput.Value())
	} else {
		worktreeDisplay = m.worktreeInput.View()
	}

	b.WriteString(renderField(1, "Worktree Name", worktreeDisplay, true))
	b.WriteString("\n")

	// Extract from File | Note Target File
	extractField := renderField(2, "Extract from File (from-note)", m.extractFromInput.View(), false)
	targetField := renderField(3, "Note Target File", m.noteTargetFileInput.View(), false)
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, extractField, "  ", targetField))
	b.WriteString("\n\n")

	b.WriteString(theme.DefaultTheme.Muted.Render("Press 'Esc' or 'b' to return to main screen"))
	b.WriteString("\n")

	return b.String()
}

// toPlanInitCmd converts the final TUI model state into a PlanInitCmd struct.
func (m planInitTUIModel) toPlanInitCmd() *PlanInitCmd {
	cmd := &PlanInitCmd{
		Dir:            m.nameInput.Value(),
		FromNote:       m.extractFromInput.Value(), // The extract field represents --from-note
		NoteTargetFile: m.noteTargetFileInput.Value(),
		OpenSession:    m.openSession,
		RunInit:        m.runInit,
		Force:          false, // Not settable from TUI
	}

	// Get selected recipe
	if selected := m.recipeList.SelectedItem(); selected != nil {
		if recipeItem, ok := selected.(item); ok && string(recipeItem) != "none" {
			cmd.Recipe = string(recipeItem)
		}
	}

	// Get selected model
	if selected := m.modelList.SelectedItem(); selected != nil {
		if model, ok := selected.(modelItem); ok && model.ID != "(default)" {
			cmd.Model = model.ID
		}
	}

	// Determine worktree value
	if m.withWorktree {
		// This signifies --worktree flag with no value.
		cmd.Worktree = "__AUTO__"
	} else if m.worktreeInput.Value() != "" {
		cmd.Worktree = m.worktreeInput.Value()
	}

	return cmd
}
