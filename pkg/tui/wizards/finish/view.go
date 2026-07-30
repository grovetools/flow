package finish

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/grovetools/core/tui/theme"
)

// View renders the wizard: header, branch status banner, selectable
// item list, optional inline repo-status details, and a footer hint.
func (m Model) View() string {
	// Show help if active.
	if m.helpModel.ShowAll {
		out := lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.NewStyle().Padding(1).Render("🏁 Plan Finish - Help"),
			m.helpModel.View(),
		)
		if m.width > 0 {
			out = lipgloss.NewStyle().MaxWidth(m.width).Render(out)
		}
		return out
	}

	var b strings.Builder

	// Header.
	b.WriteString("\n")
	b.WriteString("Plan location: " + m.planName)
	b.WriteString("\n")

	// Branch merge status banner.
	if m.branchExists {
		if m.branchIsMerged {
			b.WriteString(theme.DefaultTheme.Success.Render(theme.IconSuccess + " Branch merged into main - safe to delete"))
		} else {
			b.WriteString(theme.DefaultTheme.Warning.Render(theme.IconError + " Branch has commits not in main - review before deleting"))
		}
		b.WriteString("\n\n")
	} else {
		b.WriteString(theme.DefaultTheme.Muted.Render("Branch does not exist"))
		b.WriteString("\n\n")
	}

	// Force banner. Force turns `git worktree remove` into
	// `git worktree remove --force`, i.e. it discards uncommitted work — so it
	// is stated on its own line, in error styling when armed, rather than
	// hidden behind a help screen.
	if m.showForce {
		if m.force {
			b.WriteString(theme.DefaultTheme.Error.Bold(true).Render("[tf] FORCE ON — destructive git operations WILL discard uncommitted work"))
		} else {
			b.WriteString(theme.DefaultTheme.Muted.Render("[tf] Force: off (safe) — enable to discard uncommitted work in retained repos"))
		}
		b.WriteString("\n\n")
	}

	// Styles.
	focusedStyle := theme.DefaultTheme.Selected
	enabledCheckboxStyle := theme.DefaultTheme.Success.Bold(true)

	// List items (only show available items). Advanced actions are visually
	// separated because select-all intentionally leaves them off.
	advancedHeadingShown := false
	for i, item := range m.items {
		if item == nil || !item.IsAvailable {
			continue
		}
		if item.Advanced && !advancedHeadingShown {
			b.WriteString("\n")
			b.WriteString(theme.DefaultTheme.Muted.Bold(true).Render("Advanced (opt in individually)"))
			b.WriteString("\n")
			advancedHeadingShown = true
		}

		var line strings.Builder

		// Cursor indicator.
		if m.cursor == i {
			line.WriteString(focusedStyle.Render(theme.IconSelect + " "))
		} else {
			line.WriteString("  ")
		}

		// Checkbox.
		if item.IsEnabled {
			line.WriteString(enabledCheckboxStyle.Render(theme.IconStatusCompleted + " "))
		} else {
			line.WriteString(theme.IconStatusTodo + " ")
		}

		// Item name with proper width.
		nameWidth := 50
		itemName := item.Name
		if len(itemName) > nameWidth {
			itemName = itemName[:nameWidth-3] + "..."
		}
		nameFormatted := fmt.Sprintf("%-*s", nameWidth, itemName)

		// Status with appropriate color (strip existing ANSI codes first).
		statusStyle := getStatusStyle(item.Status)
		statusFormatted := statusStyle.Render(fmt.Sprintf("(%s)", stripANSI(item.Status)))

		// Apply styling based on focus.
		if m.cursor == i {
			nameFormatted = focusedStyle.Render(nameFormatted)
		}

		line.WriteString(nameFormatted)
		line.WriteString(" ")
		line.WriteString(statusFormatted)

		b.WriteString(line.String())
		b.WriteString("\n")
	}

	// Show detailed repo status if available (after the list of items).
	for _, item := range m.items {
		if item != nil && len(item.Details) > 0 {
			b.WriteString("\n")
			b.WriteString(renderInlineDetails(item))
			break // Only one item has details currently.
		}
	}

	out := b.String()
	if m.width > 0 {
		out = lipgloss.NewStyle().MaxWidth(m.width).Render(out)
	}
	// Composite the bottom-anchored which-key popup onto the finished frame.
	// The wizard's frame is a content block rather than the whole viewport (it
	// is embedded as a tab), so the vertical budget is passed explicitly —
	// clamping to the frame height alone would truncate the popup.
	return m.whichKey.RenderOverlayAvail(out, lipgloss.Width(out), m.height, *theme.DefaultTheme)
}

