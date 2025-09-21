package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

// planInitTUIModel represents the state of the new plan creation TUI.
type planInitTUIModel struct {
	plansDirectory   string
	focusIndex       int
	err              error
	width, height    int

	standalone bool
	quitting   bool
	// Form inputs
	nameInput        textinput.Model
	recipeList       list.Model
	withWorktree     bool
	worktreeInput    textinput.Model
	modelList        list.Model
	containerInput   textinput.Model
	extractFromInput textinput.Model
	openSession      bool
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
	recipes, _ := orchestration.ListAllRecipes() // Ignore error for TUI
	recipeItems := make([]list.Item, len(recipes)+1)
	recipeItems[0] = item("none")
	defaultRecipeIndex := 0
	for i, r := range recipes {
		recipeItems[i+1] = item(r.Name)
		if r.Name == "chat-workflow" {
			defaultRecipeIndex = i + 1
		}
	}
	m.recipeList = list.New(recipeItems, itemDelegate{}, 20, 7)
	m.recipeList.Title = ""
	m.recipeList.SetShowTitle(false)
	m.recipeList.SetShowStatusBar(false)
	m.recipeList.Select(defaultRecipeIndex) // Default to chat-workflow

	// Models List
	models := getAvailableModels()
	modelItems := make([]list.Item, len(models)+1)
	modelItems[0] = modelItem{Model: Model{ID: "(default)"}}
	for i, model := range models {
		modelItems[i+1] = modelItem{model}
	}
	m.modelList = list.New(modelItems, itemDelegate{}, 20, 6)
	m.modelList.Title = ""
	m.modelList.SetShowTitle(false)
	m.modelList.SetShowStatusBar(false)

	m.worktreeInput = textinput.New()
	m.worktreeInput.Placeholder = "feature/branch-name"
	m.worktreeInput.Width = 41

	m.containerInput = textinput.New()
	m.containerInput.Placeholder = "grove-agent-ide"
	m.containerInput.SetValue("grove-agent-ide") // Set default value
	m.containerInput.Width = 41

	m.extractFromInput = textinput.New()
	m.extractFromInput.Placeholder = "/path/to/spec.md"
	m.extractFromInput.Width = 41

	// Set default values for checkboxes
	m.withWorktree = true
	m.openSession = true

	// Apply pre-populated values from CLI flags (this may override defaults)
	m.prePopulate(initialCmd)

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

	// Handle worktree logic. Default is true (auto-mode).
	// Only change if the flag was explicitly provided with a value or set to false.
	// We don't have a direct way to check for flag presence here, so we rely on the value.
	if initialCmd.Worktree != "" && initialCmd.Worktree != "__AUTO__" {
		m.withWorktree = false
		m.worktreeInput.SetValue(initialCmd.Worktree)
	} else if initialCmd.Worktree == "__AUTO__" {
		m.withWorktree = true
	}

	// For boolean flags, if they were set on the command line, their value will be passed in.
	// Cobra handles the default values.
	m.openSession = initialCmd.OpenSession

	if initialCmd.Model != "" {
		for i, listItem := range m.modelList.Items() {
			if model, ok := listItem.(modelItem); ok && model.ID == initialCmd.Model {
				m.modelList.Select(i)
				break
			}
		}
	}
	if initialCmd.Container != "" {
		m.containerInput.SetValue(initialCmd.Container)
	}
	if initialCmd.ExtractAllFrom != "" {
		m.extractFromInput.SetValue(initialCmd.ExtractAllFrom)
	}
}

func (m planInitTUIModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m planInitTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			// Go back to the plan list view
			listModel := newPlanListTUIModel(m.plansDirectory)
			return listModel, loadPlansListCmd(m.plansDirectory)

		case "tab", "down":
			m.focusIndex++
			if m.focusIndex > 7 {
				m.focusIndex = 0
			}
			return m.updateFocus(), nil

		case "shift+tab", "up":
			m.focusIndex--
			if m.focusIndex < 0 {
				m.focusIndex = 7
			}
			return m.updateFocus(), nil

		case " ": // Toggle checkboxes
			switch m.focusIndex {
			case 2: // With Worktree
				m.withWorktree = !m.withWorktree
				if m.withWorktree {
					m.worktreeInput.SetValue("")
				}
			case 6: // Open Session
				m.openSession = !m.openSession
			}

		case "enter":
			if m.focusIndex == 7 { // Submit on the last field
				// Validate input
				if m.nameInput.Value() == "" {
					m.err = fmt.Errorf("plan name cannot be empty")
					return m, nil
				}

				// Build command
				// The final command is built by the caller from the final model state.
				// We just need to exit the TUI program.
				return m, tea.Quit
			}
		}
	}

	// Delegate to the focused component
	switch m.focusIndex {
	case 0:
		m.nameInput, cmd = m.nameInput.Update(msg)
	case 1:
		m.recipeList, cmd = m.recipeList.Update(msg)
	case 3:
		if !m.withWorktree {
			m.worktreeInput, cmd = m.worktreeInput.Update(msg)
		}
	case 4:
		m.modelList, cmd = m.modelList.Update(msg)
	case 5:
		m.containerInput, cmd = m.containerInput.Update(msg)
	case 7:
		m.extractFromInput, cmd = m.extractFromInput.Update(msg)
	}

	return m, cmd
}

