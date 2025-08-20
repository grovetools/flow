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
	titleInput      textinput.Model
	jobTypeList     list.Model
	depTree         dependencyTreeModel
	templateList    list.Model
	worktreeInput   textinput.Model
	modelInput      textinput.Model
	promptInput     textarea.Model
	
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

type itemDelegate struct{}

func (d itemDelegate) Height() int                             { return 1 }
func (d itemDelegate) Spacing() int                            { return 0 }
func (d itemDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }
func (d itemDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	i, ok := listItem.(item)
	if !ok {
		return
	}
	
	cursor := " "
	if index == m.Index() {
		cursor = ">"
	}
	
	str := fmt.Sprintf("%s %s", cursor, i)
	
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
	m.jobTypeList.Title = "Select Job Type"
	m.jobTypeList.SetShowStatusBar(false)
	m.jobTypeList.SetFilteringEnabled(false)
	m.jobTypeList.SetShowHelp(false)

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
	m.templateList.Title = "Select a Job Template (Optional)"
	m.templateList.SetShowStatusBar(false)
	m.templateList.SetFilteringEnabled(false)
	m.templateList.SetShowHelp(false)

	// 5. Worktree Input (textinput with default)
	m.worktreeInput = textinput.New()
	m.worktreeInput.Placeholder = "feature-branch"
	m.worktreeInput.Width = 50
	if plan.Config != nil && plan.Config.Worktree != "" {
		m.worktreeInput.SetValue(plan.Config.Worktree)
	}

	// 6. Model Input (textinput with default)
	m.modelInput = textinput.New()
	m.modelInput.Placeholder = "gemini-2.5-pro"
	m.modelInput.Width = 50
	if plan.Config != nil && plan.Config.Model != "" {
		m.modelInput.SetValue(plan.Config.Model)
	}
	
	// 7. Prompt Input (textarea)
	m.promptInput = textarea.New()
	m.promptInput.Placeholder = "Provide detailed instructions for the job..."
	m.promptInput.SetWidth(50)
	m.promptInput.SetHeight(5)

	return m
}

func (m tuiModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	
	// Handle dependency selection message
	switch msg := msg.(type) {
	case dependenciesSelectedMsg:
		m.jobDependencies = msg.deps
		m.focusIndex++ // Move to the next field
		return m, nil
	}
	
	// Handle keyboard input
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit
			
		case "tab", "down":
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
			if m.focusIndex == 2 && len(m.plan.Jobs) > 0 {
				// Don't process enter on dependency tree - let it handle its own enter
			} else if m.focusIndex == 6 {
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
	case 2: // Dependency tree
		var updatedTree tea.Model
		updatedTree, cmd = m.depTree.Update(msg)
		m.depTree = updatedTree.(dependencyTreeModel)
	case 3: // Template list
		m.templateList, cmd = m.templateList.Update(msg)
	case 4: // Worktree input
		m.worktreeInput, cmd = m.worktreeInput.Update(msg)
	case 5: // Model input
		m.modelInput, cmd = m.modelInput.Update(msg)
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
	m.modelInput.Blur()
	m.promptInput.Blur()
	
	// Focus the current one
	switch m.focusIndex {
	case 0:
		m.titleInput.Focus()
	case 4:
		m.worktreeInput.Focus()
	case 5:
		m.modelInput.Focus()
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
	
	// Dependencies are already stored in m.jobDependencies
	
	// Get selected template
	if selected := m.templateList.SelectedItem(); selected != nil {
		template := string(selected.(item))
		if template != "none" {
			m.jobTemplate = template
		}
	}
	
	m.jobWorktree = m.worktreeInput.Value()
	m.jobModel = m.modelInput.Value()
	m.jobPrompt = m.promptInput.Value()
}

func (m tuiModel) View() string {
	if m.quitting {
		return "Aborted.\n"
	}

	var b strings.Builder
	
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86"))
	focusedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	borderStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		Width(60)
	focusedBorderStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("205")).
		Padding(0, 1).
		Width(60)
	
	b.WriteString(titleStyle.Render("=== Add New Job ==="))
	b.WriteString("\n\n")

	// Helper to render each field with borders
	renderField := func(index int, label string, view string) {
		var fieldContent strings.Builder
		
		if m.focusIndex == index {
			fieldContent.WriteString(focusedStyle.Render("▸ " + label))
		} else {
			fieldContent.WriteString("  " + label)
		}
		fieldContent.WriteString("\n")
		fieldContent.WriteString(view)
		
		// Apply border style
		style := borderStyle
		if m.focusIndex == index {
			style = focusedBorderStyle
		}
		
		b.WriteString(style.Render(fieldContent.String()))
		b.WriteString("\n")
	}

	// 1. Title
	renderField(0, "Title:", m.titleInput.View())
	
	// 2. Job Type
	renderField(1, "Job Type:", m.jobTypeList.View())
	
	// 3. Dependencies
	if m.focusIndex == 2 {
		// When focused, show the full tree view
		var depContent strings.Builder
		depContent.WriteString(focusedStyle.Render("▸ Dependencies:"))
		depContent.WriteString("\n")
		depContent.WriteString(m.depTree.View())
		
		b.WriteString(focusedBorderStyle.Render(depContent.String()))
		b.WriteString("\n")
	} else {
		// When not focused, show selected dependencies
		depLabel := "Dependencies:"
		depValue := "(Press Enter to select)"
		if len(m.jobDependencies) > 0 {
			depValue = strings.Join(m.jobDependencies, ", ")
		}
		renderField(2, depLabel, depValue)
	}
	
	// 4. Template
	renderField(3, "Template:", m.templateList.View())
	
	// 5. Worktree
	renderField(4, "Worktree:", m.worktreeInput.View())
	
	// 6. Model
	renderField(5, "Model:", m.modelInput.View())
	
	// 7. Prompt
	renderField(6, "Prompt:", m.promptInput.View())

	// Help text
	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		BorderStyle(lipgloss.NormalBorder()).
		BorderTop(true).
		BorderForeground(lipgloss.Color("240")).
		Padding(1, 0, 0, 0)
	
	helpText := "tab/↓: next field • shift+tab/↑: prev field • space: select • enter: confirm • ctrl+c: quit"
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
		ID:           jobID,
		Title:        m.jobTitle,
		Type:         orchestration.JobType(jobType),
		Status:       "pending",
		DependsOn:    m.jobDependencies,
		Worktree:     m.jobWorktree,
		Model:        m.jobModel,
		PromptBody:   m.jobPrompt,
		Template:     m.jobTemplate,
		Output: orchestration.OutputConfig{
			Type: outputType,
		},
	}
}

