package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
)

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
	showHelp      bool   // Show extended help
	statusSummary string
	err           error
	width         int
	height        int
	confirmArchive bool  // Show archive confirmation
	planDir       string // Store plan directory for refresh
}

// Key bindings
type keyMap struct {
	Up            string
	Down          string
	Left          string
	Right         string
	Select        string
	ToggleMulti   string
	Archive       string
	Edit          string
	Run           string
	SetCompleted  string
	AddJob        string
	ToggleSummaries string
	Quit          string
	Help          string
}

var keys = keyMap{
	Up:           "k",
	Down:         "j",
	Left:         "h",
	Right:        "l",
	Select:       " ",
	ToggleMulti:  "m",
	Archive:      "a",
	Edit:         "e",
	Run:          "r",
	SetCompleted: "c",
	AddJob:       "n",
	ToggleSummaries: "s",
	Quit:         "q",
	Help:         "?",
}

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205"))

	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("205"))

	cursorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214"))  // Orange for current item
	
	indicatorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("205"))  // Pink for indicators

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	depAnnotationStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))  // Darker grey for dependency annotations

	statusStyles = map[orchestration.JobStatus]lipgloss.Style{
		orchestration.JobStatusCompleted:    lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
		orchestration.JobStatusRunning:      lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
		orchestration.JobStatusFailed:       lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
		orchestration.JobStatusBlocked:      lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
		orchestration.JobStatusNeedsReview:  lipgloss.NewStyle().Foreground(lipgloss.Color("12")),
		orchestration.JobStatusPendingUser:  lipgloss.NewStyle().Foreground(lipgloss.Color("14")),
		orchestration.JobStatusPendingLLM:   lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
		orchestration.JobStatusPending:      lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
	}
)

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

// Init initializes the TUI
func (m statusTUIModel) Init() tea.Cmd {
	return nil
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
			case "n", "N", "ctrl+c", keys.Quit:
				m.confirmArchive = false
			}
			return m, nil
		}
		switch msg.String() {
		case "ctrl+c", keys.Quit:
			return m, tea.Quit

		case keys.Up:
			if m.cursor > 0 {
				m.cursor--
			}

		case keys.Down:
			if m.cursor < len(m.jobs)-1 {
				m.cursor++
			}

		case keys.Select:
			if m.multiSelect && m.cursor < len(m.jobs) {
				job := m.jobs[m.cursor]
				if m.selected[job.ID] {
					delete(m.selected, job.ID)
				} else {
					m.selected[job.ID] = true
				}
			}

		case keys.ToggleMulti:
			m.multiSelect = !m.multiSelect
			if !m.multiSelect {
				// Clear selections when exiting multi-select mode
				m.selected = make(map[string]bool)
			}

		case keys.Archive:
			if m.cursor < len(m.jobs) {
				m.confirmArchive = true
			}

		case keys.Edit:
			if m.cursor < len(m.jobs) {
				job := m.jobs[m.cursor]
				return m, editJob(job)
			}

		case keys.Run:
			if m.cursor < len(m.jobs) {
				job := m.jobs[m.cursor]
				return m, runJob(m.plan.Directory, job)
			}

		case keys.SetCompleted:
			if m.cursor < len(m.jobs) {
				job := m.jobs[m.cursor]
				return m, tea.Sequence(
					setJobCompleted(job, m.plan),
					refreshPlan(m.planDir),
				)
			}

		case keys.AddJob:
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
		
		case keys.ToggleSummaries:
			m.showSummaries = !m.showSummaries
		
		case keys.Help:
			m.showHelp = !m.showHelp
		}
	}

	return m, nil
}

// View renders the TUI
func (m statusTUIModel) View() string {
	if m.err != nil {
		return fmt.Sprintf("Error: %v\n", m.err)
	}
	
	// Show help popup if active (centered overlay)
	if m.showHelp {
		helpView := m.renderHelp()
		return lipgloss.NewStyle().
			Width(m.width).
			Height(m.height).
			Align(lipgloss.Center, lipgloss.Center).
			Render(helpView)
	}

	var s strings.Builder

	// Title and summary
	s.WriteString(titleStyle.Render(fmt.Sprintf("Plan: %s", m.plan.Name)))
	s.WriteString("\n\n")
	s.WriteString(m.statusSummary)
	s.WriteString("\n")

	// Job tree
	s.WriteString(m.renderJobTree())
	s.WriteString("\n")

	// Show confirmation dialog if needed
	if m.confirmArchive {
		if m.cursor < len(m.jobs) {
			job := m.jobs[m.cursor]
			s.WriteString("\n")
			s.WriteString(lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("226")).
				Render(fmt.Sprintf("Archive '%s'? (y/n)", job.Filename)))
			s.WriteString("\n")
		}
	} else {
		// Help text
		s.WriteString(m.renderHelp())
	}

	return s.String()
}

// stripANSI removes ANSI escape sequences from a string
func stripANSI(str string) string {
	ansiRegex := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	return ansiRegex.ReplaceAllString(str, "")
}

