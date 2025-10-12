package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattsolo1/grove-core/tui/components/help"
	"github.com/mattsolo1/grove-core/tui/keymap"
	"github.com/mattsolo1/grove-core/tui/theme"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
)

type statusTUIKeyMap struct {
	keymap.Base
	Select        key.Binding
	ToggleMulti   key.Binding
	Archive       key.Binding
	Edit          key.Binding
	Run           key.Binding
	SetCompleted  key.Binding
	SetStatus     key.Binding
	AddJob        key.Binding
	Implement     key.Binding
	ToggleSummaries key.Binding
	GoToTop       key.Binding
	GoToBottom    key.Binding
	PageUp        key.Binding
	PageDown      key.Binding
}

func newStatusTUIKeyMap() statusTUIKeyMap {
	return statusTUIKeyMap{
		Base: keymap.NewBase(),
		Select: key.NewBinding(
			key.WithKeys(" "),
			key.WithHelp("space", "select/deselect"),
		),
		ToggleMulti: key.NewBinding(
			key.WithKeys("m"),
			key.WithHelp("m", "toggle multi-select"),
		),
		Archive: key.NewBinding(
			key.WithKeys("a"),
			key.WithHelp("a", "archive job"),
		),
		Edit: key.NewBinding(
			key.WithKeys("e"),
			key.WithHelp("e", "edit job"),
		),
		Run: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "run job"),
		),
		SetCompleted: key.NewBinding(
			key.WithKeys("c"),
			key.WithHelp("c", "mark completed"),
		),
		SetStatus: key.NewBinding(
			key.WithKeys("S"),
			key.WithHelp("S", "set status"),
		),
		AddJob: key.NewBinding(
			key.WithKeys("n"),
			key.WithHelp("n", "add job"),
		),
		Implement: key.NewBinding(
			key.WithKeys("i"),
			key.WithHelp("i", "implement selected"),
		),
		ToggleSummaries: key.NewBinding(
			key.WithKeys("s"),
			key.WithHelp("s", "toggle summaries"),
		),
		GoToTop: key.NewBinding(
			key.WithKeys("g"),
			key.WithHelp("gg", "go to top"),
		),
		GoToBottom: key.NewBinding(
			key.WithKeys("G"),
			key.WithHelp("G", "go to bottom"),
		),
		PageUp: key.NewBinding(
			key.WithKeys("ctrl+u"),
			key.WithHelp("ctrl+u", "page up"),
		),
		PageDown: key.NewBinding(
			key.WithKeys("ctrl+d"),
			key.WithHelp("ctrl+d", "page down"),
		),
	}
}

func (k statusTUIKeyMap) ShortHelp() []key.Binding {
	// Return just quit - help is shown automatically by the help component
	return []key.Binding{k.Quit}
}

func (k statusTUIKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{
			key.NewBinding(key.WithKeys(""), key.WithHelp("", "Navigation")),
			k.Up,
			k.Down,
			k.GoToTop,
			k.GoToBottom,
			k.PageUp,
			k.PageDown,
			k.Select,
			k.ToggleMulti,
			k.ToggleSummaries,
		},
		{
			key.NewBinding(key.WithKeys(""), key.WithHelp("", "Actions")),
			k.Run,
			k.Edit,
			k.SetCompleted,
			k.SetStatus,
			k.AddJob,
			k.Implement,
			k.Archive,
			k.Help,
			k.Quit,
		},
	}
}

// Status TUI model represents the state of the TUI
type statusTUIModel struct {
	plan          *orchestration.Plan
	graph         *orchestration.DependencyGraph
	jobs          []*orchestration.Job
	jobParents    map[string]*orchestration.Job // Track parent in tree structure
	jobIndents    map[string]int               // Track indentation level
	cursor        int
	scrollOffset  int                         // Track scroll position for viewport
	selected      map[string]bool // For multi-select
	multiSelect   bool
	showSummaries bool   // Toggle for showing job summaries
	statusSummary string
	err           error
	width         int
	height        int
	confirmArchive bool  // Show archive confirmation
	showStatusPicker bool // Show status picker
	statusPickerCursor int // Cursor position in status picker
	planDir       string // Store plan directory for refresh
	keyMap        statusTUIKeyMap
	help          help.Model
	waitingForG   bool   // Track if we're waiting for second 'g' in 'gg' sequence
	cursorVisible bool   // Track cursor visibility for blinking animation
}


