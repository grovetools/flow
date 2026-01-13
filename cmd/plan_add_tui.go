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
	anthropicmodels "github.com/mattsolo1/grove-anthropic/pkg/models"
	"github.com/mattsolo1/grove-core/tui/components/help"
	"github.com/mattsolo1/grove-core/tui/keymap"
	"github.com/mattsolo1/grove-core/tui/theme"
	geminimodels "github.com/mattsolo1/grove-gemini/pkg/models"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
)

// Custom keymap extending the base
type addTuiKeyMap struct {
	keymap.Base
	Next     key.Binding
	Prev     key.Binding
	Submit   key.Binding
	Toggle   key.Binding
	GoTop    key.Binding
	GoBottom key.Binding
	PageUp   key.Binding
	PageDown key.Binding
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
	GoTop: key.NewBinding(
		key.WithKeys("gg", "home"),
		key.WithHelp("gg/home", "go to top"),
	),
	GoBottom: key.NewBinding(
		key.WithKeys("G", "end"),
		key.WithHelp("G/end", "go to bottom"),
	),
	PageUp: key.NewBinding(
		key.WithKeys("ctrl+u", "pgup"),
		key.WithHelp("ctrl+u/pgup", "page up"),
	),
	PageDown: key.NewBinding(
		key.WithKeys("ctrl+d", "pgdown"),
		key.WithHelp("ctrl+d/pgdown", "page down"),
	),
}

// ShortHelp returns key bindings to show in the mini help view
func (k addTuiKeyMap) ShortHelp() []key.Binding {
	// Return just the base help to show the help menu
	return k.Base.ShortHelp()
}

// FullHelp returns keybindings for the expanded help view
func (k addTuiKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{
			key.NewBinding(key.WithHelp("", "Navigation")),
			k.Next,
			k.Prev,
			key.NewBinding(key.WithHelp("↑/↓, j/k", "Navigate lists")),
			k.GoTop,
			k.GoBottom,
			k.PageUp,
			k.PageDown,
			key.NewBinding(key.WithHelp("/", "Search lists")),
			key.NewBinding(key.WithHelp("esc", "Clear search")),
		},
		{
			key.NewBinding(key.WithHelp("", "Actions")),
			k.Toggle,
			key.NewBinding(key.WithHelp("enter", "Confirm & Next")),
			key.NewBinding(key.WithHelp("c", "Quick chat setup")),
			key.NewBinding(key.WithHelp("a", "Quick agent setup")),
			key.NewBinding(key.WithHelp("ctrl+s", "Save and exit")),
			key.NewBinding(key.WithHelp(":wq", "Vim save and exit")),
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
	unfocused  bool // Track if we're in unfocused state
	quitting   bool
	err        error

	// Form inputs
	titleInput   textinput.Model
	jobTypeList  list.Model
	depList      list.Model
	selectedDeps map[string]bool // Track selected dependencies
	templateList list.Model
	promptInput  textarea.Model

	// All available templates (for filtering)
	allTemplates []*orchestration.JobTemplate

	// Fields to store the final job data
	jobTitle        string
	jobType         string
	jobDependencies []string
	jobTemplate     string
	jobPrompt       string
}

type item string

func (i item) FilterValue() string { return string(i) }

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
	cursor := "  "
	if index == m.Index() {
		cursor = theme.DefaultTheme.Highlight.Render(theme.IconArrow + " ")
	}

	switch item := listItem.(type) {
	case item:
		// Regular items
		str = fmt.Sprintf("%s%s", cursor, item)
	default:
		return
	}

	fmt.Fprint(w, str)
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
	cursor := "  "
	if index == m.Index() {
		cursor = theme.DefaultTheme.Highlight.Render(theme.IconArrow + " ")
	}

	checkbox := theme.IconUnselect
	if (*d.selectedDeps)[depItem.job.ID] {
		checkbox = theme.IconSelect
	}

	// Format the display text
	displayText := fmt.Sprintf("%s (%s)", depItem.job.Filename, depItem.job.Title)

	// Truncate to prevent wrapping (account for cursor and checkbox = 6 chars)
	maxWidth := 35 // Conservative width for 45-char list minus cursor/checkbox
	if len(displayText) > maxWidth {
		displayText = displayText[:maxWidth-3] + "..."
	}

	str = fmt.Sprintf("%s%s %s", cursor, checkbox, displayText)

	if (*d.selectedDeps)[depItem.job.ID] {
		fmt.Fprint(w, theme.DefaultTheme.Bold.Render(str))
	} else {
		fmt.Fprint(w, theme.DefaultTheme.Muted.Render(str))
	}
}

