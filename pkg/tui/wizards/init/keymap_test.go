package planinit

import (
	"testing"

	"github.com/grovetools/core/tui/keymap"
)

// TestInitKeyMapAuditCoverage asserts the plan-init wizard keymap has no
// coverage gaps: every enabled binding (including the promoted Base fields)
// appears in exactly one Sections() entry, no help label lies about its keys,
// and no enabled binding has empty help. If this fails, the disable list or the
// Sections() membership in NewKeyMap is wrong — fix the code, not the test.
func TestInitKeyMapAuditCoverage(t *testing.T) {
	for _, g := range keymap.AuditCoverage(NewKeyMap(nil)) {
		t.Errorf("keymap coverage gap: field=%s kind=%s detail=%s", g.Field, g.Kind, g.Detail)
	}
}

// TestToggleAdvancedIsAChordUnderADeclaredNamespace pins both halves of the
// canon-60 migration: the advanced toggle is chord-only on ta (sign-off E4 —
// no flat `a` alias, which frees Ring-1 `a`), and the t… prefix is actually
// DECLARED as a namespace. An undeclared prefix would never arm, so the chord
// would simply be dead.
func TestToggleAdvancedIsAChordUnderADeclaredNamespace(t *testing.T) {
	km := NewKeyMap(nil)
	if got := km.ToggleAdvanced.Keys(); len(got) != 1 || got[0] != "ta" {
		t.Fatalf("ToggleAdvanced keys = %v, want single [ta]", got)
	}
	if got := km.ToggleAdvanced.Help().Key; got != "ta" {
		t.Errorf("ToggleAdvanced help key = %q, want %q", got, "ta")
	}

	ns := km.Namespaces()
	if len(ns) != 1 || ns[0].Prefix != "t" {
		t.Fatalf("want exactly one declared namespace with prefix t, got %+v", ns)
	}
}
