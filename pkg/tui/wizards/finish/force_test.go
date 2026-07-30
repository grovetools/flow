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

// pressChord feeds each rune of a chord through Update in order, threading the
// returned Model forward. Threading matters: WhichKeyHost carries a
// *SequenceState, so the arm buffer lives behind a pointer shared by every copy
// of the Model — re-pressing a prefix on a stale value silently appends to the
// same buffer and matches nothing.
func pressChord(t *testing.T, m Model, chord string) Model {
	t.Helper()
	for _, r := range chord {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}
	return m
}

// TestForceToggleFlipsOnTF pins the binding. Canon 60 §4.2 moved FORCE off flat
// `f` into a deliberately single-member t… namespace: it discards uncommitted
// work, so a which-key popup in front of it is a safety win, not just RULE T.
func TestForceToggleFlipsOnTF(t *testing.T) {
	m := newForceWizard(t, true)
	m = pressChord(t, m, "tf")
	if !m.Force() {
		t.Fatal("tf should enable force")
	}
	m = pressChord(t, m, "tf")
	if m.Force() {
		t.Fatal("tf should toggle force back off")
	}
}

// TestFlatFIsNoLongerBound pins the chord-only rule (sign-off E4): the retired
// flat key must not survive as an alias.
func TestFlatFIsNoLongerBound(t *testing.T) {
	m := newForceWizard(t, true)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if updated.(Model).Force() {
		t.Fatal("flat f must no longer toggle force")
	}
}

// TestForceToggleIsVisible pins that the state is rendered — an invisible
// destructive mode is worse than no mode at all.
func TestForceToggleIsVisible(t *testing.T) {
	m := newForceWizard(t, true)
	if !strings.Contains(m.View(), "Force") {
		t.Fatalf("wizard body must advertise the force toggle:\n%s", m.View())
	}
	m = pressChord(t, m, "tf")
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
	m = pressChord(t, m, "tf")
	if m.Force() {
		t.Fatal("tf must be inert when the toggle is not offered")
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
