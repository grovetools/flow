package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattsolo1/grove-core/tui/components/help"
	"github.com/mattsolo1/grove-core/tui/keymap"
	"github.com/mattsolo1/grove-core/tui/theme"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
)

// Custom keymap extending the base
type addTuiKeyMap struct {
	keymap.Base
	Next     key.Binding
	Prev     key.Binding
	Submit   key.Binding
	Toggle   key.Binding
}

var addKeys = addTuiKeyMap{
	Base: keymap.NewBase(),
	Next: key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "next field"),
	),
	Prev: key.NewBinding(
		key.WithKeys("shift+tab"),
		key.WithHelp("shift+tab", "prev field"),
	),
	Submit: key.NewBinding(
		key.WithKeys("ctrl+s"),
		key.WithHelp("ctrl+s", "submit"),
	),
	Toggle: key.NewBinding(
		key.WithKeys(" "),
		key.WithHelp("space", "toggle"),
	),
}

// ShortHelp returns key bindings to show in the mini help view
func (k addTuiKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Next, k.Toggle, k.Submit, k.Help, k.Quit}
}

// FullHelp returns keybindings for the expanded help view
func (k addTuiKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{
			key.NewBinding(key.WithHelp("", "Navigation")),
			k.Next,
			k.Prev,
			key.NewBinding(key.WithHelp("↑/↓, j/k", "Navigate lists")),
			key.NewBinding(key.WithHelp("/", "Search lists")),
			key.NewBinding(key.WithHelp("esc", "Clear search")),
		},
		{
			key.NewBinding(key.WithHelp("", "Actions")),
			k.Toggle,
			key.NewBinding(key.WithHelp("enter", "Confirm & Next")),
			k.Submit,
			k.Help,
			k.Quit,
		},
	}
}

type tuiModel struct {
	plan       *orchestration.Plan // To access existing jobs and defaults
	keys       addTuiKeyMap
	helpModel  help.Model
	focusIndex int
	quitting   bool
	err        error

	// Form inputs
	titleInput    textinput.Model
	jobTypeList   list.Model
	depList       list.Model
	selectedDeps  map[string]bool  // Track selected dependencies
	templateList  list.Model
	worktreeInput textinput.Model
	modelList     list.Model
	promptInput   textarea.Model

	// Fields to store the final job data
	jobTitle        string
	jobType         string
	jobDependencies []string
	jobTemplate     string
	jobWorktree     string
	jobModel        string
	jobPrompt       string
}

type item string

func (i item) FilterValue() string { return string(i) }

// modelItem represents a model in the list
type modelItem struct {
	Model
}

func (m modelItem) FilterValue() string { return m.ID }
func (m modelItem) Title() string       { return m.ID }
func (m modelItem) Description() string { return fmt.Sprintf("%s - %s", m.Provider, m.Note) }

// dependencyItem represents a job that can be selected as a dependency
type dependencyItem struct {
	job      *orchestration.Job
	selected bool
}

func (d dependencyItem) FilterValue() string { return d.job.Filename + " " + d.job.Title }
func (d dependencyItem) Title() string       { return d.job.Filename }
func (d dependencyItem) Description() string { return d.job.Title }

type itemDelegate struct{}

func (d itemDelegate) Height() int                               { return 1 }
func (d itemDelegate) Spacing() int                              { return 0 }
func (d itemDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }
func (d itemDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	var str string
	cursor := " "
	if index == m.Index() {
		cursor = ">"
	}

	switch item := listItem.(type) {
	case modelItem:
		// Just show the model ID, no description
		str = fmt.Sprintf("%s %s", cursor, item.ID)
	case item:
		// Regular items
		str = fmt.Sprintf("%s %s", cursor, item)
	default:
		return
	}

	if index == m.Index() {
		fmt.Fprint(w, theme.DefaultTheme.Selected.Render(str))
	} else {
		fmt.Fprint(w, str)
	}
}

// dependencyDelegate handles rendering for dependency items with checkboxes
type dependencyDelegate struct {
	selectedDeps *map[string]bool
}

