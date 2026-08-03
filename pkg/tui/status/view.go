package status

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	markdown "github.com/grovetools/core/tui/components/markdown"
	gtable "github.com/grovetools/core/tui/components/table"
	"github.com/grovetools/core/tui/theme"
	"gopkg.in/yaml.v3"

	"github.com/grovetools/flow/pkg/orchestration"
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
		orchestration.JobStatusAbandoned: theme.DefaultTheme.Muted,     // Very subtle for abandoned jobs
		orchestration.JobStatusIdle:      theme.DefaultTheme.Highlight, // Agent waiting for next input
		// Reconciled statuses: the process is gone, but nothing recorded
		// how it ended.
		orchestration.JobStatusInterrupted: theme.DefaultTheme.Magenta,
		orchestration.JobStatusOrphaned:    theme.DefaultTheme.Magenta,
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

// renderTableViewWithWidth renders the jobs as a table that fits inside
// maxWidth: low-priority columns drop out first, and if the JOB column alone
// still overflows, its cells are truncated. The table's right-hand border
// always lands inside the pane.
func (m Model) renderTableViewWithWidth(maxWidth int) string {
	if maxWidth > 0 {
		m = m.fitToWidth(maxWidth)
	}
	tableStr := m.renderTableView()
	// Final safety net: clip anything that still got through.
	if maxWidth > 0 {
		return lipgloss.NewStyle().MaxWidth(maxWidth).Render(tableStr)
	}
	return tableStr
}

// minJobCellWidth floors the truncated JOB column: narrower than this a
// filename identifies nothing, and clipping would be no worse.
const minJobCellWidth = 12

// columnDropOrder returns the columns in the order they are dropped when the
// table doesn't fit, most expendable first. JOB is never dropped — it carries
// the row's identity, status icon and tree position. Columns missing from the
// priority list go first, so a column added to availableColumns without being
// ranked here can never silently become undroppable — which is how TOKENS, the
// second-widest column, used to survive every drop pass and leave the table
// overflowing no matter how many other columns went.
//
// TITLE goes first despite being one of the widest: it mostly restates the
// filename already in the JOB column, so it is the cheapest ~50 columns in the
// table to give back.
func (m Model) columnDropOrder() []string {
	priority := []string{
		"TITLE", "DURATION", "COMPLETED", "UPDATED", "INLINE",
		"WORKTREE", "SESSION", "MODEL", "SKILL", "TEMPLATE", "TOKENS", "STATUS", "TYPE",
	}
	ranked := make(map[string]bool, len(priority))
	for _, col := range priority {
		ranked[col] = true
	}
	var order []string
	for _, col := range m.availableColumns {
		if col != "JOB" && !ranked[col] {
			order = append(order, col)
		}
	}
	return append(order, priority...)
}

// fitToWidth returns a copy of the model whose table renders no wider than
// maxWidth.
func (m Model) fitToWidth(maxWidth int) Model {
	headers := m.tableHeaders()
	if len(headers) == 0 {
		return m
	}
	// A column's width doesn't depend on which other columns are visible, so
	// one measuring pass serves the whole drop loop.
	widths := m.measureTableColumns(headers)
	if tableRenderedWidth(headers, widths) <= maxWidth {
		return m // Everything fits.
	}

	// Copy visibility map so we don't mutate the original.
	vis := make(map[string]bool, len(m.columnVisibility))
	for k, v := range m.columnVisibility {
		vis[k] = v
	}
	m.columnVisibility = vis

	for _, col := range m.columnDropOrder() {
		if !vis[col] {
			continue
		}
		vis[col] = false
		headers = m.tableHeaders()
		if tableRenderedWidth(headers, widths) <= maxWidth {
			return m
		}
	}

	// Every droppable column is gone and JOB alone still overflows (long
	// filenames in a narrow pane). Truncate its cells: an elided filename
	// beats a table whose right-hand border falls off the pane.
	over := tableRenderedWidth(headers, widths) - maxWidth
	m.jobCellCap = max(widths["JOB"]-over, minJobCellWidth)
	return m
}

