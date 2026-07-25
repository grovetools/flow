package finish

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/tui/embed"
)

func newForceWizard(t *testing.T, show bool) Model {
	t.Helper()
	return New(Config{
		PlanName: "my-plan",
		Items: []*Item{
			{ID: "prune_worktree", Name: "Prune git worktree", Status: "Exists", IsAvailable: true},
		},
		ShowForceToggle: show,
	})
}

// TestForceToggleDefaultsOff pins the safety default: force puts
// `git worktree remove --force` one keystroke from the user, so it must never
// start on.
func TestForceToggleDefaultsOff(t *testing.T) {
	m := newForceWizard(t, true)
	if m.Force() {
		t.Fatal("force must default to OFF")
	}
}

// TestForceToggleFlipsOnF pins the binding.
func TestForceToggleFlipsOnF(t *testing.T) {
	m := newForceWizard(t, true)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	m = updated.(Model)
	if !m.Force() {
		t.Fatal("f should enable force")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	m = updated.(Model)
	if m.Force() {
		t.Fatal("f should toggle force back off")
	}
}

// TestForceToggleIsVisible pins that the state is rendered — an invisible
// destructive mode is worse than no mode at all.
func TestForceToggleIsVisible(t *testing.T) {
	m := newForceWizard(t, true)
	if !strings.Contains(m.View(), "Force") {
		t.Fatalf("wizard body must advertise the force toggle:\n%s", m.View())
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	m = updated.(Model)
	body := m.View()
	if !strings.Contains(body, "FORCE ON") {
		t.Fatalf("enabled force must be loudly visible:\n%s", body)
	}
}

// TestForceToggleHiddenWhenHostCannotHonourIt pins that hosts which cannot act
// on the toggle (the standalone CLI wizard, which takes --force from the flag)
// do not advertise a control that would do nothing.
func TestForceToggleHiddenWhenHostCannotHonourIt(t *testing.T) {
	m := newForceWizard(t, false)
	if strings.Contains(m.View(), "Force") {
		t.Fatalf("force toggle must not be advertised when the host cannot honour it:\n%s", m.View())
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	m = updated.(Model)
	if m.Force() {
		t.Fatal("f must be inert when the toggle is not offered")
	}
}

// TestEscCancelsTheStandaloneWizard covers the host that the view-level esc arm
// cannot reach: `flow plan finish` runs this wizard through
// embed.RunStandalone, where nothing above it handles esc. The footer advertises
// q/esc, so both must actually work.
func TestEscCancelsTheStandaloneWizard(t *testing.T) {
	m := newForceWizard(t, false)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("esc must emit a cancel DoneMsg")
	}
	msg := cmd()
	done, ok := msg.(embed.DoneMsg)
	if !ok {
		t.Fatalf("esc produced %T, want embed.DoneMsg", msg)
	}
	if done.Result != nil {
		t.Fatalf("esc must cancel (nil Result), got %#v", done.Result)
	}
}
