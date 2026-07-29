package browser

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	fit "github.com/grovetools/core/panelkit/table"
	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/tui/components"
	"github.com/grovetools/core/tui/components/table"
	"github.com/grovetools/core/tui/theme"
)

// View renders the current state of the browser Model.
func (m Model) View() string {
	// View is the first point at which a projected snapshot is actually handed
	// to Bubble Tea's renderer. The once-guard keeps steady-state rendering free
	// of logging overhead while preserving end-to-end snapshot latency.
	m.renderProbe.record()
	padStyle := lipgloss.NewStyle().PaddingLeft(1).PaddingTop(1)
	if m.width > 0 {
		padStyle = padStyle.MaxWidth(m.width)
	}

	// Only show the placeholder before the first load of the current
	// workspace context. Background refreshes (loading=true after
	// initialLoaded) keep the existing list rendered to avoid flicker.
	if m.loading && !m.initialLoaded {
		return padStyle.Render("Loading plans...\n")
	}

	if m.err != nil {
		return padStyle.Render(theme.DefaultTheme.Error.Render(fmt.Sprintf("Error: %v\n", m.err)))
	}

	var s strings.Builder

	if m.help.ShowAll {
		return padStyle.Render(m.help.View())
	}

	if m.columnSelectMode {
		// The picker places itself in the pane; padding it would push the
		// centred box off the right edge.
		return m.renderColumnSelect()
	}

	if m.bulkConfirming {
		return padStyle.Render(m.renderBulkConfirm())
	}

	if m.editingNotes {
		s.WriteString(components.RenderHeader("Edit Plan Notes"))
		s.WriteString("\n\n")
		if m.editPlanIndex >= 0 && m.editPlanIndex < len(m.plans) {
			s.WriteString(theme.DefaultTheme.Muted.Render("Plan: "))
			s.WriteString(m.plans[m.editPlanIndex].Name)
			s.WriteString("\n\n")
		}
		s.WriteString(m.notesInput.View())
		s.WriteString("\n\n")
		s.WriteString(theme.DefaultTheme.Muted.Render("Press Enter to save, Esc to cancel"))
		return padStyle.Render(s.String())
	}

	if len(m.plans) == 0 {
		s.WriteString("No plans found in directory.\n")
		if !m.embedMode {
			s.WriteString("\n")
			s.WriteString(m.help.View())
		}
		return padStyle.Render(s.String())
	}

	tableStr := m.renderPlanTable()

	if m.showGitLog {
		var detailPane string
		var detailTitle string

		if m.cursor >= 0 && m.cursor < len(m.plans) {
			selectedPlan := m.plans[m.cursor]
			if len(selectedPlan.EcosystemRepoStatuses) > 0 {
				detailTitle = "Ecosystem Repository Status"
				detailPane = m.renderEcosystemStatusPane()

				if m.inRepoNavigationMode {
					var repoLogPane string
					if m.repoGitLogError != nil {
						repoLogPane = theme.DefaultTheme.Error.Render(m.repoGitLogError.Error())
					} else {
						repoLogPane = m.repoGitLogContent
					}

					const gitLogHeight = 10
					lines := strings.Split(repoLogPane, "\n")
					if len(lines) > gitLogHeight {
						lines = lines[:gitLogHeight]
					}
					for len(lines) < gitLogHeight {
						lines = append(lines, "")
					}
					repoLogPane = strings.Join(lines, "\n")

					boxStyle := theme.DefaultTheme.Box.Padding(0, 1).MarginLeft(4).MarginTop(-1).Width(60)
					repoLogPaneStyled := boxStyle.Render(repoLogPane)

					var repoName string
					if m.repoCursor >= 0 && m.repoCursor < len(selectedPlan.EcosystemRepoStatuses) {
						repoName = selectedPlan.EcosystemRepoStatuses[m.repoCursor].Name
					}

					ecosystemTitle := lipgloss.NewStyle().MarginLeft(2).Render(theme.DefaultTheme.Bold.Render(detailTitle))
					gitLogTitle := lipgloss.NewStyle().MarginLeft(4).Render(theme.DefaultTheme.Bold.Render(fmt.Sprintf("Git Log - %s", repoName)))

					leftColumn := lipgloss.JoinVertical(lipgloss.Left, ecosystemTitle, detailPane)
					rightColumn := lipgloss.JoinVertical(lipgloss.Left, gitLogTitle, repoLogPaneStyled)
					detailsView := lipgloss.JoinHorizontal(lipgloss.Top, leftColumn, rightColumn)

					mainContent := lipgloss.JoinVertical(lipgloss.Left, tableStr, detailsView)
					s.WriteString(mainContent)
					if !m.embedMode {
						s.WriteString("\n")
						s.WriteString(m.footerLine())
					}
					return padStyle.Render(s.String())
				}
			} else {
				detailTitle = theme.IconGit + " Git Repository Log"
				detailPane = m.renderGitLogPane()
			}
		} else {
			detailTitle = theme.IconGit + " Git Repository Log"
			detailPane = m.renderGitLogPane()
		}

		styledDetailTitle := lipgloss.NewStyle().MarginLeft(2).Render(theme.DefaultTheme.Bold.Render(detailTitle))
		mainContent := lipgloss.JoinVertical(lipgloss.Left, tableStr, styledDetailTitle, detailPane)
		s.WriteString(mainContent)
	} else {
		s.WriteString(tableStr)
	}

	if !m.embedMode {
		s.WriteString("\n")
		s.WriteString(m.footerLine())
	}

	// Composite the bottom-anchored which-key popup onto the finished frame.
	// When embedded the frame is a content block rather than the whole
	// viewport, so the vertical budget is passed explicitly — clamping to the
	// frame height alone would truncate a namespace that has room on screen.
	frame := padStyle.Render(s.String())
	return m.whichKey.RenderOverlayAvail(frame, lipgloss.Width(frame), m.height, *theme.DefaultTheme)
}