// tableHeaders returns the header row: the SEL column when a selection is
// active, then every visible column in display order.
func (m Model) tableHeaders() []string {
	var headers []string
	if len(m.Selected) > 0 {
		headers = append(headers, "SEL")
	}
	for _, colName := range m.availableColumns {
		if m.columnVisibility[colName] {
			headers = append(headers, colName)
		}
	}
	return headers
}

// measureTableColumns returns each column's rendered width: the wider of its
// header and the cells the visible rows put under it. Measured from the very
// cells renderTableView emits, so the fit decision cannot disagree with what
// lands on screen — the old estimator sized most columns by their header text
// alone, which is why a 25-column TOKENS cell read as 6.
func (m Model) measureTableColumns(headers []string) map[string]int {
	widths := make(map[string]int, len(headers))
	// Measure the header as it DRAWS, not as it is keyed: the "/" filter
	// substitutes a search field into one slot, and a long query widens that
	// column exactly like a long cell would.
	display := m.displayHeaders(headers)
	for i, h := range headers {
		widths[h] = lipgloss.Width(display[i])
	}
	visibleRows := m.getVisibleRows()
	for i := range visibleRows {
		for j, cell := range m.renderRowCells(headers, m.ScrollOffset+i, &visibleRows[i]) {
			if w := lipgloss.Width(cell); w > widths[headers[j]] {
				widths[headers[j]] = w
			}
		}
	}
	return widths
}

// tableRenderedWidth returns the width gtable.SelectableTableWithOptions
// renders for these columns. The arithmetic lives beside the renderer in core
// so every table that fits itself to a pane — this one and the plan browser —
// measures with the same formula.
func tableRenderedWidth(headers []string, widths map[string]int) int {
	return gtable.RenderedWidth(headers, widths)
}

// renderTableView renders the jobs as a table with configurable columns
func (m Model) renderTableView() string {
	t := theme.DefaultTheme

	headers := m.tableHeaders()

	var rows [][]string
	visibleRows := m.getVisibleRows()
	for i := range visibleRows {
		rows = append(rows, m.renderRowCells(headers, m.ScrollOffset+i, &visibleRows[i]))
	}

	opts := gtable.SelectableTableOptions{}
	if m.Focus == FocusJobs {
		opts.BorderColor = theme.DefaultColors.Blue
	}

	if len(rows) == 0 {
		// A filter that matches nothing still renders the table frame: the
		// header row is where the query lives, and dropping it would hide what
		// the user is typing (and that they are in search mode at all).
		if m.jobFilterVisible() && len(headers) > 0 {
			empty := make([]string, len(headers))
			// The message belongs under JOB, not in the SEL gutter column.
			msgIdx := 0
			for i, h := range headers {
				if h == "JOB" {
					msgIdx = i
					break
				}
			}
			empty[msgIdx] = t.Muted.Render("No matching jobs")
			return gtable.SelectableTableWithOptions(m.displayHeaders(headers), [][]string{empty}, -1, opts)
		}
		return "\n" + t.Muted.Render("No jobs to display.") + "\n\n" + t.Muted.Render("Press 'A' to add a job.")
	}

	return gtable.SelectableTableWithOptions(m.displayHeaders(headers), rows, m.Cursor-m.ScrollOffset, opts)
}

