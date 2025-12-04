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
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattsolo1/grove-core/pkg/tmux"
	"github.com/mattsolo1/grove-core/tui/components/help"
	gtable "github.com/mattsolo1/grove-core/tui/components/table"
	"github.com/mattsolo1/grove-core/tui/keymap"
	"github.com/mattsolo1/grove-core/tui/theme"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
)

type statusTUIKeyMap struct {
	keymap.Base
	Select        key.Binding
	SelectAll     key.Binding
	SelectNone    key.Binding
	Archive       key.Binding
	AddXmlPlan    key.Binding
	Edit          key.Binding
	Run           key.Binding
	SetCompleted  key.Binding
	SetStatus     key.Binding
	AddJob        key.Binding
	Implement     key.Binding
	AgentFromChat key.Binding
	Rename        key.Binding
	Resume        key.Binding
	EditDeps      key.Binding
	ToggleSummaries key.Binding
	ToggleView    key.Binding
	GoToTop       key.Binding
	GoToBottom    key.Binding
	PageUp        key.Binding
	PageDown      key.Binding
	ViewLogs      key.Binding
}

func newStatusTUIKeyMap() statusTUIKeyMap {
	return statusTUIKeyMap{
		Base: keymap.NewBase(),
		Select: key.NewBinding(
			key.WithKeys(" "),
			key.WithHelp("space", "toggle select"),
		),
		SelectAll: key.NewBinding(
			key.WithKeys("a"),
			key.WithHelp("a", "select all"),
		),
		SelectNone: key.NewBinding(
			key.WithKeys("N"),
			key.WithHelp("N", "deselect all"),
		),
		Archive: key.NewBinding(
			key.WithKeys("X"),
			key.WithHelp("X", "archive selected"),
		),
		AddXmlPlan: key.NewBinding(
			key.WithKeys("x"),
			key.WithHelp("x", "add XML plan job"),
		),
		Edit: key.NewBinding(
			key.WithKeys("e", "enter"),
			key.WithHelp("e/enter", "edit job"),
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
			key.WithKeys("A"),
			key.WithHelp("A", "add job"),
		),
		Implement: key.NewBinding(
			key.WithKeys("i"),
			key.WithHelp("i", "implement selected"),
		),
		AgentFromChat: key.NewBinding(
			key.WithKeys("I"),
			key.WithHelp("I", "agent from chat"),
		),
		Rename: key.NewBinding(
			key.WithKeys("ctrl+r"),
			key.WithHelp("ctrl+r", "rename job"),
		),
		Resume: key.NewBinding(
			key.WithKeys("R"),
			key.WithHelp("R", "resume job"),
		),
		EditDeps: key.NewBinding(
			key.WithKeys("D"),
			key.WithHelp("D", "edit dependencies"),
		),
		ToggleSummaries: key.NewBinding(
			key.WithKeys("s"),
			key.WithHelp("s", "toggle summaries"),
		),
		ToggleView: key.NewBinding(
			key.WithKeys("t"),
			key.WithHelp("t", "toggle view"),
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
		ViewLogs: key.NewBinding(
			key.WithKeys("l"),
			key.WithHelp("l", "view logs"),
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
		},
		{
			key.NewBinding(key.WithKeys(""), key.WithHelp("", "Selection")),
			k.Select,
			k.SelectAll,
			k.SelectNone,
		},
		{
			key.NewBinding(key.WithKeys(""), key.WithHelp("", "Views")),
			k.ToggleView,
			k.ToggleSummaries,
		},
		{
			key.NewBinding(key.WithKeys(""), key.WithHelp("", "Actions")),
			k.Run,
			k.Edit,
			k.SetCompleted,
			k.SetStatus,
			k.AddJob,
			k.AddXmlPlan,
			k.Implement,
			k.Rename,
			k.Resume,
			k.EditDeps,
			k.ViewLogs,
			k.Archive,
			k.Help,
			k.Quit,
		},
	}
}

type viewMode int

const (
	tableView viewMode = iota
	treeView
)

func (v viewMode) String() string {
	return [...]string{"table", "tree"}[v]
}

// Status TUI model represents the state of the TUI
type statusTUIModel struct {
	plan          *orchestration.Plan
	graph         *orchestration.DependencyGraph
	jobs          []*orchestration.Job
	jobParents    map[string]*orchestration.Job // Track parent in tree structure
	jobIndents    map[string]int               // Track indentation level
	cursor        int
	scrollOffset  int             // Track scroll position for viewport
	selected      map[string]bool // For multi-select
	showSummaries bool            // Toggle for showing job summaries
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
	waitingForG   bool      // Track if we're waiting for second 'g' in 'gg' sequence
	cursorVisible bool      // Track cursor visibility for blinking animation
	viewMode      viewMode  // Current view mode (table or tree)
	renaming       bool
	renameInput    textinput.Model
	renameJobIndex int
	editingDeps      bool
	editDepsJobIndex int
	editDepsSelected map[string]bool // Track which jobs are selected as dependencies
	creatingJob      bool
	createJobInput   textinput.Model
	createJobType    string // "xml" or "impl"
	createJobBaseJob *orchestration.Job
	createJobDeps    []*orchestration.Job // For multi-select case
}


// getStatusStyles returns theme-based styles for job statuses with subtle colors
func getStatusStyles() map[orchestration.JobStatus]lipgloss.Style {
	return map[orchestration.JobStatus]lipgloss.Style{
		// Completed: Success style
		orchestration.JobStatusCompleted: theme.DefaultTheme.Success,
		// Running: Info style
		orchestration.JobStatusRunning: theme.DefaultTheme.Info,
		// Failed: Error style
		orchestration.JobStatusFailed: theme.DefaultTheme.Error,
		// Blocked: Error style
		orchestration.JobStatusBlocked: theme.DefaultTheme.Error,
		// Needs Review: Info style
		orchestration.JobStatusNeedsReview: theme.DefaultTheme.Info,
		// Pending User: Highlight style
		orchestration.JobStatusPendingUser: theme.DefaultTheme.Highlight,
		// Pending LLM: Info style
		orchestration.JobStatusPendingLLM: theme.DefaultTheme.Info,
		// Pending: Muted style
		orchestration.JobStatusPending: theme.DefaultTheme.Muted,
		// New statuses
		orchestration.JobStatusTodo:      theme.DefaultTheme.Muted,
		orchestration.JobStatusHold:      theme.DefaultTheme.Warning,
		orchestration.JobStatusAbandoned: theme.DefaultTheme.Muted, // Very subtle for abandoned jobs
	}
}

// Initialize the model
func newStatusTUIModel(plan *orchestration.Plan, graph *orchestration.DependencyGraph) statusTUIModel {
	// Flatten the job tree for navigation with parent tracking
	jobs, parents, indents := flattenJobTreeWithParents(plan)

	keyMap := newStatusTUIKeyMap()
	helpModel := help.NewBuilder().
		WithKeys(keyMap).
		WithTitle("Plan Status - Help").
		Build()

	return statusTUIModel{
		plan:           plan,
		graph:          graph,
		jobs:           jobs,
		jobParents:     parents,
		jobIndents:     indents,
		cursor:         0,
		scrollOffset:   0,
		selected:       make(map[string]bool),
		statusSummary:  formatStatusSummary(plan),
		confirmArchive: false,
		planDir:        plan.Directory,
		keyMap:         keyMap,
		help:           helpModel,
		cursorVisible:  true,
		viewMode:       tableView, // Default to table view
	}
}

// getVisibleJobCount returns how many jobs can be displayed in the viewport
func (m *statusTUIModel) getVisibleJobCount() int {
	if m.height == 0 {
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
	if m.viewMode == treeView {
		chromeLines = 11 // tree view has less overhead (no table borders/headers)
	}

	availableHeight := m.height - chromeLines
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
	dependents := findAllDependents(job, plan)
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
	dependents := findAllDependents(job, plan)
	for _, dep := range dependents {
		addJobAndDependents(dep, plan, result, visited)
	}
}

// Messages
type refreshMsg struct{}
type archiveConfirmedMsg struct{ job *orchestration.Job }
type editFileAndQuitMsg struct{ filePath string }
type editFileInTmuxMsg struct{ err error }
type tickMsg time.Time
type statusUpdateMsg string
type refreshTickMsg time.Time
type renameCompleteMsg struct{ err error }
type updateDepsCompleteMsg struct{ err error }
type createJobCompleteMsg struct{ err error }

const refreshInterval = 2 * time.Second

func renameJobCmd(plan *orchestration.Plan, job *orchestration.Job, newTitle string) tea.Cmd {
	return func() tea.Msg {
		err := orchestration.RenameJob(plan, job, newTitle)
		return renameCompleteMsg{err: err}
	}
}

func updateDepsCmd(job *orchestration.Job, newDeps []string) tea.Cmd {
	return func() tea.Msg {
		err := orchestration.UpdateJobDependencies(job, newDeps)
		return updateDepsCompleteMsg{err: err}
	}
}

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
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case renameCompleteMsg:
		if msg.err != nil {
			m.statusSummary = theme.DefaultTheme.Error.Render(fmt.Sprintf("Error renaming job: %v", msg.err))
		} else {
			m.statusSummary = theme.DefaultTheme.Success.Render(theme.IconSuccess + " Job renamed successfully.")
		}
		return m, refreshPlan(m.planDir)

	case updateDepsCompleteMsg:
		if msg.err != nil {
			m.statusSummary = theme.DefaultTheme.Error.Render(fmt.Sprintf("Error updating dependencies: %v", msg.err))
		} else {
			m.statusSummary = theme.DefaultTheme.Success.Render(theme.IconSuccess + " Dependencies updated successfully.")
		}
		return m, refreshPlan(m.planDir)

	case createJobCompleteMsg:
		if msg.err != nil {
			m.statusSummary = theme.DefaultTheme.Error.Render(fmt.Sprintf("Error creating job: %v", msg.err))
		} else {
			m.statusSummary = theme.DefaultTheme.Success.Render(theme.IconSuccess + " Job created successfully.")
		}
		return m, refreshPlan(m.planDir)

	case editFileInTmuxMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		return m, tea.Quit

	case tickMsg:
		// Toggle cursor visibility for blinking effect
		m.cursorVisible = !m.cursorVisible
		return m, blink() // Schedule next tick

	case statusUpdateMsg:
		m.statusSummary = string(msg)
		return m, refreshPlan(m.planDir)

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
		m.help.Width = msg.Width
		m.help.Height = msg.Height
		m.help, _ = m.help.Update(msg)
		return m, nil

	case tea.KeyMsg:
		// Handle renaming mode
		if m.renaming {
			switch msg.String() {
			case "enter":
				if m.renameJobIndex >= 0 && m.renameJobIndex < len(m.jobs) {
					jobToRename := m.jobs[m.renameJobIndex]
					newTitle := m.renameInput.Value()
					m.renaming = false
					m.statusSummary = "Renaming job..."
					return m, renameJobCmd(m.plan, jobToRename, newTitle)
				}
			case "esc":
				m.renaming = false
				m.statusSummary = ""
				return m, nil
			}
			m.renameInput, cmd = m.renameInput.Update(msg)
			return m, cmd
		}

		// Handle job creation mode
		if m.creatingJob {
			switch msg.String() {
			case "enter":
				customTitle := m.createJobInput.Value()
				// If empty, use the placeholder as the title
				if customTitle == "" {
					customTitle = m.createJobInput.Placeholder
				}
				m.creatingJob = false
				m.statusSummary = "Creating job..."

				// Create the appropriate job type
				if m.createJobType == "xml" {
					if len(m.createJobDeps) > 0 {
						return m, createXmlPlanJobWithTitle(m.plan, m.createJobDeps, customTitle)
					}
					return m, createXmlPlanJobWithTitle(m.plan, []*orchestration.Job{m.createJobBaseJob}, customTitle)
				} else if m.createJobType == "impl" {
					if len(m.createJobDeps) > 0 {
						return m, createImplementationJobWithTitle(m.plan, m.createJobDeps, customTitle)
					}
					return m, createImplementationJobWithTitle(m.plan, []*orchestration.Job{m.createJobBaseJob}, customTitle)
				} else if m.createJobType == "agent-from-chat" {
					if len(m.createJobDeps) > 0 {
						return m, createAgentFromChatJobWithTitle(m.plan, m.createJobDeps, customTitle)
					}
					return m, createAgentFromChatJobWithTitle(m.plan, []*orchestration.Job{m.createJobBaseJob}, customTitle)
				}
			case "esc":
				m.creatingJob = false
				m.createJobBaseJob = nil
				m.createJobDeps = nil
				m.statusSummary = ""
				return m, nil
			}
			m.createJobInput, cmd = m.createJobInput.Update(msg)
			return m, cmd
		}

		// Handle dependency editing mode
		if m.editingDeps {
			switch msg.String() {
			case "enter":
				if m.editDepsJobIndex >= 0 && m.editDepsJobIndex < len(m.jobs) {
					jobToEdit := m.jobs[m.editDepsJobIndex]
					// Build list of selected dependencies (filenames)
					var newDeps []string
					for _, job := range m.jobs {
						if m.editDepsSelected[job.ID] {
							newDeps = append(newDeps, job.Filename)
						}
					}
					m.editingDeps = false
					m.editDepsSelected = nil
					m.statusSummary = "Updating dependencies..."
					return m, updateDepsCmd(jobToEdit, newDeps)
				}
			case "esc":
				m.editingDeps = false
				m.editDepsSelected = nil
				m.statusSummary = ""
				return m, nil
			case " ":
				// Toggle selection
				if m.cursor >= 0 && m.cursor < len(m.jobs) {
					job := m.jobs[m.cursor]
					// Don't allow selecting the job being edited
					if m.cursor != m.editDepsJobIndex {
						if m.editDepsSelected[job.ID] {
							delete(m.editDepsSelected, job.ID)
						} else {
							m.editDepsSelected[job.ID] = true
						}
					}
				}
				return m, nil
			case "up", "k":
				if m.cursor > 0 {
					m.cursor--
					m.adjustScrollOffset()
				}
				return m, nil
			case "down", "j":
				if m.cursor < len(m.jobs)-1 {
					m.cursor++
					m.adjustScrollOffset()
				}
				return m, nil
			}
			return m, nil
		}

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
				if len(m.selected) > 0 {
					// Archive all selected jobs
					var jobsToArchive []*orchestration.Job
					for id := range m.selected {
						for _, job := range m.jobs {
							if job.ID == id {
								jobsToArchive = append(jobsToArchive, job)
								break
							}
						}
					}
					return m, tea.Sequence(
						doArchiveJobs(m.planDir, jobsToArchive),
						refreshPlan(m.planDir),
					)
				} else if m.cursor < len(m.jobs) {
					job := m.jobs[m.cursor]
					return m, func() tea.Msg { return archiveConfirmedMsg{job: job} }
				}
			case "n", "N", "ctrl+c", "q":
				m.confirmArchive = false
			}
			return m, nil
		}

		// If help is showing, let it handle key messages (for scrolling and closing)
		if m.help.ShowAll {
			var cmd tea.Cmd
			m.help, cmd = m.help.Update(msg)
			return m, cmd
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
			m.help.Toggle()

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
			if m.cursor < len(m.jobs) {
				job := m.jobs[m.cursor]
				if m.selected[job.ID] {
					delete(m.selected, job.ID)
				} else {
					m.selected[job.ID] = true
				}
			}

		case key.Matches(msg, m.keyMap.SelectAll):
			for _, job := range m.jobs {
				m.selected[job.ID] = true
			}

		case key.Matches(msg, m.keyMap.SelectNone):
			m.selected = make(map[string]bool)

		case key.Matches(msg, m.keyMap.Archive):
			// Archive selected jobs or current job if none selected
			if len(m.selected) > 0 || m.cursor < len(m.jobs) {
				m.confirmArchive = true
			}

		case key.Matches(msg, m.keyMap.Edit):
			if m.cursor < len(m.jobs) {
				job := m.jobs[m.cursor]
				return m, editJob(job)
			}

		case key.Matches(msg, m.keyMap.ViewLogs):
			if m.cursor < len(m.jobs) {
				job := m.jobs[m.cursor]
				return m, viewLogsCmd(m.plan, job)
			}

		case key.Matches(msg, m.keyMap.Run):
			if len(m.selected) > 0 {
				var selectedJobs []*orchestration.Job
				for id := range m.selected {
					for _, job := range m.jobs {
						if job.ID == id {
							selectedJobs = append(selectedJobs, job)
							break
						}
					}
				}
				return m, runJobs(m.plan.Directory, selectedJobs)
			} else if m.cursor < len(m.jobs) {
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

		case key.Matches(msg, m.keyMap.AddXmlPlan):
			if len(m.selected) > 0 {
				// Get selected jobs for dependencies
				var selectedJobs []*orchestration.Job
				for id := range m.selected {
					for _, job := range m.jobs {
						if job.ID == id {
							selectedJobs = append(selectedJobs, job)
							break
						}
					}
				}
				// Show dialog to edit job title
				m.creatingJob = true
				m.createJobType = "xml"
				m.createJobDeps = selectedJobs
				defaultTitle := fmt.Sprintf("xml-plan-%s", selectedJobs[0].Title)

				ti := textinput.New()
				ti.Placeholder = defaultTitle
				ti.PlaceholderStyle = theme.DefaultTheme.Muted
				ti.Focus()
				ti.CharLimit = 200
				ti.Width = 50
				m.createJobInput = ti
				return m, textinput.Blink
			} else if m.cursor < len(m.jobs) {
				job := m.jobs[m.cursor]
				// Show dialog to edit job title
				m.creatingJob = true
				m.createJobType = "xml"
				m.createJobBaseJob = job
				defaultTitle := fmt.Sprintf("xml-plan-%s", job.Title)

				ti := textinput.New()
				ti.Placeholder = defaultTitle
				ti.PlaceholderStyle = theme.DefaultTheme.Muted
				ti.Focus()
				ti.CharLimit = 200
				ti.Width = 50
				m.createJobInput = ti
				return m, textinput.Blink
			}

		case key.Matches(msg, m.keyMap.AddJob):
			if len(m.selected) > 0 {
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
			if len(m.selected) > 0 {
				// Get selected jobs for dependencies
				var selectedJobs []*orchestration.Job
				for id := range m.selected {
					for _, job := range m.jobs {
						if job.ID == id {
							selectedJobs = append(selectedJobs, job)
							break
						}
					}
				}
				// Show dialog to edit job title
				m.creatingJob = true
				m.createJobType = "impl"
				m.createJobDeps = selectedJobs
				defaultTitle := fmt.Sprintf("impl-%s", selectedJobs[0].Title)

				ti := textinput.New()
				ti.Placeholder = defaultTitle
				ti.PlaceholderStyle = theme.DefaultTheme.Muted
				ti.Focus()
				ti.CharLimit = 200
				ti.Width = 50
				m.createJobInput = ti
				return m, textinput.Blink
			} else if m.cursor < len(m.jobs) {
				job := m.jobs[m.cursor]
				// Show dialog to edit job title
				m.creatingJob = true
				m.createJobType = "impl"
				m.createJobBaseJob = job
				defaultTitle := fmt.Sprintf("impl-%s", job.Title)

				ti := textinput.New()
				ti.Placeholder = defaultTitle
				ti.PlaceholderStyle = theme.DefaultTheme.Muted
				ti.Focus()
				ti.CharLimit = 200
				ti.Width = 50
				m.createJobInput = ti
				return m, textinput.Blink
			}

		case key.Matches(msg, m.keyMap.AgentFromChat):
			if len(m.selected) > 0 {
				// Get selected jobs for dependencies
				var selectedJobs []*orchestration.Job
				for id := range m.selected {
					for _, job := range m.jobs {
						if job.ID == id {
							selectedJobs = append(selectedJobs, job)
							break
						}
					}
				}
				// Show dialog to edit job title
				m.creatingJob = true
				m.createJobType = "agent-from-chat"
				m.createJobDeps = selectedJobs
				defaultTitle := fmt.Sprintf("impl-%s", selectedJobs[0].Title)

				ti := textinput.New()
				ti.Placeholder = defaultTitle
				ti.PlaceholderStyle = theme.DefaultTheme.Muted
				ti.Focus()
				ti.CharLimit = 200
				ti.Width = 50
				m.createJobInput = ti
				return m, textinput.Blink
			} else if m.cursor < len(m.jobs) {
				job := m.jobs[m.cursor]
				// Show dialog to edit job title
				m.creatingJob = true
				m.createJobType = "agent-from-chat"
				m.createJobBaseJob = job
				defaultTitle := fmt.Sprintf("impl-%s", job.Title)

				ti := textinput.New()
				ti.Placeholder = defaultTitle
				ti.PlaceholderStyle = theme.DefaultTheme.Muted
				ti.Focus()
				ti.CharLimit = 200
				ti.Width = 50
				m.createJobInput = ti
				return m, textinput.Blink
			}

		case key.Matches(msg, m.keyMap.Rename):
			if m.cursor >= 0 && m.cursor < len(m.jobs) {
				m.renaming = true
				m.renameJobIndex = m.cursor
				jobToRename := m.jobs[m.cursor]

				ti := textinput.New()
				ti.SetValue(jobToRename.Title)
				ti.Focus()
				ti.CharLimit = 200
				ti.Width = 50
				m.renameInput = ti
				return m, textinput.Blink
			}

		case key.Matches(msg, m.keyMap.Resume):
			if m.cursor >= 0 && m.cursor < len(m.jobs) {
				job := m.jobs[m.cursor]
				if (job.Type == orchestration.JobTypeInteractiveAgent || job.Type == orchestration.JobTypeAgent) && job.Status == orchestration.JobStatusCompleted {
					return m, executePlanResume(job)
				}
				m.statusSummary = theme.DefaultTheme.Error.Render("Only completed interactive agent jobs can be resumed.")
			}

		case key.Matches(msg, m.keyMap.EditDeps):
			if m.cursor >= 0 && m.cursor < len(m.jobs) {
				m.editingDeps = true
				m.editDepsJobIndex = m.cursor
				jobToEdit := m.jobs[m.cursor]

				// Initialize selection with current dependencies
				m.editDepsSelected = make(map[string]bool)
				for _, depFilename := range jobToEdit.DependsOn {
					// Find job by filename and mark as selected
					for _, job := range m.jobs {
						if job.Filename == depFilename {
							m.editDepsSelected[job.ID] = true
							break
						}
					}
				}
				return m, nil
			}

		case key.Matches(msg, m.keyMap.ToggleSummaries):
			m.showSummaries = !m.showSummaries

		case key.Matches(msg, m.keyMap.ToggleView):
			if m.viewMode == treeView {
				m.viewMode = tableView
			} else {
				m.viewMode = treeView
			}
		}
	}

	return m, nil
}

