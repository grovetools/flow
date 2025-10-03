package cmd

import (
	"context"
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
	AddJob        key.Binding
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
		AddJob: key.NewBinding(
			key.WithKeys("n"),
			key.WithHelp("n", "add job"),
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
			k.AddJob,
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
	selected      map[string]bool // For multi-select
	multiSelect   bool
	showSummaries bool   // Toggle for showing job summaries
	statusSummary string
	err           error
	width         int
	height        int
	confirmArchive bool  // Show archive confirmation
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

// blink returns a command that sends a tick message every 500ms for cursor blinking
func blink() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Init initializes the TUI
func (m statusTUIModel) Init() tea.Cmd {
	return blink()
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
		// Handle confirmation dialog first
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
			}

		case key.Matches(msg, m.keyMap.Down):
			if m.cursor < len(m.jobs)-1 {
				m.cursor++
			}

		case key.Matches(msg, m.keyMap.GoToBottom):
			if len(m.jobs) > 0 {
				m.cursor = len(m.jobs) - 1
			}

		case key.Matches(msg, m.keyMap.PageUp):
			pageSize := 10
			m.cursor -= pageSize
			if m.cursor < 0 {
				m.cursor = 0
			}

		case key.Matches(msg, m.keyMap.PageDown):
			pageSize := 10
			m.cursor += pageSize
			if m.cursor >= len(m.jobs) {
				m.cursor = len(m.jobs) - 1
			}

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

	// Use the pre-calculated parent and indent information
	rendered := make(map[string]bool)

	// Render with tree characters
	for i, job := range m.jobs {
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
		// Create a state persister to update the job status
		sp := orchestration.NewStatePersister()
		
		// Update the job status to completed
		if err := sp.UpdateJobStatus(job, orchestration.JobStatusCompleted); err != nil {
			return err
		}
		
		// Append transcript if it's an interactive agent job
		if job.Type == orchestration.JobTypeInteractiveAgent {
			if err := orchestration.AppendInteractiveTranscript(job, plan); err != nil {
				// Return the error to be displayed by the TUI
				return err
			}
		}
		
		// Summarize the job content if enabled
		flowCfg, err := loadFlowConfig()
		if err != nil {
			// Don't fail the whole operation, just return the error to be displayed
			return fmt.Errorf("could not load flow config for summarization: %w", err)
		}
		
		if flowCfg.SummarizeOnComplete {
			summaryCfg := orchestration.SummaryConfig{
				Enabled:  flowCfg.SummarizeOnComplete,
				Model:    flowCfg.SummaryModel,
				Prompt:   flowCfg.SummaryPrompt,
				MaxChars: flowCfg.SummaryMaxChars,
			}
			
			summary, err := orchestration.SummarizeJobContent(context.Background(), job, plan, summaryCfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to generate summary: %v\n", err)
			} else if summary != "" {
				_ = orchestration.AddSummaryToJobFile(job, summary) // Ignore error in TUI for now
			}
		}
		
		return refreshMsg{} // Refresh to show the status change
	}
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

// runStatusTUI runs the interactive TUI for plan status
func runStatusTUI(plan *orchestration.Plan, graph *orchestration.DependencyGraph) error {
	model := newStatusTUIModel(plan, graph)
	program := tea.NewProgram(model, tea.WithOutput(os.Stderr))

	if _, err := program.Run(); err != nil {
		return fmt.Errorf("error running status TUI: %w", err)
	}

	return nil
}