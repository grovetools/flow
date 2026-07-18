package status

import (
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/core/tui/keymap"
)

// newChordModel builds a minimal Model carrying just the keymap and a which-key
// host (fresh Sequence engine + namespaces) — enough to exercise the arming/
// firing behavior of the v/c namespaces through the host seam without a full TUI.
func newChordModel() Model {
	km := NewKeyMap(nil)
	return Model{
		KeyMap:   km,
		WhichKey: keymap.NewWhichKeyHost(nil, km.Namespaces()...),
	}
}

// chordMsg wraps a single key string as a KeyMsg for ProcessChord.
func chordMsg(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// The host resolves the gg motion (passed as the flat extra) at the head of the
// binding list, so every test drives ProcessChord with m.KeyMap.Top as extra —
// exactly the wire order the update loop uses.

// TestChordArmsAndFires drives "v" then "l": the first key leaves the sequence
// pending (popup would render), the second completes the ViewLogs chord and the
// host returns the matched binding.
func TestChordArmsAndFires(t *testing.T) {
	m := newChordModel()

	res, _, _ := m.WhichKey.ProcessChord(chordMsg("v"), m.KeyMap.Top)
	if res != keymap.ChordPending {
		t.Fatalf("after 'v': got %v, want ChordPending", res)
	}
	if !m.WhichKey.Armed() {
		t.Fatalf("after 'v': Armed = false, want true")
	}

	res, b, _ := m.WhichKey.ProcessChord(chordMsg("l"), m.KeyMap.Top)
	if res != keymap.ChordMatched {
		t.Fatalf("after 'vl': got %v, want ChordMatched", res)
	}
	if keys := b.Keys(); len(keys) == 0 || keys[0] != "vl" {
		t.Fatalf("vl matched binding %v, want ViewLogs (keys [vl])", b.Keys())
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
// (ChordNone: no match, no pending prefix), freeing it for the audit.
func TestDroppedAliasInert(t *testing.T) {
	m := newChordModel()
	res, _, _ := m.WhichKey.ProcessChord(chordMsg("L"), m.KeyMap.Top)
	if res != keymap.ChordNone {
		t.Fatalf("'L' -> %v, want ChordNone — alias dropped", res)
	}
}

// TestNoFlatVPrecedence is the precedence regression: a single "v"/"c" must never
// match (there is no flat-v/flat-c binding), otherwise the chords could never arm
// — mirrors Phase-1's prefix-vs-exact precedence invariant.
func TestNoFlatVPrecedence(t *testing.T) {
	m := newChordModel()
	if res, _, _ := m.WhichKey.ProcessChord(chordMsg("v"), m.KeyMap.Top); res == keymap.ChordMatched {
		t.Fatalf("single 'v' fired a match; flat-v squatter reintroduced")
	}
	m.WhichKey.Sequence.Clear()
	if res, _, _ := m.WhichKey.ProcessChord(chordMsg("c"), m.KeyMap.Top); res == keymap.ChordMatched {
		t.Fatalf("single 'c' fired a match; flat-c squatter reintroduced")
	}
}

// TestChangeNamespaceFires drives "c" then "c": the cc chord completes at
// SetCompleted, and "cs" completes SetStatus.
func TestChangeNamespaceFires(t *testing.T) {
	m := newChordModel()
	if res, _, _ := m.WhichKey.ProcessChord(chordMsg("c"), m.KeyMap.Top); res != keymap.ChordPending {
		t.Fatalf("after 'c': got %v, want ChordPending", res)
	}
	res, b, _ := m.WhichKey.ProcessChord(chordMsg("c"), m.KeyMap.Top)
	if res != keymap.ChordMatched || b.Keys()[0] != "cc" {
		t.Fatalf("'cc' -> res=%v keys=%v, want match at SetCompleted (cc)", res, b.Keys())
	}

	m.WhichKey.Sequence.Clear()
	m.WhichKey.ProcessChord(chordMsg("c"), m.KeyMap.Top)
	res, b, _ = m.WhichKey.ProcessChord(chordMsg("s"), m.KeyMap.Top)
	if res != keymap.ChordMatched || b.Keys()[0] != "cs" {
		t.Fatalf("'cs' -> res=%v keys=%v, want match at SetStatus (cs)", res, b.Keys())
	}
}

// TestGGStillResolves asserts the gg motion still resolves through the shared
// seam and returns the Top binding (the update loop special-cases it to gotoTop).
func TestGGStillResolves(t *testing.T) {
	m := newChordModel()
	m.WhichKey.ProcessChord(chordMsg("g"), m.KeyMap.Top)
	res, b, _ := m.WhichKey.ProcessChord(chordMsg("g"), m.KeyMap.Top)
	if res != keymap.ChordMatched {
		t.Fatalf("'gg' -> %v, want ChordMatched", res)
	}
	if !key.Matches(chordMsg(b.Keys()[0]), m.KeyMap.Top) {
		t.Fatalf("'gg' matched %v, want the Top (gg) binding", b.Keys())
	}
}

// TestGGNotNamespaceArmed guards the detail-pane routing: a pending "g" (a
// pane-level gg) must NOT count as namespace-Armed, so it stays with the viewport
// handler rather than being stolen by the top-level chord seam.
func TestGGNotNamespaceArmed(t *testing.T) {
	m := newChordModel()
	m.WhichKey.ProcessChord(chordMsg("g"), m.KeyMap.Top)
	if m.WhichKey.Armed() {
		t.Fatalf("pending 'g' reported Armed; would steal viewport gg")
	}
}
