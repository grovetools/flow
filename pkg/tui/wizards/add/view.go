package add

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/grovetools/core/tui/theme"
)

// View renders the multi-field add-job form. It lays out a full-width
// title field on top and two 2-column rows below (job type / slot 2
// list, dependencies / prompt) plus a help footer.
func (m Model) View() string {
	// If help is visible, show it and return
	if m.helpModel.ShowAll {
		m.helpModel.SetSize(95, m.jobTypeList.Height()+m.templateList.Height()+10) // estimate height
		return m.helpModel.View()
	}

	var b strings.Builder

	focusedStyle := theme.DefaultTheme.Highlight
	headingStyle := theme.DefaultTheme.Bold

	// Consistent base style for all panes: no background, no vertical
	// margin, so the layout doesn't shift when focus moves.
	borderStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(theme.DefaultColors.Border).
		Padding(0, 0).
		Width(45)

	unfocusedBorderStyle := borderStyle.
		BorderForeground(theme.DefaultColors.Border)

	focusedBorderStyle := borderStyle.
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(theme.DefaultColors.Orange)

	// Helper to render each field with borders
	renderField := func(index int, label, view string, forceHeight int) string {
		var fieldContent strings.Builder

		if m.focusIndex == index {
			fieldContent.WriteString("  " + focusedStyle.Render(headingStyle.Render(label)))
		} else {
			fieldContent.WriteString("  " + headingStyle.Render(label))
		}
		fieldContent.WriteString("\n")
		fieldContent.WriteString(view)

		var style lipgloss.Style
		if m.focusIndex == index && !m.unfocused {
			style = focusedBorderStyle
		} else if m.focusIndex == index && m.unfocused {
			style = unfocusedBorderStyle
		} else {
			style = borderStyle
		}

		if forceHeight > 0 {
			style = style.Height(forceHeight)
		}

		return style.Render(fieldContent.String())
	}

	// Row 1: Title (full width) with left margin
	var titleFieldStyle lipgloss.Style
	if m.focusIndex == 0 && !m.unfocused {
		titleFieldStyle = focusedBorderStyle.Width(93).MarginLeft(2)
	} else if m.focusIndex == 0 && m.unfocused {
		titleFieldStyle = unfocusedBorderStyle.Width(93).MarginLeft(2)
	} else {
		titleFieldStyle = borderStyle.Width(93).MarginLeft(2)
	}
	var titleContent strings.Builder
	if m.focusIndex == 0 {
		titleContent.WriteString("  " + focusedStyle.Render(headingStyle.Render("Title:")))
	} else {
		titleContent.WriteString("  " + headingStyle.Render("Title:"))
	}
	titleContent.WriteString("\n")
	titleContent.WriteString(m.titleInput.View())
	titleRow := titleFieldStyle.Render(titleContent.String())

	// Row 2: Job Type | Template (or Skills)
	jobTypeView := m.jobTypeList.View()
	jobTypeField := renderField(1, "Job Type:", jobTypeView, 0)

	// Slot 2: Skills picker for agent types, templates for chat/oneshot
	var slot2Label string
	var slot2View string
	if m.slot2IsSkills {
		slot2Label = "Skills:"
		slot2View = m.skillList.View()
	} else {
		slot2Label = "Template:"
		if selected := m.jobTypeList.SelectedItem(); selected != nil {
			jobType := string(selected.(item))
			switch jobType {
			case "interactive_agent", "headless_agent", "agent":
				slot2Label = "Agent templates:"
			case "oneshot", "chat":
				slot2Label = "Prompt templates:"
			}
		}
		slot2View = m.templateList.View()
	}
	templateField := renderField(2, slot2Label, slot2View, 0)

	row2 := lipgloss.JoinHorizontal(lipgloss.Top, jobTypeField, "  ", templateField)
	row2WithMargin := lipgloss.NewStyle().MarginLeft(2).Render(row2)

	// Row 3: Dependencies | Prompt
	var depView string
	if m.plan != nil && len(m.plan.Jobs) > 0 {
		depView = m.depList.View()
	} else {
		depView = "  No existing jobs available"
	}
	depField := renderField(3, "Dependencies:", depView, 0)

	promptField := renderField(4, "Prompt:", m.promptInput.View(), 0)

	row3 := lipgloss.JoinHorizontal(lipgloss.Top, depField, "  ", promptField)
	row3WithMargin := lipgloss.NewStyle().MarginLeft(2).Render(row3)

	// Join all rows vertically for a compact layout
	allRows := lipgloss.JoinVertical(lipgloss.Left, titleRow, row2WithMargin, row3WithMargin)

	// Row 4: Provider | Model (Phase 4b). Each field renders only when
	// its slot is visible for the selected job type — provider for
	// agent jobs, model for agent + oneshot/chat — so the row reflows
	// (no greyed placeholders) and disappears entirely for shell/file.
	var row4Fields []string
	if slotProviderVisible(&m) {
		row4Fields = append(row4Fields, renderField(int(slotProvider), "Provider:", m.providerList.View(), 0))
	}
	if slotModelVisible(&m) {
		var modelView string
		if slotModelKind(&m) == slotList {
			modelView = m.modelList.View()
		} else {
			modelView = m.modelInput.View()
		}
		row4Fields = append(row4Fields, renderField(int(slotModel), "Model:", modelView, 0))
	}
	if len(row4Fields) > 0 {
		var row4 string
		if len(row4Fields) == 2 {
			row4 = lipgloss.JoinHorizontal(lipgloss.Top, row4Fields[0], "  ", row4Fields[1])
		} else {
			row4 = row4Fields[0]
		}
		row4WithMargin := lipgloss.NewStyle().MarginLeft(2).Render(row4)
		allRows = lipgloss.JoinVertical(lipgloss.Left, allRows, row4WithMargin)
	}

	b.WriteString(allRows)

	out := b.String()

	// Clamp the rendered output to the terminal width so wide layouts
	// don't overflow in narrow terminals or embedded pager views.
	if m.width > 0 {
		out = lipgloss.NewStyle().MaxWidth(m.width).Render(out)
	}

	// Composite the bottom-anchored which-key popup onto the finished frame.
	// The wizard's frame is a content block rather than the full viewport, so
	// the vertical budget is passed explicitly (RenderOverlayAvail) — clamping
	// to the frame height alone would truncate a namespace that has room.
	return m.whichKey.RenderOverlayAvail(out, lipgloss.Width(out), m.height, *theme.DefaultTheme)
}