// footerLine builds the help + status-message line rendered at the
// bottom of the view (standalone mode) or returned via Footer() for
// pager-pinned rendering (embed mode).
func (m Model) footerLine() string {
	var b strings.Builder
	b.WriteString(m.help.View())
	if m.dataSource != "" {
		b.WriteString("\n")
		b.WriteString(theme.DefaultTheme.Muted.Render("Plan data: " + m.dataSource))
	}
	if m.statusMessage != "" {
		b.WriteString("\n\n")
		b.WriteString(theme.DefaultTheme.Success.Render(m.statusMessage))
	}
	return b.String()
}

// Footer returns the help + status line for use as a pinned pager
// footer. Only meaningful when EmbedMode is true.
func (m Model) Footer() string {
	return m.footerLine()
}

// renderGitLogPane renders the top-level workspace git log pane with a
// fixed height so the layout doesn't jitter as commits scroll.
func (m Model) renderGitLogPane() string {
	var content string
	if m.gitLogError != nil {
		content = theme.DefaultTheme.Error.Render(m.gitLogError.Error())
	} else {
		content = m.gitLogContent
	}

	const gitLogHeight = 10
	lines := strings.Split(content, "\n")
	if len(lines) > gitLogHeight {
		lines = lines[:gitLogHeight]
	}
	for len(lines) < gitLogHeight {
		lines = append(lines, "")
	}
	content = strings.Join(lines, "\n")

	boxStyle := theme.DefaultTheme.Box.Padding(0, 1).MarginLeft(2).Width(60)
	return boxStyle.Render(content)
}