func (d dependencyDelegate) Height() int                               { return 1 }
func (d dependencyDelegate) Spacing() int                              { return 0 }
func (d dependencyDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }
func (d dependencyDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	depItem, ok := listItem.(dependencyItem)
	if !ok {
		return
	}

	var str string
	cursor := " "
	if index == m.Index() {
		cursor = ">"
	}

	checkbox := "[ ]"
	if (*d.selectedDeps)[depItem.job.ID] {
		checkbox = "[✓]"
	}

	// Format the display text
	displayText := fmt.Sprintf("%s (%s)", depItem.job.Filename, depItem.job.Title)
	
	// Truncate to prevent wrapping (account for cursor and checkbox = 6 chars)
	maxWidth := 35 // Conservative width for 45-char list minus cursor/checkbox
	if len(displayText) > maxWidth {
		displayText = displayText[:maxWidth-3] + "..."
	}
	
	str = fmt.Sprintf("%s %s %s", cursor, checkbox, displayText)

	if index == m.Index() {
		fmt.Fprint(w, theme.DefaultTheme.Selected.Render(str))
	} else if (*d.selectedDeps)[depItem.job.ID] {
		fmt.Fprint(w, theme.DefaultTheme.Bold.Render(str))
	} else {
		fmt.Fprint(w, theme.DefaultTheme.Muted.Render(str))
	}
}

func initialModel(plan *orchestration.Plan) tuiModel {
	m := tuiModel{
		plan: plan,
		keys: addKeys,
		helpModel: help.NewBuilder().
			WithKeys(addKeys).
			WithTitle("✨ Add New Job - Help").
			Build(),
	}

	// 1. Title Input (textinput)
	m.titleInput = textinput.New()
	m.titleInput.Placeholder = "Implement User Authentication"
	m.titleInput.Focus()
	m.titleInput.CharLimit = 156
	m.titleInput.Width = 50

	// 2. Job Type Input (list)
	jobTypes := []list.Item{
		item("agent"),
		item("oneshot"),
		item("interactive_agent"),
		item("shell"),
		item("chat"),
	}
	m.jobTypeList = list.New(jobTypes, itemDelegate{}, 20, 5)
	m.jobTypeList.Title = ""
	m.jobTypeList.SetShowTitle(false)
	m.jobTypeList.SetShowStatusBar(false)
	m.jobTypeList.SetFilteringEnabled(true)
	m.jobTypeList.SetShowHelp(false)
	m.jobTypeList.SetShowPagination(true)
	m.jobTypeList.FilterInput.Prompt = "🔍 "
	m.jobTypeList.FilterInput.PromptStyle = theme.DefaultTheme.Bold
	m.jobTypeList.FilterInput.TextStyle = theme.DefaultTheme.Selected

	// 3. Dependencies Input (List with checkboxes)
	m.selectedDeps = make(map[string]bool)
	depItems := make([]list.Item, 0, len(plan.Jobs))
	for _, job := range plan.Jobs {
		depItems = append(depItems, dependencyItem{job: job})
	}
	m.depList = list.New(depItems, dependencyDelegate{selectedDeps: &m.selectedDeps}, 45, 7)
	m.depList.Title = ""
	m.depList.SetShowTitle(false)
	m.depList.SetShowStatusBar(false)
	m.depList.SetFilteringEnabled(true)
	m.depList.SetShowHelp(false)
	m.depList.SetShowPagination(true)
	m.depList.FilterInput.Prompt = "🔍 "
	m.depList.FilterInput.PromptStyle = theme.DefaultTheme.Bold
	m.depList.FilterInput.TextStyle = theme.DefaultTheme.Selected

	// 4. Template Input (list)
	templateManager := orchestration.NewTemplateManager()
	templates, _ := templateManager.ListTemplates() // Ignore error for now
	templateItems := make([]list.Item, len(templates)+1)
	templateItems[0] = item("none") // Add a 'none' option
	for i, t := range templates {
		templateItems[i+1] = item(t.Name)
	}
	m.templateList = list.New(templateItems, itemDelegate{}, 20, 7)
	m.templateList.Title = ""
	m.templateList.SetShowTitle(false)
	m.templateList.SetShowStatusBar(false)
	m.templateList.SetFilteringEnabled(true)
	m.templateList.SetShowHelp(false)
	m.templateList.SetShowPagination(true)
	m.templateList.FilterInput.Prompt = "🔍 "
	m.templateList.FilterInput.PromptStyle = theme.DefaultTheme.Bold
	m.templateList.FilterInput.TextStyle = theme.DefaultTheme.Selected

	// 5. Worktree Input (textinput with default)
	m.worktreeInput = textinput.New()
	m.worktreeInput.Placeholder = "feature-branch"
	m.worktreeInput.Width = 41
	if plan.Config != nil && plan.Config.Worktree != "" {
		m.worktreeInput.SetValue(plan.Config.Worktree)
	}

	// 6. Model Input (list)
	models := getAvailableModels()
	modelItems := make([]list.Item, len(models))
	defaultIndex := 0
	for i, model := range models {
		modelItems[i] = modelItem{model}
		// Set default selection based on plan config
		if plan.Config != nil && plan.Config.Model == model.ID {
			defaultIndex = i
		}
	}
	m.modelList = list.New(modelItems, itemDelegate{}, 20, 6)
	m.modelList.Title = ""
	m.modelList.SetShowTitle(false)
	m.modelList.SetShowStatusBar(false)
	m.modelList.SetFilteringEnabled(true)
	m.modelList.SetShowHelp(false)
	m.modelList.SetShowPagination(true)
	m.modelList.FilterInput.Prompt = "🔍 "
	m.modelList.FilterInput.PromptStyle = theme.DefaultTheme.Bold
	m.modelList.FilterInput.TextStyle = theme.DefaultTheme.Selected
	m.modelList.Select(defaultIndex)

	// 7. Prompt Input (textarea)
	m.promptInput = textarea.New()
	m.promptInput.Placeholder = "Enter prompt here..."
	m.promptInput.SetWidth(41)
	m.promptInput.SetHeight(5)

	return m
}