// The helper functions findRootJobs and findDependents are already defined in plan_status.go

// Dependency Tree View Components

// dependencyJob represents a job in the tree view with its display properties.
type dependencyJob struct {
	job    *orchestration.Job
	prefix string // The tree structure prefix, e.g., "├── "
}

type dependencyTreeModel struct {
	plan         *orchestration.Plan
	displayJobs  []dependencyJob // A flattened list of jobs in display order
	cursor       int
	selected     map[string]struct{} // A set of selected job filenames
	width, height int
}

func initialDependencyTreeModel(plan *orchestration.Plan) dependencyTreeModel {
	m := dependencyTreeModel{
		plan:     plan,
		selected: make(map[string]struct{}),
		width:    50,
		height:   10,
	}

	// Use a helper function to recursively build the flattened tree
	var buildDisplayJobs func(*orchestration.Job, string, map[string]bool)
	buildDisplayJobs = func(job *orchestration.Job, prefix string, printed map[string]bool) {
		if printed[job.ID] {
			return
		}
		printed[job.ID] = true

		m.displayJobs = append(m.displayJobs, dependencyJob{job: job, prefix: prefix})

		dependents := findDependents(job, plan)
		for i, dep := range dependents {
			newPrefix := prefix
			if strings.HasSuffix(prefix, "└── ") {
				newPrefix = strings.TrimSuffix(prefix, "└── ") + "    "
			} else if strings.HasSuffix(prefix, "├── ") {
				newPrefix = strings.TrimSuffix(prefix, "├── ") + "│   "
			}
			
			childConnector := "├── "
			if i == len(dependents)-1 {
				childConnector = "└── "
			}
			buildDisplayJobs(dep, newPrefix+childConnector, printed)
		}
	}

	// Start building from the root jobs
	roots := findRootJobs(plan)
	printed := make(map[string]bool)
	for i, root := range roots {
		connector := "├── "
		if i == len(roots)-1 {
			connector = "└── "
		}
		buildDisplayJobs(root, connector, printed)
	}
	
	return m
}

// Define a message to signal completion
type dependenciesSelectedMsg struct{ deps []string }

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
				if _, ok := m.selected[selectedJob.Filename]; ok {
					delete(m.selected, selectedJob.Filename)
				} else {
					m.selected[selectedJob.Filename] = struct{}{}
				}
			}
		case "enter":
			// Return selected dependencies to the main model
			var deps []string
			for filename := range m.selected {
				deps = append(deps, filename)
			}
			return m, func() tea.Msg { return dependenciesSelectedMsg{deps: deps} }
		}
	}
	return m, nil
}

func (m dependencyTreeModel) View() string {
	if len(m.displayJobs) == 0 {
		return "No existing jobs to depend on.\n\nPress enter to continue."
	}
	
	var b strings.Builder
	b.WriteString("Select dependencies (space to toggle, enter to confirm):\n\n")

	for i, dj := range m.displayJobs {
		cursor := " "
		if m.cursor == i {
			cursor = ">" // Indicates the cursor position
		}

		checked := " "
		if _, ok := m.selected[dj.job.Filename]; ok {
			checked = "x" // Indicates a selected dependency
		}

		// Build the line
		line := fmt.Sprintf("%s [%s] %s%s (%s)\n", cursor, checked, dj.prefix, dj.job.Filename, dj.job.Title)
		
		if m.cursor == i {
			// Apply a style for the selected line
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Render(line))
		} else {
			b.WriteString(line)
		}
	}

	b.WriteString("\n(Press q to quit)")
	return b.String()
}