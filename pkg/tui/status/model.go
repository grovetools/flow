package status

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/grovetools/agentlogs/pkg/usage"
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/pkg/daemon"
	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/tui/components/help"
	"github.com/grovetools/core/tui/components/logviewer"
	"github.com/grovetools/core/tui/keymap"
	"github.com/grovetools/core/tui/panes"
	"github.com/grovetools/core/tui/theme"

	"github.com/grovetools/flow/pkg/orchestration"
)

const (
	// Heights
	footerHeight            = 1
	horizontalDividerHeight = 1
	logHeaderHeight         = 3 // Header text + two divider lines
	// tableChrome is what gtable.SelectableTableWithOptions spends on the
	// table frame itself: top border, header row, header separator, bottom
	// border. Everything else in the pane is a display row.
	tableChrome = 4
	// editDepsChrome is what renderEditDepsView spends around its job list:
	// the title block as the Header style renders it plus the instructions
	// line and their blank separators (7), its own inline scroll indicator
	// behind a blank spacer (2), and the bottom half of the Margin(1, 2) it
	// wraps the whole dialog in (1).
	editDepsChrome = 10

	// Widths
	minLogsWidth           = 50
	verticalSeparatorWidth = 3 // Separator + margins
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
	ContextPaneDetail
	MemoryPaneDetail
	NativeAgentPaneDetail
	EditorPaneDetail        // BSP split editor (hosted mode only)
	TokenPaneDetail         // Per-job token usage + cost breakdown
	AccessedFilesPaneDetail // Per-job accessed-files trace (context transfer)
)