// getStatusStyles returns theme-based styles for job statuses with subtle colors
func getStatusStyles() map[orchestration.JobStatus]lipgloss.Style {
	return map[orchestration.JobStatus]lipgloss.Style{
		// Completed: Subtle spring green
		orchestration.JobStatusCompleted: lipgloss.NewStyle().Foreground(theme.DefaultColors.Green),
		// Running: Soft blue instead of bold yellow
		orchestration.JobStatusRunning: lipgloss.NewStyle().Foreground(theme.DefaultColors.Blue),
		// Failed: Muted pink instead of bright red
		orchestration.JobStatusFailed: lipgloss.NewStyle().Foreground(theme.DefaultColors.Pink),
		// Blocked: Muted pink
		orchestration.JobStatusBlocked: lipgloss.NewStyle().Foreground(theme.DefaultColors.Pink),
		// Needs Review: Soft cyan
		orchestration.JobStatusNeedsReview: lipgloss.NewStyle().Foreground(theme.DefaultColors.Cyan),
		// Pending User: Soft violet accent
		orchestration.JobStatusPendingUser: lipgloss.NewStyle().Foreground(theme.DefaultColors.Violet),
		// Pending LLM: Soft blue
		orchestration.JobStatusPendingLLM: lipgloss.NewStyle().Foreground(theme.DefaultColors.Blue),
		// Pending: Muted gray for both dot and text
		orchestration.JobStatusPending: theme.DefaultTheme.Muted,
		// New statuses
		orchestration.JobStatusTodo:      theme.DefaultTheme.Muted,
		orchestration.JobStatusHold:      lipgloss.NewStyle().Foreground(theme.DefaultColors.Yellow),
		orchestration.JobStatusAbandoned: theme.DefaultTheme.Faint,
	}
}

// Initialize the model
func newStatusTUIModel(plan *orchestration.Plan, graph *orchestration.DependencyGraph) statusTUIModel {
	// Flatten the job tree for navigation with parent tracking
	jobs, parents, indents := flattenJobTreeWithParents(plan)

	return statusTUIModel{
		plan:          plan,
		graph:         graph,
		jobs:          jobs,
		jobParents:    parents,
		jobIndents:    indents,
		cursor:        0,
		scrollOffset:  0,
		selected:      make(map[string]bool),
		multiSelect:   false,
		statusSummary: formatStatusSummary(plan),
		confirmArchive: false,
		planDir:       plan.Directory,
		keyMap:        newStatusTUIKeyMap(),
		help:          help.New(newStatusTUIKeyMap()),
		cursorVisible: true,
	}
}

// getVisibleJobCount returns how many jobs can be displayed in the viewport
func (m *statusTUIModel) getVisibleJobCount() int {
	if m.height == 0 {
		return 10 // default
	}

	// Calculate available height for job list
	// Account for: header (2 lines), scroll indicator (1 line), footer (1 line), margins (4 lines)
	// Total UI chrome: ~8 lines
	availableHeight := m.height - 8
	if availableHeight < 1 {
		availableHeight = 1
	}

	// If summaries are shown, each job might take 2 lines
	if m.showSummaries {
		availableHeight = availableHeight / 2
		if availableHeight < 1 {
			availableHeight = 1
		}
	}

	return availableHeight
}

// adjustScrollOffset ensures the cursor is visible within the viewport
func (m *statusTUIModel) adjustScrollOffset() {
	visibleLines := m.getVisibleJobCount()

	// Adjust scroll offset to keep cursor visible
	if m.cursor < m.scrollOffset {
		// Cursor is above viewport, scroll up
		m.scrollOffset = m.cursor
	} else if m.cursor >= m.scrollOffset+visibleLines {
		// Cursor is below viewport, scroll down
		m.scrollOffset = m.cursor - visibleLines + 1
	}

	// Ensure scrollOffset doesn't go negative
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}
}

// flattenJobTreeWithParents creates a flat list of jobs in tree order with parent tracking
func flattenJobTreeWithParents(plan *orchestration.Plan) ([]*orchestration.Job, map[string]*orchestration.Job, map[string]int) {
	var result []*orchestration.Job
	visited := make(map[string]bool)
	parents := make(map[string]*orchestration.Job)
	indents := make(map[string]int)
	
	// Find root jobs
	roots := findRootJobs(plan)
	
	// Add each root and its dependents
	for _, root := range roots {
		addJobAndDependentsWithParent(root, plan, &result, visited, parents, indents, nil, 0)
	}
	
	// Add any orphaned jobs
	for _, job := range plan.Jobs {
		if !visited[job.ID] {
			result = append(result, job)
			parents[job.ID] = nil
			indents[job.ID] = 0
		}
	}
	
	return result, parents, indents
}