// View renders the TUI
func (m statusTUIModel) View() string {
	if m.err != nil {
		return fmt.Sprintf("Error: %v\n", m.err)
	}

	// If renaming, show the rename dialog and return
	if m.renaming {
		return m.renderRenameDialog()
	}

	// If creating a job, show the creation dialog
	if m.creatingJob {
		return m.renderJobCreationDialog()
	}

	// If editing dependencies, show the edit deps view
	if m.editingDeps {
		return m.renderEditDepsView()
	}

	// Show status picker if active
	if m.showStatusPicker {
		return m.renderStatusPicker()
	}

	// Show help if active
	if m.help.ShowAll {
		return m.help.View()
	}

	// Calculate content width accounting for margins
	contentWidth := m.width - 4
	if contentWidth < 40 {
		contentWidth = 40
	}

	// 1. Create Header with subtle coloring
	// Header uses terminal default colors with bold for emphasis.
	// See: plans/tui-updates/14-terminal-ui-styling-philosophy.md
	headerLabel := theme.DefaultTheme.Bold.Render(theme.IconPlan + " Plan Status: ")
	planName := theme.DefaultTheme.Bold.Render(m.plan.Name)
	headerText := headerLabel + planName

	styledHeader := lipgloss.NewStyle().
		Background(theme.DefaultTheme.Header.GetBackground()).
		Align(lipgloss.Left).
		Width(contentWidth).
		MarginBottom(1).
		Render(headerText)

	// 2. Render Main Content (Table or Tree)
	var mainContent string
	switch m.viewMode {
	case tableView:
		mainContent = m.renderTableView()
	default: // treeView
		mainContent = m.renderJobTree()
	}

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
		if len(m.selected) > 0 {
			footer = "\n" + theme.DefaultTheme.Warning.
				Bold(true).
				Render(fmt.Sprintf("Archive %d selected job(s)? (y/n)", len(m.selected)))
		} else if m.cursor < len(m.jobs) {
			job := m.jobs[m.cursor]
			footer = "\n" + theme.DefaultTheme.Warning.
				Bold(true).
				Render(fmt.Sprintf("Archive '%s'? (y/n)", job.Filename))
		}
	} else {
		// Render Footer
		helpView := m.help.View()
		viewModeIndicator := theme.DefaultTheme.Muted.Render(fmt.Sprintf(" [%s]", m.viewMode))
		footer = helpView + viewModeIndicator
	}

	// 4. Combine everything
	finalView := lipgloss.JoinVertical(
		lipgloss.Left,
		styledHeader,
		mainContent,
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

// getVisibleJobs returns the jobs that should be visible in the current viewport
func (m *statusTUIModel) getVisibleJobs() []*orchestration.Job {
	// Calculate visible jobs based on scroll offset and viewport height
	visibleCount := m.getVisibleJobCount()
	start := m.scrollOffset
	end := start + visibleCount
	if end > len(m.jobs) {
		end = len(m.jobs)
	}
	if start >= end {
		return []*orchestration.Job{}
	}
	return m.jobs[start:end]
}

// renderTableView renders the jobs as a table with JOB, TYPE, and STATUS columns
func (m statusTUIModel) renderTableView() string {
	t := theme.DefaultTheme
	headers := []string{"SEL", "JOB", "TYPE", "STATUS"}
	var rows [][]string

	visibleJobs := m.getVisibleJobs()
	statusStyles := getStatusStyles()

	for i, job := range visibleJobs {
		// Selection checkbox
		var selCheckbox string
		if m.selected[job.ID] {
			selCheckbox = t.Success.Render(theme.IconSelect)
		} else {
			selCheckbox = theme.IconUnselect
		}
		// JOB column (with tree indentation and connectors)
		indent := m.jobIndents[job.ID]

		// Build tree prefix with connectors
		var treePrefix string
		if indent > 0 {
			// Add spacing for parent levels
			treePrefix = strings.Repeat("  ", indent-1)

			// Determine if this is the last child at this level
			globalIndex := m.scrollOffset + i
			isLast := true
			for j := globalIndex + 1; j < len(m.jobs); j++ {
				if m.jobIndents[m.jobs[j].ID] == indent {
					isLast = false
					break
				}
				if m.jobIndents[m.jobs[j].ID] < indent {
					break
				}
			}

			// Add tree connector
			if isLast {
				treePrefix += "└─ "
			} else {
				treePrefix += "├─ "
			}
		}

		statusIcon := m.getStatusIcon(job.Status)

		// Apply muted styling to filename for completed/abandoned jobs
		var filename string
		if job.Status == orchestration.JobStatusCompleted || job.Status == orchestration.JobStatusAbandoned {
			filename = t.Muted.Render(job.Filename)
		} else {
			filename = job.Filename
		}

		jobCol := fmt.Sprintf("%s%s %s", treePrefix, statusIcon, filename)

		// TYPE column with icon
		var jobTypeSymbol string
		switch job.Type {
		case "interactive_agent":
			jobTypeSymbol = theme.IconInteractiveAgent
		case "chat":
			jobTypeSymbol = theme.IconChat
		case "oneshot":
			jobTypeSymbol = theme.IconOneshot
		default:
			jobTypeSymbol = ""
		}
		var typeCol string
		if jobTypeSymbol != "" {
			typeCol = fmt.Sprintf("%s %s", jobTypeSymbol, job.Type)
		} else {
			typeCol = string(job.Type)
		}

		// STATUS column
		statusStyle := theme.DefaultTheme.Muted
		if style, ok := statusStyles[job.Status]; ok {
			statusStyle = style
		}
		statusText := statusStyle.Render(string(job.Status))

		// Apply muted styling to type and status for completed/abandoned jobs
		var finalTypeCol, finalStatusCol string
		if job.Status == orchestration.JobStatusCompleted || job.Status == orchestration.JobStatusAbandoned {
			finalTypeCol = t.Muted.Render(typeCol)
			finalStatusCol = t.Muted.Render(string(job.Status))
		} else {
			finalTypeCol = typeCol
			finalStatusCol = statusText
		}

		rows = append(rows, []string{selCheckbox, jobCol, finalTypeCol, finalStatusCol})
	}

	if len(rows) == 0 {
		return "\n" + t.Muted.Render("No jobs to display.")
	}

	// Use gtable.SelectableTable
	tableStr := gtable.SelectableTable(headers, rows, m.cursor-m.scrollOffset)

	return tableStr
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

		// Add arrow indicator at the very left for selected row
		var arrowIndicator string
		if i == m.cursor {
			arrowIndicator = theme.DefaultTheme.Highlight.Render(theme.IconArrowRightBold + " ")
		} else {
			arrowIndicator = "  "
		}

		// Build selection indicator after arrow
		var selectionCheckbox string
		if m.selected[job.ID] {
			selectionCheckbox = theme.DefaultTheme.Success.Render(theme.IconSelect) + " "
		} else {
			selectionCheckbox = theme.IconUnselect + " "
		}

		// Build tree structure part
		treePart := fmt.Sprintf("%s%s", prefix, treeChar)

		// Build job content with status icon
		statusIcon := m.getStatusIcon(job.Status)

		// Determine text style based on cursor/selection state
		// Use weight/emphasis instead of explicit colors for hierarchy.
		var filenameStyle lipgloss.Style

		if i == m.cursor {
			// Cursor: use bold for emphasis on the current row
			filenameStyle = lipgloss.NewStyle().Bold(true)
		} else if m.selected[job.ID] {
			// Selected: use bold to indicate selection
			filenameStyle = lipgloss.NewStyle().Bold(true)
		} else {
			// Normal: use faint for completed and abandoned jobs to de-emphasize them
			if job.Status == orchestration.JobStatusCompleted || job.Status == orchestration.JobStatusAbandoned {
				filenameStyle = lipgloss.NewStyle().Faint(true)
			} else {
				filenameStyle = lipgloss.NewStyle()
			}
		}

		coloredFilename := filenameStyle.Render(job.Filename)

		// Get job type badge with symbol prefix
		var jobTypeSymbol string
		switch job.Type {
		case "interactive_agent":
			jobTypeSymbol = theme.IconInteractiveAgent + " "
		case "chat":
			jobTypeSymbol = theme.IconChat + " "
		case "oneshot":
			jobTypeSymbol = theme.IconOneshot + " "
		default:
			jobTypeSymbol = ""
		}
		jobTypeBadge := fmt.Sprintf("%s[%s]", jobTypeSymbol, job.Type)

		// Build content without emoji (add emoji separately to avoid background bleed)
		var textContent string
		textContent = fmt.Sprintf("%s %s", coloredFilename, jobTypeBadge)

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

		// Combine all parts: arrow + selection checkbox + tree + content
		fullLine := arrowIndicator + selectionCheckbox + treePart + styledJobContent

		// Add summary on a new line if toggled on and available
		if m.showSummaries && job.Summary != "" {
			// Padding: 2 (arrow) + (indent * 4) + 4 (tree chars) + 2 (status icon + space)
			summaryPadding := 2 + (indent * 4) + 4 + 2
			summaryStyle := theme.DefaultTheme.Info.
				PaddingLeft(summaryPadding)

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
		icon = theme.IconStatusCompleted
	case orchestration.JobStatusRunning:
		icon = theme.IconStatusRunning
	case orchestration.JobStatusFailed:
		icon = theme.IconStatusFailed
	case orchestration.JobStatusBlocked:
		icon = theme.IconStatusBlocked
	case orchestration.JobStatusTodo:
		icon = theme.IconStatusTodo
	case orchestration.JobStatusHold:
		icon = theme.IconStatusHold
	case orchestration.JobStatusAbandoned:
		icon = theme.IconStatusAbandoned
	case orchestration.JobStatusNeedsReview:
		icon = theme.IconStatusNeedsReview
	default:
		// Pending, PendingUser, PendingLLM
		icon = theme.IconStatusPendingUser
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

func doArchiveJobs(planDir string, jobs []*orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		// Archive the jobs by moving them to an archive directory
		archiveDir := filepath.Join(planDir, ".archive")
		if err := os.MkdirAll(archiveDir, 0755); err != nil {
			return err
		}

		for _, job := range jobs {
			oldPath := filepath.Join(planDir, job.Filename)
			newPath := filepath.Join(archiveDir, job.Filename)

			if err := os.Rename(oldPath, newPath); err != nil {
				return err
			}
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

	// Check for tmux popup
	if os.Getenv("TMUX") != "" {
		client, err := tmux.NewClient()
		if err == nil { // Silently ignore if tmux client fails
			isPopup, _ := client.IsPopup(context.Background())
			if isPopup {
				return func() tea.Msg {
					editor := os.Getenv("EDITOR")
					if editor == "" {
						editor = "vi" // Fallback editor
					}
					ctx := context.Background()
					err := client.OpenInEditorWindow(ctx, editor, job.FilePath, "plan", 3, false)
					if err != nil {
						return editFileInTmuxMsg{err: err}
					}
					// Close the popup explicitly before quitting
					if err := client.ClosePopup(ctx); err != nil {
						// Log error but continue - the file was opened successfully
						return editFileInTmuxMsg{err: fmt.Errorf("failed to close popup: %w", err)}
					}
					return editFileInTmuxMsg{err: nil}
				}
			}
		}
	}

	// Original behavior: open editor in the current pane
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
	// Run the job using 'grove flow plan run' for workspace-awareness
	cmd := exec.Command("grove", "flow", "plan", "run", job.FilePath)
	// Set an environment variable to indicate the job is run from the TUI
	cmd.Env = append(os.Environ(), "GROVE_FLOW_TUI_MODE=true")

	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return err
		}
		return refreshMsg{} // Refresh to show status changes
	})
}

func executePlanResume(job *orchestration.Job) tea.Cmd {
	return tea.ExecProcess(exec.Command("flow", "plan", "resume", job.FilePath),
		func(err error) tea.Msg {
			if err != nil {
				return err // Propagate error to be displayed in the TUI
			}
			return refreshMsg{} // Refresh TUI on success
		})
}

func runJobs(planDir string, jobs []*orchestration.Job) tea.Cmd {
	args := []string{"flow", "plan", "run"}
	for _, job := range jobs {
		args = append(args, job.FilePath)
	}

	cmd := exec.Command("grove", args...)
	cmd.Env = append(os.Environ(), "GROVE_FLOW_TUI_MODE=true")

	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return err
		}
		return refreshMsg{}
	})
}