func (m tuiModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	// Handle keyboard input
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// If help is visible, it consumes all key presses
		if m.helpModel.ShowAll {
			m.helpModel.Toggle()
			return m, nil
		}

		// Check if we're in a text input field that should capture all keys
		inTextInput := m.focusIndex == 0 || m.focusIndex == 2 || m.focusIndex == 6
		// Check if we're in a list that needs arrow keys
		inList := m.focusIndex == 1 || m.focusIndex == 3 || m.focusIndex == 4 || m.focusIndex == 5

		switch msg.String() {
		case "?":
			m.helpModel.Toggle()
			return m, nil

		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit

		case "q":
			// Only quit on 'q' if not in text input
			if !inTextInput {
				m.quitting = true
				return m, tea.Quit
			}

		case "tab":
			// Tab always moves to next field
			m.focusIndex++
			if m.focusIndex > 6 {
				m.focusIndex = 0
			}
			return m.updateFocus(), nil

		case "down":
			// Down arrow only navigates fields when not in a list
			if !inList {
				m.focusIndex++
				if m.focusIndex > 6 {
					m.focusIndex = 0
				}
				return m.updateFocus(), nil
			}

		case "shift+tab":
			// Shift+tab always moves to previous field
			m.focusIndex--
			if m.focusIndex < 0 {
				m.focusIndex = 6
			}
			return m.updateFocus(), nil

		case "up":
			// Up arrow only navigates fields when not in a list
			if !inList {
				m.focusIndex--
				if m.focusIndex < 0 {
					m.focusIndex = 6
				}
				return m.updateFocus(), nil
			}

		case "enter":
			// Special handling for certain fields
			if m.focusIndex == 6 {
				// On the last field (prompt), enter confirms the form
				// Extract values from all inputs
				m.extractValues()
				m.quitting = true
				return m, tea.Quit
			} else if inList {
				// For lists, enter confirms selection and moves to next field
				m.focusIndex++
				if m.focusIndex > 6 {
					m.focusIndex = 0
				}
				return m.updateFocus(), nil
			}
		}
	}

	// Delegate to the focused component
	switch m.focusIndex {
	case 0: // Title input
		m.titleInput, cmd = m.titleInput.Update(msg)
	case 1: // Job type list
		m.jobTypeList, cmd = m.jobTypeList.Update(msg)
	case 2: // Worktree input
		m.worktreeInput, cmd = m.worktreeInput.Update(msg)
	case 3: // Template list
		m.templateList, cmd = m.templateList.Update(msg)
	case 4: // Dependency list
		// Handle space key for toggling selection
		if key, ok := msg.(tea.KeyMsg); ok && key.String() == " " {
			if selectedItem := m.depList.SelectedItem(); selectedItem != nil {
				if depItem, ok := selectedItem.(dependencyItem); ok {
					// Toggle selection
					m.selectedDeps[depItem.job.ID] = !m.selectedDeps[depItem.job.ID]
				}
			}
		} else {
			m.depList, cmd = m.depList.Update(msg)
		}
	case 5: // Model list
		m.modelList, cmd = m.modelList.Update(msg)
	case 6: // Prompt textarea
		m.promptInput, cmd = m.promptInput.Update(msg)
	}

	return m, cmd
}