// Model represents the state of the TUI
type Model struct {
	Plan         *orchestration.Plan
	Graph        *orchestration.DependencyGraph
	Orchestrator *orchestration.Orchestrator // Direct orchestrator for job execution
	Jobs         []*orchestration.Job
	// JobParents and JobIndents describe the presentation tree. A valid
	// ParentJobID ownership edge wins over dependency nesting; scheduling still
	// uses only the dependency graph.
	JobParents        map[string]*orchestration.Job
	JobIndents        map[string]int
	OwnershipChildren map[string][]*orchestration.Job // Cached direct lineage children by owner ID.
	// DisplayRows is the view model the jobs table renders: one RowTypeJob
	// row per job plus virtual workflow child rows. Cursor and ScrollOffset
	// index DisplayRows, never m.Jobs. Rebuilt by rebuildDisplayRows after
	// every reflatten of m.Jobs (RefreshMsg) and on workflow activity.
	DisplayRows []DisplayRow
	// FoldState records explicit user fold overrides keyed by NodeID
	// (true = collapsed). Nodes absent from the map follow the default
	// policy (running jobs auto-expand, everything else collapsed).
	FoldState      map[string]bool
	Cursor         int
	ScrollOffset   int             // Track scroll position for viewport
	Selected       map[string]bool // For multi-select
	StatusSummary  string
	Err            error
	Width          int
	Height         int
	ConfirmArchive bool // Show archive confirmation
	// fieldEditor is the single schema-driven config-field editor (the c…
	// Change namespace). Non-nil while open; it replaced the three bespoke
	// ShowStatusPicker/ShowTypePicker/ShowTemplatePicker bool+cursor pairs.
	fieldEditor      *fieldEditorState
	PlanDir          string // Store plan directory for refresh
	KeyMap           KeyMap
	Help             help.Model
	WhichKey         keymap.WhichKeyHost // Chord/which-key mixin: shared Sequence engine + namespaces + show-delay
	CursorVisible    bool                // Track cursor visibility for blinking animation
	Renaming         bool
	RenameInput      textinput.Model
	RenameJobIndex   int
	selectingRecipe  bool
	recipeList       list.Model
	EditingDeps      bool
	EditDepsJobIndex int
	EditDepsSelected map[string]bool // Track which jobs are selected as dependencies
	CreatingJob      bool
	CreateJobInput   textinput.Model
	CreateJobType    string // "xml" or "impl"
	CreateJobBaseJob *orchestration.Job
	CreateJobDeps    []*orchestration.Job // For multi-select case
	ShowLogs         bool
	LogViewer        logviewer.Model
	ActiveLogJob     *orchestration.Job
	StreamingJobID   string             // Track which job is currently streaming to prevent duplicates
	StreamCancel     context.CancelFunc // Function to cancel the active agent log stream
	ActiveDetailPane DetailPane
	HasFocus         bool // True when the host has given this panel focus
	columnSelectMode bool
	columnList       list.Model
	availableColumns []string
	columnVisibility map[string]bool
	// jobCellCap truncates JOB-column cells when even a JOB-only table is
	// wider than its pane. Set per render by fitToWidth on its own copy of
	// the model; 0 means no cap.
	jobCellCap            int
	frontmatterViewport   viewport.Model
	briefingViewport      viewport.Model
	tokenViewport         viewport.Model
	editViewport          viewport.Model
	accessedFilesViewport viewport.Model
	accessedFiles         []orchestration.AccessedFile // Deduped trace for the accessed-files pane (absolute paths)
	accessedFilesDisplay  []string                     // Workspace-rooted display form, index-aligned with accessedFiles
	skillPaneViewport     viewport.Model
	skillArtifactViewport viewport.Model                              // Scrollable artifact detail viewport
	skillPaneCursor       int                                         // Cursor position in the skill pane tree
	skillPaneNodes        []*SkillPaneNode                            // Flattened skill/artifact nodes for cursor navigation
	skillPaneStateMap     map[string]orchestration.SkillFidelityState // Cached state map

	// Workflow inline-tree state. Per-agent transcript line buffers are
	// fed by the transcript collector via MsgCh; the inline tree itself is
	// rendered from WorkflowStates by buildDisplayRows.
	workflowAgentLines map[string][]string // formatted transcript lines per agent (capped)
	// WorkflowStates is the per-job workflow registry: ONE daemon-backed
	// subscription (or per-job FileSource fallbacks) routes events here by
	// JobID — events for non-cursor jobs are folded, never dropped.
	WorkflowStates map[string]*workflowPaneState
	// workflowDaemonCancel tears down the single DaemonSource subscription
	// (and its forwarding goroutine). Nil when the daemon path is inactive.
	workflowDaemonCancel context.CancelFunc
	// workflowMonitorCancels tears down per-job FileSource fallback
	// monitors (daemon unreachable only), keyed by job ID.
	workflowMonitorCancels map[string]context.CancelFunc
	// workflowMonitorPending marks jobs whose FileSource session discovery
	// is in flight, so the 2s refresh doesn't double-start monitors.
	workflowMonitorPending map[string]bool
	// workflowSelectedAgentID is the workflow agent whose buffered
	// transcript is routed into the already-open log viewport while the
	// cursor rests on its row. Empty when the cursor is not on an agent
	// row (the viewport shows the job log as usual).
	workflowSelectedAgentID string
	// workflowDirtyJobs marks jobs whose workflow state changed since the
	// last display-row rebuild. Rebuilds are coalesced on a ~100ms tick
	// (workflowRebuildTickMsg) — never per event.
	workflowDirtyJobs map[string]bool
	// workflowRebuildPending is true while a coalescing rebuild tick is
	// already scheduled.
	workflowRebuildPending bool
	// workflowTranscriptDirty marks that the selected agent's buffered
	// transcript grew since the log viewport last refreshed; the refresh
	// rides the same coalescing tick.
	workflowTranscriptDirty bool
	// workflowArchiveChecked marks completed jobs whose archived-run
	// fallback load (.artifacts/<job-id>/workflows/) has been attempted,
	// so each job is loaded at most once per TUI session.
	workflowArchiveChecked map[string]bool
	// workflowAgentMarkdown caches historical transcript markdown loaded
	// by loadHistoricalWorkflowTranscriptCmd for completed agents, keyed
	// by agent ID. This allows detail view on completed agents without
	// live streaming.
	workflowAgentMarkdown map[string]string
	// workflowAgentLoading tracks which agent IDs are currently loading
	// their historical transcripts to avoid duplicate loads.
	workflowAgentLoading map[string]bool

	// Claw dialog
	ClawDialogActive         bool
	ClawDialogJobIndex       int
	ClawIdleInput            textinput.Model
	ClawPromptInput          textinput.Model
	ClawDialogFocus          int             // 0=idle, 1=prompt
	ClawDisabling            bool            // true when disabling (unclaw)
	ClawTargetSelectorActive bool            // true when showing target picker before claw dialog
	ClawSelectedTarget       string          // selected signal target name (empty = broadcast)
	ClawTargetCursor         int             // cursor position in target list
	ClawTargetOptions        []string        // available target options
	skillSearchActive        bool            // Whether search mode is active in skill pane
	skillSearchInput         textinput.Model // Text input for skill pane search
	frontmatterRawContent    string
	briefingRawContent       string
	tokenRawContent          string
	editRawContent           string
	skillPaneRawContent      string
	accessedFilesRawContent  string
	// tokenColumnCache memoizes the rendered TOKENS column cell per job ID
	// so the table render doesn't re-read the artifact (or re-summarize) on
	// every frame. Invalidated by refreshes via clearTokenColumnCache.
	tokenColumnCache map[string]string
	// modelColumnCache memoizes the rendered MODEL column cell per job ID.
	// The cell's fallback chain (resolveJobDisplayModel) may read the token
	// usage artifact, so it's memoized alongside tokenColumnCache and
	// invalidated at the same three sites (evictJobRenderCaches /
	// clearTokenColumnCache).
	modelColumnCache map[string]string
	// sessionColumnCache memoizes native session IDs (including misses) so the
	// filesystem registry is never queried once per frame.
	sessionColumnCache map[string]string
	// defaultProviderName is the config-default agent provider, resolved
	// once in New() (LoadFlowConfig + ResolveJobProviderName). Used by the
	// MODEL cell so a job with empty `provider:` folds under the effective
	// default rather than re-loading the flow config per row.
	defaultProviderName string
	// tokenAgentArtifact caches the per-subagent usage map (agentID →
	// AgentUsage) read from a completed job's immutable token-usage.json
	// artifact. Keyed by job ID; lazily filled and kept across refreshes
	// (the artifact never changes once written).
	tokenAgentArtifact map[string]map[string]usage.AgentUsage
	// tokenAgentLive holds the per-subagent usage map for a running job,
	// sourced from the detail pane's live Summary (and the running-ctx
	// background refresher). Keyed by job ID; overwritten as fresher
	// summaries arrive and ignored once the job completes (artifact wins).
	tokenAgentLive map[string]map[string]usage.AgentUsage
	// runningTokenCell holds the live token Summary for an in-progress agent
	// job, produced off the event loop by the running-ctx refresher. Drives
	// the TOKENS column's "$cost · NNk ctx" for running jobs (completed jobs
	// read the artifact). Survives refreshes; replaced as the job completes.
	runningTokenCell map[string]usage.Summary
	// lastRunningTokenRefresh throttles the running-ctx refresher so large
	// coordinator transcripts aren't re-summarized on every 2s tick.
	lastRunningTokenRefresh time.Time
	Focus                   ViewFocus       // Track which pane is active
	LogSplitVertical        bool            // Track log viewer layout
	LogPaneFullscreen       bool            // Track if logs pane is fullscreen
	IsRunningJob            bool            // Track if a job is currently running
	isAutorunning           bool            // True when automatically running all stages
	originalSelection       map[string]bool // Track the original user selection for autorun
	// InitializingJobs marks jobs submitted for execution from this TUI whose
	// store status hasn't caught up yet (spawning can take a while). Keyed by
	// job ID, valued with the submission time so the marker can expire. Rows
	// render a transient "initializing" state until the real status
	// (running/terminal) supersedes it or the grace window lapses.
	InitializingJobs map[string]time.Time
	// lastRefreshTickAt is when the plan-refresh tick last came back around.
	// Seeded at construction so a model that has not ticked yet is not
	// mistaken for a stalled one. Read on regaining focus to decide whether
	// the self-rearming tick loop needs reviving — see refreshStallThreshold.
	lastRefreshTickAt time.Time
	RunLogFile        string // Path to temporary log file for job output
	// MsgCh is the channel used by background streaming goroutines to deliver
	// messages into the Update loop. The Model's listenStream tea.Cmd drains it.
	// Close() closes this channel (once) so the listener goroutine unblocks
	// and the recursive listenStream cmd returns.
	MsgCh chan tea.Msg
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
	msgChCloseOnce  *sync.Once
	LogViewerWidth  int // Cached log viewer width
	LogViewerHeight int // Cached log viewer height
	FocusJobsWidth  int // Cached jobs pane width for vertical split

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

	// Manager orchestrates the pane layout (jobs + detail). The Manager
	// handles split direction, fullscreen zoom, and focus cycling.
	Manager panes.Manager

	// SkillSubFocus tracks which sub-pane within a tree+detail pane is
	// focused: 0 = tree view, 1 = detail viewport. Relevant when
	// ActiveDetailPane is SkillPane or WorkflowPaneDetail.
	SkillSubFocus int

	// viewportActive tracks whether a BSP ViewportPanel is currently open
	// in the host. When true, switching between migrated detail types
	// (logs, frontmatter, briefing) swaps content/title instead of
	// creating a new BSP split.
	viewportActive bool
}

