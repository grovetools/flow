package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
)

type tuiModel struct {
	plan       *orchestration.Plan // To access existing jobs and defaults
	focusIndex int
	quitting   bool
	err        error

	// Form inputs
	titleInput    textinput.Model
	jobTypeList   list.Model
	depTree       dependencyTreeModel
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
		fmt.Fprint(w, lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Render(str))
	} else {
		fmt.Fprint(w, str)
	}
}

func initialModel(plan *orchestration.Plan) tuiModel {
	m := tuiModel{
		plan: plan,
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
	m.jobTypeList = list.New(jobTypes, itemDelegate{}, 20, 7)
	m.jobTypeList.Title = ""
	m.jobTypeList.SetShowTitle(false)
	m.jobTypeList.SetShowStatusBar(false)
	m.jobTypeList.SetFilteringEnabled(true)
	m.jobTypeList.SetShowHelp(false)
	m.jobTypeList.FilterInput.Prompt = "🔍 "
	m.jobTypeList.FilterInput.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("147"))
	m.jobTypeList.FilterInput.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	// 3. Dependencies Input (Tree View)
	m.depTree = initialDependencyTreeModel(plan)

	// 4. Template Input (list)
	templateManager := orchestration.NewTemplateManager()
	templates, _ := templateManager.ListTemplates() // Ignore error for now
	templateItems := make([]list.Item, len(templates)+1)
	templateItems[0] = item("none") // Add a 'none' option
	for i, t := range templates {
		templateItems[i+1] = item(t.Name)
	}
	m.templateList = list.New(templateItems, itemDelegate{}, 20, 10)
	m.templateList.Title = ""
	m.templateList.SetShowTitle(false)
	m.templateList.SetShowStatusBar(false)
	m.templateList.SetFilteringEnabled(true)
	m.templateList.SetShowHelp(false)
	m.templateList.FilterInput.Prompt = "🔍 "
	m.templateList.FilterInput.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("147"))
	m.templateList.FilterInput.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

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
	m.modelList.FilterInput.Prompt = "🔍 "
	m.modelList.FilterInput.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("147"))
	m.modelList.FilterInput.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
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
		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit

		case "tab", "down":
			// Special handling for template list - confirm selection on tab
			if m.focusIndex == 3 && msg.String() == "tab" {
				// If we're in the template list and user pressed tab, treat it as confirming the selection
				// The current selection is already stored in the list model, so we just move on
			}
			m.focusIndex++
			if m.focusIndex > 6 {
				m.focusIndex = 0
			}
			return m.updateFocus(), nil

		case "shift+tab", "up":
			m.focusIndex--
			if m.focusIndex < 0 {
				m.focusIndex = 6
			}
			return m.updateFocus(), nil

		case "enter":
			// Special handling for certain fields
			if m.focusIndex == 6 {
				// On the last field (prompt), enter confirms the form
				// Extract values from all inputs
				m.extractValues()
				m.quitting = true
				return m, tea.Quit
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
	case 4: // Dependency tree
		var updatedTree tea.Model
		updatedTree, cmd = m.depTree.Update(msg)
		m.depTree = updatedTree.(dependencyTreeModel)
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

	// Get dependencies from the tree model
	var deps []string
	for jobID := range m.depTree.selected {
		// Find the corresponding job to get its filename
		for _, job := range m.plan.Jobs {
			if job.ID == jobID {
				deps = append(deps, job.Filename)
				break
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

	var b strings.Builder

	focusedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	headingStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("147"))

	// Smaller width for two-column layout
	borderStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		Width(45).
		Height(8)
	focusedBorderStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("205")).
		Padding(0, 1).
		Width(45).
		Height(8)

	// Create a stylish header
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("15")).
		Background(lipgloss.Color("63")).
		Padding(0, 2).
		MarginBottom(1).
		Align(lipgloss.Center).
		Width(95)

	planNameStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("219")).
		Background(lipgloss.Color("63"))

	// Build the header content
	if m.plan != nil && m.plan.Name != "" {
		header := lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("63")).Render("✨ Add New Job to ") +
			planNameStyle.Render(m.plan.Name) +
			lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("63")).Render(" ✨")
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

	// Dependencies - always show the tree view with fixed height
	var depContent strings.Builder
	if m.focusIndex == 4 {
		depContent.WriteString(focusedStyle.Render("▸ ") + headingStyle.Render("Dependencies:"))
	} else {
		depContent.WriteString("  " + headingStyle.Render("Dependencies:"))
	}
	depContent.WriteString("\n")

	// Show the tree view or a placeholder
	if len(m.plan.Jobs) > 0 {
		depContent.WriteString(m.depTree.View())
	} else {
		depContent.WriteString("  No existing jobs available")
	}

	// Fixed height for consistency - match template height
	depStyle := borderStyle.Copy().Height(10)
	if m.focusIndex == 4 {
		depStyle = focusedBorderStyle.Copy().Height(10)
	}
	depField := depStyle.Render(depContent.String())

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
	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		BorderStyle(lipgloss.NormalBorder()).
		BorderTop(true).
		BorderForeground(lipgloss.Color("240")).
		Padding(1, 0, 0, 0)

	helpText := "tab/↓: next • shift+tab/↑: prev • j/k: navigate • /: search • esc: clear • space: select • a: all • enter: confirm • ctrl+c: quit"
	b.WriteString("\n")
	b.WriteString(helpStyle.Render(helpText))

	return b.String()
}

