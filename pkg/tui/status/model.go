package status

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/tui/components/help"
	"github.com/grovetools/core/tui/components/logviewer"
	"github.com/grovetools/core/tui/keymap"
	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/flow/pkg/orchestration"
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
	FocusJobs            ViewFocus = iota // The main left/top job table
	FocusDetailPrimary                    // Logs, Frontmatter, Briefing, Edit, or Skill Tree
	FocusDetailSecondary                  // Artifact Viewport (active ONLY in SkillPane)
	FocusInput                            // Isolated agent chat input
)

type DetailPane int

const (
	NoPane DetailPane = iota
	LogsPaneDetail
	FrontmatterPane
	BriefingPane
	EditPane
	SkillPane
	NativeAgentPaneDetail
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
	StatusSummary      string
	Err                error
	Width              int
	Height             int
	ConfirmArchive     bool // Show archive confirmation
	ShowStatusPicker   bool // Show status picker
	StatusPickerCursor int  // Cursor position in status picker
	ShowTypePicker     bool // Show type picker
	TypePickerCursor   int  // Cursor position in type picker
	ShowTemplatePicker bool // Show template picker
	TemplatePickerCursor int  // Cursor position in template picker
	PlanDir            string // Store plan directory for refresh
	KeyMap        KeyMap
	Help          help.Model
	Sequence      *keymap.SequenceState // For detecting multi-key sequences (gg)
	CursorVisible bool                  // Track cursor visibility for blinking animation
	Renaming           bool
	RenameInput        textinput.Model
	RenameJobIndex     int
	selectingRecipe    bool
	recipeList         list.Model
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
	StreamingJobID      string             // Track which job is currently streaming to prevent duplicates
	StreamCancel        context.CancelFunc // Function to cancel the active agent log stream
	ActiveDetailPane    DetailPane
	columnSelectMode    bool
	columnList          list.Model
	availableColumns    []string
	columnVisibility    map[string]bool
	frontmatterViewport viewport.Model
	briefingViewport    viewport.Model
	editViewport        viewport.Model
	skillPaneViewport      viewport.Model
	skillArtifactViewport  viewport.Model         // Scrollable artifact detail viewport
	skillPaneCursor        int                     // Cursor position in the skill pane tree
	skillPaneNodes         []*SkillPaneNode        // Flattened skill/artifact nodes for cursor navigation
	skillPaneStateMap      map[string]orchestration.SkillFidelityState // Cached state map

	// Claw dialog
	ClawDialogActive   bool
	ClawDialogJobIndex int
	ClawIdleInput      textinput.Model
	ClawPromptInput    textinput.Model
	ClawDialogFocus    int // 0=idle, 1=prompt
	ClawDisabling      bool // true when disabling (unclaw)
	skillSearchActive      bool                    // Whether search mode is active in skill pane
	skillSearchInput       textinput.Model         // Text input for skill pane search
	skillFilterText        string                  // Current filter text for skill pane
	frontmatterRawContent string
	briefingRawContent    string
	editRawContent        string
	skillPaneRawContent    string
	Focus               ViewFocus // Track which pane is active
	LogSplitVertical    bool      // Track log viewer layout
	LogPaneFullscreen   bool      // Track if logs pane is fullscreen
	IsRunningJob        bool      // Track if a job is currently running
	isAutorunning       bool      // True when automatically running all stages
	originalSelection   map[string]bool // Track the original user selection for autorun
	RunLogFile         string    // Path to temporary log file for job output
	// MsgCh is the channel used by background streaming goroutines to deliver
	// messages into the Update loop. The Model's listenStream tea.Cmd drains it.
	// Close() closes this channel (once) so the listener goroutine unblocks
	// and the recursive listenStream cmd returns.
	MsgCh              chan tea.Msg
	// Daemon SSE stream state, owned per-Model so multiple embedded status
	// instances can coexist inside a single host without sharing state. Set
	// when daemonStreamConnectedMsg is delivered; cleared by Close().
	streamCh     <-chan daemon.StateUpdate
	streamCancel context.CancelFunc
	// streamWg tracks in-flight listener goroutines (both the daemon SSE
	// listener and the MsgCh dispatcher) so Close() can wait for them to
	// exit before returning. Pointer so the shared WaitGroup is preserved
	// across bubbletea's value-receiver Update copies of the Model.
	streamWg *sync.WaitGroup
	// msgChCloseOnce guards against double-closing MsgCh during Close().
	msgChCloseOnce *sync.Once
	LogViewerWidth     int       // Cached log viewer width
	LogViewerHeight    int       // Cached log viewer height
	FocusJobsWidth      int       // Cached jobs pane width for vertical split

	// Isolated agent input support
	IsolatedAgentInput       textinput.Model // Text input for sending to isolated agents
	IsolatedAgentInputActive bool            // Whether the input pane is active

	// Markdown rendering state for streaming logs
	MarkdownInCodeBlock bool // Track if we're inside a fenced code block during streaming

	// Agent status bar support (for isolated_agent and interactive_agent jobs)
	CurrentAgentStatus      *AgentStatus // Parsed agent status from tmux pane output
	LastEscPress            time.Time    // Track last ESC press for double-ESC interrupt
	PendingIdleConfirmation bool         // True if we saw idle once and need another poll to confirm

	// Daemon client for job submission, log streaming, and cancellation
	DaemonClient daemon.Client

	// DaemonConnected is true when streaming real-time updates from the daemon
	DaemonConnected bool

	// Hosted is true when running inside groveterm. Enables the native agent
	// pane preview feature (p key).
	Hosted bool
}