func initialModel(plan *orchestration.Plan, initialDeps []string) tuiModel {
	m := tuiModel{
		plan: plan,
		keys: addKeys,
		unfocused: false, // Start in insert mode (focused)
		helpModel: help.NewBuilder().
			WithKeys(addKeys).
			WithTitle("󰝒 Add New Job - Help").
			Build(),
	}

	// 1. Title Input (textinput)
	m.titleInput = textinput.New()
	m.titleInput.Placeholder = "New job title here"
	m.titleInput.Focus()
	m.titleInput.CharLimit = 156
	m.titleInput.Width = 50

	// 2. Job Type Input (list)
	jobTypes := []list.Item{
		item("interactive_agent"),
		item("headless_agent"),
		item("agent"),
		item("oneshot"),
		item("shell"),
		item("chat"),
		item("file"),
	}
	m.jobTypeList = list.New(jobTypes, itemDelegate{}, 20, 7)
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

	// Create a set for efficient lookup of initial dependencies.
	initialDepsSet := make(map[string]bool)
	for _, dep := range initialDeps {
		initialDepsSet[dep] = true
	}

	depItems := make([]list.Item, 0, len(plan.Jobs))
	for _, job := range plan.Jobs {
		depItems = append(depItems, dependencyItem{job: job})
		// Pre-select the job if its filename is in the initial dependencies set.
		if initialDepsSet[job.Filename] {
			m.selectedDeps[job.ID] = true
		}
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
	m.allTemplates = templates

	// Initially show all templates (no job type selected yet)
	m.templateList = m.buildTemplateList("")

	// 5. Prompt Input (textarea)
	m.promptInput = textarea.New()
	m.promptInput.Placeholder = "Enter prompt here..."
	m.promptInput.SetWidth(41)
	m.promptInput.SetHeight(7)

	return m
}

// buildTemplateList creates a filtered template list based on the selected job type
func (m tuiModel) buildTemplateList(jobType string) list.Model {
	// Filter templates based on job type
	var filteredTemplates []*orchestration.JobTemplate

	for _, t := range m.allTemplates {
		// Map job types to template types
		// Agent types (interactive_agent, headless_agent, agent) share "Agent templates"
		// Prompt types (oneshot, chat) share "Prompt templates"
		// Note: Template Type comes from directory structure: "agent" or "oneshot"
		includeTemplate := false

		switch jobType {
		case "interactive_agent", "headless_agent", "agent":
			// Show agent templates (Type == "agent")
			includeTemplate = t.Type == "agent"
		case "oneshot", "chat":
			// Show oneshot templates (Type == "oneshot", which includes chat.md)
			includeTemplate = t.Type == "oneshot"
		case "shell", "file":
			// Shell and file jobs don't use templates
			includeTemplate = false
		default:
			// If no job type selected, show all templates
			includeTemplate = true
		}

		if includeTemplate {
			filteredTemplates = append(filteredTemplates, t)
		}
	}

	// Build list items
	templateItems := make([]list.Item, len(filteredTemplates)+1)
	templateItems[0] = item("none") // Add a 'none' option
	for i, t := range filteredTemplates {
		templateItems[i+1] = item(t.Name)
	}

	templateList := list.New(templateItems, itemDelegate{}, 20, 7)
	templateList.Title = ""
	templateList.SetShowTitle(false)
	templateList.SetShowStatusBar(false)
	templateList.SetFilteringEnabled(true)
	templateList.SetShowHelp(false)
	templateList.SetShowPagination(true)
	templateList.FilterInput.Prompt = "🔍 "
	templateList.FilterInput.PromptStyle = theme.DefaultTheme.Bold
	templateList.FilterInput.TextStyle = theme.DefaultTheme.Selected

	return templateList
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(
		tea.ClearScreen,
		textinput.Blink,
	)
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
		inTextInput := !m.unfocused && (m.focusIndex == 0 || m.focusIndex == 4)
		// Check if we're in a list that needs arrow keys
		inList := !m.unfocused && (m.focusIndex == 1 || m.focusIndex == 2 || m.focusIndex == 3)

		switch msg.String() {
		case "esc", "escape":
			// ESC unfocuses any focused field (text inputs or lists)
			m.unfocused = true
			m.titleInput.Blur()
			m.promptInput.Blur()
			return m, nil
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

		case "gg", "home":
			// Go to top (first field)
			if inList {
				// If in a list, go to top of list
				switch m.focusIndex {
				case 1: // Job type list
					m.jobTypeList.Select(0)
				case 2: // Template list
					m.templateList.Select(0)
				case 3: // Dependencies list
					m.depList.Select(0)
				}
			} else {
				// Go to first field
				m.focusIndex = 0
				return m.updateFocus(), nil
			}
			return m, nil

		case "G", "end":
			// Go to bottom (last field)
			if inList {
				// If in a list, go to bottom of list
				switch m.focusIndex {
				case 1: // Job type list
					m.jobTypeList.Select(len(m.jobTypeList.Items()) - 1)
				case 2: // Template list
					m.templateList.Select(len(m.templateList.Items()) - 1)
				case 3: // Dependencies list
					m.depList.Select(len(m.depList.Items()) - 1)
				}
			} else {
				// Go to last field
				m.focusIndex = 4
				return m.updateFocus(), nil
			}
			return m, nil

		case "ctrl+u", "pgup":
			// Page up in lists
			if inList {
				switch m.focusIndex {
				case 1: // Job type list
					current := m.jobTypeList.Index()
					newIndex := current - 5
					if newIndex < 0 {
						newIndex = 0
					}
					m.jobTypeList.Select(newIndex)
				case 2: // Template list
					current := m.templateList.Index()
					newIndex := current - 5
					if newIndex < 0 {
						newIndex = 0
					}
					m.templateList.Select(newIndex)
				case 3: // Dependencies list
					current := m.depList.Index()
					newIndex := current - 5
					if newIndex < 0 {
						newIndex = 0
					}
					m.depList.Select(newIndex)
				}
			}
			return m, nil

		case "ctrl+d", "pgdown":
			// Page down in lists
			if inList {
				switch m.focusIndex {
				case 1: // Job type list
					current := m.jobTypeList.Index()
					newIndex := current + 5
					if newIndex >= len(m.jobTypeList.Items()) {
						newIndex = len(m.jobTypeList.Items()) - 1
					}
					m.jobTypeList.Select(newIndex)
				case 2: // Template list
					current := m.templateList.Index()
					newIndex := current + 5
					if newIndex >= len(m.templateList.Items()) {
						newIndex = len(m.templateList.Items()) - 1
					}
					m.templateList.Select(newIndex)
				case 3: // Dependencies list
					current := m.depList.Index()
					newIndex := current + 5
					if newIndex >= len(m.depList.Items()) {
						newIndex = len(m.depList.Items()) - 1
					}
					m.depList.Select(newIndex)
				}
			}
			return m, nil

		case "tab":
			// Tab moves to next field, preserving unfocused state if already unfocused
			m.focusIndex++
			if m.focusIndex > 4 {
				m.focusIndex = 0
			}
			return m.updateFocus(), nil

		case "down", "j":
			// Down arrow and j navigate fields when not in a list or when unfocused, but never interrupt text input
			if (!inList || m.unfocused) && !inTextInput {
				// Keep unfocused state when navigating
				m.focusIndex++
				if m.focusIndex > 4 {
					m.focusIndex = 0
				}
				return m.updateFocus(), nil
			}

		case "shift+tab":
			// Shift+tab moves to previous field, preserving unfocused state if already unfocused
			m.focusIndex--
			if m.focusIndex < 0 {
				m.focusIndex = 4
			}
			return m.updateFocus(), nil

		case "up", "k":
			// Up arrow and k navigate fields when not in a list or when unfocused, but never interrupt text input
			if (!inList || m.unfocused) && !inTextInput {
				// Keep unfocused state when navigating
				m.focusIndex--
				if m.focusIndex < 0 {
					m.focusIndex = 4
				}
				return m.updateFocus(), nil
			}

		case "left", "h":
			// Left arrow and h navigate fields when unfocused, but never interrupt text input
			if m.unfocused && !inTextInput {
				// Keep unfocused state when navigating
				m.focusIndex--
				if m.focusIndex < 0 {
					m.focusIndex = 4
				}
				return m.updateFocus(), nil
			}

		case "right", "l":
			// Right arrow and l navigate fields when unfocused, but never interrupt text input
			if m.unfocused && !inTextInput {
				// Keep unfocused state when navigating
				m.focusIndex++
				if m.focusIndex > 4 {
					m.focusIndex = 0
				}
				return m.updateFocus(), nil
			}

		case "c":
			// Quick chat setup - set type to chat and template to chat
			if m.unfocused {
				// Set job type to chat
				for i, listItem := range m.jobTypeList.Items() {
					if string(listItem.(item)) == "chat" {
						m.jobTypeList.Select(i)
						break
					}
				}
				// Update template list for chat job type (filters to oneshot templates)
				m.templateList = m.buildTemplateList("chat")
				// Set template to chat (find it in the template list)
				for i, listItem := range m.templateList.Items() {
					if string(listItem.(item)) == "chat" {
						m.templateList.Select(i)
						break
					}
				}
				return m, nil
			}

		case "a":
			// Quick agent setup - set type to interactive_agent
			if m.unfocused {
				// Set job type to interactive_agent
				for i, listItem := range m.jobTypeList.Items() {
					if string(listItem.(item)) == "interactive_agent" {
						m.jobTypeList.Select(i)
						break
					}
				}
				// Update template list for interactive_agent job type (filters to agent templates)
				m.templateList = m.buildTemplateList("interactive_agent")
				return m, nil
			}

		case "ctrl+s":
			// Save - extract values and quit (works in both normal and insert mode)
			m.extractValues()
			m.quitting = true
			return m, tea.Quit

		case ":wq":
			// Vim-style save and quit
			m.extractValues()
			m.quitting = true
			return m, tea.Quit

		case "i":
			// Insert mode - refocus current field (like vim)
			if m.unfocused {
				m.unfocused = false
				return m.updateFocus(), nil
			}

		case "enter":
			// Special handling for certain fields
			if m.focusIndex == 4 {
				// On the last field (prompt), enter confirms the form
				// Extract values from all inputs
				m.extractValues()
				m.quitting = true
				return m, tea.Quit
			} else if inList {
				// For lists, enter confirms selection and moves to next field
				m.unfocused = false
				m.focusIndex++
				if m.focusIndex > 4 {
					m.focusIndex = 0
				}
				return m.updateFocus(), nil
			} else if m.unfocused {
				// If unfocused, enter refocuses current field
				m.unfocused = false
				return m.updateFocus(), nil
			}
		}
	}

	// Delegate to the focused component
	switch m.focusIndex {
	case 0: // Title input
		m.titleInput, cmd = m.titleInput.Update(msg)
	case 1: // Job type list
		prevSelection := m.jobTypeList.SelectedItem()
		m.jobTypeList, cmd = m.jobTypeList.Update(msg)
		// Check if job type selection changed
		newSelection := m.jobTypeList.SelectedItem()
		if prevSelection != newSelection && newSelection != nil {
			// Update template list based on new job type
			selectedJobType := string(newSelection.(item))
			m.templateList = m.buildTemplateList(selectedJobType)
		}
	case 2: // Template list
		m.templateList, cmd = m.templateList.Update(msg)
	case 3: // Dependency list
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
	case 4: // Prompt textarea
		m.promptInput, cmd = m.promptInput.Update(msg)
	}

	return m, cmd
}

