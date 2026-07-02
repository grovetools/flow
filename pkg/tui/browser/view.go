package browser

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/grovetools/core/tui/components"
	"github.com/grovetools/core/tui/components/table"
	"github.com/grovetools/core/tui/theme"
)

// View renders the current state of the browser Model.
func (m Model) View() string {
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

	return padStyle.Render(s.String())
}

// footerLine builds the help + status-message line rendered at the
// bottom of the view (standalone mode) or returned via Footer() for
// pager-pinned rendering (embed mode).
func (m Model) footerLine() string {
	var b strings.Builder
	b.WriteString(m.help.View())
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

// renderPlanTable renders the main plan list table with the cursor
// highlighted on the current selection.
func (m Model) renderPlanTable() string {
	if len(m.plans) == 0 {
		return ""
	}

	headers := []string{"PLAN", "STATUS", "WORKTREE", "GIT", "MERGE", "REVIEWED", "NOTES", "UPDATED"}
	rows := make([][]string, len(m.plans))
	for i, plan := range m.plans {
		statusText := m.formatStatusWithEmoji(plan)
		updatedText := theme.DefaultTheme.Muted.Render("◦ " + formatRelativeTime(plan.LastUpdated))

		titleText := plan.Name
		if plan.Archived {
			// Archived rows render dimmed and never get the active-plan
			// or rolling-plan decorations.
			titleText = theme.DefaultTheme.Muted.Render(titleText)
		} else {
			if plan.Name == RollingPlanName {
				titleText = theme.DefaultTheme.Muted.Render("(rolling)")
			}
			if plan.Name == m.activePlan {
				titleText = theme.DefaultTheme.Bold.Render(fmt.Sprintf("%s %s", theme.IconSelect, titleText))
			}
		}

		worktreeText := plan.Worktree
		if worktreeText == "" {
			worktreeText = theme.DefaultTheme.Muted.Render("-")
		} else {
			worktreeText = theme.IconGitBranch + " " + worktreeText
		}

		var gitText string
		if plan.GitStatus != nil {
			gs := plan.GitStatus
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

		notesText := plan.Notes
		if notesText == "" {
			notesText = theme.DefaultTheme.Muted.Render("-")
		} else if len(notesText) > 30 {
			notesText = notesText[:27] + "..."
		}

		// The MERGE cell text is plan.MergeStatus — for ecosystem plans this is
		// the abbreviated count+icon rollup (icons already baked in); for
		// single-repo plans it is the bare status label. MergeVerdict (the
		// worst-case status across the group, or the status itself for single
		// repos) drives the color so the whole cell reads at a glance.
		var mergeText string
		switch plan.MergeVerdict {
		case "Ready":
			mergeText = theme.DefaultTheme.Success.Render(plan.MergeStatus)
		case "Needs Rebase", "Diverged":
			mergeText = theme.DefaultTheme.Warning.Render(plan.MergeStatus)
		case "Behind":
			mergeText = theme.DefaultTheme.Info.Render(plan.MergeStatus)
		case "Conflicts":
			mergeText = theme.DefaultTheme.Error.Render(plan.MergeStatus)
		case "Merged", "Synced":
			mergeText = theme.DefaultTheme.Muted.Render(theme.IconMerge + " Synced")
		default:
			mergeText = theme.DefaultTheme.Muted.Render(plan.MergeStatus)
		}

		var reviewedText string
		switch plan.ReviewStatus {
		case "Review":
			reviewedText = theme.DefaultTheme.Success.Render(theme.IconSuccess)
		case "Hold":
			reviewedText = theme.DefaultTheme.Warning.Render("Hold")
		case "Finished":
			reviewedText = theme.DefaultTheme.Success.Render("Finished")
		case "Archived":
			reviewedText = theme.DefaultTheme.Muted.Render("Archived")
		default:
			reviewedText = theme.DefaultTheme.Muted.Render("-")
		}

		rows[i] = []string{
			titleText,
			statusText,
			worktreeText,
			gitText,
			mergeText,
			reviewedText,
			notesText,
			updatedText,
		}
	}

	return table.SelectableTable(headers, rows, m.cursor)
}

// formatStatusWithEmoji produces the emoji-decorated status string shown
// in the STATUS column of the plan list.
func (m Model) formatStatusWithEmoji(plan PlanListItem) string {
	if len(plan.StatusParts) == 0 {
		return theme.IconPending + " no jobs"
	}

	completed := plan.StatusParts["completed"]
	running := plan.StatusParts["running"]
	pending := plan.StatusParts["pending"]
	failed := plan.StatusParts["failed"]
	blocked := plan.StatusParts["blocked"]
	hold := plan.StatusParts["hold"]
	abandoned := plan.StatusParts["abandoned"]

	var emoji string
	switch {
	case failed > 0 || blocked > 0 || abandoned > 0:
		emoji = theme.IconError
	case running > 0:
		emoji = theme.IconRunning
	case hold > 0:
		emoji = theme.IconStatusHold
	case completed > 0 && (pending > 0 || running > 0):
		emoji = theme.IconRunning
	case completed > 0:
		emoji = theme.IconSuccess
	default:
		emoji = theme.IconPending
	}

	var parts []string
	if completed > 0 {
		parts = append(parts, fmt.Sprintf("%d completed", completed))
	}
	if running > 0 {
		parts = append(parts, fmt.Sprintf("%d running", running))
	}
	if pending > 0 {
		parts = append(parts, fmt.Sprintf("%d pending", pending))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	if blocked > 0 {
		parts = append(parts, fmt.Sprintf("%d blocked", blocked))
	}
	if hold > 0 {
		parts = append(parts, fmt.Sprintf("%d on hold", hold))
	}

	statusText := emoji + " " + strings.Join(parts, ", ")

	switch {
	case failed > 0 || blocked > 0:
		return theme.DefaultTheme.Error.Render(statusText)
	case running > 0:
		return theme.DefaultTheme.Warning.Render(statusText)
	case completed > 0:
		return theme.DefaultTheme.Success.Render(statusText)
	default:
		return theme.DefaultTheme.Info.Render(statusText)
	}
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
