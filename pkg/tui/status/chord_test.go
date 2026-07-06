package status

import (
	"testing"

	"github.com/grovetools/core/tui/keymap"
)

// newChordModel builds a minimal Model carrying just the keymap and a fresh
// Sequence engine — enough to exercise chordBindings()/namespaceArmed() and the
// arming/firing behavior of the v/c namespaces without a full TUI.
func newChordModel() Model {
	return Model{
		KeyMap:   NewKeyMap(nil),
		Sequence: keymap.NewSequenceState(),
	}
}

// idxOf returns the chordBindings index whose ConfigKey (via help/field) matches
// the given first key, by comparing the primary key string.
func chordIndexByPrimary(m Model, primary string) int {
	for i, b := range m.chordBindings() {
		if keys := b.Keys(); len(keys) > 0 && keys[0] == primary {
			return i
		}
	}
	return -1
}

// TestChordArmsAndFires drives "v" then "l": the first key leaves the sequence
// pending (popup would render), the second completes the ViewLogs chord.
func TestChordArmsAndFires(t *testing.T) {
	m := newChordModel()

	res, _ := m.Sequence.ProcessKey("v", m.chordBindings()...)
	if res != keymap.SequencePending {
		t.Fatalf("after 'v': got %v, want SequencePending", res)
	}
	if !m.namespaceArmed() {
		t.Fatalf("after 'v': namespaceArmed = false, want true")
	}

	res, idx := m.Sequence.ProcessKey("l", m.chordBindings()...)
	if res != keymap.SequenceMatch {
		t.Fatalf("after 'vl': got %v, want SequenceMatch", res)
	}
	if want := chordIndexByPrimary(m, "vl"); idx != want {
		t.Fatalf("vl matched idx %d, want ViewLogs idx %d", idx, want)
	}
}

// TestPopupRowsWhileArmed asserts the View namespace surfaces its 9 completions
// (trimmed to the remaining suffix) while "v" is armed.
func TestPopupRowsWhileArmed(t *testing.T) {
	m := newChordModel()
	rows := m.KeyMap.Namespaces()[0].PendingRows("v")
	if len(rows) != 9 {
		t.Fatalf("PendingRows(\"v\") = %d rows, want 9", len(rows))
	}
	wantSuffix := map[string]bool{"l": true, "f": true, "b": true, "v": true, "t": true, "c": true, "m": true, "a": true, "s": true}
	for _, r := range rows {
		if !wantSuffix[r.Keys] {
			t.Errorf("unexpected pending row suffix %q", r.Keys)
		}
	}
}

// TestDroppedAliasInert asserts the legacy flat alias "L" no longer fires
// ViewLogs — chord-only (sign-off E4). A bare "L" is now an ordinary key
// (SequenceNone: no match, no pending prefix), freeing it for the audit.
func TestDroppedAliasInert(t *testing.T) {
	m := newChordModel()
	res, idx := m.Sequence.ProcessKey("L", m.chordBindings()...)
	if res != keymap.SequenceNone || idx != -1 {
		t.Fatalf("'L' -> (%v,%d), want (SequenceNone,-1) — alias dropped", res, idx)
	}
}

// TestNoFlatVPrecedence is the precedence regression: a single "v" must never be
// a SequenceMatch (there is no flat-v binding), otherwise the chords could never
// arm — mirrors Phase-1's prefix-vs-exact precedence invariant.
func TestNoFlatVPrecedence(t *testing.T) {
	m := newChordModel()
	res, _ := m.Sequence.ProcessKey("v", m.chordBindings()...)
	if res == keymap.SequenceMatch {
		t.Fatalf("single 'v' fired a match; flat-v squatter reintroduced")
	}
	m.Sequence.Clear()
	res, _ = m.Sequence.ProcessKey("c", m.chordBindings()...)
	if res == keymap.SequenceMatch {
		t.Fatalf("single 'c' fired a match; flat-c squatter reintroduced")
	}
}

// TestChangeNamespaceFires drives "c" then "c": the cc chord completes at the
// SetCompleted index, and "cs" completes SetStatus.
func TestChangeNamespaceFires(t *testing.T) {
	m := newChordModel()
	if res, _ := m.Sequence.ProcessKey("c", m.chordBindings()...); res != keymap.SequencePending {
		t.Fatalf("after 'c': got %v, want SequencePending", res)
	}
	res, idx := m.Sequence.ProcessKey("c", m.chordBindings()...)
	if res != keymap.SequenceMatch || idx != chordIndexByPrimary(m, "cc") {
		t.Fatalf("'cc' -> res=%v idx=%d, want match at SetCompleted", res, idx)
	}

	m.Sequence.Clear()
	m.Sequence.ProcessKey("c", m.chordBindings()...)
	res, idx = m.Sequence.ProcessKey("s", m.chordBindings()...)
	if res != keymap.SequenceMatch || idx != chordIndexByPrimary(m, "cs") {
		t.Fatalf("'cs' -> res=%v idx=%d, want match at SetStatus", res, idx)
	}
}

// TestGGStillTopIndexZero asserts the gg motion is chordBindings index 0 and
// still resolves through the shared seam.
func TestGGStillTopIndexZero(t *testing.T) {
	m := newChordModel()
	m.Sequence.ProcessKey("g", m.chordBindings()...)
	res, idx := m.Sequence.ProcessKey("g", m.chordBindings()...)
	if res != keymap.SequenceMatch || idx != 0 {
		t.Fatalf("'gg' -> res=%v idx=%d, want SequenceMatch at idx 0", res, idx)
	}
}

// TestGGNotNamespaceArmed guards the detail-pane routing: a pending "g" (a
// pane-level gg) must NOT count as namespaceArmed, so it stays with the viewport
// handler rather than being stolen by the top-level chord seam.
func TestGGNotNamespaceArmed(t *testing.T) {
	m := newChordModel()
	m.Sequence.ProcessKey("g", m.chordBindings()...)
	if m.namespaceArmed() {
		t.Fatalf("pending 'g' reported namespaceArmed; would steal viewport gg")
	}
}
