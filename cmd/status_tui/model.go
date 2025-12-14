package status_tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattsolo1/grove-core/tui/components/help"
	"github.com/mattsolo1/grove-core/tui/components/logviewer"
	"github.com/mattsolo1/grove-core/tui/theme"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
)

// initProgramRef is a package-level variable to store the program reference
// so it can be set in the model's Init method
var initProgramRef *tea.Program

type ViewMode int

const (
	TableView ViewMode = iota
	TreeView
)

type ViewFocus int

const (
	JobsPane ViewFocus = iota
	LogsPane
)

func (v ViewMode) String() string {
	return [...]string{"table", "tree"}[v]
}

// Model represents the state of the TUI
type Model struct {
	Plan               *orchestration.Plan
	Graph              *orchestration.DependencyGraph
	Orchestrator       *orchestration.Orchestrator // Direct orchestrator for job execution
	Jobs               []*orchestration.Job
	JobParents         map[string]*orchestration.Job // Track parent in tree structure
	JobIndents         map[string]int                // Track indentation level
	Cursor             int
	ScrollOffset       int             // Track scroll position for viewport
	Selected           map[string]bool // For multi-select
	ShowSummaries      bool            // Toggle for showing job summaries
	StatusSummary      string
	Err                error
	Width              int
	Height             int
	ConfirmArchive     bool // Show archive confirmation
	ShowStatusPicker   bool // Show status picker
	StatusPickerCursor int  // Cursor position in status picker
	PlanDir            string // Store plan directory for refresh
	KeyMap             KeyMap
	Help               help.Model
	WaitingForG        bool      // Track if we're waiting for second 'g' in 'gg' sequence
	CursorVisible      bool      // Track cursor visibility for blinking animation
	ViewMode           ViewMode  // Current view mode (table or tree)
	Renaming           bool
	RenameInput        textinput.Model
	RenameJobIndex     int
	EditingDeps        bool
	EditDepsJobIndex   int
	EditDepsSelected   map[string]bool // Track which jobs are selected as dependencies
	CreatingJob        bool
	CreateJobInput     textinput.Model
	CreateJobType      string // "xml" or "impl"
	CreateJobBaseJob   *orchestration.Job
	CreateJobDeps      []*orchestration.Job // For multi-select case
	ShowLogs           bool
	LogViewer          logviewer.Model
	ActiveLogJob     *orchestration.Job
	Focus            ViewFocus // Track which pane is active
	LogSplitVertical bool      // Track log viewer layout
	IsRunningJob     bool      // Track if a job is currently running
	RunLogFile         string    // Path to temporary log file for job output
	Program            *tea.Program // Reference to the tea.Program for sending messages
	LogViewerWidth     int       // Cached log viewer width
	LogViewerHeight    int       // Cached log viewer height
}