// updateFocus updates focus state for all components
func (m tuiModel) updateFocus() tuiModel {
	// Blur all inputs
	m.titleInput.Blur()
	m.promptInput.Blur()

	// Only focus if not in unfocused state
	if !m.unfocused {
		switch m.focusIndex {
		case 0:
			m.titleInput.Focus()
		case 4:
			m.promptInput.Focus()
		}
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

	m.jobPrompt = m.promptInput.Value()
}

func (m tuiModel) View() string {
	if m.quitting {
		return "Aborted.\n"
	}

	// If help is visible, show it and return
	if m.helpModel.ShowAll {
		m.helpModel.SetSize(95, m.jobTypeList.Height()+m.templateList.Height()+10) // estimate height
		return m.helpModel.View()
	}

	var b strings.Builder

	focusedStyle := theme.DefaultTheme.Highlight
	headingStyle := theme.DefaultTheme.Bold

	// Define a consistent base style for all panes.
	// This has no background and, crucially, no vertical margin, preventing layout shifts.
	borderStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultColors.Border).
		Padding(0, 0).
		Width(45)

	// Unfocused style - use theme border color
	unfocusedBorderStyle := borderStyle.Copy().
		BorderForeground(theme.DefaultColors.Border)

	// The focused style uses a bright highlighted border color and bold border.
	// This provides a clear, modern focus indicator.
	focusedBorderStyle := borderStyle.Copy().
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(theme.DefaultColors.Orange) // Orange is the theme's highlight color.

	// No header - start directly with the form

	// Helper to render each field with borders
	renderField := func(index int, label string, view string, forceHeight int) string {
		var fieldContent strings.Builder

		if m.focusIndex == index {
			fieldContent.WriteString("  " + focusedStyle.Render(headingStyle.Render(label)))
		} else {
			fieldContent.WriteString("  " + headingStyle.Render(label))
		}
		fieldContent.WriteString("\n")
		fieldContent.WriteString(view)

		// Apply border style based on focus state
		var style lipgloss.Style
		if m.focusIndex == index && !m.unfocused {
			style = focusedBorderStyle
		} else if m.focusIndex == index && m.unfocused {
			style = unfocusedBorderStyle
		} else {
			style = borderStyle
		}

		// Override height if specified
		if forceHeight > 0 {
			style = style.Copy().Height(forceHeight)
		}

		return style.Render(fieldContent.String())
	}

	// Row 1: Title (full width) with left margin
	var titleFieldStyle lipgloss.Style
	if m.focusIndex == 0 && !m.unfocused {
		titleFieldStyle = focusedBorderStyle.Copy().Width(93).MarginLeft(2)
	} else if m.focusIndex == 0 && m.unfocused {
		titleFieldStyle = unfocusedBorderStyle.Copy().Width(93).MarginLeft(2)
	} else {
		titleFieldStyle = borderStyle.Copy().Width(93).MarginLeft(2)
	}
	var titleContent strings.Builder
	if m.focusIndex == 0 {
		titleContent.WriteString("  " + focusedStyle.Render(headingStyle.Render("Title:")))
	} else {
		titleContent.WriteString("  " + headingStyle.Render("Title:"))
	}
	titleContent.WriteString("\n")
	titleContent.WriteString(m.titleInput.View())
	titleRow := titleFieldStyle.Render(titleContent.String())

	// Row 2: Job Type | Template
	jobTypeView := m.jobTypeList.View()
	jobTypeField := renderField(1, "Job Type:", jobTypeView, 0)

	// Determine template label based on selected job type
	templateLabel := "Template:"
	if selected := m.jobTypeList.SelectedItem(); selected != nil {
		jobType := string(selected.(item))
		switch jobType {
		case "interactive_agent", "headless_agent", "agent":
			templateLabel = "Agent templates:"
		case "oneshot", "chat":
			templateLabel = "Prompt templates:"
		}
	}

	templateView := m.templateList.View()
	templateField := renderField(2, templateLabel, templateView, 0)

	row2 := lipgloss.JoinHorizontal(lipgloss.Top, jobTypeField, "  ", templateField)
	row2WithMargin := lipgloss.NewStyle().MarginLeft(2).Render(row2)

	// Row 3: Dependencies | Prompt
	// Dependencies - use renderField for consistency
	var depView string
	if len(m.plan.Jobs) > 0 {
		depView = m.depList.View()
	} else {
		depView = "  No existing jobs available"
	}
	depField := renderField(3, "Dependencies:", depView, 0)

	promptField := renderField(4, "Prompt:", m.promptInput.View(), 0)

	row3 := lipgloss.JoinHorizontal(lipgloss.Top, depField, "  ", promptField)
	row3WithMargin := lipgloss.NewStyle().MarginLeft(2).Render(row3)

	// Join all rows vertically for a compact layout
	allRows := lipgloss.JoinVertical(lipgloss.Left, titleRow, row2WithMargin, row3WithMargin)
	b.WriteString(allRows)

	// Help text with left margin
	helpStyle := theme.DefaultTheme.Muted.
		Padding(1, 0, 0, 0).
		MarginLeft(2)

	helpText := m.helpModel.View()
	
	// Add mode indicator
	var modeIndicator string
	if m.unfocused {
		modeIndicator = theme.DefaultTheme.Muted.Render(" [NORMAL] hjkl navigate • i insert • q quit")
	} else {
		modeIndicator = theme.DefaultTheme.Muted.Render(" [INSERT] esc normal")
	}
	
	// Combine help text with mode indicator
	fullHelpText := helpText + modeIndicator
	b.WriteString(helpStyle.Render(fullHelpText))

	return b.String()
}

