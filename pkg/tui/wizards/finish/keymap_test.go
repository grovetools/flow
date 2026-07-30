package finish

import (
	"testing"

	"github.com/grovetools/core/tui/keymap"
)

// TestFinishKeyMapAuditCoverage asserts the plan-finish wizard keymap has no
// coverage gaps: every enabled binding (including the promoted Base fields)
// appears in exactly one Sections() entry, no help label lies about its keys,
// and no enabled binding has empty help. If this fails, the disable list or the
// Sections() membership in NewKeyMap is wrong — fix the code, not the test.
func TestFinishKeyMapAuditCoverage(t *testing.T) {
	for _, g := range keymap.AuditCoverage(NewKeyMap(nil)) {
		t.Errorf("keymap coverage gap: field=%s kind=%s detail=%s", g.Field, g.Kind, g.Detail)
	}
}

// TestForceIsAChordUnderADeclaredNamespace pins both halves of the canon-60
// migration. The key half: FORCE is chord-only on tf (sign-off E4 — no flat
// alias). The structural half: the t… prefix is actually DECLARED as a
// namespace. An undeclared prefix is not cosmetic — ProcessChord only arms
// prefixes handed to it via Namespaces(), so the chord would never fire.
func TestForceIsAChordUnderADeclaredNamespace(t *testing.T) {
	km := NewKeyMap(nil)
	if got := km.ToggleForce.Keys(); len(got) != 1 || got[0] != "tf" {
		t.Fatalf("ToggleForce keys = %v, want single [tf]", got)
	}
	if got := km.ToggleForce.Help().Key; got != "tf" {
		t.Errorf("ToggleForce help key = %q, want %q", got, "tf")
	}

	ns := km.Namespaces()
	if len(ns) != 1 || ns[0].Prefix != "t" {
		t.Fatalf("want exactly one declared namespace with prefix t, got %+v", ns)
	}
	if len(ns[0].Bindings) != 1 {
		t.Errorf("Toggle namespace members = %d, want the single force toggle", len(ns[0].Bindings))
	}
}
