package status_tui

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
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

const (
	// Heights
	headerHeight            = 2 // Includes label and bottom margin
	footerHeight            = 1
	horizontalDividerHeight = 1
	logHeaderHeight         = 3 // Header text + two divider lines

	// Widths
	minLogsWidth           = 50
	verticalSeparatorWidth = 3 // Separator + margins

	// Margins
	topMargin    = 1
	bottomMargin = 0
	leftMargin   = 2
	rightMargin  = 2
)

type ViewFocus int

const (
	JobsPane ViewFocus = iota
	LogsPane
)

type DetailPane int

const (
	NoPane DetailPane = iota
	LogsPaneDetail
	FrontmatterPane
	BriefingPane
	EditPane
)

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
	Renaming           bool
	RenameInput        textinput.Model
	RenameJobIndex     int
	EditingDeps        bool
	EditDepsJobIndex   int
	EditDepsSelected   map[string]bool // Track which jobs are selected as dependencies
	CreatingJob        bool
	CreateJobInput     textinput.Model
	CreateJobType      string // "xml" or "impl"
	CreateJobBaseJob    *orchestration.Job
	CreateJobDeps       []*orchestration.Job // For multi-select case
	ShowLogs            bool
	LogViewer           logviewer.Model
	ActiveLogJob        *orchestration.Job
	StreamingJobID      string // Track which job is currently streaming to prevent duplicates
	ActiveDetailPane    DetailPane
	columnSelectMode    bool
	columnList          list.Model
	availableColumns    []string
	columnVisibility    map[string]bool
	frontmatterViewport viewport.Model
	briefingViewport    viewport.Model
	editViewport        viewport.Model
	Focus               ViewFocus // Track which pane is active
	LogSplitVertical    bool      // Track log viewer layout
	IsRunningJob        bool      // Track if a job is currently running
	RunLogFile         string    // Path to temporary log file for job output
	Program            *tea.Program // Reference to the tea.Program for sending messages
	LogViewerWidth     int       // Cached log viewer width
	LogViewerHeight    int       // Cached log viewer height
	JobsPaneWidth      int       // Cached jobs pane width for vertical split
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

	// Initialize new viewports
	frontmatterVp := viewport.New(80, 20)
	briefingVp := viewport.New(80, 20)
	editVp := viewport.New(80, 20)

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

	// Column Visibility Setup
	availableColumns := []string{"JOB", "TITLE", "TYPE", "STATUS", "TEMPLATE", "MODEL", "WORKTREE", "PREPEND", "UPDATED", "COMPLETED", "DURATION"}
	state, err := loadState()
	if err != nil {
		// On error, use defaults
		state = &tuiState{ColumnVisibility: defaultColumnVisibility()}
	}
	columnVisibility := state.ColumnVisibility
	// Ensure all available columns have an entry in the visibility map
	for _, col := range availableColumns {
		if _, ok := columnVisibility[col]; !ok {
			// Add new columns with a default visibility (false, unless it's TEMPLATE)
			columnVisibility[col] = (col == "TEMPLATE")
		}
	}

	var columnItems []list.Item
	for _, col := range availableColumns {
		columnItems = append(columnItems, columnSelectItem{name: col})
	}

	columnList := list.New(columnItems, columnSelectDelegate{visibility: &columnVisibility}, 35, 14)
	columnList.Title = "Toggle Column Visibility"
	columnList.SetShowHelp(false)
	columnList.SetFilteringEnabled(false)
	columnList.SetShowStatusBar(false)
	columnList.SetShowPagination(false)

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
		LogViewer:        logViewerModel,
		ShowLogs:         false, // Start with logs hidden by default
		ActiveLogJob:     nil,
		ActiveDetailPane: NoPane,
		columnSelectMode:    false,
		columnList:          columnList,
		availableColumns:    availableColumns,
		columnVisibility:    columnVisibility,
		Focus:            JobsPane,
		LogSplitVertical: false, // Default to horizontal split
		IsRunningJob:        false,
		RunLogFile:          runLogPath,
		Program:             nil, // Will be set by SetProgram after creating the program
		frontmatterViewport: frontmatterVp,
		briefingViewport:    briefingVp,
		editViewport:        editVp,
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