func viewLogsCmd(plan *orchestration.Plan, job *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		// Check if we're in tmux
		if os.Getenv("TMUX") == "" {
			// Not in tmux, fall back to less
			jobSpec := fmt.Sprintf("%s/%s", plan.Name, job.Filename)
			pagerCmd := exec.Command("sh", "-c", fmt.Sprintf("grove aglogs read %s | less -R", jobSpec))

			if err := pagerCmd.Run(); err != nil {
				return statusUpdateMsg(fmt.Sprintf("Error viewing logs: %v", err))
			}
			return statusUpdateMsg("Returned from log viewer.")
		}

		// In tmux, open logs in a new window
		ctx := context.Background()
		client, err := tmux.NewClient()
		if err != nil {
			return statusUpdateMsg(fmt.Sprintf("Error creating tmux client: %v", err))
		}

		session, err := client.GetCurrentSession(ctx)
		if err != nil {
			return statusUpdateMsg(fmt.Sprintf("Error getting current session: %v", err))
		}

		// Create window name based on job
		windowName := fmt.Sprintf("logs-%s", job.Filename)
		jobSpec := fmt.Sprintf("%s/%s", plan.Name, job.Filename)

		// Create the command to run in the new window
		// Force color output by setting CLICOLOR_FORCE, then pipe to less -R
		// CLICOLOR_FORCE=1 tells programs to always output color, even when piped
		command := fmt.Sprintf("CLICOLOR_FORCE=1 grove aglogs read %s | less -R", jobSpec)

		// Create new window
		err = client.NewWindow(ctx, session, windowName, command)
		if err != nil {
			return statusUpdateMsg(fmt.Sprintf("Error creating logs window: %v", err))
		}

		// Switch to the new window
		windowTarget := session + ":" + windowName
		if err := client.SwitchClient(ctx, windowTarget); err != nil {
			// Not critical if switch fails, window was still created
			return statusUpdateMsg("Logs window created.")
		}

		return statusUpdateMsg("Opened logs in new window.")
	}
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
		{orchestration.JobStatusPending, "Pending", theme.IconPending},
		{orchestration.JobStatusTodo, "Todo", theme.IconStatusTodo},
		{orchestration.JobStatusHold, "On Hold", theme.IconStatusHold},
		{orchestration.JobStatusRunning, "Running", theme.IconStatusRunning},
		{orchestration.JobStatusCompleted, "Completed", theme.IconSuccess},
		{orchestration.JobStatusFailed, "Failed", theme.IconStatusFailed},
		{orchestration.JobStatusBlocked, "Blocked", theme.IconStatusBlocked},
		{orchestration.JobStatusNeedsReview, "Needs Review", theme.IconStatusNeedsReview},
		{orchestration.JobStatusAbandoned, "Abandoned", theme.IconStatusAbandoned},
	}

	var lines []string

	// Add title
	if m.cursor < len(m.jobs) {
		job := m.jobs[m.cursor]
		title := lipgloss.NewStyle().
			Bold(true).
			Render(fmt.Sprintf("Set Status for: %s", job.Filename))
		lines = append(lines, title)
		lines = append(lines, "")
	}

	// Add status options
	for i, opt := range statusOptions {
		prefix := "  "
		var style lipgloss.Style

		if i == m.statusPickerCursor {
			prefix = theme.IconSelect + " "
			// Use background color for selection highlight, text uses terminal default
			style = lipgloss.NewStyle().
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
	help := t.Muted.Render("↑/↓ or j/k to navigate • Enter to select • Esc/b to go back")
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

func (m statusTUIModel) renderRenameDialog() string {
	if m.renameJobIndex < 0 || m.renameJobIndex >= len(m.jobs) {
		return "Error: Invalid job selected for renaming."
	}
	job := m.jobs[m.renameJobIndex]

	var b strings.Builder
	b.WriteString(theme.DefaultTheme.Header.Render(fmt.Sprintf("Rename Job: %s", job.Filename)))
	b.WriteString("\n\nEnter new title:\n")
	b.WriteString(m.renameInput.View())
	b.WriteString("\n\n")
	b.WriteString(theme.DefaultTheme.Muted.Render("Press Enter to save, Esc to cancel"))

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultColors.Orange).
		Padding(1, 2).
		Render(b.String())

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog)
}