// IsTextEntryActive returns true when the user is focused on a text input,
// signalling that single-letter shortcuts should not be intercepted.
func (m Model) IsTextEntryActive() bool {
	return m.Help.IsTextEntryActive() || m.IsolatedAgentInputActive || m.Renaming || m.CreatingJob ||
		m.ClawDialogActive || m.ClawTargetSelectorActive || m.skillSearchActive || m.fieldEditor != nil ||
		m.EditingDeps ||
		m.selectingRecipe || m.columnSelectMode
}

// IsChordPending returns true when a multi-key sequence is armed (a namespace
// prefix like "v"/"c" with the which-key popup showing, or the gg motion). The
// host (view.Model) consults this so an esc pressed to dismiss the chord/popup
// is delegated INTO this model — where the seam's SequenceCancel consumes it —
// instead of being read by the host as "pop back to the plan browser". Without
// this, esc-to-close-which-key accidentally exits the whole status TUI.
func (m Model) IsChordPending() bool {
	return m.WhichKey.IsPending()
}

// closeCurrentDetail tears down whatever detail view is currently active and
// resets the model to NoPane/FocusJobs. It returns a tea.Cmd that emits the
// appropriate BSP close message when leaving a host-managed split (agent or
// editor), or nil for internal lipgloss panes.
func (m *Model) closeCurrentDetail() tea.Cmd {
	if m.ActiveDetailPane == NoPane {
		return nil
	}

	// If the detail pane is promoted (BSP split), demote it to tear down
	// the host split and reclaim layout space.
	if m.Manager.IsPromoted("detail") {
		var demoteCmd tea.Cmd
		m.Manager, demoteCmd = m.Manager.Demote("detail")
		m.ActiveDetailPane = NoPane
		m.ShowLogs = false
		m.Focus = FocusJobs
		m.ActiveLogJob = nil
		m.CurrentAgentStatus = nil
		m.StatusSummary = ""
		m.viewportActive = false
		// Cancel any active log stream (viewport-promoted logs).
		if m.StreamCancel != nil {
			m.StreamCancel()
			m.StreamCancel = nil
			m.StreamingJobID = ""
		}
		m.workflowSelectedAgentID = ""
		return demoteCmd
	}

	// Internal lipgloss pane — just hide it.
	m.LogViewer.Stop()
	if m.StreamCancel != nil {
		m.StreamCancel()
		m.StreamCancel = nil
	}
	m.StreamingJobID = ""
	m.workflowSelectedAgentID = ""
	m.ShowLogs = false
	m.Manager, _ = m.Manager.SetHidden("detail", true)
	m.Focus = FocusJobs
	m.ActiveLogJob = nil
	m.ActiveDetailPane = NoPane
	m.CurrentAgentStatus = nil
	m.StatusSummary = ""
	m.IsolatedAgentInputActive = false
	m.IsolatedAgentInput.Blur()
	return nil
}

// syncLayoutFromManager reads the pane dimensions from the Manager into the
// legacy layout fields (LogViewerWidth, LogViewerHeight, FocusJobsWidth) so
// existing viewport sizing code continues to work unchanged.
func (m *Model) syncLayoutFromManager() {
	dp := m.Manager.Panes[1].Model.(*DetailPaneModel)
	m.LogViewerWidth = dp.Width
	m.LogViewerHeight = dp.Height
	jp := m.Manager.Panes[0].Model.(*JobsPaneModel)
	m.FocusJobsWidth = jp.Width
}

// syncFocusFromManager reads the Manager's focus and direction state into
// the legacy fields (Focus, LogSplitVertical, LogPaneFullscreen).
func (m *Model) syncFocusFromManager() {
	p := m.Manager.ActivePane()
	if p == nil {
		return
	}
	switch p.ID {
	case "jobs":
		m.Focus = FocusJobs
	case "detail":
		if m.ActiveDetailPane == SkillPane && m.SkillSubFocus == 1 {
			m.Focus = FocusDetailSecondary
		} else {
			m.Focus = FocusDetailPrimary
		}
	}
	m.LogSplitVertical = m.Manager.Direction == panes.DirectionHorizontal
	m.LogPaneFullscreen = m.Manager.FullscreenIdx >= 0
}

// syncPaneLayout adjusts the jobs pane's Fixed/Flex values based on the
// current split direction so the Manager distributes space correctly.
func (m *Model) syncPaneLayout() {
	detailVisible := !m.Manager.IsHidden("detail") && !m.Manager.IsPromoted("detail")
	if m.Manager.Direction == panes.DirectionHorizontal && detailVisible {
		m.Manager.Panes[0].Fixed = m.calculateFocusJobsWidth()
		m.Manager.Panes[0].Flex = 0
	} else {
		m.Manager.Panes[0].Fixed = 0
		m.Manager.Panes[0].Flex = 1
	}
}

// calculateChatInputHeight returns the height needed for the chat input area,
// or 0 if it should not be shown.
func (m Model) calculateChatInputHeight() int {
	isAgentWithInput := m.ActiveLogJob != nil &&
		(m.ActiveLogJob.Type == orchestration.JobTypeIsolatedAgent ||
			m.ActiveLogJob.Type == orchestration.JobTypeInteractiveAgent)
	jobIsCompleted := m.ActiveLogJob != nil && m.ActiveLogJob.Status == orchestration.JobStatusCompleted
	showChatInput := isAgentWithInput && m.ActiveDetailPane == LogsPaneDetail && !jobIsCompleted && m.ShowLogs
	if !showChatInput {
		return 0
	}
	h := 3
	if m.CurrentAgentStatus != nil {
		h++ // status line
		if len(m.CurrentAgentStatus.TodoItems) > 0 {
			h += len(m.CurrentAgentStatus.TodoItems)
		}
	}
	return h
}

