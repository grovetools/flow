package browser

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestRefreshTickCarriesItsInstant pins what a render gate reads off the
// message. view.RenderNeutral keys its clock exception on this instant, so a
// tick that stopped carrying the tea.Tick time would either strand the
// relative-time columns or dirty on every tick.
func TestRefreshTickCarriesItsInstant(t *testing.T) {
	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if got := time.Time(RefreshTickMsg(at)); !got.Equal(at) {
		t.Errorf("carried instant = %s, want %s", got, at)
	}
}

// TestFallbackTickChangesNothingVisible is the claim behind classifying it,
// asserted against a real model rather than by reading the handler: with the
// daemon snapshot live the tick is inert (and does not even re-arm), and while
// a load is in flight it only re-arms. In neither case may the rendered view
// move — if it did, a host that skipped the frame would be showing a stale
// screen.
func TestFallbackTickChangesNothingVisible(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(Model) Model
		rearms  bool
	}{
		{
			name:    "daemon snapshot live",
			arrange: func(m Model) Model { m.hasDaemonSnapshot = true; return m },
		},
		{
			// The one state in which the tick both survives and does no
			// work: the local fallback, focused, with a load outstanding.
			name: "load already in flight",
			arrange: func(m Model) Model {
				m.dataSource = "local fallback — daemon unavailable"
				m.loading = true
				m.focused = true
				return m
			},
			rearms: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sized, _ := New(Config{PlansDir: t.TempDir()}).
				Update(tea.WindowSizeMsg{Width: 120, Height: 40})
			m := tc.arrange(sized.(Model))
			before := m.View()

			after, cmd := m.Update(RefreshTickMsg(time.Now()))
			if after.(Model).View() != before {
				t.Error("the fallback tick moved the rendered view")
			}
			if tc.rearms != (cmd != nil) {
				t.Errorf("re-armed = %v, want %v", cmd != nil, tc.rearms)
			}
		})
	}
}