func (m statusTUIModel) renderJobCreationDialog() string {
	var jobTypeName string
	if m.createJobType == "xml" {
		jobTypeName = "XML Plan Job"
	} else if m.createJobType == "impl" {
		jobTypeName = "Implementation Job"
	} else if m.createJobType == "agent-from-chat" {
		jobTypeName = "Agent from Chat Job"
	}

	var b strings.Builder
	b.WriteString(theme.DefaultTheme.Header.Render(fmt.Sprintf("Create %s", jobTypeName)))
	b.WriteString("\n\nEnter job title:\n")
	b.WriteString(m.createJobInput.View())
	b.WriteString("\n\n")
	b.WriteString(theme.DefaultTheme.Muted.Render("Press Enter to create, Esc to cancel"))

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultColors.Orange).
		Padding(1, 2).
		Render(b.String())

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog)
}

func (m statusTUIModel) renderEditDepsView() string {
	if m.editDepsJobIndex < 0 || m.editDepsJobIndex >= len(m.jobs) {
		return "Error: Invalid job selected for editing dependencies."
	}
	editJob := m.jobs[m.editDepsJobIndex]

	var b strings.Builder

	// Header
	headerText := theme.DefaultTheme.Header.Render(fmt.Sprintf("Edit Dependencies: %s", editJob.Title))
	b.WriteString(headerText)
	b.WriteString("\n\n")

	// Instructions
	instructions := theme.DefaultTheme.Muted.Render("Use ↑/↓ or j/k to navigate • Space to select/deselect • Enter to save • Esc to cancel")
	b.WriteString(instructions)
	b.WriteString("\n\n")

	// Calculate visible jobs
	visibleJobs := m.getVisibleJobs()

	// Render job list with selection indicators
	for i, job := range visibleJobs {
		globalIndex := m.scrollOffset + i

		// Build line
		var line strings.Builder

		// Cursor indicator
		if globalIndex == m.cursor {
			line.WriteString(theme.DefaultTheme.Highlight.Render(theme.IconSelect + " "))
		} else {
			line.WriteString("  ")
		}

		// Selection checkbox
		var checkbox string
		if m.editDepsSelected[job.ID] {
			checkbox = theme.DefaultTheme.Success.Render("[x]")
		} else {
			checkbox = "[ ]"
		}
		line.WriteString(checkbox)
		line.WriteString(" ")

		// Job info - don't allow selecting self as dependency
		if globalIndex == m.editDepsJobIndex {
			line.WriteString(theme.DefaultTheme.Muted.Render(fmt.Sprintf("%s (self)", job.Filename)))
		} else {
			line.WriteString(job.Filename)
		}

		// Status icon
		statusIcon := m.getStatusIcon(job.Status)
		line.WriteString(" ")
		line.WriteString(statusIcon)

		b.WriteString(line.String())
		b.WriteString("\n")
	}

	// Scroll indicators
	if len(m.jobs) > 0 {
		visibleLines := m.getVisibleJobCount()
		hasMore := m.scrollOffset+visibleLines < len(m.jobs)
		hasLess := m.scrollOffset > 0

		if hasLess || hasMore {
			b.WriteString("\n")
			indicator := ""
			if hasLess {
				indicator += "↑ "
			}
			indicator += fmt.Sprintf("[%d/%d]", m.cursor+1, len(m.jobs))
			if hasMore {
				indicator += " ↓"
			}
			b.WriteString(theme.DefaultTheme.Muted.Render(indicator))
		}
	}

	return lipgloss.NewStyle().Margin(1, 2).Render(b.String())
}

