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

	// 2. Calculate jobs pane width first (needed for rendering)
	var jobsContentWidth int
	if m.ShowLogs && m.LogSplitVertical {
		// In vertical split, use calculated width
		jobsContentWidth = m.JobsPaneWidth
	} else {
		// In horizontal split or no logs, use full content width
		jobsContentWidth = contentWidth
	}

	// 2b. Render Main Content (Table or Tree) with width constraint
	var mainContent string
	switch m.ViewMode {
	case TableView:
		mainContent = m.renderTableViewWithWidth(jobsContentWidth)
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
			jobsWidth = m.JobsPaneWidth

			// Create a single-column vertical separator split in half
			// Top half highlights when jobs pane focused, bottom half when logs focused
			separatorHeight := m.Height - 8 // Account for header, footer, and margins
			var separatorLines []string

			// Add 3 lines of spacing at the top to match log viewer shift
			separatorLines = append(separatorLines, "", "", "")

			halfHeight := separatorHeight / 2

			for i := 0; i < separatorHeight; i++ {
				if i < halfHeight {
					// Top half of separator
					if m.Focus == JobsPane {
						separatorLines = append(separatorLines, theme.DefaultTheme.Highlight.Render("│"))
					} else {
						separatorLines = append(separatorLines, lipgloss.NewStyle().Foreground(theme.DefaultColors.Border).Render("│"))
					}
				} else {
					// Bottom half of separator
					if m.Focus == LogsPane {
						separatorLines = append(separatorLines, theme.DefaultTheme.Highlight.Render("│"))
					} else {
						separatorLines = append(separatorLines, lipgloss.NewStyle().Foreground(theme.DefaultColors.Border).Render("│"))
					}
				}
			}
			separator := strings.Join(separatorLines, "\n")

			// Render panes without borders
			// Create log section header with job info - use current cursor job
			var logHeader string
			if m.Cursor < len(m.Jobs) {
				currentJob := m.Jobs[m.Cursor]
				jobIcon := getJobIcon(currentJob)
				jobTitle := currentJob.Title
				if jobTitle == "" {
					jobTitle = currentJob.Filename
				}
				statusIcon := m.getStatusIcon(currentJob.Status)

				// Build filename display (in parens if different from title)
				filenameDisplay := ""
				if jobTitle != currentJob.Filename {
					filenameDisplay = fmt.Sprintf(" (%s)", currentJob.Filename)
				}

				templateName := currentJob.Template
				if templateName == "" {
					templateName = "none"
				}
				template := theme.DefaultTheme.Muted.Italic(true).Render(fmt.Sprintf("template: %s", templateName))

				// Get scroll position info
				currentLine, totalLines := m.LogViewer.GetScrollInfo()
				scrollInfo := ""
				if totalLines > 0 {
					scrollInfo = theme.DefaultTheme.Muted.Render(fmt.Sprintf(" [%d/%d]", currentLine, totalLines))
				}

				logHeader = fmt.Sprintf("%s  %s%s • %s • %s%s", jobIcon, jobTitle, filenameDisplay, template, statusIcon, scrollInfo)
				logHeader = theme.DefaultTheme.Bold.Render(logHeader)
			}

			// Add header and spacing at the top, 1 space left padding
			logViewContent := m.LogViewer.View()

			// Add scrollbar to the right side of log content
			logContentWithScrollbar := m.addScrollbarToContent(logViewContent, m.LogViewerHeight-5) // -5 for top divider (1) + header (1) + bottom divider (1) + blank line (1) + spacing (1)

			// Add muted divider lines above and below header (span width minus left/right padding)
			dividerLine := theme.DefaultTheme.Muted.Render(strings.Repeat("─", m.LogViewerWidth-2))
			logViewWithHeader := dividerLine + "\n" + logHeader + "\n" + dividerLine + "\n" + logContentWithScrollbar
			logView := lipgloss.NewStyle().Width(m.LogViewerWidth).Height(m.LogViewerHeight).MaxHeight(m.LogViewerHeight).PaddingLeft(1).PaddingRight(1).Render(logViewWithHeader)

			// Constrain jobs pane height to prevent overflow
			maxJobsPaneHeight := m.Height - 4 // footer + margins
			if maxJobsPaneHeight < 10 {
				maxJobsPaneHeight = 10
			}
			jobsPane := lipgloss.NewStyle().Width(jobsWidth).MaxWidth(jobsWidth).MaxHeight(maxJobsPaneHeight).Render(jobsView)

			finalView = lipgloss.JoinHorizontal(lipgloss.Top, jobsPane, separator, logView)
			finalView = lipgloss.JoinVertical(lipgloss.Left, finalView, "\n", footer)

		} else {
			// Horizontal split (top/bottom)
			// Create log section header with job info - use current cursor job
			var logHeader string
			if m.Cursor < len(m.Jobs) {
				currentJob := m.Jobs[m.Cursor]
				jobIcon := getJobIcon(currentJob)
				jobTitle := currentJob.Title
				if jobTitle == "" {
					jobTitle = currentJob.Filename
				}
				statusIcon := m.getStatusIcon(currentJob.Status)

				// Build filename display (in parens if different from title)
				filenameDisplay := ""
				if jobTitle != currentJob.Filename {
					filenameDisplay = fmt.Sprintf(" (%s)", currentJob.Filename)
				}

				templateName := currentJob.Template
				if templateName == "" {
					templateName = "none"
				}
				template := theme.DefaultTheme.Muted.Italic(true).Render(fmt.Sprintf("template: %s", templateName))

				// Get scroll position info
				currentLine, totalLines := m.LogViewer.GetScrollInfo()
				scrollInfo := ""
				if totalLines > 0 {
					scrollInfo = theme.DefaultTheme.Muted.Render(fmt.Sprintf(" [%d/%d]", currentLine, totalLines))
				}

				// Add left padding to header text
				logHeader = fmt.Sprintf(" %s  %s%s • %s • %s%s", jobIcon, jobTitle, filenameDisplay, template, statusIcon, scrollInfo)
				logHeader = theme.DefaultTheme.Bold.Render(logHeader)

				// Add muted divider line under header (full width, no left padding)
				dividerWidth := m.Width - 4
				if dividerWidth < 40 {
					dividerWidth = 40
				}
				dividerLine := theme.DefaultTheme.Muted.Render(strings.Repeat("─", dividerWidth))
				logHeader = logHeader + "\n" + dividerLine + "\n"
			}

			// Add scrollbar to log content with left padding
			rawLogContent := m.LogViewer.View()
			// Add 1 space left padding to each line of content
			contentLines := strings.Split(rawLogContent, "\n")
			for i, line := range contentLines {
				contentLines[i] = " " + line
			}
			paddedContent := strings.Join(contentLines, "\n")
			logContentWithScrollbar := m.addScrollbarToContent(paddedContent, m.LogViewerHeight-4) // -4 for header (1) + divider (1) + blank line (1) + spacing (1)
			logViewContent := logHeader + logContentWithScrollbar
			logView := lipgloss.NewStyle().Height(m.LogViewerHeight).MaxHeight(m.LogViewerHeight).Render(logViewContent)

			// Calculate jobs pane height: total height minus (log height + divider + footer + margins)
			jobsPaneHeight := m.Height - m.LogViewerHeight - 5 // -5 for divider, footer, margins
			if jobsPaneHeight < 10 {
				jobsPaneHeight = 10 // Minimum
			}
			jobsPane := lipgloss.NewStyle().MaxHeight(jobsPaneHeight).Render(jobsView)

			// Create a single-line divider split in half
			// Left half highlights when jobs pane focused, right half when logs focused
			halfWidth := contentWidth / 2
			var leftHalf, rightHalf string

			if m.Focus == JobsPane {
				// Jobs pane focused: highlight left half
				leftHalf = theme.DefaultTheme.Highlight.Render(strings.Repeat("─", halfWidth))
				rightHalf = lipgloss.NewStyle().Foreground(theme.DefaultColors.Border).Render(strings.Repeat("─", contentWidth-halfWidth))
			} else {
				// Logs pane focused: highlight right half
				leftHalf = lipgloss.NewStyle().Foreground(theme.DefaultColors.Border).Render(strings.Repeat("─", halfWidth))
				rightHalf = theme.DefaultTheme.Highlight.Render(strings.Repeat("─", contentWidth-halfWidth))
			}
			divider := leftHalf + rightHalf

			finalView = lipgloss.JoinVertical(
				lipgloss.Left,
				jobsPane,
				divider,
				logView,
				footer,
			)
		}
	} else {
		// No logs shown - constrain main content to prevent overflow
		// Available height = total - header - scroll indicator - footer - margins
		maxContentHeight := m.Height - 8 // header(2) + scroll(1) + footer(1) + margins(2) + spacing(2)
		if maxContentHeight < 10 {
			maxContentHeight = 10
		}
		constrainedContent := lipgloss.NewStyle().MaxHeight(maxContentHeight).Render(mainContent)

		finalView = lipgloss.JoinVertical(
			lipgloss.Left,
			styledHeader,
			constrainedContent,
			scrollIndicator,
			"\n", // Space before footer
			footer,
		)
	}

	// Add overall margin - minimal vertical margin to maximize screen usage
	return lipgloss.NewStyle().Margin(1, 2, 0, 2).Render(finalView)
}

// calculateOptimalLogHeight calculates the log viewer height for horizontal split
// It prioritizes log visibility while ensuring jobs section remains usable
func (m *Model) calculateOptimalLogHeight() int {
	// Total chrome that takes up screen space:
	// - top margin (1)
	// - header with margin (2)
	// - footer (1)
	// - divider between jobs and logs (1)
	// - log header with dividers (3)
	// Total: 8 lines of chrome
	chromeLines := 8

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
	// - footer (1 line)
	// - margins (1 line top, 0 bottom)
	chromeLines := 10
	if m.ViewMode == TreeView {
		chromeLines = 7 // tree view has less overhead (no table borders/headers)
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