// FooterView returns the help hint for use by the pager's SetFooter
// mechanism when the wizard is embedded as a tab.
func (m Model) FooterView() string {
	hint := "? help • q/esc quit"
	if m.showForce {
		if m.force {
			return theme.DefaultTheme.Error.Render("FORCE ON (tf) • ") + theme.DefaultTheme.Muted.Render(hint)
		}
		hint = "tf force • " + hint
	}
	return theme.DefaultTheme.Muted.Render(hint)
}

// renderInlineDetails shows detailed repository status inline below
// the table. Hosts populate Item.Details for the merge/fast-forward
// item when it's the ecosystem submodule walker.
func renderInlineDetails(item *Item) string {
	var b strings.Builder

	// Group repos by status.
	merged := []string{}
	needsMerge := []string{}
	needsRebase := []string{}
	notFound := []string{}

	for _, repo := range item.Details {
		switch repo.Status {
		case "merged":
			merged = append(merged, repo.Name)
		case "needs_merge":
			needsMerge = append(needsMerge, repo.Name)
		case "needs_rebase":
			needsRebase = append(needsRebase, repo.Name)
		case "not_found":
			notFound = append(notFound, repo.Name)
		}
	}

	if len(needsRebase) > 0 {
		b.WriteString(theme.DefaultTheme.Error.Bold(true).Render(fmt.Sprintf("Needs rebase (%d): ", len(needsRebase))))
		b.WriteString(theme.DefaultTheme.Error.Render(strings.Join(needsRebase, ", ")))
		b.WriteString("\n")
	}

	if len(needsMerge) > 0 {
		b.WriteString(theme.DefaultTheme.Warning.Bold(true).Render(fmt.Sprintf("Ready to merge (%d): ", len(needsMerge))))
		b.WriteString(theme.DefaultTheme.Warning.Render(strings.Join(needsMerge, ", ")))
		b.WriteString("\n")
	}

	if len(merged) > 0 {
		b.WriteString(theme.DefaultTheme.Success.Bold(true).Render(fmt.Sprintf("Merged (%d): ", len(merged))))
		b.WriteString(theme.DefaultTheme.Success.Render(strings.Join(merged, ", ")))
		b.WriteString("\n")
	}

	if len(notFound) > 0 {
		b.WriteString(theme.DefaultTheme.Muted.Bold(true).Render(fmt.Sprintf("Skipped (%d): ", len(notFound))))
		b.WriteString(theme.DefaultTheme.Muted.Render(strings.Join(notFound, ", ")))
		b.WriteString("\n")
	}

	return b.String()
}

// getStatusStyle returns the appropriate lipgloss style for a status
// string based on substring matching on its plain (ANSI-stripped)
// text. Hosts historically built these status strings with color
// helpers, so stripping is important for the substring checks to
// work regardless of embedded escape codes.
func getStatusStyle(status string) lipgloss.Style {
	plainStatus := stripANSI(status)

	if strings.Contains(plainStatus, "Already finished") || strings.Contains(plainStatus, "Available") {
		return theme.DefaultTheme.Success
	} else if strings.Contains(plainStatus, "Exists") || strings.Contains(plainStatus, "Running") || strings.Contains(plainStatus, "Has links") || strings.Contains(plainStatus, "Checked out") {
		return theme.DefaultTheme.Warning
	} else if strings.Contains(plainStatus, "Has changes") || strings.Contains(plainStatus, "commits ahead") {
		return theme.DefaultTheme.Error
	} else if strings.Contains(plainStatus, "N/A") || strings.Contains(plainStatus, "Not found") || strings.Contains(plainStatus, "No links") {
		return theme.DefaultTheme.Muted
	}

	return theme.DefaultTheme.Bold
}
