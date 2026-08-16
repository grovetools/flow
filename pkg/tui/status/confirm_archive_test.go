package status

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The archive prompt replaces the footer, and the footer is exactly the one
// row View() budgets for it (footerHeight). A prompt taller than that pushes
// the whole frame past the terminal height and the host clips the overflow —
// which is the prompt itself, so pressing X looked like nothing happened.
func TestConfirmArchivePromptFitsFrame(t *testing.T) {
	const height = 40

	for _, tc := range []struct {
		name    string
		selects int
		want    func(m Model) string
	}{
		{
			name:    "selection",
			selects: 3,
			want:    func(Model) string { return "Archive 3 selected job(s)? (y/n)" },
		},
		{
			name:    "cursor job",
			selects: 0,
			want: func(m Model) string {
				return "Archive '" + m.CurrentJob().Filename + "'? (y/n)"
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newWideContentModel(t, 12)
			for i := range tc.selects {
				m.Selected[m.Jobs[i].ID] = true
			}
			want := tc.want(m)

			base := lipgloss.Height(m.View())
			m.ConfirmArchive = true
			view := m.View()

			if got := lipgloss.Height(view); got != base {
				t.Errorf("confirm frame height = %d, want %d (unconfirmed height); prompt overflows and is clipped", got, base)
			}
			if got := lipgloss.Height(view); got > height {
				t.Errorf("confirm frame height = %d, exceeds terminal height %d", got, height)
			}

			lines := strings.Split(view, "\n")
			last := lines[len(lines)-1]
			if !strings.Contains(last, want) {
				t.Errorf("last row = %q, want it to carry the prompt %q", last, want)
			}
		})
	}
}

// Esc backs out of the prompt like every other dialog in this TUI; only y/n
// and the two quit keys used to be wired, so Esc left it stuck on screen.
func TestConfirmArchiveCancelKeys(t *testing.T) {
	for _, k := range []string{"n", "N", "esc", "q"} {
		t.Run(k, func(t *testing.T) {
			m := newWideContentModel(t, 4)
			m.ConfirmArchive = true

			msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
			if k == "esc" {
				msg = tea.KeyMsg{Type: tea.KeyEsc}
			}

			mdl, _ := m.Update(msg)
			if mdl.(Model).ConfirmArchive {
				t.Errorf("%q did not cancel the archive prompt", k)
			}
		})
	}
}