func addJobWithDependencies(planDir string, dependencies []string) tea.Cmd {
	// Build the command
	args := []string{"plan", "add", planDir, "-i"}

	// Add dependencies if provided
	for _, dep := range dependencies {
		args = append(args, "-d", dep)
	}

	// Run flow directly - delegation through grove breaks interactive TUI
	return tea.ExecProcess(exec.Command("flow", args...), func(err error) tea.Msg {
		if err != nil {
			return err
		}
		return refreshMsg{} // Refresh to show the new job
	})
}

// createImplementationJob creates a new interactive_agent job with "impl-" prefix
// that depends on the selected job
// createXmlPlanJob creates a new oneshot job with the "agent-xml" template
// that depends on the selected job.
func createXmlPlanJob(plan *orchestration.Plan, selectedJob *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		// Create the xml plan job title
		xmlTitle := fmt.Sprintf("xml-plan-%s", selectedJob.Title)

		// Generate a unique ID for the new job
		xmlID := orchestration.GenerateUniqueJobID(plan, xmlTitle)

		// Create the new job
		newJob := &orchestration.Job{
			ID:                  xmlID,
			Title:               xmlTitle,
			Type:                orchestration.JobTypeOneshot,
			Status:              orchestration.JobStatusPending,
			DependsOn:           []string{selectedJob.ID},
			Worktree:            selectedJob.Worktree,
			Template:            "agent-xml",
			PromptBody:          "generate a detailed plan",
			PrependDependencies: true,
			Output: orchestration.OutputConfig{
				Type: "file",
			},
		}

		// Add the job to the plan
		_, err := orchestration.AddJob(plan, newJob)
		if err != nil {
			return fmt.Errorf("failed to create xml plan job: %w", err)
		}

		// Return refresh message to update the TUI
		return refreshMsg{}
	}
}