// renderJobsPane renders the top (or left) pane containing the plan header and jobs list.
func (m Model) renderJobsPane(contentWidth int) string {
	// 1. Create Header
	headerLabel := theme.DefaultTheme.Bold.Render(theme.IconPlan + " Plan Status: ")
	planName := theme.DefaultTheme.Bold.Render(m.Plan.Name)
	headerText := headerLabel + planName
	styledHeader := lipgloss.NewStyle().
		Background(theme.DefaultTheme.Header.GetBackground()).
		Align(lipgloss.Left).
		Width(contentWidth).
		MarginBottom(1).
		Render(headerText)

	// 2. Render Main Content (Table view only)
	mainContent := m.renderTableViewWithWidth(contentWidth)

	// 3. Add scroll indicators
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

	return lipgloss.JoinVertical(lipgloss.Left, styledHeader, mainContent, scrollIndicator)
}

// renderLogsPane renders the bottom (or right) pane containing the detail view.
func (m Model) renderLogsPane(contentWidth int, paneContent string) (string, string) {
	// Create log section header
	var logHeader string
	if m.Cursor < len(m.Jobs) {
		currentJob := m.Jobs[m.Cursor]

		var paneTitle string
		switch m.ActiveDetailPane {
		case LogsPaneDetail:
			paneTitle = "Logs"
		case FrontmatterPane:
			paneTitle = "Frontmatter"
		case BriefingPane:
			paneTitle = "Briefing"
		case EditPane:
			paneTitle = "Edit"
		}

		jobIcon := getJobIcon(currentJob)
		jobTitle := currentJob.Title
		if jobTitle == "" {
			jobTitle = currentJob.Filename
		}
		statusIcon := m.getStatusIcon(currentJob.Status)
		filenameDisplay := ""
		if jobTitle != currentJob.Filename {
			filenameDisplay = fmt.Sprintf(" (%s)", currentJob.Filename)
		}
		templateName := currentJob.Template
		if templateName == "" {
			templateName = "none"
		}
		template := theme.DefaultTheme.Muted.Italic(true).Render(fmt.Sprintf("template: %s", templateName))
		currentLine, totalLines := m.LogViewer.GetScrollInfo()
		scrollInfo := ""
		if totalLines > 0 {
			scrollInfo = theme.DefaultTheme.Muted.Render(fmt.Sprintf(" [%d/%d]", currentLine, totalLines))
		}
		logHeader = fmt.Sprintf("%s: %s  %s%s • %s • %s%s", paneTitle, jobIcon, jobTitle, filenameDisplay, template, statusIcon, scrollInfo)
		logHeader = theme.DefaultTheme.Bold.Render(logHeader)
	}

	// Create a separator
	var separator string
	if m.LogSplitVertical {
		separatorHeight := m.Height - 8
		var separatorLines []string
		separatorLines = append(separatorLines, "", "", "") // Top spacing
		halfHeight := separatorHeight / 2
		for i := 0; i < separatorHeight; i++ {
			style := lipgloss.NewStyle().Foreground(theme.DefaultColors.Border)
			if (i < halfHeight && m.Focus == JobsPane) || (i >= halfHeight && m.Focus == LogsPane) {
				style = theme.DefaultTheme.Highlight
			}
			separatorLines = append(separatorLines, style.Render("│"))
		}
		separator = strings.Join(separatorLines, "\n")
	} else {
		halfWidth := contentWidth / 2
		leftHalf := lipgloss.NewStyle().Foreground(theme.DefaultColors.Border).Render(strings.Repeat("─", halfWidth))
		rightHalf := lipgloss.NewStyle().Foreground(theme.DefaultColors.Border).Render(strings.Repeat("─", contentWidth-halfWidth))
		if m.Focus == JobsPane {
			leftHalf = theme.DefaultTheme.Highlight.Render(strings.Repeat("─", halfWidth))
		} else {
			rightHalf = theme.DefaultTheme.Highlight.Render(strings.Repeat("─", contentWidth-halfWidth))
		}
		separator = leftHalf + rightHalf
	}

	// Render log content with scrollbar
	logContentWithScrollbar := m.addScrollbarToContent(paneContent, m.LogViewerHeight-logHeaderHeight-1) // -1 for spacing
	dividerLine := theme.DefaultTheme.Muted.Render(strings.Repeat("─", m.LogViewerWidth-2))
	logViewWithHeader := dividerLine + "\n" + logHeader + "\n" + dividerLine + "\n" + logContentWithScrollbar

	// Adjust padding/width based on split direction
	var logView string
	if m.LogSplitVertical {
		logView = lipgloss.NewStyle().Width(m.LogViewerWidth).Height(m.LogViewerHeight).MaxHeight(m.LogViewerHeight).PaddingLeft(1).PaddingRight(1).Render(logViewWithHeader)
	} else {
		logHeader = " " + logHeader // Add left padding for horizontal view
		paddedContent := m.addScrollbarToContent(" "+paneContent, m.LogViewerHeight-logHeaderHeight)
		logView = lipgloss.NewStyle().Height(m.LogViewerHeight).MaxHeight(m.LogViewerHeight).Render(logHeader + "\n" + dividerLine + "\n" + paddedContent)
	}

	return logView, separator
}