// renderEcosystemStatusPane renders the detailed per-repo status table
// for an ecosystem plan that is currently highlighted.
func (m Model) renderEcosystemStatusPane() string {
	if m.cursor < 0 || m.cursor >= len(m.plans) {
		return ""
	}
	selectedPlan := m.plans[m.cursor]
	if len(selectedPlan.EcosystemRepoStatuses) == 0 {
		return "No ecosystem repository details to display."
	}

	headers := []string{"REPO", "GIT STATUS", "MERGE STATUS"}
	var rows [][]string

	for _, repoStatus := range selectedPlan.EcosystemRepoStatuses {
		var gitText string
		if repoStatus.GitStatus != nil {
			gs := repoStatus.GitStatus
			var parts []string
			if gs.IsDirty {
				parts = append(parts, theme.DefaultTheme.Warning.Render(theme.IconWarning+" Dirty"))
			} else {
				parts = append(parts, theme.DefaultTheme.Success.Render(theme.IconSuccess+" Clean"))
			}
			if gs.AheadCount > 0 {
				parts = append(parts, theme.DefaultTheme.Success.Render(fmt.Sprintf("%s%d", theme.IconArrowUp, gs.AheadCount)))
			}
			if gs.BehindCount > 0 {
				parts = append(parts, theme.DefaultTheme.Error.Render(fmt.Sprintf("%s%d", theme.IconArrowDown, gs.BehindCount)))
			}
			gitText = strings.Join(parts, " ")
		} else {
			gitText = theme.DefaultTheme.Muted.Render("-")
		}

		var mergeText string
		switch repoStatus.MergeStatus {
		case "Ready":
			mergeText = theme.DefaultTheme.Success.Render(theme.IconSuccess + " Ready")
		case "Needs Rebase":
			mergeText = theme.DefaultTheme.Warning.Render(theme.IconWarning + " Needs Rebase")
		case "Behind":
			mergeText = theme.DefaultTheme.Info.Render(theme.IconInfo + " Behind")
		case "Conflicts":
			mergeText = theme.DefaultTheme.Error.Render(theme.IconError + " Conflicts")
		case "Merged", "Synced":
			mergeText = theme.DefaultTheme.Muted.Render(theme.IconMerge + " Synced")
		default:
			mergeText = theme.DefaultTheme.Muted.Render(repoStatus.MergeStatus)
		}

		rows = append(rows, []string{repoStatus.Name, gitText, mergeText})
	}

	var tableOutput string
	if m.inRepoNavigationMode {
		tableOutput = table.SelectableTable(headers, rows, m.repoCursor)
	} else {
		tableOutput = table.SimpleTable(headers, rows)
	}

	return lipgloss.NewStyle().MarginLeft(0).Render(tableOutput)
}

// browserColumns is the plan table's display order. PLAN carries the row's
// identity and is never dropped or hidden; every other column is optional.
var browserColumns = []string{"PLAN", "STATUS", "WORKSPACE / REPOS", "WORKTREE", "BINDING", "GIT", "REVIEWED", "NOTES", "UPDATED"}

// browserOptionalColumns are the columns the "T" picker can toggle, in the
// order they appear in the table.
var browserOptionalColumns = browserColumns[1:]

func defaultBrowserColumnVisibility() map[string]bool {
	return map[string]bool{
		"STATUS": true, "WORKSPACE / REPOS": false, "WORKTREE": true, "BINDING": true,
		"GIT": true, "REVIEWED": true, "NOTES": true, "UPDATED": true,
	}
}

func (m Model) columnVisible(name string) bool {
	visible, exists := m.columnVisibility[name]
	return !exists || visible
}

// renderColumnSelect draws the column picker in the same idiom as the Jobs
// tab's: checkboxes in a rounded box with a centred help line, so the two
// tables' column management doesn't read as two different applications.
func (m Model) renderColumnSelect() string {
	t := theme.DefaultTheme
	lines := []string{t.Bold.Render("Toggle Column Visibility"), ""}
	for i, name := range browserOptionalColumns {
		checkbox := t.Muted.Render("[ ]")
		if m.columnVisible(name) {
			checkbox = t.Success.Render("[x]")
		}
		row := fmt.Sprintf("%s %s", checkbox, name)
		if i == m.columnCursor {
			row = lipgloss.NewStyle().Foreground(theme.DefaultColors.Orange).Render("│ " + row)
		} else {
			row = "  " + row
		}
		lines = append(lines, row)
	}

	// The box is at least as wide as its own help line: a box narrower than
	// the help wraps "T/esc close" onto a second, off-centre row.
	const help = "↑/↓ move • space/enter toggle • T/esc close"
	boxWidth := lipgloss.Width(help)
	for _, line := range lines {
		if w := lipgloss.Width(line) + 4; w > boxWidth { // 4 = horizontal padding
			boxWidth = w
		}
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultColors.Cyan).
		Padding(1, 2).
		Width(boxWidth).
		Render(strings.Join(lines, "\n"))
	helpText := lipgloss.NewStyle().
		Faint(true).
		Width(lipgloss.Width(box)).
		Align(lipgloss.Center).
		Render(help)
	content := lipgloss.JoinVertical(lipgloss.Left, box, helpText)

	if m.width > 0 && m.height > 0 {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
	}
	return content
}