// renderRowCells renders one display row's cells, one per header, in header
// order. globalIndex is the row's index in DisplayRows (the tree connectors
// scan its siblings). Both the renderer and the width measurer go through
// here, so a column can never be measured as something other than what it
// draws.
func (m Model) renderRowCells(headers []string, globalIndex int, dr *DisplayRow) []string {
	t := theme.DefaultTheme
	statusStyles := getStatusStyles()
	job := dr.Job
	row := make([]string, 0, len(headers))

	// Virtual workflow rows render their tree label into the JOB
	// column and a muted kind label into TYPE (these nodes are not
	// grove-managed jobs); every other cell stays empty.
	if dr.Type != RowTypeJob {
		for _, colName := range headers {
			switch strings.ToUpper(colName) {
			case "JOB":
				row = append(row, m.renderVirtualRowCell(globalIndex, dr))
			case "TYPE":
				row = append(row, m.virtualRowTypeCell(dr))
			case "TOKENS":
				// Subagent rows surface their own cost/total; other
				// virtual rows (runs, phases, "… +K more") stay blank.
				if dr.Type == RowTypeAgent {
					row = append(row, m.renderAgentTokenCell(dr))
				} else {
					row = append(row, "")
				}
			default:
				row = append(row, "")
			}
		}
		return row
	}

	{
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
					isLast := true
					for j := globalIndex + 1; j < len(m.DisplayRows); j++ {
						// Sibling scan over job rows only — virtual
						// workflow rows interleave at deeper depths.
						if m.DisplayRows[j].Type != RowTypeJob {
							continue
						}
						jIndent := m.JobIndents[m.DisplayRows[j].Job.ID]
						if jIndent == indent {
							isLast = false
							break
						}
						if jIndent < indent {
							break
						}
					}
					if isLast {
						treePrefix += "└─ "
					} else {
						treePrefix += "├─ "
					}
				}
				statusIcon := m.jobStatusIcon(job)

				filename := job.Filename

				if job.Status == orchestration.JobStatusCompleted || job.Status == orchestration.JobStatusAbandoned {
					filename = t.Muted.Render(filename)
				}
				cell = fmt.Sprintf("%s%s %s", treePrefix, statusIcon, filename)
				if badges := m.jobBadges(job); badges != "" {
					cell += " " + badges
				}
				if m.jobCellCap > 0 {
					cell = ansi.Truncate(cell, m.jobCellCap, "…")
				}
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
			case "SKILL":
				skillName := job.Skill
				if skillName == "" {
					cell = t.Muted.Render("-")
				} else {
					if job.Status == orchestration.JobStatusCompleted || job.Status == orchestration.JobStatusAbandoned {
						cell = t.Muted.Render(skillName)
					} else {
						cell = skillName
					}
				}
			case "TYPE":
				var jobTypeSymbol string
				var typeLabel string
				var isClaw bool
				// Check if this is a claw-enabled agent
				if len(job.Channels) > 0 && job.Type == orchestration.JobTypeInteractiveAgent {
					jobTypeSymbol = theme.IconClaw
					typeLabel = "claw"
					isClaw = true
				} else if job.IsPiSessionResponded() {
					// pi-session chat: one persistent seeded Pi process owns
					// the whole dialogue; turns arrive via `flow plan say`.
					jobTypeSymbol = theme.IconRobot
					typeLabel = "pi chat"
				} else if job.IsAgentResponded() {
					// Agent-responded chat (responder: agent): response turns
					// authored by a fresh agent session per turn, never
					// dispatched to an LLM API.
					jobTypeSymbol = theme.IconRobot
					typeLabel = "agent chat"
				} else {
					switch job.Type {
					case "interactive_agent":
						jobTypeSymbol = theme.IconInteractiveAgent
					case "isolated_agent":
						jobTypeSymbol = theme.IconInteractiveAgent
					case "headless_agent":
						jobTypeSymbol = theme.IconHeadlessAgent
					case "chat":
						jobTypeSymbol = theme.IconChat
					case "oneshot":
						jobTypeSymbol = theme.IconOneshot
					case "shell":
						jobTypeSymbol = theme.IconShell
					case "file":
						jobTypeSymbol = theme.IconFile
					default:
						jobTypeSymbol = ""
					}
					typeLabel = string(job.Type)
				}
				var typeCol string
				if jobTypeSymbol != "" {
					typeCol = fmt.Sprintf("%s %s", jobTypeSymbol, typeLabel)
				} else {
					typeCol = typeLabel
				}
				if isClaw {
					typeCol = lipgloss.NewStyle().Foreground(theme.DefaultColors.Violet).Render(typeCol)
				}
				_ = isClaw
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
				if m.isInitializing(job) {
					cell = theme.DefaultTheme.Info.Render("initializing")
				} else if job.Status == orchestration.JobStatusCompleted || job.Status == orchestration.JobStatusAbandoned {
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
				cell = m.renderModelColumnCell(job)
			case "SESSION":
				cell = m.renderSessionColumnCell(job)
			case "WORKTREE":
				worktreeText := job.Worktree
				if worktreeText == "" {
					cell = t.Muted.Render("-")
				} else {
					cell = t.Muted.Render(worktreeText)
				}
			case "INLINE":
				cell = t.Muted.Render(inlineCellText(job))
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
			case "TOKENS":
				cell = m.renderTokenColumnCell(job)
			default:
				cell = t.Muted.Render("?")
			}
			row = append(row, cell)
		}
	}
	return row
}