// renderFooter renders the help and status message footer.
func (m Model) renderFooter() string {
	helpView := m.Help.View()
	followStatus := ""
	if m.ShowLogs {
		if m.LogViewer.IsFollowing() {
			followStatus = theme.DefaultTheme.Muted.Render(" • Follow: ON")
		} else {
			followStatus = theme.DefaultTheme.Muted.Render(" • Follow: OFF")
		}
	}
	return helpView + followStatus
}

// View renders the TUI
func (m Model) View() string {
	if m.Err != nil {
		return fmt.Sprintf("Error: %v\n", m.Err)
	}

	// If column selection mode is active, render it and return
	if m.columnSelectMode {
		return m.renderColumnSelectView()
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

	// Calculate jobs pane width for proper rendering
	var jobsContentWidth int
	if m.ShowLogs && m.LogSplitVertical {
		jobsContentWidth = m.JobsPaneWidth
	} else {
		jobsContentWidth = contentWidth
	}

	// Render the main components
	jobsPane := m.renderJobsPane(jobsContentWidth)

	// Handle confirmation dialog or regular footer
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
		footer = m.renderFooter()
	}

	var finalView string
	if m.ActiveDetailPane != NoPane {
		var detailContent string
		switch m.ActiveDetailPane {
		case LogsPaneDetail:
			detailContent = m.LogViewer.View()
		case FrontmatterPane:
			detailContent = m.frontmatterViewport.View()
		case BriefingPane:
			detailContent = m.briefingViewport.View()
		case EditPane:
			detailContent = m.editViewport.View()
		}

		// Use the existing renderLogsPane structure but pass in the dynamic content
		logsPane, separator := m.renderLogsPane(contentWidth, detailContent)
		if m.LogSplitVertical {
			// Vertical split: constrain jobs pane height
			maxJobsPaneHeight := m.Height - (footerHeight + topMargin + bottomMargin + 2) // +2 for newline and spacing
			if maxJobsPaneHeight < 10 {
				maxJobsPaneHeight = 10
			}
			jobsPaneStyled := lipgloss.NewStyle().Width(m.JobsPaneWidth).MaxWidth(m.JobsPaneWidth).MaxHeight(maxJobsPaneHeight).Render(jobsPane)

			combinedPanes := lipgloss.JoinHorizontal(lipgloss.Top, jobsPaneStyled, separator, logsPane)
			finalView = lipgloss.JoinVertical(lipgloss.Left, combinedPanes, "\n", footer)
		} else {
			// Horizontal split: account for log viewer height
			maxJobsPaneHeight := m.Height - m.LogViewerHeight - (horizontalDividerHeight + footerHeight + topMargin + bottomMargin)
			if maxJobsPaneHeight < 10 {
				maxJobsPaneHeight = 10
			}
			jobsPaneStyled := lipgloss.NewStyle().MaxHeight(maxJobsPaneHeight).Render(jobsPane)

			// Push footer to bottom by setting height on combined content
			contentHeight := m.Height - topMargin - bottomMargin - footerHeight
			combinedContent := lipgloss.JoinVertical(lipgloss.Left, jobsPaneStyled, separator, logsPane)
			combinedContent = lipgloss.NewStyle().Height(contentHeight).Render(combinedContent)
			finalView = lipgloss.JoinVertical(lipgloss.Left, combinedContent, footer)
		}
	} else {
		// No logs: use same calculation as vertical split
		maxJobsPaneHeight := m.Height - (footerHeight + topMargin + bottomMargin + 2) // +2 for newline and spacing
		if maxJobsPaneHeight < 10 {
			maxJobsPaneHeight = 10
		}
		jobsPaneStyled := lipgloss.NewStyle().MaxHeight(maxJobsPaneHeight).Render(jobsPane)
		finalView = lipgloss.JoinVertical(lipgloss.Left, jobsPaneStyled, "\n", footer)
	}

	// Add overall margin - minimal vertical margin to maximize screen usage
	return lipgloss.NewStyle().Margin(1, 2, 0, 2).Render(finalView)
}