// IsTextEntryActive returns true when the user is focused on a text input,
// signalling that single-letter shortcuts should not be intercepted.
func (m Model) IsTextEntryActive() bool {
	return m.IsolatedAgentInputActive || m.Renaming || m.CreatingJob ||
		m.ClawDialogActive || m.skillSearchActive || m.ShowStatusPicker ||
		m.ShowTypePicker || m.ShowTemplatePicker || m.EditingDeps ||
		m.selectingRecipe || m.columnSelectMode
}

// Config carries the dependencies a status TUI Model needs. Callers construct
// a Config and pass it to New. The CLI wrapper and the terminal embed panel
// both supply their own Config so daemon clients, keymaps, and layout
// preferences are injected from the host rather than rebuilt inside the TUI.
type Config struct {
	// Plan is the loaded plan whose jobs this TUI displays. Required.
	Plan *orchestration.Plan
	// Graph is the dependency graph for Plan. Required.
	Graph *orchestration.DependencyGraph
	// DaemonClient is an optional pre-constructed daemon client used for job
	// submission, log streaming, and cancellation. If nil, the TUI runs in
	// orchestrator-only mode (no daemon features).
	DaemonClient daemon.Client
	// LogSplitVertical sets the initial orientation of the log pane split.
	// If false, the orientation is loaded from persisted tuiState.
	LogSplitVertical bool
	// Hosted is true when the TUI is embedded inside groveterm. When true,
	// the 'p' key emits SplitAgentRequestMsg to preview native agent PTY
	// panes alongside the plan. When false, the key shows a warning.
	Hosted bool
}