// createXmlPlanJobWithDeps creates a new oneshot job with the "agent-xml" template
// that depends on multiple selected jobs.
func createXmlPlanJobWithDeps(plan *orchestration.Plan, selectedJobs []*orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		if len(selectedJobs) == 0 {
			return fmt.Errorf("no jobs selected")
		}

		// Create the xml plan job title - use first job's title
		xmlTitle := fmt.Sprintf("xml-plan-%s", selectedJobs[0].Title)

		// Generate a unique ID for the new job
		xmlID := orchestration.GenerateUniqueJobID(plan, xmlTitle)

		// Collect all dependency IDs
		var depIDs []string
		for _, job := range selectedJobs {
			depIDs = append(depIDs, job.ID)
		}

		// Use the worktree from the first selected job
		worktree := selectedJobs[0].Worktree

		// Create the new job
		newJob := &orchestration.Job{
			ID:                  xmlID,
			Title:               xmlTitle,
			Type:                orchestration.JobTypeOneshot,
			Status:              orchestration.JobStatusPending,
			DependsOn:           depIDs,
			Worktree:            worktree,
			Template:            "agent-xml",
			PromptBody:          "generate a detailed plan",
			PrependDependencies: true,
			Output: orchestration.OutputConfig{
				Type: "file",
			},
		}

		// Add the job to the plan
		_, err := orchestration.AddJob(plan, newJob)
		if err != nil {
			return fmt.Errorf("failed to create xml plan job: %w", err)
		}

		// Return refresh message to update the TUI
		return refreshMsg{}
	}
}