// bulkSkipDisplayLimit caps the skipped section so a large portfolio cannot
// push the confirmation prompt itself off the screen.
const bulkSkipDisplayLimit = 10

// renderBulkConfirm renders the fast-forward-all confirmation: what will be
// rebased, and — equally important — what will not be, with the preflight
// reason for every refusal.
func (m Model) renderBulkConfirm() string {
	var b strings.Builder
	b.WriteString(components.RenderHeader("Fast-forward Plans from main"))
	b.WriteString("\n\n")

	if len(m.bulkCandidates) == 0 {
		b.WriteString(theme.DefaultTheme.Warning.Render("No plan is eligible: every plan is dirty, conflicting, unbound, or already up to date."))
	} else {
		b.WriteString(theme.DefaultTheme.Bold.Render(fmt.Sprintf("Rebase %s onto main across %s:",
			pluralize(len(m.bulkCandidates), "plan"), pluralize(bulkCandidateRepoTotal(m.bulkCandidates), "repo"))))
		b.WriteString("\n")
		for _, candidate := range m.bulkCandidates {
			b.WriteString(fmt.Sprintf("  %s %s %s\n",
				theme.DefaultTheme.Success.Render(theme.IconArrowDown),
				candidate.name,
				theme.DefaultTheme.Muted.Render("("+pluralize(candidate.repos, "repo")+")")))
		}
	}

	if len(m.bulkSkipped) > 0 {
		b.WriteString("\n")
		b.WriteString(theme.DefaultTheme.Muted.Render(fmt.Sprintf("Skipping %s:", pluralize(len(m.bulkSkipped), "plan"))))
		b.WriteString("\n")
		shown := m.bulkSkipped
		if len(shown) > bulkSkipDisplayLimit {
			shown = shown[:bulkSkipDisplayLimit]
		}
		for _, skip := range shown {
			b.WriteString(theme.DefaultTheme.Muted.Render(fmt.Sprintf("  · %s — %s\n", skip.name, skip.reason)))
		}
		if remaining := len(m.bulkSkipped) - len(shown); remaining > 0 {
			b.WriteString(theme.DefaultTheme.Muted.Render(fmt.Sprintf("  · +%d more\n", remaining)))
		}
	}

	b.WriteString("\n")
	if len(m.bulkCandidates) == 0 {
		b.WriteString(theme.DefaultTheme.Muted.Render("esc close"))
		return b.String()
	}
	b.WriteString(theme.DefaultTheme.Muted.Render("y/enter fast-forward  •  n/esc cancel"))
	return b.String()
}

// planTableHeaders returns the visible columns in display order. WORKTREE
// carries information only when it differs from the plan slug, so it is gated
// on more than visibility.
func (m Model) planTableHeaders() []string {
	headers := make([]string, 0, len(browserColumns))
	for _, name := range browserColumns {
		if name != "PLAN" && !m.columnVisible(name) {
			continue
		}
		if name == "WORKTREE" && !m.hasDistinctWorktree() {
			continue
		}
		headers = append(headers, name)
	}
	return headers
}

// visibleRowRange returns the half-open slice of m.plans the viewport shows.
func (m Model) visibleRowRange() (int, int) {
	start := m.scrollOffset
	if start < 0 {
		start = 0
	}
	if start >= len(m.plans) {
		start = len(m.plans) - 1
	}
	end := start + m.visibleRowCount()
	if end > len(m.plans) {
		end = len(m.plans)
	}
	return start, end
}

// minPlanCellWidth floors the truncated PLAN column: narrower than this a plan
// slug identifies nothing, and clipping would be no worse.
const minPlanCellWidth = 12

