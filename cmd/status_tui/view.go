package status_tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	gtable "github.com/mattsolo1/grove-core/tui/components/table"
	"github.com/mattsolo1/grove-core/tui/theme"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
)

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

// renderTableViewWithWidth renders the jobs as a table with a maximum width constraint
func (m Model) renderTableViewWithWidth(maxWidth int) string {
	tableStr := m.renderTableView()
	// Apply width constraint to prevent overflow
	if maxWidth > 0 {
		return lipgloss.NewStyle().MaxWidth(maxWidth).Render(tableStr)
	}
	return tableStr
}

// renderTableView renders the jobs as a table with JOB, TYPE, and STATUS columns
func (m Model) renderTableView() string {
	t := theme.DefaultTheme
	headers := []string{"SEL", "JOB", "TYPE", "STATUS"}
	var rows [][]string

	visibleJobs := m.getVisibleJobs()
	statusStyles := getStatusStyles()

	for i, job := range visibleJobs {
		// Selection checkbox
		var selCheckbox string
		if m.Selected[job.ID] {
			selCheckbox = t.Success.Render(theme.IconSelect)
		} else {
			selCheckbox = theme.IconUnselect
		}
		// JOB column (with tree indentation and connectors)
		indent := m.JobIndents[job.ID]

		// Build tree prefix with connectors
		var treePrefix string
		if indent > 0 {
			// Add spacing for parent levels
			treePrefix = strings.Repeat("  ", indent-1)

			// Determine if this is the last child at this level
			globalIndex := m.ScrollOffset + i
			isLast := true
			for j := globalIndex + 1; j < len(m.Jobs); j++ {
				if m.JobIndents[m.Jobs[j].ID] == indent {
					isLast = false
					break
				}
				if m.JobIndents[m.Jobs[j].ID] < indent {
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
		case "headless_agent":
			jobTypeSymbol = theme.IconHeadlessAgent
		case "chat":
			jobTypeSymbol = theme.IconChat
		case "oneshot":
			jobTypeSymbol = theme.IconOneshot
		case "shell":
			jobTypeSymbol = theme.IconShell
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
	tableStr := gtable.SelectableTable(headers, rows, m.Cursor-m.ScrollOffset)

	return tableStr
}

// renderJobTree renders the job tree with proper indentation
func (m Model) renderJobTree() string {
	var s strings.Builder

	// Calculate viewport bounds using the shared helper
	visibleLines := m.getVisibleJobCount()
	visibleStart := m.ScrollOffset
	visibleEnd := m.ScrollOffset + visibleLines

	// Ensure we don't go past the end
	if visibleEnd > len(m.Jobs) {
		visibleEnd = len(m.Jobs)
	}

	// Use the pre-calculated parent and indent information
	rendered := make(map[string]bool)

	// Render with tree characters - only render visible jobs
	for i, job := range m.Jobs {
		// Skip jobs outside the visible viewport
		if i < visibleStart || i >= visibleEnd {
			continue
		}

		indent := m.JobIndents[job.ID]
		prefix := strings.Repeat("    ", indent)

		// Determine if this is the last job at this indent level
		isLast := true
		for j := i + 1; j < len(m.Jobs); j++ {
			if m.JobIndents[m.Jobs[j].ID] == indent {
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
		if i == m.Cursor {
			arrowIndicator = theme.DefaultTheme.Highlight.Render(theme.IconArrowRightBold + " ")
		} else {
			arrowIndicator = "  "
		}

		// Build selection indicator after arrow
		var selectionCheckbox string
		if m.Selected[job.ID] {
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

		if i == m.Cursor {
			// Cursor: use bold for emphasis on the current row
			filenameStyle = lipgloss.NewStyle().Bold(true)
		} else if m.Selected[job.ID] {
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
		case "headless_agent":
			jobTypeSymbol = theme.IconHeadlessAgent + " "
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
		if m.ShowSummaries && job.Summary != "" {
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

// getJobIcon returns the icon for a job type
func getJobIcon(job *orchestration.Job) string {
	switch job.Type {
	case "interactive_agent":
		return theme.IconInteractiveAgent
	case "headless_agent":
		return theme.IconHeadlessAgent
	case "chat":
		return theme.IconChat
	case "oneshot":
		return theme.IconOneshot
	case "shell":
		return theme.IconShell
	default:
		return theme.IconChat // Default fallback
	}
}

// getStatusIcon returns a colored dot indicator for a job status
func (m Model) getStatusIcon(status orchestration.JobStatus) string {
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

func (m Model) renderStatusPicker() string {
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
	if m.Cursor < len(m.Jobs) {
		job := m.Jobs[m.Cursor]
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

		if i == m.StatusPickerCursor {
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

func (m Model) renderRenameDialog() string {
	if m.RenameJobIndex < 0 || m.RenameJobIndex >= len(m.Jobs) {
		return "Error: Invalid job selected for renaming."
	}
	job := m.Jobs[m.RenameJobIndex]

	var b strings.Builder
	b.WriteString(theme.DefaultTheme.Header.Render(fmt.Sprintf("Rename Job: %s", job.Filename)))
	b.WriteString("\n\nEnter new title:\n")
	b.WriteString(m.RenameInput.View())
	b.WriteString("\n\n")
	b.WriteString(theme.DefaultTheme.Muted.Render("Press Enter to save, Esc to cancel"))

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultColors.Orange).
		Padding(1, 2).
		Render(b.String())

	return lipgloss.Place(m.Width, m.Height, lipgloss.Center, lipgloss.Center, dialog)
}

func (m Model) renderJobCreationDialog() string {
	var jobTypeName string
	if m.CreateJobType == "xml" {
		jobTypeName = "XML Plan Job"
	} else if m.CreateJobType == "impl" {
		jobTypeName = "Implementation Job"
	} else if m.CreateJobType == "agent-from-chat" {
		jobTypeName = "Agent from Chat Job"
	}

	var b strings.Builder
	b.WriteString(theme.DefaultTheme.Header.Render(fmt.Sprintf("Create %s", jobTypeName)))
	b.WriteString("\n\nEnter job title:\n")
	b.WriteString(m.CreateJobInput.View())
	b.WriteString("\n\n")
	b.WriteString(theme.DefaultTheme.Muted.Render("Press Enter to create, Esc to cancel"))

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultColors.Orange).
		Padding(1, 2).
		Render(b.String())

	return lipgloss.Place(m.Width, m.Height, lipgloss.Center, lipgloss.Center, dialog)
}

func (m Model) renderEditDepsView() string {
	if m.EditDepsJobIndex < 0 || m.EditDepsJobIndex >= len(m.Jobs) {
		return "Error: Invalid job selected for editing dependencies."
	}
	editJob := m.Jobs[m.EditDepsJobIndex]

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
		globalIndex := m.ScrollOffset + i

		// Build line
		var line strings.Builder

		// Cursor indicator
		if globalIndex == m.Cursor {
			line.WriteString(theme.DefaultTheme.Highlight.Render(theme.IconSelect + " "))
		} else {
			line.WriteString("  ")
		}

		// Selection checkbox
		var checkbox string
		if m.EditDepsSelected[job.ID] {
			checkbox = theme.DefaultTheme.Success.Render("[x]")
		} else {
			checkbox = "[ ]"
		}
		line.WriteString(checkbox)
		line.WriteString(" ")

		// Job info - don't allow selecting self as dependency
		if globalIndex == m.EditDepsJobIndex {
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
	if len(m.Jobs) > 0 {
		visibleLines := m.getVisibleJobCount()
		hasMore := m.ScrollOffset+visibleLines < len(m.Jobs)
		hasLess := m.ScrollOffset > 0

		if hasLess || hasMore {
			b.WriteString("\n")
			indicator := ""
			if hasLess {
				indicator += "↑ "
			}
			indicator += fmt.Sprintf("[%d/%d]", m.Cursor+1, len(m.Jobs))
			if hasMore {
				indicator += " ↓"
			}
			b.WriteString(theme.DefaultTheme.Muted.Render(indicator))
		}
	}

	return lipgloss.NewStyle().Margin(1, 2).Render(b.String())
}

// getVisibleJobs returns the jobs that should be visible in the current viewport
func (m *Model) getVisibleJobs() []*orchestration.Job {
	// Calculate visible jobs based on scroll offset and viewport height
	visibleCount := m.getVisibleJobCount()
	start := m.ScrollOffset
	end := start + visibleCount
	if end > len(m.Jobs) {
		end = len(m.Jobs)
	}
	if start >= end {
		return []*orchestration.Job{}
	}
	return m.Jobs[start:end]
}

func (m Model) renderFrontmatterPane() string {
	return lipgloss.NewStyle().Align(lipgloss.Center).Render("Frontmatter View Placeholder")
}

func (m Model) renderBriefingPane() string {
	return lipgloss.NewStyle().Align(lipgloss.Center).Render("Briefing View Placeholder")
}

func (m Model) renderEditPane() string {
	return lipgloss.NewStyle().Align(lipgloss.Center).Render("Edit View Placeholder")
}