// addJobAndDependentsWithParent recursively adds a job and its dependents with parent tracking
func addJobAndDependentsWithParent(job *orchestration.Job, plan *orchestration.Plan, result *[]*orchestration.Job, visited map[string]bool, parents map[string]*orchestration.Job, indents map[string]int, parent *orchestration.Job, indent int) {
	if visited[job.ID] {
		return
	}
	visited[job.ID] = true
	*result = append(*result, job)
	parents[job.ID] = parent
	indents[job.ID] = indent
	
	// Find and add dependents using the same logic as vanilla status
	// This ensures jobs appear under their dependency with maximum height
	dependents := findDependents(job, plan)
	for _, dep := range dependents {
		addJobAndDependentsWithParent(dep, plan, result, visited, parents, indents, job, indent+1)
	}
}

// flattenJobTree creates a flat list of jobs in tree order (kept for compatibility)
func flattenJobTree(plan *orchestration.Plan) []*orchestration.Job {
	jobs, _, _ := flattenJobTreeWithParents(plan)
	return jobs
}

// addJobAndDependents recursively adds a job and its dependents to the result
func addJobAndDependents(job *orchestration.Job, plan *orchestration.Plan, result *[]*orchestration.Job, visited map[string]bool) {
	if visited[job.ID] {
		return
	}
	visited[job.ID] = true
	*result = append(*result, job)
	
	// Find and add dependents using the same logic as vanilla status
	// This ensures jobs appear under their dependency with maximum height
	dependents := findDependents(job, plan)
	for _, dep := range dependents {
		addJobAndDependents(dep, plan, result, visited)
	}
}

// Messages
type refreshMsg struct{}
type archiveConfirmedMsg struct{ job *orchestration.Job }
type editFileAndQuitMsg struct{ filePath string }
type tickMsg time.Time
type refreshTickMsg time.Time

const refreshInterval = 2 * time.Second

// blink returns a command that sends a tick message every 500ms for cursor blinking
func blink() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// refreshTick returns a command that sends a refresh message periodically
func refreshTick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg {
		return refreshTickMsg(t)
	})
}

// Init initializes the TUI
func (m statusTUIModel) Init() tea.Cmd {
	return tea.Batch(
		blink(),
		refreshTick(),
	)
}

// refreshPlan reloads the plan from disk
func refreshPlan(planDir string) tea.Cmd {
	return func() tea.Msg {
		return refreshMsg{}
	}
}