// browserColumnDropPriority ranks the columns for the fitting pass: the lowest
// rank is dropped first when the table doesn't fit a narrow pane. PLAN is
// absent because it is the identity column — never dropped, truncated instead.
//
// A column missing from this map gets priority 0 and goes first, so a column
// added to browserColumns without being ranked here can never silently become
// undroppable.
//
// WORKSPACE / REPOS leads because it is both the widest and off by default;
// STATUS goes last because it is the reason most people open this table.
var browserColumnDropPriority = map[string]int{
	"WORKSPACE / REPOS": 1, "UPDATED": 2, "NOTES": 3, "REVIEWED": 4,
	"WORKTREE": 5, "BINDING": 6, "GIT": 7, "STATUS": 8,
}

// browserFitColumns describes the given headers to the shared fitting widget,
// in display order.
func browserFitColumns(headers []string) []fit.Column {
	cols := make([]fit.Column, 0, len(headers))
	for _, name := range headers {
		col := fit.Column{Name: name, Priority: browserColumnDropPriority[name]}
		if name == "PLAN" {
			col.Identity = true
			col.MinWidth = minPlanCellWidth
		}
		cols = append(cols, col)
	}
	return cols
}

// browserColumnDropOrder returns every browser column in the order the fitting
// pass gives it up, most expendable first.
func browserColumnDropOrder() []string {
	return fit.DropOrder(browserFitColumns(browserColumns))
}

// fitToWidth returns a copy of the model whose plan table renders no wider
// than maxWidth: low-priority columns drop out first, and if PLAN alone still
// overflows, its cells are truncated. The table's right-hand border always
// lands inside the pane.
//
// The drop-and-truncate pass is core/panelkit/table, shared with the Jobs tab
// which used to hold a near-verbatim copy of it. Measuring stays here: a
// plan's cells share per-row work, so measuring them one column at a time
// would redo it once per column. The invariant that matters — measure from the
// cells the renderer actually emits — is preserved by measureTableColumns
// going through renderRowCells.
func (m Model) fitToWidth(maxWidth int) Model {
	headers := m.planTableHeaders()
	if maxWidth <= 0 || len(headers) == 0 {
		return m
	}
	// A column's width doesn't depend on which other columns are visible, so
	// one measuring pass serves the whole drop loop.
	widths := m.measureTableColumns(headers)
	layout := fit.Fit(browserFitColumns(headers), widths, maxWidth, fit.SelectableChrome)
	if len(layout.Dropped) == 0 && layout.IdentityCap == 0 {
		return m // Everything fits.
	}

	if len(layout.Dropped) > 0 {
		// Copy the visibility map: a column dropped to fit the pane is a
		// rendering decision, and must never be written back to the user's
		// preferences.
		vis := make(map[string]bool, len(m.columnVisibility))
		for k, v := range m.columnVisibility {
			vis[k] = v
		}
		for _, col := range layout.Dropped {
			vis[col] = false
		}
		m.columnVisibility = vis
	}
	m.planCellCap = layout.IdentityCap
	return m
}

// measureTableColumns returns each column's rendered width: the wider of its
// header and the cells the visible rows put under it. Measured from the very
// cells renderPlanTable emits, so the fit decision cannot disagree with what
// lands on screen.
func (m Model) measureTableColumns(headers []string) map[string]int {
	widths := make(map[string]int, len(headers))
	for _, h := range headers {
		widths[h] = lipgloss.Width(h)
	}
	start, end := m.visibleRowRange()
	for i := start; i < end; i++ {
		for j, cell := range m.renderRowCells(headers, m.plans[i]) {
			if w := lipgloss.Width(cell); w > widths[headers[j]] {
				widths[headers[j]] = w
			}
		}
	}
	return widths
}

