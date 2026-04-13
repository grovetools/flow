package planinit

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/grovetools/core/pkg/workspace"
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
	return container.Render(b.String())
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

	borderStyleWide := borderStyle.Copy().Width(85)

	unfocusedBorderStyle := borderStyle.Copy().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240"))

	unfocusedBorderStyleWide := borderStyleWide.Copy().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240"))

	focusedBorderStyle := borderStyle.Copy().
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(theme.DefaultColors.Orange)

	focusedBorderStyleWide := borderStyleWide.Copy().
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(theme.DefaultColors.Orange)

	renderField := func(index int, title string, content string, wide bool) string {
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

	isInheritedContext := false
	currentNode, err := workspace.GetProjectByPath(".")
	if err == nil && currentNode.Kind == workspace.KindEcosystemWorktreeSubProjectWorktree {
		isInheritedContext = true
	}

	autoWorktreeDisplay := "[ ]"
	if m.withWorktree {
		autoWorktreeDisplay = "[x]"
	}
	if isInheritedContext {
		autoWorktreeDisplay = theme.DefaultTheme.Muted.Render("[ ] (Inherited)")
	}

	openSessionDisplay := "[ ]"
	if m.openSession {
		openSessionDisplay = "[x]"
	}

	autoWorktreeField := renderField(3, "Auto-create Worktree", autoWorktreeDisplay, false)
	openSessionField := renderField(4, "Open Session on Create", openSessionDisplay, false)
	row3 := lipgloss.JoinHorizontal(lipgloss.Top, autoWorktreeField, "  ", openSessionField)
	b.WriteString(row3)
	b.WriteString("\n")

	return b.String()
}

// renderAdvancedScreen renders the advanced options screen.
func (m Model) renderAdvancedScreen() string {
	var b strings.Builder

	b.WriteString(theme.DefaultTheme.Header.Bold(true).Render("󰠡 Advanced Options"))
	b.WriteString("\n\n")

	borderStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultColors.Border).
		Padding(0, 1).
		Width(40)

	borderStyleWide := borderStyle.Copy().Width(85)

	unfocusedBorderStyle := borderStyle.Copy().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240"))

	unfocusedBorderStyleWide := borderStyleWide.Copy().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240"))

	focusedBorderStyle := borderStyle.Copy().
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(theme.DefaultColors.Orange)

	focusedBorderStyleWide := borderStyleWide.Copy().
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(theme.DefaultColors.Orange)

	renderField := func(index int, title string, content string, wide bool) string {
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

	isInheritedContext := false
	currentNode, err := workspace.GetProjectByPath(".")
	if err == nil && currentNode.Kind == workspace.KindEcosystemWorktreeSubProjectWorktree {
		isInheritedContext = true
	}

	var worktreeDisplay string
	if m.withWorktree {
		worktreeDisplay = theme.DefaultTheme.Muted.Render("(matches plan name)")
	} else if isInheritedContext {
		worktreeDisplay = theme.DefaultTheme.Info.Render(m.worktreeInput.Value())
	} else {
		worktreeDisplay = m.worktreeInput.View()
	}

	b.WriteString(renderField(1, "Worktree Name", worktreeDisplay, true))
	b.WriteString("\n")

	extractField := renderField(2, "Extract from File (from-note)", m.extractFromInput.View(), false)
	targetField := renderField(3, "Note Target File", m.noteTargetFileInput.View(), false)
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, extractField, "  ", targetField))
	b.WriteString("\n\n")

	b.WriteString(theme.DefaultTheme.Muted.Render("Press 'Esc' or 'b' to return to main screen"))
	b.WriteString("\n")

	return b.String()
}