func createImplementationJob(plan *orchestration.Plan, selectedJob *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		// Create the implementation job title
		implTitle := fmt.Sprintf("impl-%s", selectedJob.Title)

		// Generate a unique ID for the new job
		implID := orchestration.GenerateUniqueJobID(plan, implTitle)

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

// createImplementationJobWithDeps creates a new interactive_agent job with "impl-" prefix
// that depends on multiple selected jobs.
func createImplementationJobWithDeps(plan *orchestration.Plan, selectedJobs []*orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		if len(selectedJobs) == 0 {
			return fmt.Errorf("no jobs selected")
		}

		// Create the implementation job title - use first job's title
		implTitle := fmt.Sprintf("impl-%s", selectedJobs[0].Title)

		// Generate a unique ID for the new job
		implID := orchestration.GenerateUniqueJobID(plan, implTitle)

		// Collect all dependency IDs
		var depIDs []string
		for _, job := range selectedJobs {
			depIDs = append(depIDs, job.ID)
		}

		// Use the worktree from the first selected job
		worktree := selectedJobs[0].Worktree

		// Create the new job
		newJob := &orchestration.Job{
			ID:        implID,
			Title:     implTitle,
			Type:      orchestration.JobTypeInteractiveAgent,
			Status:    orchestration.JobStatusPending,
			DependsOn: depIDs,
			Worktree:  worktree,
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

// createXmlPlanJobWithTitle creates an XML plan job with a custom title
func createXmlPlanJobWithTitle(plan *orchestration.Plan, selectedJobs []*orchestration.Job, customTitle string) tea.Cmd {
	return func() tea.Msg {
		if len(selectedJobs) == 0 {
			return fmt.Errorf("no jobs selected")
		}

		// Generate a unique ID for the new job
		xmlID := orchestration.GenerateUniqueJobID(plan, customTitle)

		// Collect all dependency IDs
		var depIDs []string
		for _, job := range selectedJobs {
			depIDs = append(depIDs, job.ID)
		}

		// Use the worktree from the first selected job
		worktree := selectedJobs[0].Worktree

		// Create the new job
		newJob := &orchestration.Job{
			ID:                  xmlID,
			Title:               customTitle,
			Type:                orchestration.JobTypeOneshot,
			Status:              orchestration.JobStatusPending,
			DependsOn:           depIDs,
			Worktree:            worktree,
			Template:            "agent-xml",
			PromptBody:          "generate a detailed plan",
			PrependDependencies: true,
			Output: orchestration.OutputConfig{
				Type: "file",
			},
		}

		// Add the job to the plan
		_, err := orchestration.AddJob(plan, newJob)
		if err != nil {
			return createJobCompleteMsg{err: err}
		}

		return createJobCompleteMsg{err: nil}
	}
}

// createImplementationJobWithTitle creates an implementation job with a custom title
func createImplementationJobWithTitle(plan *orchestration.Plan, selectedJobs []*orchestration.Job, customTitle string) tea.Cmd {
	return func() tea.Msg {
		if len(selectedJobs) == 0 {
			return fmt.Errorf("no jobs selected")
		}

		// Generate a unique ID for the new job
		implID := orchestration.GenerateUniqueJobID(plan, customTitle)

		// Collect all dependency IDs
		var depIDs []string
		for _, job := range selectedJobs {
			depIDs = append(depIDs, job.ID)
		}

		// Use the worktree from the first selected job
		worktree := selectedJobs[0].Worktree

		// Create the new job
		newJob := &orchestration.Job{
			ID:        implID,
			Title:     customTitle,
			Type:      orchestration.JobTypeInteractiveAgent,
			Status:    orchestration.JobStatusPending,
			DependsOn: depIDs,
			Worktree:  worktree,
			Output: orchestration.OutputConfig{
				Type: "file",
			},
		}

		// Add the job to the plan
		_, err := orchestration.AddJob(plan, newJob)
		if err != nil {
			return createJobCompleteMsg{err: err}
		}

		return createJobCompleteMsg{err: nil}
	}
}

func createAgentFromChatJobWithTitle(plan *orchestration.Plan, selectedJobs []*orchestration.Job, customTitle string) tea.Cmd {
	return func() tea.Msg {
		if len(selectedJobs) == 0 {
			return fmt.Errorf("no jobs selected")
		}

		// Generate a unique ID for the new job
		jobID := orchestration.GenerateUniqueJobID(plan, customTitle)

		// Collect all dependency IDs
		var depIDs []string
		for _, job := range selectedJobs {
			depIDs = append(depIDs, job.ID)
		}

		// Use the worktree from the first selected job
		worktree := selectedJobs[0].Worktree

		// Create the new job using the agent-from-chat template
		newJob := &orchestration.Job{
			ID:               jobID,
			Title:            customTitle,
			Type:             orchestration.JobTypeInteractiveAgent,
			Status:           orchestration.JobStatusPending,
			DependsOn:        depIDs,
			Worktree:         worktree,
			Template:         "agent-from-chat",
			GeneratePlanFrom: true,
			PromptBody:       "Implement the detailed plan that will be generated from the dependency.",
			Output: orchestration.OutputConfig{
				Type: "commit",
			},
		}

		// Add the job to the plan
		_, err := orchestration.AddJob(plan, newJob)
		if err != nil {
			return createJobCompleteMsg{err: err}
		}

		return createJobCompleteMsg{err: nil}
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