// calculateOptimalLogHeight calculates the log viewer height for horizontal split
// It prioritizes log visibility while ensuring jobs section remains usable
func (m *Model) calculateOptimalLogHeight() int {
	// Total chrome height
	chromeLines := topMargin + headerHeight + footerHeight + horizontalDividerHeight

	// Total available for content (jobs list + log content)
	availableHeight := m.Height - chromeLines
	if availableHeight < 10 {
		availableHeight = 10 // Ensure some minimum
	}

	// Calculate minimum jobs section height (table chrome + minimum visible rows)
	minJobsHeight := 4 // Table headers and borders

	// Add minimum visible job rows (ensure at least 5-8 jobs are visible)
	minVisibleJobs := 5
	if len(m.Jobs) > 8 {
		minVisibleJobs = 8 // Show fewer jobs to give logs more space
	}
	if len(m.Jobs) < minVisibleJobs {
		minVisibleJobs = len(m.Jobs) // Don't exceed actual job count
	}
	if m.ShowSummaries {
		minJobsHeight += minVisibleJobs * 2 // Each job takes 2 lines with summaries
	} else {
		minJobsHeight += minVisibleJobs
	}

	// Add scroll indicator if needed
	if len(m.Jobs) > minVisibleJobs {
		minJobsHeight += 1 // Scroll indicator line
	}

	// Give logs most of the available space, but ensure jobs get their minimum
	logHeight := availableHeight - minJobsHeight - 4 // Reserve 4 lines buffer

	// Ensure jobs section has minimum space
	if availableHeight - logHeight < minJobsHeight {
		// Not enough room for both, give jobs minimum and logs get the rest
		logHeight = availableHeight - minJobsHeight
	}

	// Ensure logs get at least some reasonable space
	if logHeight < 8 {
		logHeight = 8 // Absolute minimum for logs
	}

	return logHeight
}

// calculateJobsPaneWidth calculates the optimal width for the jobs pane
// based on the content of the currently visible columns.
func (m *Model) calculateJobsPaneWidth() int {
	if len(m.Jobs) == 0 {
		return 60 // Default minimum
	}

	// 1. Initialize max widths with header lengths
	columnWidths := make(map[string]int)
	for _, colName := range m.availableColumns {
		if m.columnVisibility[colName] {
			columnWidths[colName] = lipgloss.Width(colName)
		}
	}

	// 2. Iterate through visible jobs to find the max width for each visible column
	visibleJobs := m.getVisibleJobs()
	if len(visibleJobs) == 0 {
		// Fallback if no jobs are visible (e.g., empty plan)
		return 60
	}

	for _, job := range visibleJobs {
		// Calculate rendered width for each potential column
		// This logic mirrors the rendering in view.go
		if m.columnVisibility["JOB"] {
			indent := m.JobIndents[job.ID]
			treePrefixWidth := 0
			if indent > 0 {
				// Matches view.go: strings.Repeat("  ", indent-1) + "└─ " or "├─ "
				treePrefixWidth = ((indent - 1) * 2) + 3 // "  " per level above 1 + "└─ " (3 chars)
			}
			// statusIcon (1 visual char, but may have ANSI codes) + space (1) + filename
			jobColWidth := treePrefixWidth + 2 + lipgloss.Width(job.Filename)
			// Cap at maxJobColumnWidth to match truncation in view.go
			if jobColWidth > maxJobColumnWidth {
				jobColWidth = maxJobColumnWidth
			}
			if jobColWidth > columnWidths["JOB"] {
				columnWidths["JOB"] = jobColWidth
			}
		}
		if m.columnVisibility["TITLE"] {
			titleWidth := lipgloss.Width(job.Title)
			// Cap at maxTitleColumnWidth to match truncation in view.go
			if titleWidth > maxTitleColumnWidth {
				titleWidth = maxTitleColumnWidth
			}
			if titleWidth > columnWidths["TITLE"] {
				columnWidths["TITLE"] = titleWidth
			}
		}
		if m.columnVisibility["TYPE"] {
			// icon + space + type name
			typeWidth := 2 + lipgloss.Width(string(job.Type))
			if typeWidth > columnWidths["TYPE"] {
				columnWidths["TYPE"] = typeWidth
			}
		}
		// ... other columns can be added here if needed ...
	}

	// 3. Sum up the widths of visible columns and add padding/borders
	totalWidth := 0
	hasSelection := len(m.Selected) > 0

	// Count visible columns
	visibleColCount := 0
	if hasSelection {
		visibleColCount++
		totalWidth += 3 // "SEL" column width
	}

	for _, colName := range m.availableColumns {
		if m.columnVisibility[colName] {
			visibleColCount++
			width := columnWidths[colName]
			totalWidth += width
		}
	}

	// Add table formatting: left border (1) + right border (1) + separators between columns (3 each)
	// Format is: "│ col1 │ col2 │ col3 │"
	if visibleColCount > 0 {
		totalWidth += 2 // Left and right borders
		totalWidth += (visibleColCount - 1) * 3 // Separators between columns: " │ "
		totalWidth += visibleColCount * 2 // Padding: 1 space on each side of each column
		totalWidth += 4 // Extra spacing buffer
	}

	// 4. Apply reasonable bounds to the final calculated width
	if totalWidth < 60 {
		totalWidth = 60 // Absolute minimum
	}
	// Cap at 80% of terminal width to ensure logs are always somewhat visible
	maxWidth := int(float64(m.Width) * 0.8)
	if totalWidth > maxWidth {
		totalWidth = maxWidth
	}

	return totalWidth
}