// Update handles messages
func (m statusTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		// Toggle cursor visibility for blinking effect
		m.cursorVisible = !m.cursorVisible
		return m, blink() // Schedule next tick

	case refreshTickMsg:
		return m, tea.Batch(
			refreshPlan(m.planDir),
			refreshTick(),
		)

	case refreshMsg:
		// Reload the plan
		plan, err := orchestration.LoadPlan(m.planDir)
		if err != nil {
			m.err = err
			return m, nil
		}
		
		graph, err := orchestration.BuildDependencyGraph(plan)
		if err != nil {
			m.err = err
			return m, nil
		}
		
		// Update model with refreshed data
		m.plan = plan
		m.graph = graph
		jobs, parents, indents := flattenJobTreeWithParents(plan)
		m.jobs = jobs
		m.jobParents = parents
		m.jobIndents = indents
		m.statusSummary = formatStatusSummary(plan)
		
		// Adjust cursor if needed
		if m.cursor >= len(m.jobs) {
			m.cursor = len(m.jobs) - 1
		}
		if m.cursor < 0 && len(m.jobs) > 0 {
			m.cursor = 0
		}
		
		// Clear selections that no longer exist
		newSelected := make(map[string]bool)
		for id := range m.selected {
			for _, job := range m.jobs {
				if job.ID == id {
					newSelected[id] = true
					break
				}
			}
		}
		m.selected = newSelected
		
		return m, nil

	case archiveConfirmedMsg:
		// Perform the actual archive
		return m, tea.Sequence(
			doArchiveJob(m.planDir, msg.job),
			refreshPlan(m.planDir),
		)

	case editFileAndQuitMsg:
		// Print protocol string and quit - Neovim plugin will handle the file opening
		fmt.Printf("EDIT_FILE:%s\n", msg.filePath)
		return m, tea.Quit

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		// Handle status picker first
		if m.showStatusPicker {
			switch msg.String() {
			case "up", "k":
				if m.statusPickerCursor > 0 {
					m.statusPickerCursor--
				}
				return m, nil
			case "down", "j":
				if m.statusPickerCursor < 8 { // 9 status options (0-8)
					m.statusPickerCursor++
				}
				return m, nil
			case "enter":
				m.showStatusPicker = false
				if m.cursor < len(m.jobs) {
					job := m.jobs[m.cursor]
					statuses := []orchestration.JobStatus{
						orchestration.JobStatusPending,
						orchestration.JobStatusTodo,
						orchestration.JobStatusHold,
						orchestration.JobStatusRunning,
						orchestration.JobStatusCompleted,
						orchestration.JobStatusFailed,
						orchestration.JobStatusBlocked,
						orchestration.JobStatusNeedsReview,
						orchestration.JobStatusAbandoned,
					}
					return m, tea.Sequence(
						setJobStatus(job, m.plan, statuses[m.statusPickerCursor]),
						refreshPlan(m.planDir),
					)
				}
				return m, nil
			case "esc", "ctrl+c", "q", "b":
				m.showStatusPicker = false
				return m, nil
			default:
				// Any other key while status picker is open - just consume it
				return m, nil
			}
		}

		// Handle confirmation dialog
		if m.confirmArchive {
			switch msg.String() {
			case "y", "Y":
				m.confirmArchive = false
				if m.cursor < len(m.jobs) {
					job := m.jobs[m.cursor]
					return m, func() tea.Msg { return archiveConfirmedMsg{job: job} }
				}
			case "n", "N", "ctrl+c", "q":
				m.confirmArchive = false
			}
			return m, nil
		}
		// Handle 'gg' sequence for going to top
		if msg.String() == "g" {
			if m.waitingForG {
				// Second 'g' - go to top
				m.cursor = 0
				m.scrollOffset = 0
				m.waitingForG = false
			} else {
				// First 'g' - wait for second
				m.waitingForG = true
			}
			return m, nil
		} else {
			// Any other key resets the 'g' waiting state
			m.waitingForG = false
		}

		switch {
		case key.Matches(msg, m.keyMap.Quit):
			return m, tea.Quit

		case key.Matches(msg, m.keyMap.Help):
			m.help.ShowAll = !m.help.ShowAll

		case key.Matches(msg, m.keyMap.Up):
			if m.cursor > 0 {
				m.cursor--
				m.adjustScrollOffset()
			}

		case key.Matches(msg, m.keyMap.Down):
			if m.cursor < len(m.jobs)-1 {
				m.cursor++
				m.adjustScrollOffset()
			}

		case key.Matches(msg, m.keyMap.GoToBottom):
			if len(m.jobs) > 0 {
				m.cursor = len(m.jobs) - 1
				m.adjustScrollOffset()
			}

		case key.Matches(msg, m.keyMap.PageUp):
			pageSize := 10
			m.cursor -= pageSize
			if m.cursor < 0 {
				m.cursor = 0
			}
			m.adjustScrollOffset()

		case key.Matches(msg, m.keyMap.PageDown):
			pageSize := 10
			m.cursor += pageSize
			if m.cursor >= len(m.jobs) {
				m.cursor = len(m.jobs) - 1
			}
			m.adjustScrollOffset()

		case key.Matches(msg, m.keyMap.Select):
			if m.multiSelect && m.cursor < len(m.jobs) {
				job := m.jobs[m.cursor]
				if m.selected[job.ID] {
					delete(m.selected, job.ID)
				} else {
					m.selected[job.ID] = true
				}
			}

		case key.Matches(msg, m.keyMap.ToggleMulti):
			m.multiSelect = !m.multiSelect
			if !m.multiSelect {
				// Clear selections when exiting multi-select mode
				m.selected = make(map[string]bool)
			}

		case key.Matches(msg, m.keyMap.Archive):
			if m.cursor < len(m.jobs) {
				m.confirmArchive = true
			}

		case key.Matches(msg, m.keyMap.Edit):
			if m.cursor < len(m.jobs) {
				job := m.jobs[m.cursor]
				return m, editJob(job)
			}

		case key.Matches(msg, m.keyMap.Run):
			if m.cursor < len(m.jobs) {
				job := m.jobs[m.cursor]
				return m, runJob(m.plan.Directory, job)
			}

		case key.Matches(msg, m.keyMap.SetCompleted):
			if m.cursor < len(m.jobs) {
				job := m.jobs[m.cursor]
				return m, tea.Sequence(
					setJobCompleted(job, m.plan),
					refreshPlan(m.planDir),
				)
			}

		case key.Matches(msg, m.keyMap.SetStatus):
			if m.cursor < len(m.jobs) {
				m.showStatusPicker = true
				m.statusPickerCursor = 0
			}

		case key.Matches(msg, m.keyMap.AddJob):
			if m.multiSelect && len(m.selected) > 0 {
				// Get selected job IDs for dependencies
				var deps []string
				for id := range m.selected {
					for _, job := range m.jobs {
						if job.ID == id {
							deps = append(deps, job.Filename)
							break
						}
					}
				}
				return m, addJobWithDependencies(m.plan.Directory, deps)
			} else {
				return m, addJobWithDependencies(m.plan.Directory, nil)
			}

		case key.Matches(msg, m.keyMap.Implement):
			if m.cursor < len(m.jobs) {
				job := m.jobs[m.cursor]
				return m, createImplementationJob(m.plan, job)
			}

		case key.Matches(msg, m.keyMap.ToggleSummaries):
			m.showSummaries = !m.showSummaries
		}
	}

	return m, nil
}