// updateFocus updates focus state for all components
func (m tuiModel) updateFocus() tuiModel {
	// Blur all inputs
	m.titleInput.Blur()
	m.worktreeInput.Blur()
	m.promptInput.Blur()

	// Focus the current one
	switch m.focusIndex {
	case 0:
		m.titleInput.Focus()
	case 2:
		m.worktreeInput.Focus()
	case 6:
		m.promptInput.Focus()
	}

	return m
}

// extractValues gets the final values from all components
func (m *tuiModel) extractValues() {
	m.jobTitle = m.titleInput.Value()

	// Get selected job type
	if selected := m.jobTypeList.SelectedItem(); selected != nil {
		m.jobType = string(selected.(item))
	}

	// Get dependencies from selected items
	var deps []string
	for jobID, selected := range m.selectedDeps {
		if selected {
			// Find the corresponding job to get its filename
			for _, job := range m.plan.Jobs {
				if job.ID == jobID {
					deps = append(deps, job.Filename)
					break
				}
			}
		}
	}
	m.jobDependencies = deps

	// Get selected template
	if selected := m.templateList.SelectedItem(); selected != nil {
		template := string(selected.(item))
		if template != "none" {
			m.jobTemplate = template
		}
	}

	m.jobWorktree = m.worktreeInput.Value()

	// Get selected model
	if selected := m.modelList.SelectedItem(); selected != nil {
		if modelItem, ok := selected.(modelItem); ok {
			m.jobModel = modelItem.ID
		}
	}

	m.jobPrompt = m.promptInput.Value()
}

