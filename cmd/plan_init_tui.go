package cmd

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattsolo1/grove-core/pkg/workspace"
	"github.com/mattsolo1/grove-core/tui/components/help"
	"github.com/mattsolo1/grove-core/tui/keymap"
	"github.com/mattsolo1/grove-core/tui/theme"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
)

var ErrTUIQuit = errors.New("quit")

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
	}
}

func (k planInitTUIKeyMap) ShortHelp() []key.Binding {
	return k.Base.ShortHelp()
}

func (k planInitTUIKeyMap) FullHelp() [][]key.Binding {
	return k.Base.FullHelp()
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
	nameInput           textinput.Model
	recipeList          list.Model
	modelList           list.Model
	openSession         bool

	// Advanced options
	showAdvanced        bool
	withWorktree        bool
	worktreeInput       textinput.Model
	extractFromInput    textinput.Model
	noteTargetFileInput textinput.Model

	keyMap              planInitTUIKeyMap
	help                help.Model
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
	m.recipeList = list.New(recipeItems, itemDelegate{}, 20, 7)
	m.recipeList.Title = ""
	m.recipeList.SetShowTitle(false)
	m.recipeList.SetShowStatusBar(false)
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
	m.modelList = list.New(modelItems, itemDelegate{}, 20, 6)
	m.modelList.Title = ""
	m.modelList.SetShowTitle(false)
	m.modelList.SetShowStatusBar(false)
	m.modelList.Select(defaultModelIndex)

	m.worktreeInput = textinput.New()
	m.worktreeInput.Placeholder = "feature/branch-name"
	m.worktreeInput.Width = 41

	m.extractFromInput = textinput.New()
	m.extractFromInput.Placeholder = "/path/to/spec.md"
	m.extractFromInput.Width = 41

	m.noteTargetFileInput = textinput.New()
	m.noteTargetFileInput.Placeholder = "02-spec.md"
	m.noteTargetFileInput.SetValue("02-spec.md")
	m.noteTargetFileInput.Width = 41

	// Set default values for checkboxes
	m.withWorktree = true
	m.openSession = false // Changed default to false since it's now in advanced

	// Advanced section starts hidden by default
	m.showAdvanced = false

	// Initialize keymap and help
	m.keyMap = newPlanInitTUIKeyMap()
	m.help = help.New(newPlanInitTUIKeyMap())

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

	// For boolean flags, if they were set on the command line, their value will be passed in.
	// Cobra handles the default values.
	m.openSession = initialCmd.OpenSession

	// Advanced section fields - auto-show if any are populated
	hasAdvancedOptions := false

	// Handle worktree logic. Default is true (auto-mode).
	// Only change if the flag was explicitly provided with a value or set to false.
	// We don't have a direct way to check for flag presence here, so we rely on the value.
	if initialCmd.Worktree != "" && initialCmd.Worktree != "__AUTO__" {
		m.withWorktree = false
		m.worktreeInput.SetValue(initialCmd.Worktree)
		hasAdvancedOptions = true
	} else if initialCmd.Worktree == "__AUTO__" {
		m.withWorktree = true
		hasAdvancedOptions = true
	}

	// Populate extract from field - FromNote takes precedence over ExtractAllFrom
	if initialCmd.FromNote != "" {
		m.extractFromInput.SetValue(initialCmd.FromNote)
		hasAdvancedOptions = true
	} else if initialCmd.ExtractAllFrom != "" {
		m.extractFromInput.SetValue(initialCmd.ExtractAllFrom)
		hasAdvancedOptions = true
	}

	if initialCmd.NoteTargetFile != "" {
		m.noteTargetFileInput.SetValue(initialCmd.NoteTargetFile)
		hasAdvancedOptions = true
	}

	// Show advanced section if any advanced options were pre-populated
	if hasAdvancedOptions {
		m.showAdvanced = true
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

	case tea.KeyMsg:
		// Let help model handle its own keys first
		if m.help.ShowAll {
			var helpCmd tea.Cmd
			m.help, helpCmd = m.help.Update(msg)
			return m, helpCmd
		}

		// Check if we're in a text input field that should capture all keys
		// Basic field: 0=name, Advanced fields: 5=worktree, 6=extractFrom, 7=noteTargetFile
		inTextInput := !m.unfocused && (m.focusIndex == 0 || m.focusIndex == 5 || m.focusIndex == 6 || m.focusIndex == 7)
		// Check if we're in a list that needs arrow keys
		// Basic lists: 1=recipe, 2=model
		inList := !m.unfocused && (m.focusIndex == 1 || m.focusIndex == 2)

		switch msg.String() {
		case "esc", "escape":
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
			// Toggle help
			m.help.ShowAll = !m.help.ShowAll
			return m, nil

		case "a":
			// Toggle advanced options (only when not in text input or in normal mode)
			if !inTextInput || m.unfocused {
				m.showAdvanced = !m.showAdvanced
				return m, nil
			}

		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit

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
			maxIndex := 4 // Basic fields: 0-4
			if m.showAdvanced {
				maxIndex = 7 // Include advanced fields: 5-7
			}
			if m.focusIndex > maxIndex {
				m.focusIndex = 0
			}
			// Update highest focus index for progress tracking
			if m.focusIndex > m.highestFocusIndex {
				m.highestFocusIndex = m.focusIndex
			}
			return m.updateFocus(), nil

		case "shift+tab":
			// Shift+tab always moves to previous field
			m.focusIndex--
			if m.focusIndex < 0 {
				maxIndex := 4 // Basic fields: 0-4
				if m.showAdvanced {
					maxIndex = 7 // Include advanced fields: 5-7
				}
				m.focusIndex = maxIndex
			}
			// Update highest focus index for progress tracking
			if m.focusIndex > m.highestFocusIndex {
				m.highestFocusIndex = m.focusIndex
			}
			return m.updateFocus(), nil

		case "j":
			// j moves to next field only when not in text input or when unfocused
			if !inTextInput || m.unfocused {
				m.focusIndex++
				maxIndex := 4 // Basic fields: 0-4
				if m.showAdvanced {
					maxIndex = 7 // Include advanced fields: 5-7
				}
				if m.focusIndex > maxIndex {
					m.focusIndex = 0
				}
				// Update highest focus index for progress tracking
				if m.focusIndex > m.highestFocusIndex {
					m.highestFocusIndex = m.focusIndex
				}
				return m.updateFocus(), nil
			}

		case "k":
			// k moves to previous field only when not in text input or when unfocused
			if !inTextInput || m.unfocused {
				m.focusIndex--
				if m.focusIndex < 0 {
					maxIndex := 4 // Basic fields: 0-4
					if m.showAdvanced {
						maxIndex = 7 // Include advanced fields: 5-7
					}
					m.focusIndex = maxIndex
				}
				// Update highest focus index for progress tracking
				if m.focusIndex > m.highestFocusIndex {
					m.highestFocusIndex = m.focusIndex
				}
				return m.updateFocus(), nil
			}

		case "h":
			// h moves to previous field when in normal mode
			if m.unfocused && !inTextInput {
				m.focusIndex--
				if m.focusIndex < 0 {
					maxIndex := 4 // Basic fields: 0-4
					if m.showAdvanced {
						maxIndex = 7 // Include advanced fields: 5-7
					}
					m.focusIndex = maxIndex
				}
				if m.focusIndex > m.highestFocusIndex {
					m.highestFocusIndex = m.focusIndex
				}
				return m.updateFocus(), nil
			}

		case "l":
			// l moves to next field when in normal mode
			if m.unfocused && !inTextInput {
				m.focusIndex++
				maxIndex := 4 // Basic fields: 0-4
				if m.showAdvanced {
					maxIndex = 7 // Include advanced fields: 5-7
				}
				if m.focusIndex > maxIndex {
					m.focusIndex = 0
				}
				if m.focusIndex > m.highestFocusIndex {
					m.highestFocusIndex = m.focusIndex
				}
				return m.updateFocus(), nil
			}

		case " ":
			// Space toggles checkboxes
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

		case "enter":
			// Enter submits the form or confirms selection
			if inList {
				// For lists, enter confirms selection and moves to next field
				m.unfocused = false
				m.focusIndex++
				if m.focusIndex > 7 {
					m.focusIndex = 0
				}
				if m.focusIndex > m.highestFocusIndex {
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
		switch m.focusIndex {
		case 0:
			m.nameInput, cmd = m.nameInput.Update(msg)
		case 1:
			m.recipeList, cmd = m.recipeList.Update(msg)
		case 2:
			m.modelList, cmd = m.modelList.Update(msg)
		case 5:
			if !m.withWorktree {
				m.worktreeInput, cmd = m.worktreeInput.Update(msg)
			}
		case 6:
			m.extractFromInput, cmd = m.extractFromInput.Update(msg)
		case 7:
			m.noteTargetFileInput, cmd = m.noteTargetFileInput.Update(msg)
		}
	}

	return m, cmd
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
		switch m.focusIndex {
		case 0:
			m.nameInput.Focus()
		case 5:
			if !m.withWorktree {
				m.worktreeInput.Focus()
			}
		case 6:
			m.extractFromInput.Focus()
		case 7:
			m.noteTargetFileInput.Focus()
		}
	}
	return m
}

func (m planInitTUIModel) View() string {
	if m.help.ShowAll {
		return lipgloss.JoinVertical(lipgloss.Left,
			theme.DefaultTheme.Header.Render("✨ Create New Plan - Help"),
			m.help.View(),
		)
	}

	var b strings.Builder

	// Header with progress indicator
	maxSteps := 5 // Basic fields: 0-4
	if m.showAdvanced {
		maxSteps = 8 // Include advanced fields: 5-7
	}
	progressText := fmt.Sprintf("[Step %d of %d]", m.highestFocusIndex+1, maxSteps)
	header := fmt.Sprintf("🌲 ✨ Create New Plan                          %s", progressText)
	b.WriteString(theme.DefaultTheme.Header.Bold(true).Render(header))
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

		// Add checkmark if field has been visited
		titlePrefix := "  "
		if index <= m.highestFocusIndex {
			titlePrefix = theme.DefaultTheme.Success.Render("✓ ")
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

	// Plan Configuration Section
	b.WriteString(theme.DefaultTheme.Info.Render("Plan Configuration"))
	b.WriteString("\n")
	b.WriteString(strings.Repeat("─", 90))
	b.WriteString("\n")

	// Row 1: Plan Name (full width)
	b.WriteString(renderField(0, "Plan Name", m.nameInput.View(), true))
	b.WriteString("\n")

	// Row 2: Recipe | Default Model
	recipeDisplay := "none"
	if selected := m.recipeList.SelectedItem(); selected != nil {
		if recipeItem, ok := selected.(item); ok {
			recipeDisplay = fmt.Sprintf("[%s]", string(recipeItem))
		}
	}

	modelDisplay := "(default)"
	if selected := m.modelList.SelectedItem(); selected != nil {
		if modelItem, ok := selected.(modelItem); ok {
			modelDisplay = fmt.Sprintf("[%s]", modelItem.ID)
		}
	}

	recipeField := renderField(1, "Recipe", recipeDisplay, false)
	modelField := renderField(2, "Default Model", modelDisplay, false)
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

	// Advanced Options Section (conditionally shown)
	if m.showAdvanced {
		b.WriteString("\n")
		b.WriteString(theme.DefaultTheme.Info.Render("Advanced Options"))
		b.WriteString("\n")
		b.WriteString(strings.Repeat("─", 90))
		b.WriteString("\n")

		// Worktree Name (only shown if not auto-creating)
		var worktreeDisplay string
		if m.withWorktree {
			worktreeDisplay = theme.DefaultTheme.Muted.Render("(matches plan name)")
		} else if isInheritedContext {
			worktreeDisplay = theme.DefaultTheme.Info.Render(m.worktreeInput.Value())
		} else {
			worktreeDisplay = m.worktreeInput.View()
		}

		b.WriteString(renderField(5, "Worktree Name", worktreeDisplay, true))
		b.WriteString("\n")

		// Note integration options
		extractField := renderField(6, "Extract from File (from-note)", m.extractFromInput.View(), false)
		targetFileField := renderField(7, "Note Target File", m.noteTargetFileInput.View(), false)
		noteRow := lipgloss.JoinHorizontal(lipgloss.Top, extractField, "  ", targetFileField)
		b.WriteString(noteRow)
		b.WriteString("\n")
	} else {
		// Show hint to toggle advanced options
		b.WriteString("\n")
		b.WriteString(theme.DefaultTheme.Muted.Render("Press 'a' to toggle advanced options (worktree name, note integration)"))
		b.WriteString("\n")
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

	return b.String()
}

// toPlanInitCmd converts the final TUI model state into a PlanInitCmd struct.
func (m planInitTUIModel) toPlanInitCmd() *PlanInitCmd {
	cmd := &PlanInitCmd{
		Dir:            m.nameInput.Value(),
		FromNote:       m.extractFromInput.Value(), // The extract field represents --from-note
		NoteTargetFile: m.noteTargetFileInput.Value(),
		OpenSession:    m.openSession,
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

