package status

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/tui/keymap"

	"github.com/grovetools/flow/pkg/orchestration"
)

// newSeamTestModel builds a Model carrying just the keymap and a which-key host
// (fresh Sequence engine + namespaces, show-delay forced to 0) — enough to drive
// the chord seam in Update() for keys that resolve inside the seam (pending /
// cancel / stray) without touching the pane Manager or plan state.
func newSeamTestModel() Model {
	km := NewKeyMap(nil)
	h := keymap.NewWhichKeyHost(nil, km.Namespaces()...)
	h.Delay = 0
	return Model{
		KeyMap:   km,
		WhichKey: h,
	}
}

// driveKey feeds one key through Update and returns the updated Model + cmd.
func driveKey(t *testing.T, m Model, s string) (Model, tea.Cmd) {
	t.Helper()
	var msg tea.KeyMsg
	if s == "esc" {
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	} else {
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	mdl, cmd := m.Update(msg)
	return mdl.(Model), cmd
}

// TestEscConsumesAndStays: esc while a namespace chord is armed clears the
// buffer and is CONSUMED by the seam — it must not fall through to the flat
// switch (where esc reaches CloseDetailPane). We prove non-fall-through with an
// agent ActiveLogJob: the fall-through path records LastEscPress ("press esc
// again to interrupt"); the seam's SequenceCancel path leaves it zero.
func TestEscConsumesAndStays(t *testing.T) {
	m := newSeamTestModel()
	m.ActiveLogJob = &orchestration.Job{ID: "j", Type: orchestration.JobTypeIsolatedAgent}

	m, _ = driveKey(t, m, "v")
	if !m.WhichKey.IsPending() {
		t.Fatalf("'v' should arm the chord")
	}

	m, _ = driveKey(t, m, "esc")
	if m.WhichKey.IsPending() {
		t.Errorf("esc should clear the armed chord (buffer=%q)", m.WhichKey.Sequence.Buffer())
	}
	if !m.LastEscPress.IsZero() {
		t.Errorf("esc fell through to CloseDetailPane (agent double-esc path) — the seam must consume it while armed")
	}
}

// TestStrayKeyConsumedWhileArmed: a stray non-continuation key ("x") while the
// View menu is armed closes the menu and is consumed — it must not fire the
// top-level action ("x" = AddXmlPlan, which sets CreatingJob).
func TestStrayKeyConsumedWhileArmed(t *testing.T) {
	m := newSeamTestModel()

	m, _ = driveKey(t, m, "v")
	if !m.WhichKey.Armed() {
		t.Fatalf("'v' should arm the View namespace")
	}

	m, _ = driveKey(t, m, "x")
	if m.WhichKey.IsPending() {
		t.Errorf("stray 'x' should clear the buffer (buffer=%q)", m.WhichKey.Sequence.Buffer())
	}
	if m.CreatingJob {
		t.Errorf("stray 'x' while armed fired AddXmlPlan — it must be consumed by the seam")
	}
}

// TestDelaySuppressesFastChord: the popup visibility gate hides a freshly-armed
// chord under a large show-delay and reveals it immediately at delay 0.
func TestDelaySuppressesFastChord(t *testing.T) {
	slow := newSeamTestModel()
	slow.WhichKey.Delay = time.Hour
	slow, _ = driveKey(t, slow, "v")
	if slow.WhichKey.PopupVisible() {
		t.Errorf("a fast chord under a large delay must not show the popup")
	}

	fast := newSeamTestModel() // Delay 0
	fast, _ = driveKey(t, fast, "v")
	if !fast.WhichKey.PopupVisible() {
		t.Errorf("delay 0 should show the popup immediately")
	}
}

// TestWhichKeyTickRerenders: arming a namespace schedules a re-render tick;
// the flat gg prefix does not; and the tick's message is a no-op that preserves
// the pending chord.
func TestWhichKeyTickRerenders(t *testing.T) {
	// Namespace prefix → tick scheduled.
	nsModel := newSeamTestModel()
	nsModel, cmd := driveKey(t, nsModel, "v")
	if cmd == nil {
		t.Errorf("arming the View namespace should schedule a which-key tick")
	}

	// Flat gg prefix → no tick (its footer hint is immediate).
	flat := newSeamTestModel()
	_, flatCmd := driveKey(t, flat, "g")
	if flatCmd != nil {
		t.Errorf("the flat gg prefix should not schedule a which-key tick")
	}

	// WhichKeyShowMsg is a no-op that must not disturb the armed chord.
	mdl, showCmd := nsModel.Update(keymap.WhichKeyShowMsg{})
	after := mdl.(Model)
	if showCmd != nil {
		t.Errorf("WhichKeyShowMsg should be a no-op (nil cmd)")
	}
	if !after.WhichKey.IsPending() {
		t.Errorf("WhichKeyShowMsg must not clear the pending chord")
	}
}