// renderRowCells renders one plan's cells, one per header, in header order.
// Both the renderer and the width measurer go through here, so a column can
// never be measured as something other than what it draws.
func (m Model) renderRowCells(headers []string, plan PlanListItem) []string {
	t := theme.DefaultTheme

	titleText := plan.Name
	if plan.Archived {
		// Archived rows render dimmed and never get the active-plan
		// or rolling-plan decorations.
		titleText = t.Muted.Render(titleText)
	} else {
		if plan.Name == RollingPlanName {
			titleText = t.Muted.Render("(rolling)")
		}
		if plan.Name == m.activePlan {
			titleText = t.Bold.Render(fmt.Sprintf("%s %s", theme.IconSelect, titleText))
		}
	}
	if m.planCellCap > 0 {
		titleText = ansi.Truncate(titleText, m.planCellCap, "…")
	}

	worktreeText := plan.Worktree
	if worktreeText == "" {
		worktreeText = t.Muted.Render("-")
	} else {
		worktreeText = theme.IconGitBranch + " " + worktreeText
	}

	bindingText := string(plan.Binding.Health)
	if plan.Name == RollingPlanName && !plan.Archived {
		bindingText = t.Muted.Render("main workspace")
	} else if bindingText == "" {
		bindingText = string(coreplan.BindingUnbound)
	}
	if plan.Binding.Valid() {
		bindingText = t.Success.Render(theme.IconSuccess + " bound")
	} else if plan.Binding.Health == coreplan.BindingArchived || plan.Binding.Health == coreplan.BindingUnbound {
		bindingText = t.Muted.Render(bindingText)
	} else {
		bindingText = t.Error.Render(theme.IconWarning + " " + bindingText)
	}

	var gitText string
	if plan.GitStatus != nil {
		gs := plan.GitStatus
		var parts []string
		if gs.IsDirty {
			parts = append(parts, t.Warning.Render(theme.IconWarning+" Dirty"))
		} else {
			parts = append(parts, t.Success.Render(theme.IconSuccess+" Clean"))
		}
		if gs.AheadCount > 0 {
			parts = append(parts, t.Success.Render(fmt.Sprintf("%s%d", theme.IconArrowUp, gs.AheadCount)))
		}
		if gs.BehindCount > 0 {
			parts = append(parts, t.Error.Render(fmt.Sprintf("%s%d", theme.IconArrowDown, gs.BehindCount)))
		}
		gitText = strings.Join(parts, " ")
	} else {
		gitText = t.Muted.Render("-")
	}

	notesText := fmt.Sprintf("%d", plan.NoteCount)
	if plan.NoteCount == 0 {
		notesText = t.Muted.Render("0")
	}

	// Lifecycle states get the same icons the Jobs tab gives the equivalent
	// job states; a bare tick for one state and bare words for the rest was
	// the other half of the "inconsistent icons" complaint.
	var reviewedText string
	switch plan.ReviewStatus {
	case "Review":
		reviewedText = t.Info.Render(theme.IconStatusNeedsReview) + " Review"
	case "Hold":
		reviewedText = t.Warning.Render(theme.IconStatusHold) + " Hold"
	case "Finished":
		reviewedText = t.Success.Render(theme.IconStatusCompleted) + " Finished"
	case "Archived":
		reviewedText = t.Muted.Render(theme.IconStatusAbandoned + " Archived")
	default:
		reviewedText = t.Muted.Render("-")
	}
	if hold, pending := m.holdPending[planItemKey(plan)]; pending {
		if hold {
			reviewedText = t.Warning.Render("Holding…")
		} else {
			reviewedText = t.Warning.Render("Unholding…")
		}
	}

	identityText := formatPlanIdentity(plan.Workspace, len(plan.Repositories))
	if identityText == "" {
		identityText = t.Muted.Render("-")
	}

	cells := map[string]string{
		"PLAN": titleText, "STATUS": m.formatStatusCell(plan),
		"WORKSPACE / REPOS": identityText, "WORKTREE": worktreeText,
		"BINDING": bindingText, "GIT": gitText, "REVIEWED": reviewedText,
		"NOTES": notesText, "UPDATED": t.Muted.Render("◦ " + formatRelativeTime(plan.LastUpdated)),
	}
	row := make([]string, len(headers))
	for i, name := range headers {
		row[i] = cells[name]
	}
	return row
}

func (m Model) renderPlanTable() string {
	if len(m.plans) == 0 {
		return ""
	}
	// The view pads the table by one column on the left; the table has to fit
	// in what's left of the pane.
	m = m.fitToWidth(m.width - 1)

	headers := m.planTableHeaders()
	start, end := m.visibleRowRange()
	rows := make([][]string, end-start)
	for i := start; i < end; i++ {
		rows[i-start] = m.renderRowCells(headers, m.plans[i])
	}

	// Focus tints the border, the same signal the Jobs table gives.
	opts := table.SelectableTableOptions{}
	if m.focused {
		opts.BorderColor = theme.DefaultColors.Blue
	}
	tableView := table.SelectableTableWithOptions(headers, rows, m.cursor-start, opts)
	rangeText := theme.DefaultTheme.Muted.Render(fmt.Sprintf("%d–%d of %d", start+1, end, len(m.plans)))
	return lipgloss.JoinVertical(lipgloss.Left, tableView, rangeText)
}