// renderJobTree renders the job tree with proper indentation
func (m statusTUIModel) renderJobTree() string {
	var s strings.Builder
	s.WriteString(fmt.Sprintf("📁 %s\n", m.plan.Name))

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
		
		// Build job content WITHOUT indicators first
		jobContent := fmt.Sprintf("%s %s", m.getStatusIcon(job.Status), job.Filename)
		if job.Title != "" {
			jobContent += fmt.Sprintf(" (%s)", job.Title)
		}
		
		// Add dependency annotations if job has multiple dependencies
		var depAnnotation string
		if len(job.Dependencies) > 1 && m.jobParents[job.ID] != nil {
			var otherDeps []string
			for _, dep := range job.Dependencies {
				if dep != nil && dep.ID != m.jobParents[job.ID].ID {
					otherDeps = append(otherDeps, dep.Filename)
				}
			}
			if len(otherDeps) > 0 {
				// Store annotation separately to apply different styling
				depAnnotation = fmt.Sprintf(" ⚠️  Also: %s", strings.Join(otherDeps, ", "))
			}
		}
		
		// Apply styling to the job content based on state
		styledJobContent := jobContent
		if i == m.cursor && m.selected[job.ID] {
			// Both cursor and selected - use cursor style
			styledJobContent = cursorStyle.Render(jobContent)
		} else if i == m.cursor {
			// Just cursor
			styledJobContent = cursorStyle.Render(jobContent)
		} else if m.selected[job.ID] {
			// Just selected  
			styledJobContent = selectedStyle.Render(jobContent)
		}
		
		// Build indicators separately with their own style
		indicators := ""
		if m.selected[job.ID] {
			indicators += indicatorStyle.Render(" ◆")
		}
		if i == m.cursor {
			indicators += indicatorStyle.Render(" ◀")
		}
		
		// Style the dependency annotation in grey
		styledDepAnnotation := ""
		if depAnnotation != "" {
			styledDepAnnotation = depAnnotationStyle.Render(depAnnotation)
		}
		
		// Combine all parts
		fullLine := treePart + styledJobContent + styledDepAnnotation + indicators
		
		// Add summary on a new line if toggled on and available
		if m.showSummaries && job.Summary != "" {
			summaryStyle := lipgloss.NewStyle().
				Foreground(lipgloss.Color("244")). // A muted color
				Italic(true).
				PaddingLeft(indent*4 + 6) // indent level * 4 spaces + tree chars + space
			
			fullLine += "\n" + summaryStyle.Render("↳ "+job.Summary)
		}
		
		s.WriteString(fullLine + "\n")
		rendered[job.ID] = true
	}

	return s.String()
}

// getStatusIcon returns the icon for a job status
func (m statusTUIModel) getStatusIcon(status orchestration.JobStatus) string {
	icon := ""
	style := lipgloss.NewStyle()
	
	switch status {
	case orchestration.JobStatusCompleted:
		icon = "✓"
		style = statusStyles[status]
	case orchestration.JobStatusRunning:
		icon = "⚡"
		style = statusStyles[status]
	case orchestration.JobStatusFailed:
		icon = "✗"
		style = statusStyles[status]
	case orchestration.JobStatusBlocked:
		icon = "🚫"
		style = statusStyles[status]
	case orchestration.JobStatusNeedsReview:
		icon = "👁"
		style = statusStyles[status]
	case orchestration.JobStatusPendingUser:
		icon = "💬"
		style = statusStyles[status]
	case orchestration.JobStatusPendingLLM:
		icon = "🤖"
		style = statusStyles[status]
	default:
		icon = "⏳"
		style = statusStyles[orchestration.JobStatusPending]
	}
	
	return style.Render(icon)
}

// renderHelp renders the help text
func (m statusTUIModel) renderHelp() string {
	if m.showHelp {
		// Create styles matching grove-context
		boxStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("241")).
			Padding(2, 3).
			Width(70).
			Align(lipgloss.Center)
		
		titleStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("12")).
			MarginBottom(1)
		
		columnStyle := lipgloss.NewStyle().
			Width(30).
			MarginRight(4)
		
		keyStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("34")).
			Bold(true)
		
		// Navigation column
		navItems := []string{
			"Navigation:",
			"",
			keyStyle.Render("j/k") + " - Move up/down",
			keyStyle.Render("h/l") + " - Move left/right",
			"",
			"Selection:",
			"",
			keyStyle.Render("m") + " - Toggle multi-select",
			keyStyle.Render("Space") + " - Select/deselect",
			"",
			"Actions:",
			"",
			keyStyle.Render("r") + " - Run job(s)",
			keyStyle.Render("e") + " - Edit job file",
			keyStyle.Render("c") + " - Mark completed",
			keyStyle.Render("s") + " - Toggle summaries",
			keyStyle.Render("a") + " - Archive job",
			keyStyle.Render("n") + " - Add new job",
			"",
			keyStyle.Render("q") + " - Quit",
			keyStyle.Render("?") + " - Toggle this help",
		}
		
		// Legend column
		legendItems := []string{
			"Legend:",
			"",
			"Job Status:",
			"  ✓ - Completed",
			"  ⚡ - Running",
			"  ✗ - Failed",
			"  🚫 - Blocked",
			"  👁 - Needs Review",
			"  💬 - Pending User",
			"  🤖 - Pending LLM",
			"  ⏳ - Pending",
			"",
			"Symbols:",
			"  ◀ - Current item",
			"  ◆ - Selected item",
			"  ↳ - Job summary",
			"",
			"Other:",
			"  ⚠️ - Multiple dependencies",
		}
		
		// Render columns
		navColumn := columnStyle.Render(strings.Join(navItems, "\n"))
		legendColumn := columnStyle.Copy().MarginRight(0).Render(strings.Join(legendItems, "\n"))
		
		// Combine columns
		content := lipgloss.JoinHorizontal(lipgloss.Top, navColumn, legendColumn)
		
		// Add title and wrap in box
		title := titleStyle.Render("Flow Plan Status - Help")
		fullContent := lipgloss.JoinVertical(lipgloss.Center, title, content)
		
		return boxStyle.Render(fullContent)
	}
	
	// Minimal help - just show navigation and help key
	help := []string{
		"j/k: up/down • r: run • c: complete • s: summaries • ?: help • q: quit",
	}
	
	return helpStyle.Render(strings.Join(help, "\n"))
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
	program := tea.NewProgram(model, tea.WithAltScreen())
	
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("error running status TUI: %w", err)
	}
	
	return nil
}