// renderDetailHeader returns the pre-rendered header line for the detail pane.
func (m Model) renderDetailHeader() string {
	currentJob := m.CurrentJob()
	if currentJob == nil {
		return ""
	}

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
	case ContextPaneDetail:
		paneTitle = "Context"
	case MemoryPaneDetail:
		paneTitle = "Memory"
	case TokenPaneDetail:
		paneTitle = "Token Usage"
	case AccessedFilesPaneDetail:
		paneTitle = "Accessed Files"
	}

	jobIcon := getJobIcon(currentJob)
	jobTitle := currentJob.Title
	if jobTitle == "" {
		jobTitle = currentJob.Filename
	}
	statusIcon := m.jobStatusIcon(currentJob)
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

	header := fmt.Sprintf("%s: %s  %s%s • %s • %s%s", paneTitle, jobIcon, jobTitle, filenameDisplay, template, statusIcon, scrollInfo)
	detailFocused := m.Focus == FocusDetailPrimary || m.Focus == FocusDetailSecondary
	if detailFocused {
		header = theme.DefaultTheme.Bold.Render(header)
	} else {
		header = theme.DefaultTheme.Muted.Render(header)
	}
	if m.LogViewerWidth > 0 {
		header = ansi.Truncate(header, m.LogViewerWidth, "…")
	}
	return header
}

// renderDetailContent returns the pre-rendered content for the active detail pane.
func (m Model) renderDetailContent() string {
	switch m.ActiveDetailPane {
	case LogsPaneDetail:
		return m.LogViewer.View()
	case FrontmatterPane:
		return addScrollbarToViewport(&m.frontmatterViewport)
	case BriefingPane:
		return addScrollbarToViewport(&m.briefingViewport)
	case TokenPaneDetail:
		return addScrollbarToViewport(&m.tokenViewport)
	case AccessedFilesPaneDetail:
		return addScrollbarToViewport(&m.accessedFilesViewport)
	case EditPane:
		return addScrollbarToViewport(&m.editViewport)
	case SkillPane:
		treeView := addScrollbarToViewport(&m.skillPaneViewport)
		artifactView := addScrollbarToViewport(&m.skillArtifactViewport)
		sepLine := lipgloss.NewStyle().Foreground(theme.DefaultColors.Border).Render(strings.Repeat("─", m.LogViewerWidth))
		return treeView + "\n" + sepLine + "\n" + artifactView
	default:
		return ""
	}
}

