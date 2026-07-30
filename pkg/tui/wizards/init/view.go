package planinit

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/grovetools/core/tui/theme"
)

// View renders the plan-init wizard.
func (m Model) View() string {
	if m.help.ShowAll {
		return m.help.View()
	}

	var b strings.Builder

	switch m.currentScreen {
	case MainScreen:
		b.WriteString(m.renderMainScreen())
	case AdvancedScreen:
		b.WriteString(m.renderAdvancedScreen())
	case ReviewScreen:
		b.WriteString(m.renderReviewScreen())
	}
	if m.validating {
		b.WriteString("\n" + theme.DefaultTheme.Info.Render("Validating target, collisions, git state, registry ownership, and permissions…") + "\n")
	}

	if m.err != nil {
		b.WriteString("\n")
		b.WriteString(theme.DefaultTheme.Error.Render(m.err.Error()))
		b.WriteString("\n")
	}

	container := lipgloss.NewStyle().PaddingLeft(2)
	if m.width > 0 {
		container = container.MaxWidth(m.width)
	}
	// Composite the bottom-anchored which-key popup onto the finished frame.
	// The wizard's frame is a content block rather than the whole viewport (it
	// is embedded as a tab), so the vertical budget is passed explicitly —
	// clamping to the frame height alone would truncate the popup.
	frame := container.Render(b.String())
	return m.whichKey.RenderOverlayAvail(frame, lipgloss.Width(frame), m.height, *theme.DefaultTheme)
}

// FooterView returns the mode indicator + help text for use by the
// pager's SetFooter mechanism when the wizard is embedded as a tab.
func (m Model) FooterView() string {
	var modeIndicator string
	if m.unfocused {
		modeIndicator = "[NORMAL]"
	} else {
		modeIndicator = "[INSERT]"
	}

	helpText := m.help.View()
	if helpText != "" {
		return theme.DefaultTheme.Muted.Render(modeIndicator + " • " + helpText)
	}
	return theme.DefaultTheme.Muted.Render(modeIndicator)
}

// renderMainScreen renders the main configuration screen.
func (m Model) renderMainScreen() string {
	var b strings.Builder

	borderStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultColors.Border).
		Padding(0, 1).
		Width(40)

	borderStyleWide := borderStyle.Width(85)

	unfocusedBorderStyle := borderStyle.
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultColors.MutedText)

	unfocusedBorderStyleWide := borderStyleWide.
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultColors.MutedText)

	focusedBorderStyle := borderStyle.
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(theme.DefaultColors.Orange)

	focusedBorderStyleWide := borderStyleWide.
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(theme.DefaultColors.Orange)

	renderField := func(index int, title, content string, wide bool) string {
		var fieldBuilder strings.Builder

		titlePrefix := "  "
		if index <= m.highestFocusIndex {
			titlePrefix = theme.DefaultTheme.Success.Render("* ")
		}
		fieldBuilder.WriteString(titlePrefix + theme.DefaultTheme.Bold.Render(title))
		fieldBuilder.WriteString("\n")
		fieldBuilder.WriteString(content)

		var style lipgloss.Style
		if wide {
			if m.focusIndex == index && !m.unfocused {
				style = focusedBorderStyleWide
			} else if m.focusIndex == index && m.unfocused {
				style = unfocusedBorderStyleWide
			} else {
				style = borderStyleWide
			}
		} else {
			if m.focusIndex == index && !m.unfocused {
				style = focusedBorderStyle
			} else if m.focusIndex == index && m.unfocused {
				style = unfocusedBorderStyle
			} else {
				style = borderStyle
			}
		}
		return style.Render(fieldBuilder.String())
	}

	b.WriteString(renderField(0, "Plan Name", m.nameInput.View(), true))
	b.WriteString("\n")

	recipeField := renderField(1, "Recipe", m.recipeList.View(), false)
	modelField := renderField(2, "Default Model For API", m.modelList.View(), false)
	row2 := lipgloss.JoinHorizontal(lipgloss.Top, recipeField, "  ", modelField)
	b.WriteString(row2)
	b.WriteString("\n")

	if m.showAnchor {
		anchorField := renderField(3, "Anchor Repository (press / to search)", m.anchorList.View(), true)
		b.WriteString(anchorField)
		b.WriteString("\n")
	}

	autoWorktreeDisplay := "[ ]"
	if m.withWorktree {
		autoWorktreeDisplay = "[x]"
	}

	autoWorktreeField := renderField(4, "Auto-create Worktree", autoWorktreeDisplay, true)
	b.WriteString(autoWorktreeField)
	b.WriteString("\n")

	return b.String()
}

