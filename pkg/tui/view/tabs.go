package view

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	core_theme "github.com/grovetools/core/tui/theme"
)

func coreThemeIconNumeric(n int) string {
	switch n {
	case 1:
		return core_theme.IconNumeric1CircleOutline
	case 2:
		return core_theme.IconNumeric2CircleOutline
	case 3:
		return core_theme.IconNumeric3CircleOutline
	case 4:
		return core_theme.IconNumeric4CircleOutline
	case 5:
		return core_theme.IconNumeric5CircleOutline
	case 6:
		return core_theme.IconNumeric6CircleOutline
	case 7:
		return core_theme.IconNumeric7CircleOutline
	case 8:
		return core_theme.IconNumeric8CircleOutline
	case 9:
		return core_theme.IconNumeric9CircleOutline
	default:
		return ""
	}
}

type tabSegmentState int

const (
	tabSegmentInactive tabSegmentState = iota
	tabSegmentActive
	tabSegmentDisabled
)

// renderTabSegment formats one tab as "<icon> <name>" with the icon
// and name styled independently. Matches nav / core pager styling.
func renderTabSegment(icon, name string, state tabSegmentState) string {
	th := core_theme.DefaultTheme
	switch state {
	case tabSegmentActive:
		numStyle := lipgloss.NewStyle().Foreground(th.Colors.Violet).Bold(true)
		nameStyle := lipgloss.NewStyle().Foreground(th.Colors.LightText).Bold(true)
		return numStyle.Render(icon) + " " + nameStyle.Render(name)
	case tabSegmentDisabled:
		faint := th.Muted.Faint(true)
		return faint.Render(icon) + " " + faint.Render(name)
	default:
		numStyle := lipgloss.NewStyle().Foreground(th.Colors.MutedText)
		return numStyle.Render(icon) + " " + th.Muted.Render(name)
	}
}

func joinTabs(parts []string) string {
	separator := core_theme.DefaultTheme.Muted.Faint(true).Render("  •  ")
	return strings.Join(parts, separator)
}