// View renders the TUI
func (m statusTUIModel) View() string {
	if m.err != nil {
		return fmt.Sprintf("Error: %v\n", m.err)
	}

	// Show status picker if active
	if m.showStatusPicker {
		return m.renderStatusPicker()
	}

	// Show help if active
	if m.help.ShowAll {
		iconLegend := lipgloss.NewStyle().
			MarginTop(1).
			MarginBottom(1).
			Render("Status Icons:\n  ● Completed  ◐ Running  ✗ Failed/Blocked  ○ Pending")

		return lipgloss.JoinVertical(lipgloss.Left,
			theme.DefaultTheme.Header.Render("📈 Plan Status - Help"),
			m.help.View(),
			iconLegend,
		)
	}

	// Calculate content width accounting for margins
	contentWidth := m.width - 4
	if contentWidth < 40 {
		contentWidth = 40
	}

	// 1. Create Header with subtle coloring
	headerLabel := lipgloss.NewStyle().
		Foreground(theme.DefaultTheme.Header.GetForeground()).
		Bold(true).
		Render("📈 Plan Status: ")

	planName := lipgloss.NewStyle().
		Foreground(theme.DefaultColors.Orange).
		Bold(true).
		Render(m.plan.Name)

	headerText := headerLabel + planName

	styledHeader := lipgloss.NewStyle().
		Background(theme.DefaultTheme.Header.GetBackground()).
		Align(lipgloss.Left).
		Width(contentWidth).
		MarginBottom(1).
		Render(headerText)

	// 2. Render Job Tree
	jobTree := m.renderJobTree()

	// 2b. Add scroll indicators if needed
	scrollIndicator := ""
	if len(m.jobs) > 0 {
		visibleLines := m.getVisibleJobCount()
		hasMore := m.scrollOffset+visibleLines < len(m.jobs)
		hasLess := m.scrollOffset > 0

		if hasLess || hasMore {
			indicator := ""
			if hasLess {
				indicator += "↑ "
			}
			indicator += fmt.Sprintf("[%d/%d]", m.cursor+1, len(m.jobs))
			if hasMore {
				indicator += " ↓"
			}
			scrollIndicator = "\n" + theme.DefaultTheme.Muted.Render(indicator)
		}
	}

	// 3. Handle confirmation dialog or help footer
	var footer string
	if m.confirmArchive {
		if m.cursor < len(m.jobs) {
			job := m.jobs[m.cursor]
			footer = "\n" + theme.DefaultTheme.Warning.
				Bold(true).
				Render(fmt.Sprintf("Archive '%s'? (y/n)", job.Filename))
		}
	} else {
		// Render Footer
		footer = m.help.View()
	}

	// 4. Combine everything
	finalView := lipgloss.JoinVertical(
		lipgloss.Left,
		styledHeader,
		jobTree,
		scrollIndicator,
		"\n", // Space before footer
		footer,
	)

	// Add overall margin
	return lipgloss.NewStyle().Margin(1, 2).Render(finalView)
}