// updateLayoutDimensions centralizes the logic for calculating pane sizes.
func (m *Model) updateLayoutDimensions() {
	if m.LogSplitVertical {
		m.JobsPaneWidth = m.calculateJobsPaneWidth()
		if m.Width < m.JobsPaneWidth+minLogsWidth+verticalSeparatorWidth {
			m.LogSplitVertical = false
			m.StatusSummary = theme.DefaultTheme.Muted.Render("Switched to horizontal split (terminal too narrow)")
		}
	}

	if m.ShowLogs {
		if m.LogSplitVertical {
			m.LogViewerWidth = m.Width - m.JobsPaneWidth - verticalSeparatorWidth
			m.LogViewerHeight = m.Height - (headerHeight + footerHeight + topMargin)
		} else {
			m.LogViewerWidth = m.Width - (leftMargin + rightMargin)
			m.LogViewerHeight = m.calculateOptimalLogHeight()
		}

		// Ensure minimum dimensions
		if m.LogViewerHeight < 8 { // Increased minimum height for usability
			m.LogViewerHeight = 8
		}
		if m.LogViewerWidth < 20 {
			m.LogViewerWidth = 20
		}
	}
}

// getVisibleJobCount returns how many jobs can be displayed in the viewport
func (m *Model) getVisibleJobCount() int {
	if m.Height == 0 {
		return 10 // default
	}

	// Calculate available height for job list
	tableChrome := 4 // table headers and borders

	chromeLines := topMargin + headerHeight + tableChrome + footerHeight + 1 // +1 for scroll indicator

	availableHeight := m.Height - chromeLines

	// Adjust for log viewer in horizontal split
	if m.ShowLogs && !m.LogSplitVertical {
		availableHeight -= (m.LogViewerHeight + horizontalDividerHeight)
	}

	// Account for footer spacing in vertical split and no-logs modes
	if (m.ShowLogs && m.LogSplitVertical) || !m.ShowLogs {
		availableHeight -= 2 // Newline and spacing before footer
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

// addScrollbarToContent adds a scrollbar to the right side of log content
func (m *Model) addScrollbarToContent(content string, viewHeight int) string {
	lines := strings.Split(content, "\n")
	currentLine, totalLines := m.LogViewer.GetScrollInfo()
	scrollPercent := m.LogViewer.GetScrollPercent()

	if totalLines == 0 || viewHeight == 0 {
		return content
	}

	// Generate scrollbar
	scrollbar := m.generateScrollbar(viewHeight, currentLine, totalLines, scrollPercent)

	// The viewport has already wrapped content to its width
	// We need to ensure each line + scrollbar fits within LogViewerWidth
	// Account for: left padding (1) + content + space before scrollbar (1) + scrollbar (1) + right padding (1) = 4
	// So max line width should be LogViewerWidth - 4
	maxLineWidth := m.LogViewerWidth - 4

	// Combine lines with scrollbar
	var result []string
	for i := 0; i < viewHeight; i++ {
		scrollbarChar := " "
		if i < len(scrollbar) {
			scrollbarChar = scrollbar[i]
		}

		// Get the line, or empty string if we've run out of content lines
		line := ""
		if i < len(lines) {
			line = lines[i]
		}

		// Measure the visual width (accounting for ANSI codes)
		visualWidth := lipgloss.Width(line)

		// Pad to fixed width to align scrollbar
		if visualWidth < maxLineWidth {
			// Add padding spaces
			line = line + strings.Repeat(" ", maxLineWidth-visualWidth)
		} else if visualWidth > maxLineWidth {
			// Truncate if somehow too long (shouldn't happen if viewport is sized correctly)
			// This is tricky with ANSI codes, so we use lipgloss to handle it
			line = lipgloss.NewStyle().MaxWidth(maxLineWidth).Render(line)
			// Re-pad after truncation
			visualWidth = lipgloss.Width(line)
			if visualWidth < maxLineWidth {
				line = line + strings.Repeat(" ", maxLineWidth-visualWidth)
			}
		}

		result = append(result, line+" "+scrollbarChar)
	}

	return strings.Join(result, "\n")
}

// generateScrollbar creates a visual scrollbar
func (m *Model) generateScrollbar(viewHeight, currentLine, totalLines int, scrollPercent float64) []string {
	if totalLines == 0 || viewHeight == 0 {
		return []string{}
	}

	scrollbar := make([]string, viewHeight)

	// If content fits entirely in view, show all thumb
	if totalLines <= viewHeight {
		for i := 0; i < viewHeight; i++ {
			scrollbar[i] = theme.DefaultTheme.Muted.Render("█") // All thumb
		}
		return scrollbar
	}

	// Calculate scrollbar thumb size proportional to visible content
	thumbSize := max(1, (viewHeight*viewHeight)/totalLines)

	// Use the viewport's scroll percentage directly (0.0 to 1.0)
	// This is more reliable than calculating it ourselves
	scrollProgress := scrollPercent
	if scrollProgress < 0 {
		scrollProgress = 0
	}
	if scrollProgress > 1 {
		scrollProgress = 1
	}

	// Calculate thumb position in scrollbar
	maxThumbStart := viewHeight - thumbSize
	thumbStart := int(float64(maxThumbStart)*scrollProgress + 0.5) // +0.5 for rounding

	// Ensure thumb doesn't go out of bounds
	if thumbStart < 0 {
		thumbStart = 0
	}
	if thumbStart > maxThumbStart {
		thumbStart = maxThumbStart
	}

	// Generate scrollbar characters with muted styling
	for i := 0; i < viewHeight; i++ {
		if i >= thumbStart && i < thumbStart+thumbSize {
			scrollbar[i] = theme.DefaultTheme.Muted.Render("█") // Thumb
		} else {
			scrollbar[i] = theme.DefaultTheme.Muted.Render("░") // Track
		}
	}

	return scrollbar
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
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
	// Status summary is no longer used in TUI-only mode
	return ""
}

// columnSelectItem represents an item in the column visibility list
type columnSelectItem struct {
	name string
}

func (i columnSelectItem) FilterValue() string { return i.name }
func (i columnSelectItem) Title() string       { return i.name }
func (i columnSelectItem) Description() string { return "" }

// columnSelectDelegate is a custom delegate with minimal spacing
type columnSelectDelegate struct {
	visibility *map[string]bool
}

func (d columnSelectDelegate) Height() int                             { return 1 }
func (d columnSelectDelegate) Spacing() int                            { return 0 }
func (d columnSelectDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }
func (d columnSelectDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	i, ok := item.(columnSelectItem)
	if !ok {
		return
	}

	var checkbox string
	if (*d.visibility)[i.name] {
		checkbox = theme.DefaultTheme.Success.Render("[x]")
	} else {
		checkbox = theme.DefaultTheme.Muted.Render("[ ]")
	}

	str := fmt.Sprintf("%s %s", checkbox, i.Title())
	if index == m.Index() {
		str = lipgloss.NewStyle().Foreground(theme.DefaultTheme.Colors.Orange).Render("│ " + str)
	} else {
		str = "  " + str
	}

	fmt.Fprint(w, str)
}