// getJobIcon returns the icon for a job type
func getJobIcon(job *orchestration.Job) string {
	// Agent-authored chats (responder: agent / pi-session) get their own icon:
	// neither is an oracle API call, and the rail should not read as one.
	if job.IsAPIDispatchVetoed() {
		return theme.IconRobot
	}
	switch job.Type {
	case "interactive_agent":
		return theme.IconInteractiveAgent
	case "isolated_agent":
		return theme.IconInteractiveAgent // Uses same icon as interactive_agent
	case "headless_agent":
		return theme.IconHeadlessAgent
	case "chat":
		return theme.IconChat
	case "oneshot":
		return theme.IconOneshot
	case "shell":
		return theme.IconShell
	case "file":
		return theme.IconFile
	default:
		return theme.IconChat // Default fallback
	}
}

// initializingGrace bounds how long a job may render as "initializing" after
// submission. If the store status never catches up (e.g. the daemon held the
// job), the real status wins again after this window.
const initializingGrace = 2 * time.Minute

// isInitializing reports whether a job was just submitted from this TUI and
// its store status hasn't reflected the launch yet. Any status the daemon
// writes once execution actually starts (running, or a terminal state)
// supersedes the marker.
func (m Model) isInitializing(job *orchestration.Job) bool {
	submittedAt, ok := m.InitializingJobs[job.ID]
	if !ok || time.Since(submittedAt) > initializingGrace {
		return false
	}
	switch job.Status {
	case orchestration.JobStatusPending, orchestration.JobStatusBlocked,
		orchestration.JobStatusFailed, orchestration.JobStatusPendingUser,
		orchestration.JobStatusTodo:
		return true
	}
	return false
}

// jobStatusIcon returns the status icon for a job row, rendering a transient
// "initializing" indicator for jobs submitted from this TUI that haven't
// visibly started yet.
func (m Model) jobStatusIcon(job *orchestration.Job) string {
	if m.isInitializing(job) {
		return theme.DefaultTheme.Info.Render(theme.IconRunning)
	}
	return m.getStatusIcon(job.Status)
}