// stripANSI removes ANSI escape sequences from a string
func stripANSI(str string) string {
	ansiRegex := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	return ansiRegex.ReplaceAllString(str, "")
}

// renderJobTree renders the job tree with proper indentation
func (m statusTUIModel) renderJobTree() string {
	var s strings.Builder

	// Calculate viewport bounds using the shared helper
	visibleLines := m.getVisibleJobCount()
	visibleStart := m.scrollOffset
	visibleEnd := m.scrollOffset + visibleLines

	// Ensure we don't go past the end
	if visibleEnd > len(m.jobs) {
		visibleEnd = len(m.jobs)
	}

	// Use the pre-calculated parent and indent information
	rendered := make(map[string]bool)

	// Render with tree characters - only render visible jobs
	for i, job := range m.jobs {
		// Skip jobs outside the visible viewport
		if i < visibleStart || i >= visibleEnd {
			continue
		}

		indent := m.jobIndents[job.ID]
		prefix := strings.Repeat("    ", indent)
		
		// Determine if this is the last job at this indent level
		isLast := true
		for j := i + 1; j < len(m.jobs); j++ {
			if m.jobIndents[m.jobs[j].ID] == indent {
				isLast = false
				break
			}
		}
		
		// Build the tree prefix
		treeChar := "├── "
		if isLast {
			treeChar = "└── "
		}

		// Build tree structure part
		treePart := fmt.Sprintf("  %s%s", prefix, treeChar)

		// Build job content with status icon
		statusIcon := m.getStatusIcon(job.Status)

		// Determine text style based on cursor/selection state
		var filenameStyle lipgloss.Style
		var titleStyle lipgloss.Style

		if i == m.cursor {
			// Cursor: check status for filename color
			if job.Status == orchestration.JobStatusCompleted {
				filenameStyle = lipgloss.NewStyle().Foreground(theme.DefaultColors.LightText)
			} else {
				filenameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
			}
			titleStyle = lipgloss.NewStyle().
				Foreground(theme.DefaultColors.Pink).
				Background(theme.DefaultColors.SelectedBackground)
		} else if m.selected[job.ID] {
			// Selected: use accent color
			filenameStyle = lipgloss.NewStyle().Foreground(theme.DefaultColors.Violet)
			titleStyle = theme.DefaultTheme.Muted
		} else {
			// Normal: check if completed, use light text; otherwise brighter gray
			if job.Status == orchestration.JobStatusCompleted {
				filenameStyle = lipgloss.NewStyle().Foreground(theme.DefaultColors.LightText)
			} else {
				filenameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
			}
			titleStyle = theme.DefaultTheme.Muted
		}

		coloredFilename := filenameStyle.Render(job.Filename)

		// Get job type badge with subtle blue/cyan color
		jobTypeBadge := lipgloss.NewStyle().
			Foreground(lipgloss.Color("69")). // Subtle blue
			Render(fmt.Sprintf("[%s]", job.Type))

		// Build content without emoji (add emoji separately to avoid background bleed)
		var textContent string
		if job.Title != "" {
			// Render title in appropriate style
			textContent = fmt.Sprintf("%s %s %s", coloredFilename,
				titleStyle.Render(fmt.Sprintf("(%s)", job.Title)),
				jobTypeBadge)
		} else {
			textContent = fmt.Sprintf("%s %s", coloredFilename, jobTypeBadge)
		}

		// Check for missing dependencies
		var hasMissingDeps bool
		for _, dep := range job.Dependencies {
			if dep == nil {
				hasMissingDeps = true
				break
			}
		}
		if hasMissingDeps {
			textContent += " " + theme.DefaultTheme.Error.Render("[? missing deps]")
		}
		// Combine emoji (no background) with styled text content
		styledJobContent := statusIcon + " " + textContent

		// Build indicators separately with their own style (foreground only, no background)
		indicators := ""
		if m.selected[job.ID] {
			indicators += lipgloss.NewStyle().
				Foreground(theme.DefaultTheme.Accent.GetForeground()).
				Render(" ◆")
		}
		if i == m.cursor {
			cursorChar := " "
			if m.cursorVisible {
				cursorChar = "◀"
			}
			indicators += lipgloss.NewStyle().
				Foreground(theme.DefaultColors.Orange).
				Render(" " + cursorChar)
		}

		// Combine all parts
		fullLine := treePart + styledJobContent + indicators
		
		// Add summary on a new line if toggled on and available
		if m.showSummaries && job.Summary != "" {
			summaryStyle := theme.DefaultTheme.Info.
				PaddingLeft(indent*4 + 6) // indent level * 4 spaces + tree chars + space
			
			fullLine += "\n" + summaryStyle.Render("↳ "+job.Summary)
		}
		
		s.WriteString(fullLine + "\n")
		rendered[job.ID] = true
	}

	return s.String()
}