// New creates a new Model
func New(plan *orchestration.Plan, graph *orchestration.DependencyGraph) Model {
	// Set TUI mode env var early so loggers are configured correctly
	os.Setenv("GROVE_FLOW_TUI_MODE", "true")

	// Flatten the job tree for navigation with parent tracking
	jobs, parents, indents := flattenJobTreeWithParents(plan)

	keyMap := NewKeyMap()
	helpModel := help.NewBuilder().
		WithKeys(keyMap).
		WithTitle("Plan Status - Help").
		Build()

	logViewerModel := logviewer.New(80, 20) // Initial size, will be updated

	// Create orchestrator for direct job execution
	orchConfig := &orchestration.OrchestratorConfig{
		MaxParallelJobs:     1,    // TUI runs one job or selection at a time
		CheckInterval:       5 * time.Second,
		MaxConsecutiveSteps: 20,
		SkipInteractive:     true, // Don't prompt for user input in TUI mode
	}

	// Create the orchestrator instance
	orch, err := orchestration.NewOrchestrator(plan, orchConfig)
	if err != nil {
		// Log error but continue - the old path can still work
		fmt.Fprintf(os.Stderr, "Warning: Failed to create orchestrator for TUI: %v\n", err)
	}

	// Create a temporary log file in the project's .grove/logs directory (kept for compatibility)
	logsDir := filepath.Join(plan.Directory, ".grove", "logs")
	os.MkdirAll(logsDir, 0755)
	logFile, err2 := os.CreateTemp(logsDir, "flow-tui-run-*.log")
	var runLogPath string
	if err2 == nil {
		runLogPath = logFile.Name()
		logFile.Close() // Close it for now, it will be truncated on each run
	}

	// Start cursor at the bottom-most row
	initialCursor := 0
	if len(jobs) > 0 {
		initialCursor = len(jobs) - 1
	}

	return Model{
		Plan:             plan,
		Graph:            graph,
		Orchestrator:     orch,
		Jobs:             jobs,
		JobParents:       parents,
		JobIndents:       indents,
		Cursor:           initialCursor,
		ScrollOffset:     0,
		Selected:         make(map[string]bool),
		StatusSummary:    formatStatusSummaryHelper(plan),
		ConfirmArchive:   false,
		PlanDir:          plan.Directory,
		KeyMap:           keyMap,
		Help:             helpModel,
		CursorVisible:    true,
		ViewMode:         TableView, // Default to table view
		LogViewer:        logViewerModel,
		ShowLogs:         false, // Start with logs hidden by default
		ActiveLogJob:     nil,
		Focus:            JobsPane,
		LogSplitVertical: false, // Default to horizontal split
		IsRunningJob:     false,
		RunLogFile:       runLogPath,
		Program:          nil, // Will be set by SetProgram after creating the program
	}
}

// SetProgramRef sets the package-level program reference
// This is called by runStatusTUI before starting the program
func SetProgramRef(program *tea.Program) {
	initProgramRef = program
}

// SetProgram sets the program reference in the model (deprecated - kept for compatibility)
func (m *Model) SetProgram(program *tea.Program) {
	m.Program = program
}

// Init initializes the TUI
func (m Model) Init() tea.Cmd {
	// Return a command that will send the initProgramMsg after the program has started
	return tea.Batch(
		func() tea.Msg { return InitProgramMsg{} },
		blink(),
		refreshTick(),
	)
}