// toJob converts the TUI model data into a Job struct
func (m tuiModel) toJob(plan *orchestration.Plan) *orchestration.Job {
	// Generate job ID from title
	jobID := orchestration.GenerateUniqueJobID(plan, m.jobTitle)

	// Default job type if none selected
	jobType := m.jobType
	if jobType == "" {
		jobType = "agent"
	}

	// The prompt body is simply the user's input. The executor will load the template.
	promptBody := m.jobPrompt

	jobStatus := orchestration.JobStatusPending
	if jobType == "chat" {
		jobStatus = orchestration.JobStatusPendingUser
	} else if jobType == "file" {
		jobStatus = orchestration.JobStatusCompleted
	}

	return &orchestration.Job{
		ID:         jobID,
		Title:      m.jobTitle,
		Type:       orchestration.JobType(jobType),
		Status:     jobStatus,
		DependsOn:  m.jobDependencies,
		PromptBody: promptBody,
		Template:   m.jobTemplate,
	}
}

// The helper functions findRootJobs and findDependents are already defined in plan_status.go

// Model represents an LLM model option
type Model struct {
	ID       string
	Provider string
	Note     string
}

// modelItem represents a model in the list
type modelItem struct {
	Model
}

func (m modelItem) FilterValue() string { return m.ID }
func (m modelItem) Title() string       { return m.ID }
func (m modelItem) Description() string { return fmt.Sprintf("%s - %s", m.Provider, m.Note) }

// getAvailableModels returns the list of available LLM models (current, non-legacy only)
func getAvailableModels() []Model {
	var models []Model

	// Add current Gemini models
	for _, m := range geminimodels.CurrentModels() {
		models = append(models, Model{
			ID:       m.ID,
			Provider: m.Provider,
			Note:     m.Note,
		})
	}

	// Add current Anthropic models (use alias if available for shorter display)
	for _, m := range anthropicmodels.CurrentModels() {
		id := m.ID
		if m.Alias != "" {
			id = m.Alias // Use shorter alias in TUI
		}
		models = append(models, Model{
			ID:       id,
			Provider: m.Provider,
			Note:     m.Note,
		})
	}

	return models
}