// renderInputContent returns the pre-rendered input pane content
// (agent status bar + chat input box).
func (m Model) renderInputContent() string {
	var parts []string

	isAgentWithInput := m.ActiveLogJob != nil &&
		(m.ActiveLogJob.Type == orchestration.JobTypeIsolatedAgent ||
			m.ActiveLogJob.Type == orchestration.JobTypeInteractiveAgent)
	jobIsCompleted := m.ActiveLogJob != nil && m.ActiveLogJob.Status == orchestration.JobStatusCompleted
	showStatusBar := m.CurrentAgentStatus != nil && isAgentWithInput && !jobIsCompleted

	if showStatusBar {
		statusBar := m.renderAgentStatusBar(m.LogViewerWidth)
		parts = append(parts, statusBar)
	}

	chatBox := m.renderAgentInputBox(false)
	parts = append(parts, chatBox)

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// resizeAllDetailViewports updates all viewport dimensions and re-wraps content
// based on the current LogViewerWidth/LogViewerHeight. Call this after any
// layout change (direction toggle, fullscreen toggle, window resize).
func (m *Model) resizeAllDetailViewports() {
	m.frontmatterViewport.Width = m.LogViewerWidth
	m.frontmatterViewport.Height = m.LogViewerHeight - logHeaderHeight
	m.briefingViewport.Width = m.LogViewerWidth
	m.briefingViewport.Height = m.LogViewerHeight - logHeaderHeight
	m.tokenViewport.Width = m.LogViewerWidth
	m.tokenViewport.Height = m.LogViewerHeight - logHeaderHeight
	m.accessedFilesViewport.Width = m.LogViewerWidth
	m.accessedFilesViewport.Height = m.LogViewerHeight - logHeaderHeight
	m.editViewport.Width = m.LogViewerWidth
	m.editViewport.Height = m.LogViewerHeight - logHeaderHeight
	m.updateSkillViewportSizes()

	if m.frontmatterRawContent != "" {
		styledContent := renderStyledFrontmatter(m.frontmatterRawContent)
		wrappedContent := wrapContentForViewport(styledContent, m.frontmatterViewport.Width-1)
		m.frontmatterViewport.SetContent(wrappedContent)
	}
	if m.briefingRawContent != "" {
		styledContent := renderStyledBriefing(m.briefingRawContent)
		wrappedContent := wrapContentForViewport(styledContent, m.briefingViewport.Width-1)
		m.briefingViewport.SetContent(wrappedContent)
	}
	if m.tokenRawContent != "" {
		wrappedContent := wrapContentForViewport(m.tokenRawContent, m.tokenViewport.Width-1)
		m.tokenViewport.SetContent(wrappedContent)
	}
	if m.accessedFilesRawContent != "" {
		wrappedContent := wrapContentForViewport(m.accessedFilesRawContent, m.accessedFilesViewport.Width-1)
		m.accessedFilesViewport.SetContent(wrappedContent)
	}
	if m.editRawContent != "" {
		styledContent := renderStyledMarkdown(m.editRawContent)
		wrappedContent := wrapContentForViewport(styledContent, m.editViewport.Width-1)
		m.editViewport.SetContent(wrappedContent)
	}
	if m.skillPaneRawContent != "" {
		m.skillPaneViewport.SetContent(wrapContentForViewport(m.skillPaneRawContent, m.skillPaneViewport.Width-1))
	}
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
	tokenVp := viewport.New(80, 20)
	accessedFilesVp := viewport.New(80, 20)
	editVp := viewport.New(80, 20)
	skillPaneVp := viewport.New(80, 20)
	skillArtifactVp := viewport.New(80, 10)

	// Initialize search input for skill pane
	skillSearch := textinput.New()
	skillSearch.Placeholder = "Search skills..."
	skillSearch.CharLimit = 256

	// Create orchestrator for direct job execution
	orchConfig := &orchestration.OrchestratorConfig{
		MaxParallelJobs:     1, // TUI runs one job or selection at a time
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
	availableColumns := []string{"JOB", "TITLE", "SKILL", "TYPE", "STATUS", "TEMPLATE", "MODEL", "SESSION", "WORKTREE", "INLINE", "UPDATED", "COMPLETED", "DURATION", "TOKENS"}
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

	// The cursor starts on the bottom-most row. It indexes DisplayRows — which
	// len(jobs) bounds in neither direction, since folding hides job rows and
	// workflows add virtual ones — so it is set below, once the rows exist.

	// Initialize text input for isolated agent input
	isolatedInput := textinput.New()
	isolatedInput.Placeholder = "Type input for isolated agent..."
	isolatedInput.CharLimit = 4096
	isolatedInput.Width = 60

	// Resolve the config-default agent provider once so the MODEL column can
	// fold provider into non-claude cells without re-loading the flow config
	// per row (ResolveJobProviderNameFromConfig calls LoadFlowConfig each time).
	flowCfg, _ := orchestration.LoadFlowConfig()
	if flowCfg == nil {
		flowCfg = &orchestration.FlowConfig{}
	}
	defaultProviderName := orchestration.ResolveJobProviderName(nil, *flowCfg)

	// Daemon client is passed in via Config so the host (CLI wrapper or
	// terminal panel) can share a single multiplexed client. May be nil.
	daemonClient := cfg.DaemonClient

	// Prefer the caller's LogSplitVertical preference; fall back to the
	// value persisted in tuiState.
	logSplitVertical := cfg.LogSplitVertical
	if !logSplitVertical {
		logSplitVertical = state.LogSplitVertical
	}

	// Initialize the pane manager with jobs + detail panes.
	// The input pane is rendered separately outside the Manager.
	mgr := panes.New(
		panes.Pane{ID: "jobs", Model: NewJobsPaneModel(), Flex: 1, MinSize: 20},
		panes.Pane{ID: "detail", Model: NewDetailPaneModel(), Flex: 2, MinSize: 20, Hidden: true},
	)
	// The pane Manager toggles fullscreen ("zoom") on its own KeyMap, which
	// defaults to "z". flow-status rebound its ToggleFullscreen action off the
	// reserved fold prefix "z" onto "f" (sign-off E2); the fullscreen handler
	// forwards the keypress via m.Manager.Update(msg), so the Manager's binding
	// must match "f" too — otherwise the toggle silently no-ops (the original
	// "fullscreen f didn't work" bug had this second cause beyond the ShowLogs
	// gate).
	mgr.KeyMap.ToggleFullscreen = key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "zoom"))
	if logSplitVertical {
		mgr.Direction = panes.DirectionHorizontal
		mgr.Panes[0].Fixed = 45 // Will be recalculated on first WindowSizeMsg
		mgr.Panes[0].Flex = 0
	} else {
		mgr.Direction = panes.DirectionVertical
	}

	m := Model{
		Plan:                     plan,
		Graph:                    graph,
		Orchestrator:             orch,
		Jobs:                     jobs,
		JobParents:               parents,
		JobIndents:               indents,
		OwnershipChildren:        indexOwnershipChildren(jobs, parents),
		FoldState:                make(map[string]bool),
		Cursor:                   0,
		ScrollOffset:             0,
		Selected:                 make(map[string]bool),
		StatusSummary:            formatStatusSummaryHelper(plan),
		ConfirmArchive:           false,
		PlanDir:                  plan.Directory,
		KeyMap:                   keyMap,
		Help:                     helpModel,
		WhichKey:                 keymap.NewWhichKeyHost(cliCfg, keyMap.Namespaces()...),
		CursorVisible:            true,
		LogViewer:                logViewerModel,
		ShowLogs:                 false, // Start with logs hidden by default
		ActiveLogJob:             nil,
		ActiveDetailPane:         NoPane,
		columnSelectMode:         false,
		columnList:               columnList,
		availableColumns:         availableColumns,
		columnVisibility:         columnVisibility,
		Focus:                    FocusJobs,
		LogSplitVertical:         logSplitVertical,
		IsRunningJob:             false,
		RunLogFile:               "", // No longer creating TUI-specific log files
		MsgCh:                    make(chan tea.Msg, 1024),
		lastRefreshTickAt:        time.Now(),
		streamWg:                 &sync.WaitGroup{},
		msgChCloseOnce:           &sync.Once{},
		frontmatterViewport:      frontmatterVp,
		briefingViewport:         briefingVp,
		tokenViewport:            tokenVp,
		accessedFilesViewport:    accessedFilesVp,
		editViewport:             editVp,
		tokenColumnCache:         make(map[string]string),
		modelColumnCache:         make(map[string]string),
		sessionColumnCache:       make(map[string]string),
		defaultProviderName:      defaultProviderName,
		tokenAgentArtifact:       make(map[string]map[string]usage.AgentUsage),
		tokenAgentLive:           make(map[string]map[string]usage.AgentUsage),
		runningTokenCell:         make(map[string]usage.Summary),
		skillPaneViewport:        skillPaneVp,
		skillArtifactViewport:    skillArtifactVp,
		workflowAgentLines:       make(map[string][]string),
		WorkflowStates:           make(map[string]*workflowPaneState),
		workflowMonitorCancels:   make(map[string]context.CancelFunc),
		workflowMonitorPending:   make(map[string]bool),
		workflowDirtyJobs:        make(map[string]bool),
		workflowArchiveChecked:   make(map[string]bool),
		workflowAgentMarkdown:    make(map[string]string),
		workflowAgentLoading:     make(map[string]bool),
		skillSearchInput:         skillSearch,
		IsolatedAgentInput:       isolatedInput,
		IsolatedAgentInputActive: false,
		DaemonClient:             daemonClient,
		Hosted:                   cfg.Hosted,
		Manager:                  mgr,
	}
	m.DisplayRows = m.buildDisplayRows()
	m.Cursor = len(m.DisplayRows) - 1
	m.clampCursor()
	return m
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
	m.closeWorkflowMonitors()
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

// PlanTitle returns the styled plan name for use as a pager title row.
// Exported so the view meta-panel's page adapter can surface it via
// PageWithTitle without duplicating the rolling-plan logic.
func (m Model) PlanTitle() string {
	label := theme.IconPlan + " Plan Status: "
	if m.Plan.Name == coreplan.RollingPlanName {
		return label + theme.DefaultTheme.Muted.Render("(rolling)") + "  " + theme.DefaultTheme.Muted.Italic(true).Render("auto-created for quick tasks")
	}
	parts := []string{label + m.Plan.Name}
	if m.Plan.Config != nil {
		if m.Plan.Config.Worktree != "" {
			parts = append(parts, theme.DefaultTheme.Muted.Render("worktree/branch: "+m.Plan.Config.Worktree))
		}
		if repoCount := len(m.Plan.Config.Repos); repoCount > 0 {
			label := "repos"
			if repoCount == 1 {
				label = "repo"
			}
			parts = append(parts, theme.DefaultTheme.Muted.Render(fmt.Sprintf("%d %s", repoCount, label)))
		}
	}
	return strings.Join(parts, "  •  ")
}

// renderFocusJobs renders the top (or left) pane containing the jobs list.
// The pane holds nothing but the table: the scroll indicator rides the footer
// row (see renderFooter) rather than sitting under the table behind a blank
// spacer, which used to cost the table two of its own rows.
func (m Model) renderFocusJobs(contentWidth int) string {
	return m.renderTableViewWithWidth(contentWidth)
}

// scrollIndicator returns the styled "↑ [cursor/total] ↓" marker, or "" when
// every display row already fits on screen.
func (m Model) scrollIndicator() string {
	if len(m.DisplayRows) == 0 {
		return ""
	}
	visibleLines := m.getVisibleJobCount()
	hasMore := m.ScrollOffset+visibleLines < len(m.DisplayRows)
	hasLess := m.ScrollOffset > 0
	if !hasLess && !hasMore {
		return ""
	}
	indicator := ""
	if hasLess {
		indicator += "↑ "
	}
	indicator += fmt.Sprintf("[%d/%d]", m.Cursor+1, len(m.DisplayRows))
	if hasMore {
		indicator += " ↓"
	}
	return theme.DefaultTheme.Muted.Render(indicator)
}

// renderFocusDetailPrimary renders the bottom (or right) pane containing the detail view.
// chatBoxHeight should be passed to account for chat input box in vertical split separator calculation.
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
	footer := helpView + followStatus
	// When a flat multi-key chord is armed (gg — the only enabled one here), show
	// a pending indicator so single-key arming is not invisible. Namespace chords
	// (v…/c…) render the which-key popup instead, so PendingHint returns "" for
	// them and only the popup shows.
	if hint := m.WhichKey.FooterHint(keymap.CommonSequenceBindings(m.KeyMap.Base)...); hint != "" {
		footer += theme.DefaultTheme.Muted.Render("  " + hint)
	}
	return m.alignScrollIndicator(footer)
}