// toJob converts the TUI model data into a Job struct
func (m tuiModel) toJob(plan *orchestration.Plan) *orchestration.Job {
	// Generate job ID from title
	jobID := generateJobIDFromTitle(plan, m.jobTitle)
	

	// Default job type if none selected
	jobType := m.jobType
	if jobType == "" {
		jobType = "agent"
	}

	// Default output type
	outputType := "file"

	return &orchestration.Job{
		ID:         jobID,
		Title:      m.jobTitle,
		Type:       orchestration.JobType(jobType),
		Status:     "pending",
		DependsOn:  m.jobDependencies,
		Worktree:   m.jobWorktree,
		Model:      m.jobModel,
		PromptBody: m.jobPrompt,
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

// Dependency Tree View Components

// dependencyJob represents a job in the tree view with its display properties.
type dependencyJob struct {
	job    *orchestration.Job
	prefix string // The tree structure prefix, e.g., "├── "
}

type dependencyTreeModel struct {
	plan          *orchestration.Plan
	displayJobs   []dependencyJob // A flattened list of jobs in display order
	cursor        int
	selected      map[string]struct{} // A set of selected job IDs
	width, height int
}

func initialDependencyTreeModel(plan *orchestration.Plan) dependencyTreeModel {
	m := dependencyTreeModel{
		plan:     plan,
		selected: make(map[string]struct{}),
		width:    50,
		height:   10,
	}

	// Build a simple flat list of jobs for now to avoid complex tree logic
	// This makes selection easier and avoids indentation issues
	for _, job := range plan.Jobs {
		m.displayJobs = append(m.displayJobs, dependencyJob{
			job:    job,
			prefix: "", // No tree prefixes for simplicity
		})
	}

	return m
}


func (m dependencyTreeModel) Init() tea.Cmd {
	return nil
}

func (m dependencyTreeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "j", "down":
			if m.cursor < len(m.displayJobs)-1 {
				m.cursor++
			}
		case " ": // Spacebar to toggle selection
			if len(m.displayJobs) > 0 {
				selectedJob := m.displayJobs[m.cursor].job
				if _, ok := m.selected[selectedJob.ID]; ok {
					delete(m.selected, selectedJob.ID)
				} else {
					m.selected[selectedJob.ID] = struct{}{}
				}
			}
		case "a", "A": // Select/deselect all
			if len(m.selected) == len(m.displayJobs) {
				// If all selected, deselect all
				m.selected = make(map[string]struct{})
			} else {
				// Otherwise, select all
				for _, dj := range m.displayJobs {
					m.selected[dj.job.ID] = struct{}{}
				}
			}
		}
	}
	return m, nil
}

func (m dependencyTreeModel) View() string {
	if len(m.displayJobs) == 0 {
		return "No existing jobs available"
	}

	var b strings.Builder

	// Style for the list
	normalStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	selectedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	for i, dj := range m.displayJobs {
		// Cursor
		if m.cursor == i {
			b.WriteString(selectedStyle.Render("▸ "))
		} else {
			b.WriteString("  ")
		}

		// Checkbox
		if _, ok := m.selected[dj.job.ID]; ok {
			b.WriteString(selectedStyle.Render("[✓] "))
		} else {
			b.WriteString("[ ] ")
		}

		// Job info - show filename and title
		jobInfo := fmt.Sprintf("%s (%s)", dj.job.Filename, dj.job.Title)

		if m.cursor == i {
			b.WriteString(selectedStyle.Render(jobInfo))
		} else if _, ok := m.selected[dj.job.ID]; ok {
			b.WriteString(normalStyle.Render(jobInfo))
		} else {
			b.WriteString(dimStyle.Render(jobInfo))
		}

		b.WriteString("\n")
	}

	return b.String()
}