// getStatusIcon returns a colored dot indicator for a job status
func (m statusTUIModel) getStatusIcon(status orchestration.JobStatus) string {
	statusStyles := getStatusStyles()
	icon := "●" // Solid dot for completed
	style := theme.DefaultTheme.Muted

	// Use different icons for different statuses
	switch status {
	case orchestration.JobStatusCompleted:
		icon = "●" // Solid dot
	case orchestration.JobStatusRunning:
		icon = "◐" // Half-filled circle
	case orchestration.JobStatusFailed, orchestration.JobStatusBlocked:
		icon = "✗" // X mark
	case orchestration.JobStatusTodo:
		icon = "📝" // Todo icon
	case orchestration.JobStatusHold:
		icon = "⏸" // Pause symbol
	case orchestration.JobStatusAbandoned:
		icon = "-" // Dash for abandoned
	default:
		// Pending, PendingUser, PendingLLM, NeedsReview
		icon = "○" // Hollow circle
	}

	// Use the status style to color the icon
	if s, ok := statusStyles[status]; ok {
		style = s
	}

	return style.Render(icon)
}


// Command functions that return tea.Cmd

func doArchiveJob(planDir string, job *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		// Archive the job by moving it to an archive directory
		archiveDir := filepath.Join(planDir, ".archive")
		if err := os.MkdirAll(archiveDir, 0755); err != nil {
			return err
		}
		
		oldPath := filepath.Join(planDir, job.Filename)
		newPath := filepath.Join(archiveDir, job.Filename)
		
		if err := os.Rename(oldPath, newPath); err != nil {
			return err
		}
		
		return nil // Just return nil, we'll refresh after
	}
}

func editJob(job *orchestration.Job) tea.Cmd {
	// If running inside Neovim plugin, signal to quit and let plugin handle editing
	if os.Getenv("GROVE_NVIM_PLUGIN") == "true" {
		return func() tea.Msg {
			return editFileAndQuitMsg{filePath: job.FilePath}
		}
	}

	// Open the job file in the default editor
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	// Use tea.ExecProcess to properly handle terminal control
	return tea.ExecProcess(exec.Command(editor, job.FilePath), func(err error) tea.Msg {
		if err != nil {
			return err
		}
		return refreshMsg{} // Refresh to show any changes
	})
}

func runJob(planDir string, job *orchestration.Job) tea.Cmd {
	// Run the job using flow plan run
	return tea.ExecProcess(exec.Command("flow", "plan", "run", job.FilePath), func(err error) tea.Msg {
		if err != nil {
			return err
		}
		return refreshMsg{} // Refresh to show status changes
	})
}

func setJobCompleted(job *orchestration.Job, plan *orchestration.Plan) tea.Cmd {
	return func() tea.Msg {
		// Use the shared completion function (silent mode for TUI)
		if err := completeJob(job, plan, true); err != nil {
			return err
		}
		return refreshMsg{} // Refresh to show the status change
	}
}

func setJobStatus(job *orchestration.Job, plan *orchestration.Plan, status orchestration.JobStatus) tea.Cmd {
	return func() tea.Msg {
		sp := orchestration.NewStatePersister()
		if err := sp.UpdateJobStatus(job, status); err != nil {
			return err
		}
		return refreshMsg{} // Refresh to show the status change
	}
}

