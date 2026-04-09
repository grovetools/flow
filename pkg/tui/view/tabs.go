package view

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	core_theme "github.com/grovetools/core/tui/theme"
)

// coreThemeIconNumeric returns the numeric circle-outline glyph for
// the given 1-based tab number. Falls back to an empty string for
// out-of-range values so the caller doesn't need bounds checks.
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

// renderTabSegment formats one tab entry as "<icon> <name>" with
// the icon and name styled independently so the numeric glyph pops
// in violet the same way nav and the shared core/tui/components/
// pager component render theirs. Keeping this in lockstep across
// the ecosystem avoids the flow meta-panel looking like a different
// widget.
//
// active → violet bold icon + light bold name
// inactive → muted icon + muted name
// disabled → muted-faint icon + muted-faint name (prerequisites not met)
func renderTabSegment(icon, name string, state tabSegmentState) string {
	th := core_theme.DefaultTheme
	switch state {
	case tabSegmentActive:
		numStyle := lipgloss.NewStyle().
			Foreground(th.Colors.Violet).
			Bold(true)
		nameStyle := lipgloss.NewStyle().
			Foreground(th.Colors.LightText).
			Bold(true)
		return numStyle.Render(icon) + " " + nameStyle.Render(name)
	case tabSegmentDisabled:
		faint := th.Muted.Faint(true)
		return faint.Render(icon) + " " + faint.Render(name)
	default:
		numStyle := lipgloss.NewStyle().
			Foreground(th.Colors.MutedText)
		nameStyle := th.Muted
		return numStyle.Render(icon) + " " + nameStyle.Render(name)
	}
}

// tabSegmentState enumerates the three tab visual states. It is a
// small internal type rather than a pair of booleans so new states
// (e.g. "error") can be added without changing call sites.
type tabSegmentState int

const (
	tabSegmentInactive tabSegmentState = iota
	tabSegmentActive
	tabSegmentDisabled
)

// joinTabs joins the pre-rendered tab segments with a bullet
// separator, matching the visual style of nav/pager.
func joinTabs(parts []string) string {
	separator := core_theme.DefaultTheme.Muted.Faint(true).Render("  •  ")
	return strings.Join(parts, separator)
}