// hasDistinctWorktree reports whether the column carries information beyond
// the PLAN column. Normal plans use the same slug for both and rolling has no
// worktree, so the column appears only for a shared/reassigned worktree.
func (m Model) hasDistinctWorktree() bool {
	for _, plan := range m.plans {
		if plan.Worktree != "" && plan.Worktree != plan.Name {
			return true
		}
	}
	return false
}

func formatPlanIdentity(workspaceName string, repositoryCount int) string {
	if repositoryCount == 0 {
		return workspaceName
	}
	label := "repos"
	if repositoryCount == 1 {
		label = "repo"
	}
	if workspaceName == "" {
		return fmt.Sprintf("%d %s", repositoryCount, label)
	}
	return fmt.Sprintf("%s / %d %s", workspaceName, repositoryCount, label)
}

// planStatusKinds is the order the STATUS column lists job states in. It
// matches the aggregation in normalizedJobCounts: "pending" already folds in
// pending_user and todo.
var planStatusKinds = []string{"completed", "running", "pending", "failed", "blocked", "hold", "abandoned"}

// planStatusIconAndStyle returns the icon and colour the Jobs tab draws for a
// job in this state, so a plan row uses the same vocabulary as the per-job
// table it summarizes. Mirrors status.Model.getStatusIcon; "pending" covers
// the pending/pending_user/todo bucket that tab's default branch handles.
func planStatusIconAndStyle(kind string) (string, lipgloss.Style) {
	t := theme.DefaultTheme
	switch kind {
	case "completed":
		return theme.IconStatusCompleted, t.Success
	case "running":
		return theme.IconStatusRunning, t.Info
	case "failed":
		return theme.IconStatusFailed, t.Error
	case "blocked":
		return theme.IconStatusBlocked, t.Error
	case "hold":
		return theme.IconStatusHold, t.Warning
	case "abandoned":
		return theme.IconStatusAbandoned, t.Muted
	default:
		return theme.IconStatusPendingUser, t.Muted
	}
}

// formatStatusCell produces the STATUS cell of the plan list: one
// icon-prefixed count per job state present. Only the icons carry colour — a
// whole cell tinted by whichever state happened to win read as an alarm on
// every row, and left no colour budget for the states that matter.
func (m Model) formatStatusCell(plan PlanListItem) string {
	var parts []string
	for _, kind := range planStatusKinds {
		count := plan.StatusParts[kind]
		if count == 0 {
			continue
		}
		icon, style := planStatusIconAndStyle(kind)
		label := kind
		if kind == "hold" {
			label = "on hold"
		}
		parts = append(parts, fmt.Sprintf("%s %d %s", style.Render(icon), count, label))
	}
	if len(parts) == 0 {
		icon, style := planStatusIconAndStyle("pending")
		return style.Render(icon) + " " + theme.DefaultTheme.Muted.Render("no jobs")
	}
	return strings.Join(parts, "  ")
}

// formatRelativeTime renders a time.Time as a relative string like
// "2 hours ago" for use in the UPDATED column.
func formatRelativeTime(t time.Time) string {
	now := time.Now()
	diff := now.Sub(t)

	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		minutes := int(diff.Minutes())
		if minutes == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", minutes)
	case diff < 24*time.Hour:
		hours := int(diff.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	case diff < 7*24*time.Hour:
		days := int(diff.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	case diff < 30*24*time.Hour:
		weeks := int(diff.Hours() / (24 * 7))
		if weeks == 1 {
			return "1 week ago"
		}
		return fmt.Sprintf("%d weeks ago", weeks)
	default:
		months := int(diff.Hours() / (24 * 30))
		if months == 1 {
			return "1 month ago"
		}
		return fmt.Sprintf("%d months ago", months)
	}
}