func (m statusTUIModel) renderStatusPicker() string {
	t := theme.DefaultTheme

	statusOptions := []struct {
		status orchestration.JobStatus
		label  string
		icon   string
	}{
		{orchestration.JobStatusPending, "Pending", "⏳"},
		{orchestration.JobStatusTodo, "Todo", "📝"},
		{orchestration.JobStatusHold, "On Hold", "⏸"},
		{orchestration.JobStatusRunning, "Running", "⚡"},
		{orchestration.JobStatusCompleted, "Completed", "✓"},
		{orchestration.JobStatusFailed, "Failed", "✗"},
		{orchestration.JobStatusBlocked, "Blocked", "🚫"},
		{orchestration.JobStatusNeedsReview, "Needs Review", "👁"},
		{orchestration.JobStatusAbandoned, "Abandoned", "🗑️"},
	}

	var lines []string

	// Add title
	if m.cursor < len(m.jobs) {
		job := m.jobs[m.cursor]
		title := lipgloss.NewStyle().
			Bold(true).
			Foreground(theme.DefaultColors.Cyan).
			Render(fmt.Sprintf("Set Status for: %s", job.Filename))
		lines = append(lines, title)
		lines = append(lines, "")
	}

	// Add status options
	for i, opt := range statusOptions {
		prefix := "  "
		var style lipgloss.Style

		if i == m.statusPickerCursor {
			prefix = "▸ "
			style = lipgloss.NewStyle().
				Foreground(theme.DefaultColors.Cyan).
				Bold(true).
				Background(theme.DefaultColors.SubtleBackground)
		} else {
			style = t.Muted
		}

		line := fmt.Sprintf("%s%s %s", prefix, opt.icon, opt.label)
		lines = append(lines, style.Render(line))
	}

	lines = append(lines, "")

	// Add help text at bottom
	help := lipgloss.NewStyle().
		Foreground(theme.DefaultColors.MutedText).
		Render("↑/↓ or j/k to navigate • Enter to select • Esc/b to go back")
	lines = append(lines, help)

	content := strings.Join(lines, "\n")

	// Wrap in a box with border
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultColors.Border).
		Padding(1, 2).
		Render(content)

	// Add margin to position it slightly from the edge
	return lipgloss.NewStyle().
		Margin(1, 2).
		Render(box)
}

func addJobWithDependencies(planDir string, dependencies []string) tea.Cmd {
	// Build the command
	args := []string{"plan", "add", planDir, "-i"}

	// Add dependencies if provided
	for _, dep := range dependencies {
		args = append(args, "-d", dep)
	}

	// Run flow plan add in interactive mode
	return tea.ExecProcess(exec.Command("flow", args...), func(err error) tea.Msg {
		if err != nil {
			return err
		}
		return refreshMsg{} // Refresh to show the new job
	})
}

// createImplementationJob creates a new interactive_agent job with "impl-" prefix
// that depends on the selected job
func createImplementationJob(plan *orchestration.Plan, selectedJob *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		// Create the implementation job title
		implTitle := fmt.Sprintf("impl-%s", selectedJob.Title)

		// Generate a unique ID for the new job
		implID := GenerateJobIDFromTitle(plan, implTitle)

		// Create the new job
		newJob := &orchestration.Job{
			ID:        implID,
			Title:     implTitle,
			Type:      orchestration.JobTypeInteractiveAgent,
			Status:    orchestration.JobStatusPending,
			DependsOn: []string{selectedJob.ID},
			Worktree:  selectedJob.Worktree,
			Output: orchestration.OutputConfig{
				Type: "file",
			},
		}

		// Add the job to the plan
		_, err := orchestration.AddJob(plan, newJob)
		if err != nil {
			return fmt.Errorf("failed to create implementation job: %w", err)
		}

		// Return refresh message to update the TUI
		return refreshMsg{}
	}
}

// runStatusTUI runs the interactive TUI for plan status
func runStatusTUI(plan *orchestration.Plan, graph *orchestration.DependencyGraph) error {
	model := newStatusTUIModel(plan, graph)

	// Use alt screen only when not in Neovim (to fix screen duplication)
	// But disable it in Neovim to allow editor functionality
	var opts []tea.ProgramOption
	if os.Getenv("GROVE_NVIM_PLUGIN") != "true" {
		opts = append(opts, tea.WithAltScreen())
	}
	opts = append(opts, tea.WithOutput(os.Stderr))

	program := tea.NewProgram(model, opts...)

	if _, err := program.Run(); err != nil {
		return fmt.Errorf("error running status TUI: %w", err)
	}

	return nil
}