// FooterView returns the mode indicator + help text for use by the
// pager's SetFooter mechanism when the wizard is embedded as a tab.
func (m Model) FooterView() string {
	helpText := m.helpModel.View()

	var modeIndicator string
	if m.unfocused {
		modeIndicator = " [NORMAL] hjkl navigate • i insert • ctrl+s save • q quit"
	} else {
		modeIndicator = " [INSERT] esc normal • ctrl+s save"
	}

	// Claw indicator — only shown for interactive_agent
	var clawSuffix string
	if selected := m.jobTypeList.SelectedItem(); selected != nil && string(selected.(item)) == "interactive_agent" {
		if m.clawEnabled {
			clawSuffix = "  " + lipgloss.NewStyle().Foreground(theme.DefaultColors.Green).Render(" claw: signal+auto")
		} else {
			clawSuffix = "  " + lipgloss.NewStyle().Foreground(theme.DefaultColors.MutedText).Render(" claw: off")
		}
		// Derived, not restated: the toggle-claw key moved off ctrl+g (which
		// treemux owns as its action-chord arm) and a hardcoded label here
		// would have kept advertising the dead key.
		clawSuffix += lipgloss.NewStyle().Foreground(theme.DefaultColors.MutedText).Render(" " + m.keys.ToggleClaw.Help().Key)
	}

	return theme.DefaultTheme.Muted.Render(helpText + modeIndicator + clawSuffix)
}
