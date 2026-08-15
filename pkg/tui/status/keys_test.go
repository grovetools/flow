package status

import (
	"strings"
	"testing"

	"github.com/grovetools/core/tui/keymap"
)

// allBindings walks a KeymapInfo() TUIInfo and returns every exported binding.
func allBindings(t *testing.T) []keymap.BindingInfo {
	t.Helper()
	var out []keymap.BindingInfo
	for _, sec := range KeymapInfo().Sections {
		out = append(out, sec.Bindings...)
	}
	return out
}

// TestConfigKeysStableAfterNamespacing pins the 14 namespaced ConfigKeys: moving
// the keys onto v/c chords must NOT change any ConfigKey (they are minted from
// the named struct fields, so grove.toml overrides survive the rebind).
func TestConfigKeysStableAfterNamespacing(t *testing.T) {
	want := map[string]bool{
		"view_logs": true, "view_frontmatter": true, "view_briefing": true,
		"view_edit": true, "view_tokens": true, "view_context": true,
		"view_memory": true, "view_native_agent": true, "view_skill_pane": true,
		"view_action_error": true,
		"set_status":        true, "set_type": true, "set_template": true, "set_completed": true,
		"toggle_fullscreen": true,
	}
	got := map[string]bool{}
	for _, b := range allBindings(t) {
		if want[b.ConfigKey] {
			got[b.ConfigKey] = true
		}
	}
	for k := range want {
		if !got[k] {
			t.Errorf("ConfigKey %q missing from registry after namespacing", k)
		}
	}
}

// TestNoFlatPrefixKeys asserts no enabled binding is bound flat to a reserved
// prefix (v, c, t, z). Retaining any as a flat alias would re-flag the audit
// squatter and prevent the chord from ever arming.
func TestNoFlatPrefixKeys(t *testing.T) {
	reserved := map[string]bool{"v": true, "c": true, "t": true, "z": true}
	for _, b := range allBindings(t) {
		if !b.Enabled {
			continue
		}
		for _, k := range b.Keys {
			if reserved[k] {
				t.Errorf("binding %q (%s) is bound flat to reserved prefix %q", b.Name, b.ConfigKey, k)
			}
		}
	}
}

// TestNamespaceMembership asserts the View namespace has 13 members and Change
// has 15 (the four original set-family members plus the schema-driven field
// editor's model/provider/effort/responder/cache_ttl/cache_layout enums, the
// memory/auto_complete toggles, the migrated rename/edit_deps mutators, and the
// claw toggle that canon 60 §4.3 moved off flat `C`), and every member key is a
// chord under its prefix.
func TestNamespaceMembership(t *testing.T) {
	km := NewKeyMap(nil)
	ns := km.Namespaces()
	if len(ns) != 2 {
		t.Fatalf("expected 2 namespaces, got %d", len(ns))
	}
	if ns[0].Prefix != "v" || len(ns[0].Bindings) != 13 {
		t.Errorf("View namespace: prefix=%q members=%d, want v/13", ns[0].Prefix, len(ns[0].Bindings))
	}
	if ns[1].Prefix != "c" || len(ns[1].Bindings) != 15 {
		t.Errorf("Change namespace: prefix=%q members=%d, want c/15", ns[1].Prefix, len(ns[1].Bindings))
	}
	for _, n := range ns {
		for _, b := range n.Bindings {
			keys := b.Keys()
			if len(keys) == 0 {
				t.Errorf("namespace %q member has no keys", n.Prefix)
				continue
			}
			// The primary key must be a chord under the prefix.
			if !strings.HasPrefix(keys[0], n.Prefix) || len(keys[0]) < 2 {
				t.Errorf("namespace %q member primary key %q is not a chord under the prefix", n.Prefix, keys[0])
			}
			// Any secondary key must be a single-char non-prefix alias.
			for _, k := range keys[1:] {
				if len(k) != 1 {
					t.Errorf("namespace %q member %q has non-single-char alias %q", n.Prefix, keys[0], k)
				}
			}
		}
	}
}

// TestChordOnly asserts the chord-only migration (sign-off E4, no deprecation
// window — the fleet precedent): every namespace member is bound to exactly one
// key, and that key is a chord under its prefix. The legacy flat aliases
// (L,b,O,w,M,p,F,S,Y,E) are gone, so each action is reachable only via its
// chord. This replaces the Phase-3 TestLegacyAliasesPresent.
func TestChordOnly(t *testing.T) {
	km := NewKeyMap(nil)
	for _, ns := range km.Namespaces() {
		for _, b := range ns.Bindings {
			keys := b.Keys()
			if len(keys) != 1 {
				t.Errorf("namespace %q member %v has %d keys, want exactly 1 (chord-only)", ns.Prefix, keys, len(keys))
				continue
			}
			if !strings.HasPrefix(keys[0], ns.Prefix) || len(keys[0]) < 2 {
				t.Errorf("namespace %q member key %q is not a chord under the prefix %q", ns.Prefix, keys[0], ns.Prefix)
			}
		}
	}
	// The rebound single-press fullscreen action likewise stays chord/single
	// only ("f", never the old flat "z").
	if keys := km.ToggleFullscreen.Keys(); len(keys) != 1 || keys[0] != "f" {
		t.Errorf("ToggleFullscreen keys = %v, want [f] only", keys)
	}
}

// TestStatusKeyMapAuditCoverage asserts the flow status TUI keymap has no
// coverage gaps: every enabled binding appears in exactly one Sections()
// entry, no help-label lies, and no empty-help bindings. If this fails, the
// disable list or the Sections() membership in NewKeyMap is wrong — fix the
// code, not the test.
func TestStatusKeyMapAuditCoverage(t *testing.T) {
	if gaps := keymap.AuditCoverage(NewKeyMap(nil)); len(gaps) != 0 {
		t.Fatalf("audit gaps: %+v", gaps)
	}
}
