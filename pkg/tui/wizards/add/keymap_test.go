package add

import (
	"testing"

	"github.com/grovetools/core/tui/keymap"
)

// TestAddKeyMapAuditCoverage asserts the add-job wizard keymap has no coverage
// gaps: every enabled binding appears in exactly one Sections() entry, no
// help-label lies, and no empty-help bindings. If this fails, the disable list
// or the Sections() membership in NewKeyMap is wrong — fix the code, not the
// test.
func TestAddKeyMapAuditCoverage(t *testing.T) {
	if gaps := keymap.AuditCoverage(NewKeyMap(nil)); len(gaps) != 0 {
		t.Fatalf("audit gaps: %+v", gaps)
	}
}

// TestToggleClawIsNotCtrlG guards the rebind away from treemux's action-chord
// arm. ctrl+g arms the host chord that reaches quit/reload/help/rail, so it is
// on the host's permanently-non-deferrable list: host/hosted key arbitration
// can hand back an ordinary global like ctrl+f, but never this one. The
// binding was simply dead whenever the wizard ran inside treemux, and it could
// only be fixed here.
func TestToggleClawIsNotCtrlG(t *testing.T) {
	km := NewKeyMap(nil)
	for _, k := range km.ToggleClaw.Keys() {
		if k == "ctrl+g" {
			t.Fatal("ToggleClaw is bound to ctrl+g — treemux's action-chord arm swallows it and always will")
		}
	}
	if len(km.ToggleClaw.Keys()) != 1 || km.ToggleClaw.Keys()[0] != "ctrl+t" {
		t.Fatalf("ToggleClaw keys = %v, want [ctrl+t]", km.ToggleClaw.Keys())
	}
	// The advertised label must move with the binding, or help lies.
	if km.ToggleClaw.Help().Key != "ctrl+t" {
		t.Errorf("ToggleClaw help key = %q, want %q", km.ToggleClaw.Help().Key, "ctrl+t")
	}
}