func (m tuiModel) View() string {
	if m.quitting {
		return "Aborted.\n"
	}

	// If help is visible, show it and return
	if m.helpModel.ShowAll {
		m.helpModel.SetSize(95, m.jobTypeList.Height()+m.templateList.Height()+m.modelList.Height()+10) // estimate height
		return m.helpModel.View()
	}

	var b strings.Builder

	focusedStyle := theme.DefaultTheme.Selected
	headingStyle := theme.DefaultTheme.Bold

	// Smaller width for two-column layout
	borderStyle := theme.DefaultTheme.Box.
		BorderStyle(lipgloss.RoundedBorder()).
		Padding(0, 1).
		Width(45).
		Height(8)
	focusedBorderStyle := theme.DefaultTheme.Selected.
		BorderStyle(lipgloss.RoundedBorder()).
		Padding(0, 1).
		Width(45).
		Height(8)

	// Create a stylish header
	headerStyle := theme.DefaultTheme.Header.
		Padding(0, 2).
		MarginBottom(1).
		Align(lipgloss.Center).
		Width(95)

	planNameStyle := theme.DefaultTheme.Selected.
		Background(theme.DefaultTheme.Header.GetBackground())

	// Build the header content
	if m.plan != nil && m.plan.Name != "" {
		header := theme.DefaultTheme.Header.Render("✨ Add New Job to ") +
			planNameStyle.Render(m.plan.Name) +
			theme.DefaultTheme.Header.Render(" ✨")
		b.WriteString(headerStyle.Render(header))
	} else {
		b.WriteString(headerStyle.Render("✨ Add New Job ✨"))
	}
	b.WriteString("\n\n")

	// Helper to render each field with borders
	renderField := func(index int, label string, view string, forceHeight int) string {
		var fieldContent strings.Builder

		if m.focusIndex == index {
			fieldContent.WriteString(focusedStyle.Render("▸ ") + headingStyle.Render(label))
		} else {
			fieldContent.WriteString("  " + headingStyle.Render(label))
		}
		fieldContent.WriteString("\n")
		fieldContent.WriteString(view)

		// Apply border style
		style := borderStyle
		if m.focusIndex == index {
			style = focusedBorderStyle
		}

		// Override height if specified
		if forceHeight > 0 {
			if m.focusIndex == index {
				style = style.Copy().Height(forceHeight)
			} else {
				style = borderStyle.Copy().Height(forceHeight)
			}
		}

		return style.Render(fieldContent.String())
	}

	// Row 1: Title (full width)
	var titleFieldStyle lipgloss.Style
	if m.focusIndex == 0 {
		titleFieldStyle = focusedBorderStyle.Copy().Width(93).Height(2)
	} else {
		titleFieldStyle = borderStyle.Copy().Width(93).Height(2)
	}
	var titleContent strings.Builder
	if m.focusIndex == 0 {
		titleContent.WriteString(focusedStyle.Render("▸ ") + headingStyle.Render("Title:"))
	} else {
		titleContent.WriteString("  " + headingStyle.Render("Title:"))
	}
	titleContent.WriteString("\n")
	titleContent.WriteString(m.titleInput.View())
	b.WriteString(titleFieldStyle.Render(titleContent.String()))
	b.WriteString("\n")

	// Row 2: Job Type | Worktree
	jobTypeView := m.jobTypeList.View()
	jobTypeField := renderField(1, "Job Type:", jobTypeView, 0)
	worktreeField := renderField(2, "Worktree:", m.worktreeInput.View(), 0)

	// Join side by side
	row2 := lipgloss.JoinHorizontal(lipgloss.Top, jobTypeField, "  ", worktreeField)
	b.WriteString(row2)
	b.WriteString("\n")

	// Row 3: Template | Dependencies
	templateView := m.templateList.View()
	templateField := renderField(3, "Template:", templateView, 10)

	// Dependencies - use renderField for consistency
	var depView string
	if len(m.plan.Jobs) > 0 {
		depView = m.depList.View()
	} else {
		depView = "  No existing jobs available"
	}
	depField := renderField(4, "Dependencies:", depView, 10)

	row3 := lipgloss.JoinHorizontal(lipgloss.Top, templateField, "  ", depField)
	b.WriteString(row3)
	b.WriteString("\n")

	// Row 4: Model | Prompt
	modelView := m.modelList.View()
	modelField := renderField(5, "Model:", modelView, 10)
	promptField := renderField(6, "Prompt:", m.promptInput.View(), 10)

	row4 := lipgloss.JoinHorizontal(lipgloss.Top, modelField, "  ", promptField)
	b.WriteString(row4)

	// Help text
	helpStyle := theme.DefaultTheme.Muted.
		BorderStyle(lipgloss.NormalBorder()).
		BorderTop(true).
		BorderForeground(lipgloss.Color("240")).
		Padding(1, 0, 0, 0)

	helpText := m.helpModel.View()
	b.WriteString("\n")
	b.WriteString(helpStyle.Render(helpText))

	return b.String()
}

// toJob converts the TUI model data into a Job struct
func (m tuiModel) toJob(plan *orchestration.Plan) *orchestration.Job {
	// Generate job ID from title
	jobID := GenerateJobIDFromTitle(plan, m.jobTitle)

	// Default job type if none selected
	jobType := m.jobType
	if jobType == "" {
		jobType = "agent"
	}

	// Default output type
	outputType := "file"

	// The prompt body is simply the user's input. The executor will load the template.
	promptBody := m.jobPrompt

	return &orchestration.Job{
		ID:         jobID,
		Title:      m.jobTitle,
		Type:       orchestration.JobType(jobType),
		Status:     "pending",
		DependsOn:  m.jobDependencies,
		Worktree:   m.jobWorktree,
		Model:      m.jobModel,
		PromptBody: promptBody,
		Template:   m.jobTemplate,
		Output: orchestration.OutputConfig{
			Type: outputType,
		},
	}
}

// The helper functions findRootJobs and findDependents are already defined in plan_status.go

// Model represents an LLM model option
type Model struct {
	ID       string
	Provider string
	Note     string
}

// getAvailableModels returns the list of available LLM models
func getAvailableModels() []Model {
	return []Model{
		{"gemini-2.5-pro", "Google", "Latest Gemini Pro model"},
		{"gemini-2.5-flash", "Google", "Fast, efficient model"},
		{"gemini-2.0-flash", "Google", "Previous generation flash model"},
		{"claude-4-sonnet", "Anthropic", "Claude 4 Sonnet"},
		{"claude-4-opus", "Anthropic", "Claude 4 Opus - most capable"},
		{"claude-3-haiku", "Anthropic", "Fast, lightweight model"},
	}
}

