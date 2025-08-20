package cmd

import (
	"fmt"
	"io"

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
	depInput        textinput.Model
	templateList    list.Model
	worktreeInput   textinput.Model
	modelInput      textinput.Model
	promptInput     textarea.Model
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

	// 3. Dependencies Input (placeholder for now)
	// For now, use a simple text input as a placeholder. We will replace this
	// with the interactive tree in a later step.
	m.depInput = textinput.New()
	m.depInput.Placeholder = "01-previous-job.md (comma-separated)"
	m.depInput.Width = 50

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