// updateFocus updates focus state for all components
func (m planInitTUIModel) updateFocus() planInitTUIModel {
	m.nameInput.Blur()
	m.worktreeInput.Blur()
	m.containerInput.Blur()
	m.extractFromInput.Blur()

	switch m.focusIndex {
	case 0:
		m.nameInput.Focus()
	case 3:
		if !m.withWorktree {
			m.worktreeInput.Focus()
		}
	case 5:
		m.containerInput.Focus()
	case 7:
		m.extractFromInput.Focus()
	}
	return m
}

func (m planInitTUIModel) View() string {
	var b strings.Builder

	b.WriteString(lipgloss.NewStyle().Bold(true).Render("✨ Create New Plan"))
	b.WriteString("\n\n")

	// Render fields
	b.WriteString(renderTextInput("Plan Name:", m.nameInput, m.focusIndex == 0))
	b.WriteString(renderList("Recipe:", m.recipeList, m.focusIndex == 1))
	b.WriteString(renderCheckbox("Auto-create Worktree:", m.withWorktree, m.focusIndex == 2))

	if m.withWorktree {
		b.WriteString(renderTextInputDisabled("Worktree Name:", "(matches plan name)"))
	} else {
		b.WriteString(renderTextInput("Worktree Name:", m.worktreeInput, m.focusIndex == 3))
	}

	b.WriteString(renderList("Default Model:", m.modelList, m.focusIndex == 4))
	b.WriteString(renderTextInput("Target Container:", m.containerInput, m.focusIndex == 5))
	b.WriteString(renderCheckbox("Open Session on Create:", m.openSession, m.focusIndex == 6))
	b.WriteString(renderTextInput("Extract from File:", m.extractFromInput, m.focusIndex == 7))

	// Submit button
	submitStyle := lipgloss.NewStyle().Padding(1, 2)
	if m.focusIndex == 7 {
		submitStyle = submitStyle.Background(lipgloss.Color("205")).Foreground(lipgloss.Color("255"))
	}
	b.WriteString("\n")
	b.WriteString(submitStyle.Render("[ Submit ]"))
	b.WriteString("\n")

	// Error message
	if m.err != nil {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render(m.err.Error()))
	}

	b.WriteString("\n")
	b.WriteString("tab: next field • space: toggle • enter: submit • q: back")

	return b.String()
}

// Helper rendering functions for consistency
func renderTextInput(label string, input textinput.Model, focused bool) string {
	style := lipgloss.NewStyle().Padding(0, 1)
	if focused {
		style = style.Foreground(lipgloss.Color("205"))
	}
	return style.Render(fmt.Sprintf("%-25s %s", label, input.View())) + "\n"
}

func renderTextInputDisabled(label, value string) string {
	style := lipgloss.NewStyle().Padding(0, 1).Foreground(lipgloss.Color("240"))
	return style.Render(fmt.Sprintf("%-25s %s", label, value)) + "\n"
}

func renderList(label string, l list.Model, focused bool) string {
	style := lipgloss.NewStyle().Padding(0, 1)
	if focused {
		style = style.Foreground(lipgloss.Color("205"))
	}
	
	// Handle different item types
	var displayValue string
	if selected := l.SelectedItem(); selected != nil {
		switch v := selected.(type) {
		case item:
			displayValue = v.FilterValue()
		case modelItem:
			displayValue = v.FilterValue()
		default:
			displayValue = "unknown"
		}
	} else {
		displayValue = "none"
	}
	
	return style.Render(fmt.Sprintf("%-25s [%s]", label, displayValue)) + "\n"
}

func renderCheckbox(label string, checked bool, focused bool) string {
	box := "[ ]"
	if checked {
		box = "[x]"
	}
	style := lipgloss.NewStyle().Padding(0, 1)
	if focused {
		style = style.Foreground(lipgloss.Color("205"))
	}
	return style.Render(fmt.Sprintf("%-25s %s", label, box)) + "\n"
}

// toPlanInitCmd converts the final TUI model state into a PlanInitCmd struct.
func (m planInitTUIModel) toPlanInitCmd() *PlanInitCmd {
	cmd := &PlanInitCmd{
		Dir:            m.nameInput.Value(),
		Container:      m.containerInput.Value(),
		ExtractAllFrom: m.extractFromInput.Value(),
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