// renderAdvancedScreen renders the advanced options screen.
func (m Model) renderReviewScreen() string {
	var b strings.Builder
	b.WriteString(theme.DefaultTheme.Header.Bold(true).Render("󰄬 Validate & Review"))
	b.WriteString("\n\n")
	if m.validation == nil {
		return b.String()
	}
	for _, check := range m.validation.Checks {
		icon := theme.IconSuccess
		style := theme.DefaultTheme.Success
		if !check.OK {
			icon = theme.IconWarning
			style = theme.DefaultTheme.Warning
			if check.Severity == "error" {
				style = theme.DefaultTheme.Error
			}
		}
		b.WriteString(style.Render(icon + " " + check.ID + ": " + check.Detail))
		b.WriteString("\n")
	}
	b.WriteString("\n" + theme.DefaultTheme.Bold.Render("Mutations") + "\n")
	if m.manifest != nil {
		for _, step := range m.manifest.Steps {
			reversibility := "rollback available"
			if !step.Reversible {
				reversibility = "not automatically reversible"
			}
			b.WriteString("  • " + step.Kind + " → " + step.Target + " (" + reversibility + ")\n")
		}
	}
	if m.validation.Valid() {
		b.WriteString("\n" + theme.DefaultTheme.Success.Render("Press Enter to create; Esc returns without mutation."))
	} else {
		b.WriteString("\n" + theme.DefaultTheme.Error.Render("Creation is blocked. Esc to correct the failed checks."))
	}
	return b.String()
}

func (m Model) renderAdvancedScreen() string {
	var b strings.Builder

	b.WriteString(theme.DefaultTheme.Header.Bold(true).Render("󰠡 Advanced Options"))
	b.WriteString("\n\n")

	borderStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultColors.Border).
		Padding(0, 1).
		Width(40)

	borderStyleWide := borderStyle.Width(85)

	unfocusedBorderStyle := borderStyle.
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultColors.MutedText)

	unfocusedBorderStyleWide := borderStyleWide.
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultColors.MutedText)

	focusedBorderStyle := borderStyle.
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(theme.DefaultColors.Orange)

	focusedBorderStyleWide := borderStyleWide.
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(theme.DefaultColors.Orange)

	renderField := func(index int, title, content string, wide bool) string {
		var fieldBuilder strings.Builder
		fieldBuilder.WriteString("  " + theme.DefaultTheme.Bold.Render(title))
		fieldBuilder.WriteString("\n")
		fieldBuilder.WriteString(content)

		var style lipgloss.Style
		if wide {
			if m.focusIndex == index && !m.unfocused {
				style = focusedBorderStyleWide
			} else if m.focusIndex == index && m.unfocused {
				style = unfocusedBorderStyleWide
			} else {
				style = borderStyleWide
			}
		} else {
			if m.focusIndex == index && !m.unfocused {
				style = focusedBorderStyle
			} else if m.focusIndex == index && m.unfocused {
				style = unfocusedBorderStyle
			} else {
				style = borderStyle
			}
		}
		return style.Render(fieldBuilder.String())
	}

	runInitDisplay := "[ ]"
	if m.runInit {
		runInitDisplay = "[x]"
	}
	b.WriteString(renderField(0, "Run Init Actions", runInitDisplay, true))
	b.WriteString("\n")

	var worktreeDisplay string
	if m.withWorktree {
		worktreeDisplay = theme.DefaultTheme.Muted.Render("(matches plan name)")
	} else {
		worktreeDisplay = m.worktreeInput.View()
	}

	b.WriteString(renderField(1, "Worktree Name", worktreeDisplay, true))
	b.WriteString("\n")

	extractField := renderField(2, "Extract from File (from-note)", m.extractFromInput.View(), false)
	targetField := renderField(3, "Note Target File", m.noteTargetFileInput.View(), false)
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, extractField, "  ", targetField))
	b.WriteString("\n")

	layoutField := renderField(4, "Worktree Location Layout", m.layoutInput.View(), true)
	b.WriteString(layoutField)
	b.WriteString("\n\n")

	b.WriteString(theme.DefaultTheme.Muted.Render("Press 'Esc' or 'b' to return to main screen"))
	b.WriteString("\n")

	return b.String()
}
