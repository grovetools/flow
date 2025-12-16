package status_tui

import (
	"fmt"
	"strings"
	"time"

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

// formatRelativeTime formats a time as a relative string (e.g., "2h ago", "3d ago")
func formatRelativeTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}

	duration := time.Since(t)

	if duration < time.Minute {
		return "just now"
	} else if duration < time.Hour {
		minutes := int(duration.Minutes())
		return fmt.Sprintf("%dm ago", minutes)
	} else if duration < 24*time.Hour {
		hours := int(duration.Hours())
		return fmt.Sprintf("%dh ago", hours)
	} else if duration < 7*24*time.Hour {
		days := int(duration.Hours() / 24)
		return fmt.Sprintf("%dd ago", days)
	} else if duration < 30*24*time.Hour {
		weeks := int(duration.Hours() / 24 / 7)
		return fmt.Sprintf("%dw ago", weeks)
	} else {
		months := int(duration.Hours() / 24 / 30)
		return fmt.Sprintf("%dmo ago", months)
	}
}

// formatDuration formats a duration as a compact string (e.g., "2m 5s", "1h 23m")
func formatDuration(d time.Duration) string {
	if d == 0 {
		return "-"
	}

	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	} else if d < time.Hour {
		minutes := int(d.Minutes())
		seconds := int(d.Seconds()) % 60
		if seconds > 0 {
			return fmt.Sprintf("%dm %ds", minutes, seconds)
		}
		return fmt.Sprintf("%dm", minutes)
	} else if d < 24*time.Hour {
		hours := int(d.Hours())
		minutes := int(d.Minutes()) % 60
		if minutes > 0 {
			return fmt.Sprintf("%dh %dm", hours, minutes)
		}
		return fmt.Sprintf("%dh", hours)
	} else {
		days := int(d.Hours() / 24)
		hours := int(d.Hours()) % 24
		if hours > 0 {
			return fmt.Sprintf("%dd %dh", days, hours)
		}
		return fmt.Sprintf("%dd", days)
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

// renderTableView renders the jobs as a table with configurable columns
func (m Model) renderTableView() string {
	t := theme.DefaultTheme

	// Check if any jobs are selected
	hasSelection := len(m.Selected) > 0

	// Dynamically build headers from visible columns
	var headers []string

	// Always add SEL column first if there are selections
	if hasSelection {
		headers = append(headers, "SEL")
	}

	// Add other visible columns
	for _, colName := range m.availableColumns {
		if m.columnVisibility[colName] {
			headers = append(headers, colName)
		}
	}

	var rows [][]string

	visibleJobs := m.getVisibleJobs()
	statusStyles := getStatusStyles()

	for i, job := range visibleJobs {
		var row []string

		for _, colName := range headers {
			var cell string
			switch strings.ToUpper(colName) {
			case "SEL":
				// This case is only reached if hasSelection is true
				// (otherwise SEL is not in headers)
				if m.Selected[job.ID] {
					cell = t.Success.Render(theme.IconSelect)
				} else {
					cell = theme.IconUnselect
				}
			case "JOB":
				indent := m.JobIndents[job.ID]
				var treePrefix string
				if indent > 0 {
					treePrefix = strings.Repeat("  ", indent-1)
					globalIndex := m.ScrollOffset + i
					isLast := true
					for j := globalIndex + 1; j < len(m.Jobs); j++ {
						if m.JobIndents[m.Jobs[j].ID] == indent {
							isLast = false; break
						}
						if m.JobIndents[m.Jobs[j].ID] < indent {
							break
						}
					}
					if isLast { treePrefix += "└─ " } else { treePrefix += "├─ " }
				}
				statusIcon := m.getStatusIcon(job.Status)
				var filename string
				if job.Status == orchestration.JobStatusCompleted || job.Status == orchestration.JobStatusAbandoned {
					filename = t.Muted.Render(job.Filename)
				} else {
					filename = job.Filename
				}
				cell = fmt.Sprintf("%s%s %s", treePrefix, statusIcon, filename)
			case "TITLE":
				titleText := job.Title
				if titleText == "" {
					cell = t.Muted.Render("-")
				} else {
					if job.Status == orchestration.JobStatusCompleted || job.Status == orchestration.JobStatusAbandoned {
						cell = t.Muted.Render(titleText)
					} else {
						cell = titleText
					}
				}
			case "TYPE":
				var jobTypeSymbol string
				switch job.Type {
				case "interactive_agent": jobTypeSymbol = theme.IconInteractiveAgent
				case "headless_agent": jobTypeSymbol = theme.IconHeadlessAgent
				case "chat": jobTypeSymbol = theme.IconChat
				case "oneshot": jobTypeSymbol = theme.IconOneshot
				case "shell": jobTypeSymbol = theme.IconShell
				default: jobTypeSymbol = ""
				}
				var typeCol string
				if jobTypeSymbol != "" { typeCol = fmt.Sprintf("%s %s", jobTypeSymbol, job.Type) } else { typeCol = string(job.Type) }
				if job.Status == orchestration.JobStatusCompleted || job.Status == orchestration.JobStatusAbandoned {
					cell = t.Muted.Render(typeCol)
				} else {
					cell = typeCol
				}
			case "STATUS":
				statusStyle := theme.DefaultTheme.Muted
				if style, ok := statusStyles[job.Status]; ok {
					statusStyle = style
				}
				statusText := statusStyle.Render(string(job.Status))
				if job.Status == orchestration.JobStatusCompleted || job.Status == orchestration.JobStatusAbandoned {
					cell = t.Muted.Render(string(job.Status))
				} else {
					cell = statusText
				}
			case "TEMPLATE":
				templateText := job.Template
				if templateText == "" {
					templateText = t.Muted.Render("-")
				}
				cell = templateText
			case "MODEL":
				modelText := job.Model
				if modelText == "" {
					cell = t.Muted.Render("-")
				} else {
					cell = t.Muted.Render(modelText)
				}
			case "WORKTREE":
				worktreeText := job.Worktree
				if worktreeText == "" {
					cell = t.Muted.Render("-")
				} else {
					cell = t.Muted.Render(worktreeText)
				}
			case "PREPEND":
				if job.PrependDependencies {
					cell = t.Success.Render("✓")
				} else {
					cell = t.Muted.Render("-")
				}
			case "UPDATED":
				cell = t.Muted.Render(formatRelativeTime(job.UpdatedAt))
			case "COMPLETED":
				if !job.CompletedAt.IsZero() {
					cell = t.Muted.Render(formatRelativeTime(job.CompletedAt))
				} else {
					cell = t.Muted.Render("-")
				}
			case "DURATION":
				if job.Duration > 0 {
					cell = t.Muted.Render(formatDuration(job.Duration))
				} else {
					cell = t.Muted.Render("-")
				}
			default:
				cell = t.Muted.Render("?")
			}
			row = append(row, cell)
		}
		rows = append(rows, row)
	}

	if len(rows) == 0 {
		return "\n" + t.Muted.Render("No jobs to display.")
	}

	tableStr := gtable.SelectableTable(headers, rows, m.Cursor-m.ScrollOffset)
	return tableStr
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

// renderColumnSelectView renders the UI for toggling column visibility.
func (m Model) renderColumnSelectView() string {
	listView := m.columnList.View()
	styledView := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultTheme.Colors.Cyan).
		Padding(1, 2).
		Render(listView)
	helpText := lipgloss.NewStyle().
		Faint(true).
		Width(lipgloss.Width(styledView)).
		Align(lipgloss.Center).
		Render("\n\nPress space to toggle • Enter/Esc/T to close")
	content := lipgloss.JoinVertical(lipgloss.Left, styledView, helpText)
	return lipgloss.Place(m.Width, m.Height, lipgloss.Center, lipgloss.Center, content)
}