// New creates a new Model from the given Config.
func New(cfg Config) Model {
	// Set TUI mode env var early so loggers are configured correctly
	os.Setenv("GROVE_FLOW_TUI_MODE", "true")

	plan := cfg.Plan
	graph := cfg.Graph

	// Load user-configurable keybindings
	cliCfg, _ := config.LoadDefault() // Ignore error - NewKeyMap handles nil config gracefully

	// Flatten the job tree for navigation with parent tracking
	jobs, parents, indents := flattenJobTreeWithParents(plan)

	keyMap := NewKeyMap(cliCfg)
	helpModel := help.NewBuilder().
		WithKeys(keyMap).
		WithTitle("Plan Status - Help").
		Build()

	logViewerModel := logviewer.New(80, 20) // Initial size, will be updated

	// Initialize new viewports
	frontmatterVp := viewport.New(80, 20)
	briefingVp := viewport.New(80, 20)
	editVp := viewport.New(80, 20)
	skillPaneVp := viewport.New(80, 20)
	skillArtifactVp := viewport.New(80, 10)

	// Initialize search input for skill pane
	skillSearch := textinput.New()
	skillSearch.Placeholder = "Search skills..."
	skillSearch.CharLimit = 256

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
	availableColumns := []string{"JOB", "TITLE", "SKILL", "TYPE", "STATUS", "TEMPLATE", "MODEL", "WORKTREE", "PREPEND", "UPDATED", "COMPLETED", "DURATION"}
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

	// Start cursor at the bottom-most row
	initialCursor := 0
	if len(jobs) > 0 {
		initialCursor = len(jobs) - 1
	}

	// Initialize text input for isolated agent input
	isolatedInput := textinput.New()
	isolatedInput.Placeholder = "Type input for isolated agent..."
	isolatedInput.CharLimit = 4096
	isolatedInput.Width = 60

	// Daemon client is passed in via Config so the host (CLI wrapper or
	// terminal panel) can share a single multiplexed client. May be nil.
	daemonClient := cfg.DaemonClient

	// Prefer the caller's LogSplitVertical preference; fall back to the
	// value persisted in tuiState.
	logSplitVertical := cfg.LogSplitVertical
	if !logSplitVertical {
		logSplitVertical = state.LogSplitVertical
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
		Sequence:         keymap.NewSequenceState(),
		CursorVisible:    true,
		LogViewer:        logViewerModel,
		ShowLogs:         false, // Start with logs hidden by default
		ActiveLogJob:     nil,
		ActiveDetailPane: NoPane,
		columnSelectMode:    false,
		columnList:          columnList,
		availableColumns:    availableColumns,
		columnVisibility:    columnVisibility,
		Focus:            FocusJobs,
		LogSplitVertical: logSplitVertical,
		IsRunningJob:        false,
		RunLogFile:          "", // No longer creating TUI-specific log files
		MsgCh:               make(chan tea.Msg, 1024),
		streamWg:            &sync.WaitGroup{},
		msgChCloseOnce:      &sync.Once{},
		frontmatterViewport: frontmatterVp,
		briefingViewport:    briefingVp,
		editViewport:             editVp,
		skillPaneViewport:         skillPaneVp,
		skillArtifactViewport:     skillArtifactVp,
		skillSearchInput:          skillSearch,
		IsolatedAgentInput:       isolatedInput,
		IsolatedAgentInputActive: false,
		DaemonClient:             daemonClient,
		Hosted:                   cfg.Hosted,
	}
}

// streamMsg wraps a tea.Msg delivered via the Model's MsgCh channel. The Update
// loop unwraps it, dispatches the inner message, and re-arms the listener so
// subsequent messages continue to flow.
type streamMsg struct{ Inner tea.Msg }

// listenStream returns a tea.Cmd that blocks on m.MsgCh and wraps the next
// delivered message in a streamMsg so the Update loop can re-arm itself.
// The listener goroutine is tracked by streamWg so Close() can wait for
// it to exit after closing MsgCh.
func (m Model) listenStream() tea.Cmd {
	ch := m.MsgCh
	if ch == nil {
		return nil
	}
	wg := m.streamWg
	if wg != nil {
		wg.Add(1)
	}
	return func() tea.Msg {
		if wg != nil {
			defer wg.Done()
		}
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return streamMsg{Inner: msg}
	}
}

// listenToDaemon returns a tea.Cmd that blocks on the Model's daemon SSE
// stream channel for the next state update. The listener goroutine is
// tracked by streamWg so Close() can wait for it to exit after the stream
// context is cancelled.
func (m Model) listenToDaemon() tea.Cmd {
	ch := m.streamCh
	if ch == nil {
		return nil
	}
	wg := m.streamWg
	if wg != nil {
		wg.Add(1)
	}
	return func() tea.Msg {
		if wg != nil {
			defer wg.Done()
		}
		update, ok := <-ch
		if !ok {
			return daemonStreamErrorMsg{err: nil}
		}
		return daemonStateUpdateMsg{update: update}
	}
}

