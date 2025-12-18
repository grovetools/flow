package status_tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	gtable "github.com/mattsolo1/grove-core/tui/components/table"
	"github.com/mattsolo1/grove-core/tui/theme"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"gopkg.in/yaml.v3"
)

const (
	// Column width caps for table rendering
	maxJobColumnWidth   = 30
	maxTitleColumnWidth = 30
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
		"interrupted":                    theme.DefaultTheme.Magenta, // Magenta for interrupted jobs
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

				// Calculate available width for filename
				// Cap JOB column at reasonable width
				maxFilenameWidth := maxJobColumnWidth - len(treePrefix) - 2 // 2 for icon + space
				filename := job.Filename
				if len(filename) > maxFilenameWidth && maxFilenameWidth > 3 {
					filename = filename[:maxFilenameWidth-3] + "..."
				}

				if job.Status == orchestration.JobStatusCompleted || job.Status == orchestration.JobStatusAbandoned {
					filename = t.Muted.Render(filename)
				}
				cell = fmt.Sprintf("%s%s %s", treePrefix, statusIcon, filename)
			case "TITLE":
				titleText := job.Title
				if titleText == "" {
					cell = t.Muted.Render("-")
				} else {
					// Cap title at maxTitleColumnWidth with ellipsis
					if len(titleText) > maxTitleColumnWidth {
						titleText = titleText[:maxTitleColumnWidth-3] + "..."
					}
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
	case "interrupted": // Jobs that were running but process is dead
		icon = theme.IconStatusInterrupted
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
// renderStyledFrontmatter parses raw YAML and renders it as a styled key-value list with sections.
func renderStyledFrontmatter(rawYAML string) string {
	var data map[string]interface{}
	if err := yaml.Unmarshal([]byte(rawYAML), &data); err != nil {
		return theme.DefaultTheme.Error.Render("Error parsing YAML: " + err.Error())
	}

	if len(data) == 0 {
		return theme.DefaultTheme.Muted.Render("No properties found.")
	}

	// Define property sections
	type section struct {
		title      string
		properties []string
	}

	sections := []section{
		{title: "Identity", properties: []string{"id", "title", "filename"}},
		{title: "Execution", properties: []string{"status", "type", "template", "model"}},
		{title: "Context", properties: []string{"repository", "worktree", "depends_on", "prepend_dependencies", "git_changes"}},
		{title: "Timestamps", properties: []string{"duration", "completed_at", "updated_at", "created_at"}},
	}

	// Build categorized map
	categorized := make(map[string]bool)
	for _, sec := range sections {
		for _, prop := range sec.properties {
			categorized[prop] = true
		}
	}

	// Collect remaining properties
	var remainingKeys []string
	for k := range data {
		if !categorized[k] {
			remainingKeys = append(remainingKeys, k)
		}
	}
	sort.Strings(remainingKeys)

	// Define styles
	keyStyle := theme.DefaultTheme.Muted.Copy().Italic(true)
	dimStyle := theme.DefaultTheme.Muted
	sectionStyle := lipgloss.NewStyle().Foreground(theme.DefaultColors.Cyan)

	var builder strings.Builder
	firstSection := true

	// Render sections
	for _, sec := range sections {
		// Check if section has properties
		hasProps := false
		for _, prop := range sec.properties {
			if _, exists := data[prop]; exists {
				hasProps = true
				break
			}
		}
		if !hasProps {
			continue
		}

		// Section spacing and header
		if !firstSection {
			builder.WriteString("\n")
		}
		firstSection = false
		builder.WriteString(sectionStyle.Render(sec.title) + "\n")

		// Render properties in this section
		for _, k := range sec.properties {
			v, exists := data[k]
			if !exists {
				continue
			}
			renderProperty(&builder, k, v, keyStyle, dimStyle)
		}
	}

	// Render remaining properties
	if len(remainingKeys) > 0 {
		if !firstSection {
			builder.WriteString("\n")
		}
		builder.WriteString(sectionStyle.Render("Other") + "\n")
		for _, k := range remainingKeys {
			renderProperty(&builder, k, data[k], keyStyle, dimStyle)
		}
	}

	return builder.String()
}

func renderProperty(builder *strings.Builder, k string, v interface{}, keyStyle, dimStyle lipgloss.Style) {
	bullet := dimStyle.Render("  " + theme.IconBullet + " ")

	switch val := v.(type) {
	case string:
		var valueStr string
		var icon string
		var hasStyle bool
		var valueStyle lipgloss.Style

		// Add icons and colors for specific fields
		switch k {
		case "status":
			switch val {
			case "completed":
				icon, valueStyle, hasStyle = theme.IconStatusCompleted+" ", theme.DefaultTheme.Success, true
			case "running":
				icon, valueStyle, hasStyle = theme.IconStatusRunning+" ", theme.DefaultTheme.Info, true
			case "failed":
				icon, valueStyle, hasStyle = theme.IconStatusFailed+" ", theme.DefaultTheme.Error, true
			case "blocked":
				icon, valueStyle, hasStyle = theme.IconStatusBlocked+" ", theme.DefaultTheme.Error, true
			case "pending":
				icon, valueStyle, hasStyle = theme.IconPending+" ", theme.DefaultTheme.Muted, true
			case "todo":
				icon, valueStyle, hasStyle = theme.IconStatusTodo+" ", theme.DefaultTheme.Muted, true
			case "hold":
				icon, valueStyle, hasStyle = theme.IconStatusHold+" ", theme.DefaultTheme.Warning, true
			case "abandoned":
				icon, valueStyle, hasStyle = theme.IconStatusAbandoned+" ", theme.DefaultTheme.Muted, true
			case "needs_review":
				icon, valueStyle, hasStyle = theme.IconStatusNeedsReview+" ", theme.DefaultTheme.Info, true
			}
		case "type":
			switch val {
			case "interactive_agent":
				icon = theme.IconInteractiveAgent + " "
			case "headless_agent":
				icon = theme.IconHeadlessAgent + " "
			case "chat":
				icon = theme.IconChat + " "
			case "oneshot":
				icon = theme.IconOneshot + " "
			case "shell":
				icon = theme.IconShell + " "
			}
		case "git_changes":
			if val == "true" {
				icon = theme.IconGit + " "
			}
		case "worktree":
			if val != "" {
				icon = theme.IconWorktree + " "
			}
		case "repository":
			if val != "" {
				icon = theme.IconRepo + " "
			}
		}

		if val == "" {
			valueStr = dimStyle.Render("-")
		} else if hasStyle {
			valueStr = valueStyle.Render(icon + val)
		} else {
			valueStr = icon + val
		}
		builder.WriteString(fmt.Sprintf("%s%s%s %s\n", bullet, keyStyle.Render(k), dimStyle.Render(":"), valueStr))

	case int, int64, float64:
		builder.WriteString(fmt.Sprintf("%s%s%s %v\n", bullet, keyStyle.Render(k), dimStyle.Render(":"), val))

	case bool:
		valueStr := theme.DefaultTheme.Success.Render("✓")
		if !val {
			valueStr = dimStyle.Render("-")
		}
		builder.WriteString(fmt.Sprintf("%s%s%s %s\n", bullet, keyStyle.Render(k), dimStyle.Render(":"), valueStr))

	case []interface{}:
		if len(val) == 0 {
			builder.WriteString(fmt.Sprintf("%s%s%s %s\n", bullet, keyStyle.Render(k), dimStyle.Render(":"), dimStyle.Render("-")))
		} else {
			builder.WriteString(fmt.Sprintf("%s%s%s\n", bullet, keyStyle.Render(k), dimStyle.Render(":")))
			for i, item := range val {
				connector := "├─"
				if i == len(val)-1 {
					connector = "└─"
				}
				builder.WriteString(fmt.Sprintf("      %s %v\n", dimStyle.Render(connector), item))
			}
		}

	case map[string]interface{}:
		builder.WriteString(fmt.Sprintf("%s%s%s %s\n", bullet, keyStyle.Render(k), dimStyle.Render(":"), dimStyle.Render(fmt.Sprintf("(%d items)", len(val)))))

	default:
		builder.WriteString(fmt.Sprintf("%s%s%s %v\n", bullet, keyStyle.Render(k), dimStyle.Render(":"), val))
	}
}

// renderStyledMarkdown applies basic syntax highlighting to markdown content.
func renderStyledMarkdown(rawContent string) string {
	// Find frontmatter boundaries
	if !strings.HasPrefix(rawContent, "---\n") {
		return rawContent
	}

	secondSeparator := strings.Index(rawContent[4:], "\n---\n")
	if secondSeparator == -1 {
		return rawContent
	}

	frontmatterEnd := 4 + secondSeparator + 5
	frontmatterBlock := rawContent[:frontmatterEnd]
	bodyBlock := rawContent[frontmatterEnd:]

	styledFrontmatter := theme.DefaultTheme.Muted.Italic(true).Render(frontmatterBlock)

	var bodyBuilder strings.Builder
	h1Style := theme.DefaultTheme.Header.Copy().Bold(true).Foreground(theme.DefaultColors.Cyan)
	h2Style := theme.DefaultTheme.Header.Copy().Foreground(theme.DefaultColors.Blue)
	h3Style := theme.DefaultTheme.Header.Copy().Foreground(theme.DefaultColors.Violet)

	for _, line := range strings.Split(bodyBlock, "\n") {
		if strings.HasPrefix(line, "### ") {
			bodyBuilder.WriteString(h3Style.Render(line) + "\n")
		} else if strings.HasPrefix(line, "## ") {
			bodyBuilder.WriteString(h2Style.Render(line) + "\n")
		} else if strings.HasPrefix(line, "# ") {
			bodyBuilder.WriteString(h1Style.Render(line) + "\n")
		} else {
			styledLine := styleInlineMarkdown(line)
			bodyBuilder.WriteString(styledLine + "\n")
		}
	}

	return styledFrontmatter + bodyBuilder.String()
}

// styleInlineMarkdown applies styling for bold and italic markdown syntax.
func styleInlineMarkdown(line string) string {
	boldStyle := lipgloss.NewStyle().Bold(true)
	italicStyle := lipgloss.NewStyle().Italic(true)
	result := line

	// Handle **bold**
	for {
		start := strings.Index(result, "**")
		if start == -1 {
			break
		}
		end := strings.Index(result[start+2:], "**")
		if end == -1 {
			break
		}
		end += start + 2
		result = result[:start] + boldStyle.Render(result[start+2:end]) + result[end+2:]
	}

	// Handle __bold__
	for {
		start := strings.Index(result, "__")
		if start == -1 {
			break
		}
		end := strings.Index(result[start+2:], "__")
		if end == -1 {
			break
		}
		end += start + 2
		result = result[:start] + boldStyle.Render(result[start+2:end]) + result[end+2:]
	}

	// Handle *italic*
	for {
		start := strings.Index(result, "*")
		if start == -1 {
			break
		}
		if start > 0 && result[start-1] == '*' {
			result = result[:start] + result[start+1:]
			continue
		}
		end := strings.Index(result[start+1:], "*")
		if end == -1 {
			break
		}
		end += start + 1
		if end+1 < len(result) && result[end+1] == '*' {
			result = result[:start] + result[start+1:]
			continue
		}
		result = result[:start] + italicStyle.Render(result[start+1:end]) + result[end+1:]
	}

	// Handle _italic_
	for {
		start := strings.Index(result, "_")
		if start == -1 {
			break
		}
		if start > 0 && result[start-1] == '_' {
			result = result[:start] + result[start+1:]
			continue
		}
		end := strings.Index(result[start+1:], "_")
		if end == -1 {
			break
		}
		end += start + 1
		if end+1 < len(result) && result[end+1] == '_' {
			result = result[:start] + result[start+1:]
			continue
		}
		result = result[:start] + italicStyle.Render(result[start+1:end]) + result[end+1:]
	}

	return result
}

// renderStyledBriefing applies syntax highlighting to XML briefing content.
func renderStyledBriefing(rawContent string) string {
	// Check if it's XML
	if !strings.HasPrefix(strings.TrimSpace(rawContent), "<") {
		return rawContent
	}

	var builder strings.Builder
	dimStyle := theme.DefaultTheme.Muted
	tagStyle := lipgloss.NewStyle().Foreground(theme.DefaultColors.Blue)
	attrNameStyle := lipgloss.NewStyle().Foreground(theme.DefaultColors.Cyan)
	attrValueStyle := lipgloss.NewStyle().Foreground(theme.DefaultColors.Green)
	commentStyle := dimStyle.Copy().Italic(true)

	lines := strings.Split(rawContent, "\n")
	for _, line := range lines {
		styledLine := styleXMLLine(line, tagStyle, attrNameStyle, attrValueStyle, commentStyle, dimStyle)
		builder.WriteString(styledLine + "\n")
	}

	return builder.String()
}

// styleXMLLine applies styling to a single line of XML.
func styleXMLLine(line string, tagStyle, attrNameStyle, attrValueStyle, commentStyle, dimStyle lipgloss.Style) string {
	trimmed := strings.TrimSpace(line)

	// Handle XML comments
	if strings.HasPrefix(trimmed, "<!--") {
		return commentStyle.Render(line)
	}

	// Simple XML styling: tags, attributes, content
	result := line
	var styled strings.Builder

	i := 0
	for i < len(result) {
		char := result[i]

		if char == '<' {
			// Start of a tag
			tagEnd := i + 1
			
			// Find the end of the tag
			for tagEnd < len(result) && result[tagEnd] != '>' {
				tagEnd++
			}
			if tagEnd < len(result) {
				tagEnd++ // Include the '>'
			}

			tagContent := result[i:tagEnd]
			styledTag := styleXMLTag(tagContent, tagStyle, attrNameStyle, attrValueStyle, dimStyle)
			styled.WriteString(styledTag)
			i = tagEnd
		} else {
			// Content outside tags
			contentStart := i
			for i < len(result) && result[i] != '<' {
				i++
			}
			content := result[contentStart:i]
			
			// Only render non-whitespace content
			if strings.TrimSpace(content) != "" {
				styled.WriteString(content)
			} else {
				styled.WriteString(content)
			}
		}
	}

	return styled.String()
}

// styleXMLTag styles the interior of an XML tag.
func styleXMLTag(tag string, tagStyle, attrNameStyle, attrValueStyle, dimStyle lipgloss.Style) string {
	if len(tag) < 2 {
		return tag
	}

	// Extract tag name and attributes
	// Format: <tagname attr="value">
	var result strings.Builder
	result.WriteString(dimStyle.Render("<"))

	inner := tag[1 : len(tag)-1]
	
	// Handle closing tags
	if strings.HasPrefix(inner, "/") {
		result.WriteString(dimStyle.Render("/"))
		inner = inner[1:]
	}

	// Handle self-closing tags
	selfClosing := strings.HasSuffix(inner, "/")
	if selfClosing {
		inner = strings.TrimSuffix(inner, "/")
	}

	// Split tag name from attributes
	parts := strings.Fields(inner)
	if len(parts) == 0 {
		result.WriteString(dimStyle.Render(">"))
		return result.String()
	}

	// Tag name
	result.WriteString(tagStyle.Render(parts[0]))

	// Attributes
	if len(parts) > 1 {
		attrString := strings.Join(parts[1:], " ")
		styledAttrs := styleAttributes(attrString, attrNameStyle, attrValueStyle, dimStyle)
		result.WriteString(" ")
		result.WriteString(styledAttrs)
	}

	if selfClosing {
		result.WriteString(dimStyle.Render("/"))
	}
	result.WriteString(dimStyle.Render(">"))

	return result.String()
}

// styleAttributes styles XML attributes in the form name="value".
func styleAttributes(attrString string, attrNameStyle, attrValueStyle, dimStyle lipgloss.Style) string {
	var result strings.Builder
	i := 0

	for i < len(attrString) {
		// Skip whitespace
		for i < len(attrString) && attrString[i] == ' ' {
			result.WriteString(" ")
			i++
		}
		if i >= len(attrString) {
			break
		}

		// Find attribute name (up to '=')
		nameStart := i
		for i < len(attrString) && attrString[i] != '=' && attrString[i] != ' ' {
			i++
		}
		attrName := attrString[nameStart:i]
		
		if attrName != "" {
			result.WriteString(attrNameStyle.Render(attrName))
		}

		// Skip spaces around '='
		for i < len(attrString) && attrString[i] == ' ' {
			result.WriteString(" ")
			i++
		}

		// Handle '='
		if i < len(attrString) && attrString[i] == '=' {
			result.WriteString(dimStyle.Render("="))
			i++
		}

		// Skip spaces after '='
		for i < len(attrString) && attrString[i] == ' ' {
			result.WriteString(" ")
			i++
		}

		// Handle quoted value
		if i < len(attrString) && (attrString[i] == '"' || attrString[i] == '\'') {
			quote := attrString[i]
			result.WriteString(dimStyle.Render(string(quote)))
			i++

			valueStart := i
			for i < len(attrString) && attrString[i] != quote {
				i++
			}
			value := attrString[valueStart:i]
			result.WriteString(attrValueStyle.Render(value))

			if i < len(attrString) {
				result.WriteString(dimStyle.Render(string(quote)))
				i++
			}
		}
	}

	return result.String()
}