// getStatusIcon returns a colored dot indicator for a job status
func (m Model) getStatusIcon(status orchestration.JobStatus) string {
	statusStyles := getStatusStyles()
	var icon string
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
	case orchestration.JobStatusIdle:
		icon = theme.IconStatusPendingUser // Pause icon - waiting for user input
	case orchestration.JobStatusInterrupted, orchestration.JobStatusOrphaned:
		// Jobs that were running but whose process is gone.
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

// renderFieldEditor renders the single schema-driven config-field editor that
// replaced the three bespoke renderStatusPicker/renderTypePicker/
// renderTemplatePicker methods. It generalizes their visual idiom (bold title +
// optional "(N jobs selected)" / "for: <filename>" line + option list or text
// input + help line, boxed with a rounded border) across every enum and text
// descriptor in jobFields. Toggles never reach here — they dispatch directly.
func (m Model) renderFieldEditor() string {
	t := theme.DefaultTheme
	desc := m.fieldEditor.desc

	var lines []string

	title := lipgloss.NewStyle().
		Bold(true).
		Render("Set " + desc.Label)
	lines = append(lines, title)

	if len(m.Selected) > 0 {
		lines = append(lines, fmt.Sprintf("(%d jobs selected)", len(m.Selected)))
	} else if job := m.CurrentJob(); job != nil {
		lines = append(lines, fmt.Sprintf("for: %s", job.Filename))
	}
	lines = append(lines, "")

	switch desc.Kind {
	case fieldText:
		lines = append(lines, m.fieldEditor.input.View())
		lines = append(lines, "")
		lines = append(lines, t.Muted.Render("Enter to save • Esc to cancel"))
	default: // fieldEnum
		for i, opt := range desc.Options {
			prefix := "  "
			var style lipgloss.Style
			if i == m.fieldEditor.cursor {
				prefix = theme.IconSelect + " "
				style = lipgloss.NewStyle().
					Bold(true).
					Background(theme.DefaultColors.SubtleBackground)
			} else {
				style = t.Muted
			}
			label := opt
			if label == "" {
				label = "(none / clear)"
			}
			lines = append(lines, style.Render(fmt.Sprintf("%s%s", prefix, label)))
		}
		lines = append(lines, "")
		lines = append(lines, t.Muted.Render("↑/↓ or j/k to navigate • Enter to select • Esc/b to go back"))
	}

	content := strings.Join(lines, "\n")

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultColors.Border).
		Padding(1, 2).
		Render(content)

	return lipgloss.NewStyle().
		Margin(1, 2).
		Render(box)
}

func (m Model) renderRenameDialog() string {
	job := m.jobAtRow(m.RenameJobIndex)
	if job == nil {
		return "Error: Invalid job selected for renaming."
	}

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
	} else if m.CreateJobType == "generic" {
		jobTypeName = "Job"
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

func (m Model) renderClawTargetSelector() string {
	t := theme.DefaultTheme
	var lines []string

	if job := m.jobAtRow(m.ClawDialogJobIndex); job != nil {
		title := lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("Signal Target: %s", job.Title))
		lines = append(lines, title)
		lines = append(lines, "")
	}

	for i, opt := range m.ClawTargetOptions {
		prefix := "  "
		var style lipgloss.Style
		if i == m.ClawTargetCursor {
			prefix = theme.IconSelect + " "
			style = lipgloss.NewStyle().Bold(true).Background(theme.DefaultColors.SubtleBackground)
		} else {
			style = t.Muted
		}
		lines = append(lines, style.Render(prefix+opt))
	}

	lines = append(lines, "")
	lines = append(lines, t.Muted.Render("↑/↓ to navigate • Enter to select • Esc to cancel"))

	content := strings.Join(lines, "\n")
	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultColors.Orange).
		Padding(1, 2).
		Width(50).
		Render(content)

	return lipgloss.Place(m.Width, m.Height, lipgloss.Center, lipgloss.Center, dialog)
}