// Close releases resources owned by the Model: it cancels the daemon SSE
// stream (which causes the server to close the stream channel so the
// listener goroutine unblocks), closes MsgCh so the Model's own message
// dispatcher goroutine unblocks, and waits for both listener goroutines
// to exit before returning. Hosts that embed the status Model (e.g.
// grove terminal) must call Close() before discarding a Model instance
// so background goroutines don't leak across instance lifetimes.
func (m *Model) Close() error {
	if m.streamCancel != nil {
		m.streamCancel()
		m.streamCancel = nil
		m.streamCh = nil
	}
	if m.msgChCloseOnce != nil && m.MsgCh != nil {
		ch := m.MsgCh
		m.msgChCloseOnce.Do(func() {
			defer func() { _ = recover() }()
			close(ch)
		})
	}
	if m.streamWg != nil {
		m.streamWg.Wait()
	}
	return nil
}

// Init initializes the TUI
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		blink(),
		refreshTick(),
		subscribeToDaemonCmd(),
		m.listenStream(),
	)
}

// RollingPlanName is the name of the auto-created rolling plan.
// This constant is duplicated here to avoid import cycles with cmd package.
const RollingPlanName = "rolling"

// PlanTitle returns the styled plan name for use as a pager title row.
// Exported so the view meta-panel's page adapter can surface it via
// PageWithTitle without duplicating the rolling-plan logic.
func (m Model) PlanTitle() string {
	label := theme.IconPlan + " Plan Status: "
	if m.Plan.Name == RollingPlanName {
		return label + theme.DefaultTheme.Muted.Render("(rolling)") + "  " + theme.DefaultTheme.Muted.Italic(true).Render("auto-created for quick tasks")
	}
	return label + m.Plan.Name
}

// renderFocusJobs renders the top (or left) pane containing the jobs list.
func (m Model) renderFocusJobs(contentWidth int) string {
	// 1. Render Main Content (Table view only)
	mainContent := m.renderTableViewWithWidth(contentWidth)

	// 2. Add scroll indicators
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

	return lipgloss.JoinVertical(lipgloss.Left, mainContent, scrollIndicator)
}

