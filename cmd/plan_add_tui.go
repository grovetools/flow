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
	
	// Store selected dependencies
	dependencies    []string
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
	// Placeholder logic
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m tuiModel) View() string {
	if m.quitting {
		return "Aborted.\n"
	}
	return "TUI is running...\n" // Placeholder view
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
			connector := "│   "
			if i == len(dependents)-1 {
				connector = "    "
			}
			buildDisplayJobs(dep, prefix+connector, printed)
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