func (m Model) renderClawDialog() string {
	job := m.jobAtRow(m.ClawDialogJobIndex)
	if job == nil {
		return "Error: Invalid job selected."
	}

	var b strings.Builder

	if m.ClawDisabling {
		b.WriteString(theme.DefaultTheme.Header.Render(fmt.Sprintf("Disable Claw: %s", job.Title)))
		b.WriteString("\n\n")
		b.WriteString("This will disable Signal channels and autonomous pinging.\n\n")
		b.WriteString(theme.DefaultTheme.Muted.Render("Press Enter to confirm, Esc to cancel"))
	} else {
		b.WriteString(theme.DefaultTheme.Header.Render(fmt.Sprintf("Enable Claw: %s", job.Title)))
		b.WriteString("\n\n")

		idleLabel := "Idle minutes: "
		promptLabel := "Idle prompt:  "
		if m.ClawDialogFocus == 0 {
			idleLabel = lipgloss.NewStyle().Foreground(theme.DefaultColors.Orange).Render(idleLabel)
		}
		if m.ClawDialogFocus == 1 {
			promptLabel = lipgloss.NewStyle().Foreground(theme.DefaultColors.Orange).Render(promptLabel)
		}

		b.WriteString(idleLabel)
		b.WriteString(m.ClawIdleInput.View())
		b.WriteString("\n")
		b.WriteString(promptLabel)
		b.WriteString(m.ClawPromptInput.View())
		b.WriteString("\n\n")
		b.WriteString(theme.DefaultTheme.Muted.Render("Tab to switch fields • Enter to enable • Esc to cancel"))
	}

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultColors.Orange).
		Padding(1, 2).
		Width(60).
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
		statusIcon := m.jobStatusIcon(job)
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
		Render("Press space to toggle • Enter/Esc/T to close")
	content := lipgloss.JoinVertical(lipgloss.Left, styledView, helpText)
	return lipgloss.Place(m.Width, m.Height, lipgloss.Center, lipgloss.Center, content)
}