// View renders the TUI
func (m Model) View() string {
	if m.Err != nil {
		return fmt.Sprintf("Error: %v\n", m.Err)
	}

	// If renaming, show the rename dialog and return
	if m.Renaming {
		return m.renderRenameDialog()
	}

	// If creating a job, show the creation dialog
	if m.CreatingJob {
		return m.renderJobCreationDialog()
	}

	// If editing dependencies, show the edit deps view
	if m.EditingDeps {
		return m.renderEditDepsView()
	}

	// Show status picker if active
	if m.ShowStatusPicker {
		return m.renderStatusPicker()
	}

	// Show help if active
	if m.Help.ShowAll {
		return m.Help.View()
	}

	// Calculate content width accounting for margins
	contentWidth := m.Width - 4
	if contentWidth < 40 {
		contentWidth = 40
	}

	// 1. Create Header with subtle coloring
	// Header uses terminal default colors with bold for emphasis.
	// See: plans/tui-updates/14-terminal-ui-styling-philosophy.md
	headerLabel := theme.DefaultTheme.Bold.Render(theme.IconPlan + " Plan Status: ")
	planName := theme.DefaultTheme.Bold.Render(m.Plan.Name)
	headerText := headerLabel + planName

	styledHeader := lipgloss.NewStyle().
		Background(theme.DefaultTheme.Header.GetBackground()).
		Align(lipgloss.Left).
		Width(contentWidth).
		MarginBottom(1).
		Render(headerText)

	// 2. Render Main Content (Table or Tree)
	var mainContent string
	switch m.ViewMode {
	case TableView:
		mainContent = m.renderTableView()
	default: // TreeView
		mainContent = m.renderJobTree()
	}

	// 2b. Add scroll indicators if needed
	scrollIndicator := ""
	if len(m.Jobs) > 0 {
		visibleLines := m.getVisibleJobCount()
		hasMore := m.ScrollOffset+visibleLines < len(m.Jobs)
		hasLess := m.ScrollOffset > 0

		if hasLess || hasMore {
			indicator := ""
			if hasLess {
				indicator += "↑ "
			}
			indicator += fmt.Sprintf("[%d/%d]", m.Cursor+1, len(m.Jobs))
			if hasMore {
				indicator += " ↓"
			}
			scrollIndicator = "\n" + theme.DefaultTheme.Muted.Render(indicator)
		}
	}

	// 3. Handle confirmation dialog or help footer
	var footer string
	if m.ConfirmArchive {
		if len(m.Selected) > 0 {
			footer = "\n" + theme.DefaultTheme.Warning.
				Bold(true).
				Render(fmt.Sprintf("Archive %d selected job(s)? (y/n)", len(m.Selected)))
		} else if m.Cursor < len(m.Jobs) {
			job := m.Jobs[m.Cursor]
			footer = "\n" + theme.DefaultTheme.Warning.
				Bold(true).
				Render(fmt.Sprintf("Archive '%s'? (y/n)", job.Filename))
		}
	} else {
		// Render Footer
		helpView := m.Help.View()
		viewModeIndicator := theme.DefaultTheme.Muted.Render(fmt.Sprintf(" [%s]", m.ViewMode))

		// Add follow status if logs are shown
		followStatus := ""
		if m.ShowLogs {
			if m.LogViewer.IsFollowing() {
				followStatus = theme.DefaultTheme.Muted.Render(" • Follow: ON")
			} else {
				followStatus = theme.DefaultTheme.Muted.Render(" • Follow: OFF")
			}
		}

		footer = helpView + viewModeIndicator + followStatus
	}

	// 4. Combine everything
	var finalView string
	if m.ShowLogs {
		jobsView := lipgloss.JoinVertical(
			lipgloss.Left,
			styledHeader,
			mainContent,
			scrollIndicator,
		)

		var jobsWidth int

		if m.LogSplitVertical {
			// Vertical split (side-by-side)
			jobsWidth = m.Width/2 - 2

			// Create a simple vertical separator
			separatorChar := "│"
			if m.Focus == LogsPane {
				separatorChar = "┃" // Thicker line when logs are focused
			}
			separatorHeight := m.Height - 13 // Account for header and footer
			// Add 3 lines of spacing at the top to match log viewer shift
			separatorContent := "\n\n\n" + strings.Repeat(separatorChar+"\n", separatorHeight)
			separator := lipgloss.NewStyle().
				Foreground(theme.DefaultColors.Border).
				Render(separatorContent)

			// Render panes without borders
			// Add 3 lines of spacing at the top to shift log viewer down, and 2 spaces right padding
			logViewWithSpacing := "\n\n\n" + m.LogViewer.View()
			logView := lipgloss.NewStyle().Width(m.LogViewerWidth).PaddingRight(2).Render(logViewWithSpacing)
			jobsPane := lipgloss.NewStyle().Width(jobsWidth).Render(jobsView)

			finalView = lipgloss.JoinHorizontal(lipgloss.Top, jobsPane, separator, logView)
			finalView = lipgloss.JoinVertical(lipgloss.Left, finalView, "\n", footer)

		} else {
			// Horizontal split (top/bottom)
			// Don't set explicit heights - let content flow naturally
			logView := lipgloss.NewStyle().Render(m.LogViewer.View())

			jobsPane := lipgloss.NewStyle().Render(jobsView)

			// Create a divider - thicker if logs are focused
			dividerChar := "─"
			if m.Focus == LogsPane {
				dividerChar = "━"
			}
			divider := lipgloss.NewStyle().
				Width(contentWidth).
				Foreground(theme.DefaultColors.Border).
				Render(strings.Repeat(dividerChar, contentWidth))

			finalView = lipgloss.JoinVertical(
				lipgloss.Left,
				jobsPane,
				"\n",
				divider,
				logView,
				"\n",
				footer,
			)
		}
	} else {
		finalView = lipgloss.JoinVertical(
			lipgloss.Left,
			styledHeader,
			mainContent,
			scrollIndicator,
			"\n", // Space before footer
			footer,
		)
	}

	// Add overall margin
	return lipgloss.NewStyle().Margin(1, 2).Render(finalView)
}