// renderFocusDetailPrimary renders the bottom (or right) pane containing the detail view.
// chatBoxHeight should be passed to account for chat input box in vertical split separator calculation.
func (m Model) renderFocusDetailPrimary(contentWidth int, paneContent string, chatBoxHeight int) (string, string) {
	// Create log section header
	var logHeader string
	if m.Cursor < len(m.Jobs) {
		currentJob := m.Jobs[m.Cursor]

		var paneTitle string
		switch m.ActiveDetailPane {
		case LogsPaneDetail:
			paneTitle = "Logs"
		case FrontmatterPane:
			paneTitle = "Job Properties"
		case BriefingPane:
			paneTitle = "Briefing"
		case EditPane:
			paneTitle = "Preview"
		case SkillPane:
			paneTitle = "Skills"
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
		// Truncate so a long job title can't wrap onto the jobs
		// pane on the left.
		if m.LogViewerWidth > 0 {
			logHeader = ansi.Truncate(logHeader, m.LogViewerWidth, "…")
		}
	}

	// Create a separator
	var separator string
	if m.LogSplitVertical {
		// Original separator height was m.Height - 8, adjust for chat box
		separatorHeight := m.Height - 8 - chatBoxHeight
		if separatorHeight < 1 {
			separatorHeight = 1
		}
		var separatorLines []string
		separatorLines = append(separatorLines, "", "", "") // Top spacing
		halfHeight := separatorHeight / 2
		dimStyle := lipgloss.NewStyle().Foreground(theme.DefaultColors.Border)
		highlightStyle := lipgloss.NewStyle().Foreground(theme.DefaultColors.Blue)
		for i := 0; i < separatorHeight; i++ {
			if m.ActiveDetailPane == SkillPane {
				// Skill pane: upper half for tree, lower half for artifact viewport
				if (i < halfHeight && m.Focus == FocusDetailPrimary) ||
					(i >= halfHeight && m.Focus == FocusDetailSecondary) {
					separatorLines = append(separatorLines, highlightStyle.Render("│"))
				} else {
					separatorLines = append(separatorLines, dimStyle.Render("│"))
				}
			} else if m.Focus == FocusDetailPrimary {
				// Non-skill panes: whole divider lights up
				separatorLines = append(separatorLines, highlightStyle.Render("│"))
			} else {
				separatorLines = append(separatorLines, dimStyle.Render("│"))
			}
		}
		separator = strings.Join(separatorLines, "\n")
	} else {
		separator = lipgloss.NewStyle().Foreground(theme.DefaultColors.Border).Render(strings.Repeat("─", contentWidth))
	}

	// Render log content - the viewport handles wrapping and scrollbar
	dividerLine := theme.DefaultTheme.Muted.Render(strings.Repeat("─", m.LogViewerWidth))
	logViewWithHeader := dividerLine + "\n" + logHeader + "\n" + dividerLine + "\n" + paneContent

	// Adjust padding/width based on split direction
	var logView string
	if m.LogSplitVertical {
		// Add padding around the content
		logView = lipgloss.NewStyle().Height(m.LogViewerHeight).MaxHeight(m.LogViewerHeight).PaddingLeft(1).PaddingRight(1).Render(logViewWithHeader)
	} else {
		logHeader = " " + logHeader // Add left padding for horizontal view
		paddedContent := lipgloss.NewStyle().PaddingLeft(1).Render(paneContent)
		logView = lipgloss.NewStyle().Height(m.LogViewerHeight).MaxHeight(m.LogViewerHeight).Render(logHeader + "\n" + dividerLine + "\n" + paddedContent)
	}

	return logView, separator
}

// renderAgentInputBox renders the chat input box for isolated agents.
// If rightAligned is true, adds left margin to align with log pane in vertical split.
func (m Model) renderAgentInputBox(rightAligned bool) string {
	boxWidth := m.LogViewerWidth - 4
	if boxWidth < 20 {
		boxWidth = 20
	}

	// Dynamically set the text input width to fit inside the box
	// Create a copy to avoid modifying the original
	inputWidth := boxWidth - 2 // Account for padding
	if inputWidth < 10 {
		inputWidth = 10
	}

	// Dynamic border highlighting based on whether input is focused
	borderColor := theme.DefaultColors.Cyan // Unfocused: cyan (visible but subtle)
	if m.IsolatedAgentInputActive {
		borderColor = theme.DefaultColors.Orange // Focused: orange (highlight)
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1).
		Width(boxWidth)

	// Create a temporary copy with the correct width for rendering
	inputCopy := m.IsolatedAgentInput
	inputCopy.Width = inputWidth
	inputView := inputCopy.View()
	result := boxStyle.Render(inputView)

	// Add left margin to align with log pane in vertical split
	if rightAligned {
		leftMargin := m.FocusJobsWidth + 3 // +3 for separator
		marginStyle := lipgloss.NewStyle().MarginLeft(leftMargin)
		result = marginStyle.Render(result)
	}

	return result
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

	// If claw dialog is active, show it
	if m.ClawDialogActive {
		return m.renderClawDialog()
	}

	// If editing dependencies, show the edit deps view
	if m.EditingDeps {
		return m.renderEditDepsView()
	}

	// If selecting a recipe, render the selector and return
	if m.selectingRecipe {
		return m.renderRecipeSelector()
	}

	// Show status picker if active
	if m.ShowStatusPicker {
		return m.renderStatusPicker()
	}

	// Show type picker if active
	if m.ShowTypePicker {
		return m.renderTypePicker()
	}

	// Show template picker if active
	if m.ShowTemplatePicker {
		return m.renderTemplatePicker()
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
		jobsContentWidth = m.FocusJobsWidth
	} else {
		jobsContentWidth = contentWidth
	}

	// Render the main components
	jobsPane := m.renderFocusJobs(jobsContentWidth)

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
	if m.ActiveDetailPane == NativeAgentPaneDetail {
		// Native agent pane preview is active: the host has split the BSP
		// pane and placed the agent PTY alongside us. Just render the job
		// table at full width — no internal detail pane.
		contentHeight := m.Height - topMargin - bottomMargin - footerHeight
		jobsPaneStyled := lipgloss.NewStyle().MaxHeight(contentHeight).Render(jobsPane)
		finalView = lipgloss.JoinVertical(lipgloss.Left, jobsPaneStyled, footer)
	} else if m.ActiveDetailPane != NoPane {
		var detailContent string
		switch m.ActiveDetailPane {
		case LogsPaneDetail:
			detailContent = m.LogViewer.View()
		case FrontmatterPane:
			detailContent = addScrollbarToViewport(&m.frontmatterViewport)
		case BriefingPane:
			detailContent = addScrollbarToViewport(&m.briefingViewport)
		case EditPane:
			detailContent = addScrollbarToViewport(&m.editViewport)
		case SkillPane:
			treeView := addScrollbarToViewport(&m.skillPaneViewport)
			artifactView := addScrollbarToViewport(&m.skillArtifactViewport)
			sepLine := lipgloss.NewStyle().Foreground(theme.DefaultColors.Border).Render(strings.Repeat("─", m.LogViewerWidth))
			detailContent = treeView + "\n" + sepLine + "\n" + artifactView
		}


		// Determine if chat input should be visible based on job type and status
		// For isolated_agent and interactive_agent jobs, show the chat input when logs pane is open
		// but NOT for completed jobs (no point sending input to a finished agent)
		isAgentWithInput := m.ActiveLogJob != nil &&
			(m.ActiveLogJob.Type == orchestration.JobTypeIsolatedAgent ||
				m.ActiveLogJob.Type == orchestration.JobTypeInteractiveAgent)
		jobIsCompleted := m.ActiveLogJob != nil && m.ActiveLogJob.Status == orchestration.JobStatusCompleted
		showChatInput := isAgentWithInput && m.ActiveDetailPane == LogsPaneDetail && !jobIsCompleted

		// Calculate chatBoxHeight based on what's actually shown
		showStatusBar := m.CurrentAgentStatus != nil && isAgentWithInput && !jobIsCompleted
		chatBoxHeight := 0
		if showChatInput {
			chatBoxHeight = 3
			if showStatusBar {
				// Status bar without border: 1 line for status + todo items
				statusBarHeight := 1
				if m.CurrentAgentStatus != nil && len(m.CurrentAgentStatus.TodoItems) > 0 {
					statusBarHeight += len(m.CurrentAgentStatus.TodoItems)
				}
				chatBoxHeight += statusBarHeight
			}
		}

		// Check if fullscreen mode is active
		if m.LogPaneFullscreen {
			// Fullscreen: render only the logs pane at full width
			logsPane, _ := m.renderFocusDetailPrimary(contentWidth, detailContent, chatBoxHeight)
			contentHeight := m.Height - topMargin - bottomMargin - footerHeight - chatBoxHeight
			logsPaneStyled := lipgloss.NewStyle().Height(contentHeight).Render(logsPane)
			// Account for chat input box if visible
			if showChatInput {
				chatBox := m.renderAgentInputBox(false) // Not right-aligned in fullscreen
				if showStatusBar {
					statusBar := m.renderAgentStatusBar(m.LogViewerWidth)
					finalView = lipgloss.JoinVertical(lipgloss.Left, logsPaneStyled, statusBar, chatBox, footer)
				} else {
					finalView = lipgloss.JoinVertical(lipgloss.Left, logsPaneStyled, chatBox, footer)
				}
			} else {
				finalView = lipgloss.JoinVertical(lipgloss.Left, logsPaneStyled, footer)
			}
		} else {
			// Use the existing renderFocusDetailPrimary structure but pass in the dynamic content
			logsPane, separator := m.renderFocusDetailPrimary(contentWidth, detailContent, chatBoxHeight)
			if m.LogSplitVertical {
				// Vertical split: constrain jobs pane height
				maxFocusJobsHeight := m.Height - (footerHeight + topMargin + bottomMargin + chatBoxHeight)
				if maxFocusJobsHeight < 10 {
					maxFocusJobsHeight = 10
				}
				jobsPaneStyled := lipgloss.NewStyle().Width(m.FocusJobsWidth).MaxWidth(m.FocusJobsWidth).MaxHeight(maxFocusJobsHeight).Render(jobsPane)

				combinedPanes := lipgloss.JoinHorizontal(lipgloss.Top, jobsPaneStyled, separator, logsPane)
				// Add chat input box if visible (right-aligned to match log pane)
				if showChatInput {
					chatBox := m.renderAgentInputBox(true) // Right-aligned in vertical split
					if showStatusBar {
						statusBar := m.renderAgentStatusBar(m.LogViewerWidth)
						// Apply margin to status bar to align with chat box
						leftMargin := m.FocusJobsWidth + 3 // +3 for separator
						statusBarStyled := lipgloss.NewStyle().MarginLeft(leftMargin).Render(statusBar)
						finalView = lipgloss.JoinVertical(lipgloss.Left, combinedPanes, statusBarStyled, chatBox, footer)
					} else {
						finalView = lipgloss.JoinVertical(lipgloss.Left, combinedPanes, chatBox, footer)
					}
				} else {
					finalView = lipgloss.JoinVertical(lipgloss.Left, combinedPanes, footer)
				}
			} else {
				// Horizontal split: account for log viewer height
				maxFocusJobsHeight := m.Height - m.LogViewerHeight - (horizontalDividerHeight + footerHeight + topMargin + bottomMargin + chatBoxHeight)
				if maxFocusJobsHeight < 10 {
					maxFocusJobsHeight = 10
				}
				jobsPaneStyled := lipgloss.NewStyle().MaxHeight(maxFocusJobsHeight).Render(jobsPane)

				// Push footer to bottom by setting height on combined content
				contentHeight := m.Height - topMargin - bottomMargin - footerHeight - chatBoxHeight
				combinedContent := lipgloss.JoinVertical(lipgloss.Left, jobsPaneStyled, separator, logsPane)
				combinedContent = lipgloss.NewStyle().Height(contentHeight).Render(combinedContent)
				// Add chat input box if visible
				if showChatInput {
					chatBox := m.renderAgentInputBox(false) // Not right-aligned in horizontal split
					if showStatusBar {
						statusBar := m.renderAgentStatusBar(m.LogViewerWidth)
						finalView = lipgloss.JoinVertical(lipgloss.Left, combinedContent, statusBar, chatBox, footer)
					} else {
						finalView = lipgloss.JoinVertical(lipgloss.Left, combinedContent, chatBox, footer)
					}
				} else {
					finalView = lipgloss.JoinVertical(lipgloss.Left, combinedContent, footer)
				}
			}
		}
	} else {
		// No logs: use same calculation as vertical split
		maxFocusJobsHeight := m.Height - (footerHeight + topMargin + bottomMargin + 2) // +2 for newline and spacing
		if maxFocusJobsHeight < 10 {
			maxFocusJobsHeight = 10
		}
		jobsPaneStyled := lipgloss.NewStyle().MaxHeight(maxFocusJobsHeight).Render(jobsPane)
		finalView = lipgloss.JoinVertical(lipgloss.Left, jobsPaneStyled, "\n", footer)
	}

	// Zero top/bottom margin; horizontal padding only. The embedding
	// meta-panel provides its own chrome, and standalone mode also
	// looks fine without the extra top row.
	return lipgloss.NewStyle().Margin(0, 2).Render(finalView)
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
	minJobsHeight += minVisibleJobs

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

// calculateFocusJobsWidth calculates the optimal width for the jobs pane
// based on the content of the currently visible columns.
func (m *Model) calculateFocusJobsWidth() int {
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
			if jobColWidth > columnWidths["JOB"] {
				columnWidths["JOB"] = jobColWidth
			}
		}
		if m.columnVisibility["TITLE"] {
			titleWidth := lipgloss.Width(job.Title)
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
		m.FocusJobsWidth = m.calculateFocusJobsWidth()
		if m.Width < m.FocusJobsWidth+minLogsWidth+verticalSeparatorWidth {
			m.LogSplitVertical = false
			m.StatusSummary = theme.DefaultTheme.Muted.Render("Switched to horizontal split (terminal too narrow)")
		}
	}

	if m.ShowLogs {
		// Calculate chat input height based on what's actually shown
		isAgentWithInput := m.ActiveLogJob != nil &&
			(m.ActiveLogJob.Type == orchestration.JobTypeIsolatedAgent ||
				m.ActiveLogJob.Type == orchestration.JobTypeInteractiveAgent)
		jobIsCompleted := m.ActiveLogJob != nil && m.ActiveLogJob.Status == orchestration.JobStatusCompleted
		chatInputHeight := 0
		if isAgentWithInput && m.ActiveDetailPane == LogsPaneDetail && !jobIsCompleted {
			chatInputHeight = 3
			// Add height for status bar if it's visible (no border, just content)
			if m.CurrentAgentStatus != nil {
				statusBarHeight := 1
				if len(m.CurrentAgentStatus.TodoItems) > 0 {
					statusBarHeight += len(m.CurrentAgentStatus.TodoItems)
				}
				chatInputHeight += statusBarHeight
			}
		}

		if m.LogPaneFullscreen {
			// Fullscreen: use full terminal dimensions minus margins
			m.LogViewerWidth = m.Width - (leftMargin + rightMargin) - 1
			m.LogViewerHeight = m.Height - (footerHeight + topMargin + chatInputHeight)
		} else if m.LogSplitVertical {
			// In vertical split, the container has PaddingLeft(1) and PaddingRight(1)
			// So the content width is LogViewerWidth - 2
			// The header is inside the jobs pane, not separate, so don't subtract headerHeight
			m.LogViewerWidth = m.Width - m.FocusJobsWidth - verticalSeparatorWidth - 2
			m.LogViewerHeight = m.Height - (footerHeight + topMargin + chatInputHeight)
		} else {
			// In horizontal split, only PaddingLeft(1) is applied
			m.LogViewerWidth = m.Width - (leftMargin + rightMargin) - 1
			m.LogViewerHeight = m.calculateOptimalLogHeight() - chatInputHeight
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

	return availableHeight
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

	// Find root jobs via the orchestration package.
	roots := orchestration.FindRootJobs(plan)

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
	dependents := orchestration.FindAllDependents(job, plan)
	for _, dep := range dependents {
		addJobAndDependentsWithParent(dep, plan, result, visited, parents, indents, job, indent+1)
	}
}

func formatStatusSummaryHelper(plan *orchestration.Plan) string {
	// Status summary is no longer used in TUI-only mode
	return ""
}

// recipeItem represents a recipe in the selection list
type recipeItem struct {
	name        string
	description string
}

func (i recipeItem) FilterValue() string { return i.name }
func (i recipeItem) Title() string       { return i.name }
func (i recipeItem) Description() string { return i.description }

// recipeDelegate renders items in the recipe selection list
type recipeDelegate struct{}

func (d recipeDelegate) Height() int                             { return 1 }
func (d recipeDelegate) Spacing() int                            { return 0 }
func (d recipeDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }
func (d recipeDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	i, ok := item.(recipeItem)
	if !ok {
		return
	}

	var str string
	if index == m.Index() {
		str = fmt.Sprintf("%s %s", theme.DefaultTheme.Highlight.Render("▶"), i.Title())
	} else {
		str = fmt.Sprintf("  %s", i.Title())
	}

	fmt.Fprint(w, str)
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