// renderRecipeSelector renders the UI for choosing a recipe.
func (m Model) renderRecipeSelector() string {
	listView := m.recipeList.View()
	styledView := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultTheme.Colors.Cyan).
		Padding(1, 2).
		Render(listView)
	helpText := lipgloss.NewStyle().
		Faint(true).
		Width(lipgloss.Width(styledView)).
		Align(lipgloss.Center).
		Render("\n\nPress Enter to select • Esc to cancel")
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
		{title: "Skills", properties: []string{"skill", "skill_sequence", "produces"}},
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
	keyStyle := theme.DefaultTheme.Muted.Italic(true)
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
			statusIcon, statusStyle := theme.StatusIconAndStyle(val, theme.DefaultTheme)
			if statusIcon != "" {
				icon, valueStyle, hasStyle = statusIcon+" ", statusStyle, true
			}
		case "type":
			switch val {
			case "interactive_agent":
				icon = theme.IconInteractiveAgent + " "
			case "isolated_agent":
				icon = theme.IconInteractiveAgent + " " // Uses same icon as interactive_agent
			case "headless_agent":
				icon = theme.IconHeadlessAgent + " "
			case "chat":
				icon = theme.IconChat + " "
			case "oneshot":
				icon = theme.IconOneshot + " "
			case "shell":
				icon = theme.IconShell + " "
			case "file":
				icon = theme.IconFile + " "
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
		valueStr := theme.DefaultTheme.Success.Render("*")
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

// renderStyledMarkdown delegates to the shared markdown package.
func renderStyledMarkdown(rawContent string) string {
	return markdown.Render(rawContent, theme.DefaultTheme)
}

// styleStreamingLogLine delegates to the shared markdown package.
func styleStreamingLogLine(line string, inCodeBlock *bool) string {
	return markdown.StyleStreamingLogLine(line, inCodeBlock, theme.DefaultTheme)
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
	commentStyle := dimStyle.Italic(true)

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

// renderAgentStatusBar renders a status bar showing Claude session metadata.
// Returns an empty string if there's no status to show.
// Renders without a bounding box, like Claude Code's status line.
// Format: · Calculating… (1s · ↑ 0 tokens)                            ○ idle · 58.5k tokens [▓▓▓░░░░░░░]
func (m Model) renderAgentStatusBar(width int) string {
	if m.CurrentAgentStatus == nil {
		return ""
	}

	status := m.CurrentAgentStatus

	var lines []string

	// Build left section (activity/status) and right section (state + tokens + progress bar)
	var leftSection string
	var rightSection string

	// Token progress bar constants
	const maxTokens = 185000
	const progressBarWidth = 10

	// Helper to format token count
	formatTokens := func(tokens int) string {
		if tokens >= 1000 {
			return fmt.Sprintf("%.1fk tokens", float64(tokens)/1000)
		}
		return fmt.Sprintf("%d tokens", tokens)
	}

	// Helper to render progress bar (using small squares for consistent width)
	renderProgressBar := func(current, max int) string {
		ratio := float64(current) / float64(max)
		if ratio > 1.0 {
			ratio = 1.0
		}
		if ratio < 0 {
			ratio = 0
		}
		filled := int(ratio * float64(progressBarWidth))
		empty := progressBarWidth - filled
		bar := strings.Repeat("▪", filled) + strings.Repeat("▫", empty)
		return theme.DefaultTheme.Muted.Render("[" + bar + "]")
	}

	if status.State == "running" && status.RawLine != "" {
		// When running, left section shows the raw status line from Claude
		leftSection = status.RawLine
		// Right section shows state and total tokens with progress bar
		rightParts := []string{
			theme.DefaultTheme.Info.Render("● running"),
		}
		if status.TotalTokens > 0 {
			rightParts = append(rightParts, theme.DefaultTheme.Muted.Render(formatTokens(status.TotalTokens)))
			rightParts = append(rightParts, renderProgressBar(status.TotalTokens, maxTokens))
		}
		rightSection = strings.Join(rightParts, " · ")
	} else if status.State == "idle" {
		// When idle, left section is empty or minimal
		leftSection = ""
		// Right section shows idle state, total tokens, and progress bar
		rightParts := []string{
			theme.DefaultTheme.Muted.Render("○ idle"),
		}
		if status.TotalTokens > 0 {
			rightParts = append(rightParts, theme.DefaultTheme.Muted.Render(formatTokens(status.TotalTokens)))
			rightParts = append(rightParts, renderProgressBar(status.TotalTokens, maxTokens))
		}
		rightSection = strings.Join(rightParts, " · ")
	} else {
		// Fallback
		leftSection = theme.DefaultTheme.Muted.Render("...")
		rightSection = ""
	}

	// Calculate widths for proper alignment
	leftWidth := lipgloss.Width(leftSection)
	rightWidth := lipgloss.Width(rightSection)
	availableWidth := width - 4 // Account for padding

	// Build the status line with right-justified right section
	if leftSection != "" && rightSection != "" {
		// Both sections present
		gap := availableWidth - leftWidth - rightWidth
		if gap < 1 {
			gap = 1
		}
		statusLine := leftSection + strings.Repeat(" ", gap) + rightSection
		lines = append(lines, statusLine)
	} else if rightSection != "" {
		// Only right section (e.g., idle state)
		gap := availableWidth - rightWidth
		if gap < 0 {
			gap = 0
		}
		statusLine := strings.Repeat(" ", gap) + rightSection
		lines = append(lines, statusLine)
	} else {
		// Only left section
		lines = append(lines, leftSection)
	}

	// Add todo items if present
	if len(status.TodoItems) > 0 {
		for _, todo := range status.TodoItems {
			var icon string
			var style lipgloss.Style
			if todo.Completed {
				icon = theme.IconStatusCompleted
				style = theme.DefaultTheme.Success
			} else {
				icon = theme.IconStatusTodo
				style = theme.DefaultTheme.Muted
			}
			todoLine := fmt.Sprintf("  %s %s", style.Render(icon), todo.Text)
			lines = append(lines, todoLine)
		}
	}

	content := strings.Join(lines, "\n")

	// No bounding box - just return the content with minimal left padding to align with chat input
	return lipgloss.NewStyle().PaddingLeft(2).Render(content)
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