// alignScrollIndicator pins the job-table scroll indicator to the right edge of
// the footer row. The footer is mostly empty space and the indicator is one
// short token, so sharing the row hands the table back the two rows the
// indicator used to occupy under it (its own row plus the spacer above it).
//
// The row must never wrap — a wrapped footer would immediately cost back the
// row just reclaimed — so when the pane is too narrow to hold both, the help
// text is truncated to make room, and if even that leaves nothing the
// indicator is dropped entirely (the table's cursor row still shows position).
func (m Model) alignScrollIndicator(footer string) string {
	indicator := m.scrollIndicator()
	if indicator == "" || m.Width <= 0 {
		return footer
	}
	// One column of separation between the help text and the indicator.
	helpWidth := m.Width - lipgloss.Width(indicator) - 1
	if helpWidth < 1 {
		return footer
	}
	if lipgloss.Width(footer) > helpWidth {
		footer = ansi.Truncate(footer, helpWidth, "…")
	}
	// Width() pads the (now guaranteed short enough) help text out so the
	// indicator lands flush right without a manual space run.
	return lipgloss.NewStyle().Width(helpWidth).Render(footer) + " " + indicator
}

// View renders the TUI
func (m Model) View() string {
	if m.Err != nil {
		return fmt.Sprintf("Error: %v\n", m.Err)
	}

	// Dialog overlays take over the full view
	if m.columnSelectMode {
		return m.renderColumnSelectView()
	}
	if m.Renaming {
		return m.renderRenameDialog()
	}
	if m.CreatingJob {
		return m.renderJobCreationDialog()
	}
	if m.ClawTargetSelectorActive {
		return m.renderClawTargetSelector()
	}
	if m.ClawDialogActive {
		return m.renderClawDialog()
	}
	if m.EditingDeps {
		return m.renderEditDepsView()
	}
	if m.selectingRecipe {
		return m.renderRecipeSelector()
	}
	if m.fieldEditor != nil {
		return m.renderFieldEditor()
	}
	if m.Help.ShowAll {
		return m.Help.View()
	}

	// ── Sync content to pane models ──────────────────────────────────

	// Jobs pane: pre-render the table at the allocated width
	jobsPane := m.Manager.Panes[0].Model.(*JobsPaneModel)
	jobsWidth := jobsPane.Width
	if jobsWidth < 40 {
		jobsWidth = 40
	}
	jobsPane.Content = m.renderFocusJobs(jobsWidth)

	// Detail pane: pre-render header + content (skip when promoted to BSP viewport).
	if !m.Manager.IsHidden("detail") && !m.Manager.IsPromoted("detail") {
		detailPane := m.Manager.Panes[1].Model.(*DetailPaneModel)
		detailPane.Header = m.renderDetailHeader()
		detailPane.Content = m.renderDetailContent()
		detailPane.Focused = m.Focus == FocusDetailPrimary || m.Focus == FocusDetailSecondary
	}

	// ── Render layout via Manager ────────────────────────────────────

	layout := m.Manager.View()

	// ── Which-key popup: overlaid onto the BOTTOM rows of the pane area (just
	// above the input/footer), matching treemux (popup sits above the status
	// bar). The host gates on the show-delay + arm state and keeps the frame
	// height fixed (see keymap.WhichKeyHost.RenderOverlay); the delayed
	// keymap.WhichKeyShowMsg tick from the chord seam forces the re-render.
	layout = m.WhichKey.RenderOverlay(layout, lipgloss.Width(layout), *theme.DefaultTheme)

	// ── Input area (below Manager, not managed by it) ────────────────

	chatInputHeight := m.calculateChatInputHeight()
	var inputView string
	if chatInputHeight > 0 {
		inputView = m.renderInputContent()
	}

	// ── Footer ───────────────────────────────────────────────────────

	var footer string
	if m.ConfirmArchive {
		if len(m.Selected) > 0 {
			footer = "\n" + theme.DefaultTheme.Warning.
				Bold(true).
				Render(fmt.Sprintf("Archive %d selected job(s)? (y/n)", len(m.Selected)))
		} else if job := m.CurrentJob(); job != nil {
			footer = "\n" + theme.DefaultTheme.Warning.
				Bold(true).
				Render(fmt.Sprintf("Archive '%s'? (y/n)", job.Filename))
		}
	} else {
		footer = m.renderFooter()
	}

	// ── Assemble final view ──────────────────────────────────────────

	parts := []string{layout}
	if inputView != "" {
		parts = append(parts, inputView)
	}
	parts = append(parts, footer)

	finalView := lipgloss.JoinVertical(lipgloss.Left, parts...)
	return lipgloss.NewStyle().MaxWidth(m.Width).Render(finalView)
}