// getVisibleJobCount returns how many jobs can be displayed in the viewport
func (m *Model) getVisibleJobCount() int {
	if m.Height == 0 {
		return 10 // default
	}

	// Calculate available height for job list
	// Account for UI chrome:
	// - header (3 lines: label + margin)
	// - table headers/borders (4 lines in table view, 1 in tree view)
	// - scroll indicator (1 line)
	// - footer spacing (2 lines)
	// - margins (4 lines: 2 top + 2 bottom)
	chromeLines := 14
	if m.ViewMode == TreeView {
		chromeLines = 11 // tree view has less overhead (no table borders/headers)
	}

	availableHeight := m.Height - chromeLines

	// If logs are shown in horizontal split, reduce available height for jobs pane
	if m.ShowLogs && !m.LogSplitVertical {
		// Subtract log viewer height and divider (2 lines for divider + newlines)
		availableHeight = availableHeight - m.LogViewerHeight - 2
	}

	if availableHeight < 1 {
		availableHeight = 1
	}

	// If summaries are shown, each job might take 2 lines
	if m.ShowSummaries {
		availableHeight = availableHeight / 2
		if availableHeight < 1 {
			availableHeight = 1
		}
	}

	return availableHeight
}

// adjustScrollOffset ensures the cursor is visible within the viewport
func (m *Model) adjustScrollOffset() {
	visibleLines := m.getVisibleJobCount()

	// Adjust scroll offset to keep cursor visible
	if m.Cursor < m.ScrollOffset {
		// Cursor is above viewport, scroll up
		m.ScrollOffset = m.Cursor
	} else if m.Cursor >= m.ScrollOffset+visibleLines {
		// Cursor is below viewport, scroll down
		m.ScrollOffset = m.Cursor - visibleLines + 1
	}

	// Ensure scrollOffset doesn't go negative
	if m.ScrollOffset < 0 {
		m.ScrollOffset = 0
	}
}

// flattenJobTreeWithParents creates a flat list of jobs in tree order with parent tracking
func flattenJobTreeWithParents(plan *orchestration.Plan) ([]*orchestration.Job, map[string]*orchestration.Job, map[string]int) {
	var result []*orchestration.Job
	visited := make(map[string]bool)
	parents := make(map[string]*orchestration.Job)
	indents := make(map[string]int)

	// Find root jobs - these need to be imported from cmd package
	// We'll call them via the cmd package
	roots := findRootJobsHelper(plan)

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
	dependents := findAllDependentsHelper(job, plan)
	for _, dep := range dependents {
		addJobAndDependentsWithParent(dep, plan, result, visited, parents, indents, job, indent+1)
	}
}

// Helper function type declarations - will be set by the cmd package
var (
	FindRootJobsFunc       func(*orchestration.Plan) []*orchestration.Job
	FindAllDependentsFunc  func(*orchestration.Job, *orchestration.Plan) []*orchestration.Job
	FormatStatusSummaryFunc func(*orchestration.Plan) string
)

// Helper functions that call the injected functions
func findRootJobsHelper(plan *orchestration.Plan) []*orchestration.Job {
	if FindRootJobsFunc != nil {
		return FindRootJobsFunc(plan)
	}
	return []*orchestration.Job{}
}

func findAllDependentsHelper(job *orchestration.Job, plan *orchestration.Plan) []*orchestration.Job {
	if FindAllDependentsFunc != nil {
		return FindAllDependentsFunc(job, plan)
	}
	return []*orchestration.Job{}
}

func formatStatusSummaryHelper(plan *orchestration.Plan) string {
	if FormatStatusSummaryFunc != nil {
		return FormatStatusSummaryFunc(plan)
	}
	return ""
}
