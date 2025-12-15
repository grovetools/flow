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

	// 2. Render Main Content (Table or Tree)
	var mainContent string
	switch m.ViewMode {
	case TableView:
		mainContent = m.renderTableViewWithWidth(contentWidth)
	default: // TreeView
		mainContent = m.renderJobTree()
	}

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

// renderLogsPane renders the bottom (or right) pane containing the log viewer.
func (m Model) renderLogsPane(contentWidth int) (string, string) {
	// Create log section header
	var logHeader string
	if m.Cursor < len(m.Jobs) {
		currentJob := m.Jobs[m.Cursor]
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
		logHeader = fmt.Sprintf("%s  %s%s • %s • %s%s", jobIcon, jobTitle, filenameDisplay, template, statusIcon, scrollInfo)
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
	logContentWithScrollbar := m.addScrollbarToContent(m.LogViewer.View(), m.LogViewerHeight-logHeaderHeight-1) // -1 for spacing
	dividerLine := theme.DefaultTheme.Muted.Render(strings.Repeat("─", m.LogViewerWidth-2))
	logViewWithHeader := dividerLine + "\n" + logHeader + "\n" + dividerLine + "\n" + logContentWithScrollbar

	// Adjust padding/width based on split direction
	var logView string
	if m.LogSplitVertical {
		logView = lipgloss.NewStyle().Width(m.LogViewerWidth).Height(m.LogViewerHeight).MaxHeight(m.LogViewerHeight).PaddingLeft(1).PaddingRight(1).Render(logViewWithHeader)
	} else {
		logHeader = " " + logHeader // Add left padding for horizontal view
		paddedContent := m.addScrollbarToContent(" "+m.LogViewer.View(), m.LogViewerHeight-logHeaderHeight)
		logView = lipgloss.NewStyle().Height(m.LogViewerHeight).MaxHeight(m.LogViewerHeight).Render(logHeader + "\n" + dividerLine + "\n" + paddedContent)
	}

	return logView, separator
}

// renderFooter renders the help and status message footer.
func (m Model) renderFooter() string {
	helpView := m.Help.View()
	viewModeIndicator := theme.DefaultTheme.Muted.Render(fmt.Sprintf(" [%s]", m.ViewMode))
	followStatus := ""
	if m.ShowLogs {
		if m.LogViewer.IsFollowing() {
			followStatus = theme.DefaultTheme.Muted.Render(" • Follow: ON")
		} else {
			followStatus = theme.DefaultTheme.Muted.Render(" • Follow: OFF")
		}
	}
	return helpView + viewModeIndicator + followStatus
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
	if m.ShowLogs {
		logsPane, separator := m.renderLogsPane(contentWidth)
		if m.LogSplitVertical {
			// Constrain jobs pane height for vertical split
			// Account for: footer + newline before footer + top/bottom margins
			maxJobsPaneHeight := m.Height - (footerHeight + topMargin + bottomMargin + 2) // +2 for newline and spacing
			if maxJobsPaneHeight < 10 {
				maxJobsPaneHeight = 10
			}
			jobsPaneStyled := lipgloss.NewStyle().Width(m.JobsPaneWidth).MaxWidth(m.JobsPaneWidth).MaxHeight(maxJobsPaneHeight).Render(jobsPane)

			combinedPanes := lipgloss.JoinHorizontal(lipgloss.Top, jobsPaneStyled, separator, logsPane)
			finalView = lipgloss.JoinVertical(lipgloss.Left, combinedPanes, "\n", footer)
		} else {
			// Constrain jobs pane height for horizontal split
			jobsPaneHeight := m.Height - m.LogViewerHeight - (horizontalDividerHeight + footerHeight + topMargin + bottomMargin)
			if jobsPaneHeight < 10 {
				jobsPaneHeight = 10
			}
			jobsPaneStyled := lipgloss.NewStyle().MaxHeight(jobsPaneHeight).Render(jobsPane)

			finalView = lipgloss.JoinVertical(lipgloss.Left, jobsPaneStyled, separator, logsPane, footer)
		}
	} else {
		// No logs view - use same height calculation as vertical split mode
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
	minJobsHeight := 0
	if m.ViewMode == TableView {
		minJobsHeight += 4 // Table headers and borders
	} else {
		minJobsHeight += 1 // Tree view overhead
	}

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

	// Give logs 55-60% of available space, but ensure jobs get their minimum
	logHeight := (availableHeight * 55) / 100

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

// calculateMinJobsPaneWidth calculates the minimum width needed for the jobs pane
// based on visible jobs (job names, indentation, type, status)
func (m *Model) calculateMinJobsPaneWidth() int {
	if len(m.Jobs) == 0 {
		return 60 // Default minimum
	}

	// Only measure visible jobs to optimize width calculation
	visibleJobs := m.getVisibleJobs()
	if len(visibleJobs) == 0 {
		return 60 // Default minimum
	}

	maxJobColWidth := 0
	maxTypeColWidth := 0
	maxStatusColWidth := 0

	for _, job := range visibleJobs {
		// Calculate JOB column width: indent + tree prefix + icon + space + filename
		indent := m.JobIndents[job.ID]
		treeChars := 0
		if indent > 0 {
			treeChars = (indent * 2) + 3 // "  " per level + "└─ " or "├─ "
		}

		// Icon (3 chars with space) + filename
		jobColWidth := treeChars + 3 + len(job.Filename)
		if jobColWidth > maxJobColWidth {
			maxJobColWidth = jobColWidth
		}

		// Calculate TYPE column width: icon + space + type name
		typeWidth := 3 + len(job.Type) // icon + space + type
		if typeWidth > maxTypeColWidth {
			maxTypeColWidth = typeWidth
		}

		// Calculate STATUS column width
		statusWidth := len(job.Status)
		if statusWidth > maxStatusColWidth {
			maxStatusColWidth = statusWidth
		}
	}

	// Cap individual column widths to prevent extremely long filenames from dominating
	const maxJobColWidthCap = 50  // Long filenames will wrap/truncate in table
	if maxJobColWidth > maxJobColWidthCap {
		maxJobColWidth = maxJobColWidthCap
	}

	// SEL column is fixed at ~5 chars
	selWidth := 5

	// Add padding and borders: each column has padding, plus table borders
	// Typical table has: | SEL | JOB | TYPE | STATUS |
	// That's 5 separators + column padding (2 per column = 8)
	borders := 13

	totalWidth := selWidth + maxJobColWidth + maxTypeColWidth + maxStatusColWidth + borders

	// Apply reasonable bounds
	if totalWidth < 60 {
		totalWidth = 60 // Absolute minimum
	}
	if totalWidth > 100 {
		totalWidth = 100 // Cap at reasonable max to give logs more space
	}

	return totalWidth
}

// updateLayoutDimensions centralizes the logic for calculating pane sizes.
func (m *Model) updateLayoutDimensions() {
	if m.LogSplitVertical {
		minJobsWidth := m.calculateMinJobsPaneWidth()
		if m.Width < minJobsWidth+minLogsWidth+verticalSeparatorWidth {
			m.LogSplitVertical = false
			m.StatusSummary = theme.DefaultTheme.Muted.Render("Switched to horizontal split (terminal too narrow)")
		} else {
			m.JobsPaneWidth = minJobsWidth
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
	var tableChrome int
	if m.ViewMode == TableView {
		tableChrome = 4 // table headers and borders
	} else {
		tableChrome = 1 // tree view has less overhead
	}

	chromeLines := topMargin + headerHeight + tableChrome + footerHeight + 1 // +1 for scroll indicator

	availableHeight := m.Height - chromeLines

	// If logs are shown in horizontal split, reduce available height
	if m.ShowLogs && !m.LogSplitVertical {
		availableHeight -= (m.LogViewerHeight + horizontalDividerHeight)
	}

	// In vertical split mode with logs OR no-logs mode, account for additional spacing
	if (m.ShowLogs && m.LogSplitVertical) || !m.ShowLogs {
		availableHeight -= 2 // Account for newline and spacing before footer (same as vertical split)
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