// calculateFocusJobsWidth calculates the optimal width for the jobs pane
// based on the content of the currently visible columns.
func (m *Model) calculateFocusJobsWidth() int {
	if len(m.DisplayRows) == 0 {
		return 30 // Default minimum
	}

	// 1. Initialize max widths with header lengths
	columnWidths := make(map[string]int)
	for _, colName := range m.availableColumns {
		if m.columnVisibility[colName] {
			columnWidths[colName] = lipgloss.Width(colName)
		}
	}

	// 2. Iterate through visible rows to find the max width for each visible column
	visibleRows := m.getVisibleRows()
	if len(visibleRows) == 0 {
		// Fallback if no rows are visible (e.g., empty plan)
		return 30
	}

	for _, row := range visibleRows {
		if row.Type != RowTypeJob {
			// Virtual workflow rows render only into the JOB column.
			if m.columnVisibility["JOB"] {
				if w := m.virtualRowCellWidth(&row); w > columnWidths["JOB"] {
					columnWidths["JOB"] = w
				}
			}
			continue
		}
		job := row.Job
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
			jobColWidth := treePrefixWidth + 2 + lipgloss.Width(job.Filename) + m.jobBadgeWidth(job)
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
		if m.columnVisibility["MODEL"] {
			// The folded "provider · model" cell exceeds the bare header, so
			// measure the rendered cell (capped) rather than the header alone.
			w := lipgloss.Width(m.renderModelColumnCell(job))
			if w > 24 {
				w = 24
			}
			if w > columnWidths["MODEL"] {
				columnWidths["MODEL"] = w
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

	// Add table formatting chrome. Separators are single │ chars (1 cell);
	// the spaces around them come from cell padding (already counted).
	if visibleColCount > 0 {
		totalWidth += 2                         // left + right borders
		totalWidth += (visibleColCount - 1) * 1 // separators: │ (1 char)
		totalWidth += visibleColCount * 2       // cell padding: 1 space each side
		totalWidth += 2                         // selection indicator (▶ or spaces)
		totalWidth += 2                         // safety buffer
	}

	// 4. Apply reasonable bounds to the final calculated width.
	// Use a low minimum so narrow panes still get a usable table
	// (column dropping in renderTableViewWithWidth handles the rest).
	if totalWidth < 30 {
		totalWidth = 30
	}
	// Cap at 80% of terminal width to ensure logs are always somewhat visible
	maxWidth := int(float64(m.Width) * 0.8)
	if maxWidth > 0 && totalWidth > maxWidth {
		totalWidth = maxWidth
	}

	return totalWidth
}

// defaultBSPJobPaneRatio is the fraction of the split the job table keeps
// when a detail pane (vv preview, vc context, va agent, …) is promoted into
// a host BSP split. Even 50/50 rather than a content-derived width: the job
// table drops low-priority columns to fit whatever it gets
// (withResponsiveColumns), whereas the detail pane — an editor, an agent
// terminal, a context dump — has real minimum-width content and used to be
// squeezed into whatever was left over.
const defaultBSPJobPaneRatio = 0.5

// calculateBSPJobPaneRatio returns the initial BSP split ratio for the job
// table. Only the *initial* one: tuimux remembers any orientation or ratio
// the user sets afterwards (leader-| / resize) and replays it on the next
// open, including after the detail pane is closed and reopened.
func (m *Model) calculateBSPJobPaneRatio() float64 {
	return defaultBSPJobPaneRatio
}

// updateLayoutDimensions recalculates pane sizes by redistributing
// through the Manager so viewport widths always match what renderPaneContent
// will constrain to. This prevents the scrollbar overlay (█) from wrapping
// to its own line when content is pre-wrapped to a different width.
func (m *Model) updateLayoutDimensions() {
	if m.LogSplitVertical {
		fjw := m.calculateFocusJobsWidth()
		if m.Width < fjw+minLogsWidth+verticalSeparatorWidth {
			m.LogSplitVertical = false
			m.Manager.Direction = panes.DirectionVertical
			m.StatusSummary = theme.DefaultTheme.Muted.Render("Switched to horizontal split (terminal too narrow)")
		}
	}

	if !m.ShowLogs {
		return
	}

	// Update the Manager's pane layout (Fixed/Flex) from current state,
	// then redistribute dimensions so inner models get accurate sizes.
	m.syncPaneLayout()
	chatInputHeight := m.calculateChatInputHeight()
	contentWidth := m.Width
	if contentWidth < 40 {
		contentWidth = 40
	}
	mgrMsg := tea.WindowSizeMsg{
		Width:  contentWidth,
		Height: m.Height - footerHeight - chatInputHeight,
	}
	m.Manager, _ = m.Manager.Update(mgrMsg)
	m.syncLayoutFromManager()
}

// getVisibleJobCount returns how many display rows fit in the jobs pane.
//
// m.Height is the height the host pager already handed us AFTER subtracting
// its own chrome (tab bar, spacer, title row, outer padding), so nothing above
// the pane is ours to reserve. What we actually spend inside that budget,
// mirroring View() and the pane Manager's distribution:
//
//	m.Height
//	  − footerHeight     the help/scroll-indicator row View() joins below the
//	                     Manager's layout
//	  − chat input       the agent chat box, on the jobs that show one
//	  = the Manager's height — and therefore the jobs pane's height whenever
//	    the jobs pane spans the whole cross axis: detail hidden, detail
//	    side-by-side, or jobs zoomed fullscreen
//	  − detail + divider stacked split only (see below)
//	  − tableChrome      the table's own frame, the only chrome renderFocusJobs
//	                     still emits inside the pane
func (m *Model) getVisibleJobCount() int {
	if m.Height == 0 {
		return 10 // default
	}

	// The Edit Dependencies overlay is not the jobs pane at all — View()
	// returns it in place of the whole layout — and it shares this counter via
	// getVisibleJobs, so it gets its own budget against its own chrome.
	if m.EditingDeps {
		if h := m.Height - editDepsChrome; h > 1 {
			return h
		}
		return 1
	}

	availableHeight := m.Height - footerHeight - m.calculateChatInputHeight() - tableChrome

	// Stacked split: the detail pane below and the Manager's separator row
	// come out of the jobs pane's share. A side-by-side split costs nothing
	// vertically (both panes span the full height), and while one pane is
	// zoomed the Manager hands the zoomed pane the whole area — LogViewerHeight
	// is stale in that state and must not be subtracted.
	if m.ShowLogs && !m.LogSplitVertical && m.Manager.FullscreenIdx < 0 {
		availableHeight -= m.LogViewerHeight + horizontalDividerHeight
	}

	if availableHeight < 1 {
		availableHeight = 1
	}

	return availableHeight
}

// adjustScrollOffset ensures the cursor is visible within the viewport, and
// that the viewport is showing as much of the list as it can.
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

	// The offset may never sit past the last full page: rows above the
	// viewport with blank space below the table is never the right picture.
	// Without this, a pane that grows (terminal resize, a split closing) kept
	// the offset it was scrolled to while short — leaving the reclaimed rows
	// as dead space under the table. Pulling the offset back can only move
	// the cursor further inside the viewport, never out of it.
	if maxOffset := len(m.DisplayRows) - visibleLines; m.ScrollOffset > maxOffset {
		m.ScrollOffset = maxOffset
	}

	// Ensure scrollOffset doesn't go negative
	if m.ScrollOffset < 0 {
		m.ScrollOffset = 0
	}
}

// viewportAtBottom reports whether the last display row is on screen.
func (m *Model) viewportAtBottom() bool {
	return m.ScrollOffset+m.getVisibleJobCount() >= len(m.DisplayRows)
}

// flattenJobTreeWithParents creates the presentation tree used by the status
// table. Valid ParentJobID lineage is the primary presentation edge; the
// existing dependency tree remains the fallback. ParentJobID never changes
// scheduling or runnability.
func flattenJobTreeWithParents(plan *orchestration.Plan) ([]*orchestration.Job, map[string]*orchestration.Job, map[string]int) {
	fallbackOrder := make([]*orchestration.Job, 0, len(plan.Jobs))
	fallbackParents := make(map[string]*orchestration.Job)
	visited := make(map[string]bool)
	for _, root := range orchestration.FindRootJobs(plan) {
		addJobAndDependentsWithParent(root, plan, &fallbackOrder, visited, fallbackParents, nil)
	}
	for _, job := range plan.Jobs {
		if !visited[job.ID] {
			fallbackOrder = append(fallbackOrder, job)
			fallbackParents[job.ID] = nil
		}
	}

	byID := make(map[string]*orchestration.Job, len(plan.Jobs))
	for _, job := range plan.Jobs {
		byID[job.ID] = job
	}

	parents := make(map[string]*orchestration.Job, len(plan.Jobs))
	for _, job := range plan.Jobs {
		parents[job.ID] = fallbackParents[job.ID]
		if ownershipParentIsValid(job, byID) {
			parents[job.ID] = byID[job.ParentJobID]
		}
	}

	children := make(map[string][]*orchestration.Job)
	var roots []*orchestration.Job
	// fallbackOrder preserves the status TUI's established dependency tree
	// while ownership children are relocated beneath their owner.
	for _, job := range fallbackOrder {
		if parent := parents[job.ID]; parent != nil {
			children[parent.ID] = append(children[parent.ID], job)
		} else {
			roots = append(roots, job)
		}
	}
	// Ownership siblings must follow plan/file order even when their unrelated
	// dependency paths placed them differently in fallbackOrder.
	planOrder := make(map[string]int, len(plan.Jobs))
	for i, job := range plan.Jobs {
		planOrder[job.ID] = i
	}
	for parentID := range children {
		sort.SliceStable(children[parentID], func(i, j int) bool {
			return planOrder[children[parentID][i].ID] < planOrder[children[parentID][j].ID]
		})
	}

	result := make([]*orchestration.Job, 0, len(plan.Jobs))
	indents := make(map[string]int, len(plan.Jobs))
	visited = make(map[string]bool)
	var appendTree func(*orchestration.Job, int)
	appendTree = func(job *orchestration.Job, depth int) {
		if job == nil || visited[job.ID] {
			return
		}
		visited[job.ID] = true
		result = append(result, job)
		indents[job.ID] = depth
		for _, child := range children[job.ID] {
			appendTree(child, depth+1)
		}
	}
	for _, root := range roots {
		appendTree(root, 0)
	}
	// Defensive fallback for malformed dependency/lineage cycles.
	for _, job := range fallbackOrder {
		if !visited[job.ID] {
			parents[job.ID] = nil
			appendTree(job, 0)
		}
	}
	return result, parents, indents
}

// indexOwnershipChildren caches valid direct ownership children for folding,
// summaries, and attention propagation.
func indexOwnershipChildren(jobs []*orchestration.Job, parents map[string]*orchestration.Job) map[string][]*orchestration.Job {
	children := make(map[string][]*orchestration.Job)
	for _, job := range jobs {
		parent := parents[job.ID]
		if job.ParentJobID != "" && parent != nil && parent.ID == job.ParentJobID {
			children[parent.ID] = append(children[parent.ID], job)
		}
	}
	return children
}

// ownershipParentIsValid rejects missing parents and any lineage chain that
// cycles. Invalid lineage falls back to dependency/root presentation instead
// of making jobs disappear.
func ownershipParentIsValid(job *orchestration.Job, byID map[string]*orchestration.Job) bool {
	if job == nil || job.ParentJobID == "" || byID[job.ParentJobID] == nil {
		return false
	}
	seen := map[string]bool{job.ID: true}
	for id := job.ParentJobID; id != ""; {
		if seen[id] {
			return false
		}
		seen[id] = true
		parent := byID[id]
		if parent == nil {
			return true
		}
		id = parent.ParentJobID
	}
	return true
}

// addJobAndDependentsWithParent records the legacy dependency presentation
// tree. Ownership lineage is overlaid after this pass.
func addJobAndDependentsWithParent(job *orchestration.Job, plan *orchestration.Plan, result *[]*orchestration.Job, visited map[string]bool, parents map[string]*orchestration.Job, parent *orchestration.Job) {
	if visited[job.ID] {
		return
	}
	visited[job.ID] = true
	*result = append(*result, job)
	parents[job.ID] = parent
	for _, dep := range orchestration.FindAllDependents(job, plan) {
		addJobAndDependentsWithParent(dep, plan, result, visited, parents, job)
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
