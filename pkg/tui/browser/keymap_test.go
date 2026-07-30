package browser

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/tui/keymap"
)

// pressChord feeds each rune of a chord through handleKeyMsg in order, threading
// the returned Model forward, and yields the model plus the LAST command.
//
// Threading matters: WhichKeyHost carries a *SequenceState, so the arm buffer
// lives behind a pointer shared by every copy of the Model. Pressing "t" on a
// model value and then "tc" on the SAME value appends to one buffer ("ttc") and
// silently matches nothing — always chain through the returned model, and never
// re-press a prefix already consumed.
func pressChord(t *testing.T, m Model, chord string) (Model, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, r := range chord {
		updated, c := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m, cmd = updated.(Model), c
	}
	return m, cmd
}

// TestBrowserKeyMapAuditCoverage asserts the plan-browser keymap has no coverage
// gaps: every enabled binding (including the promoted Base fields) appears in
// exactly one Sections() entry, no help label lies about its keys, and no
// enabled binding has empty help. If this fails, the disable list or the
// Sections() membership in NewKeyMap is wrong — fix the code, not the test.
func TestBrowserKeyMapAuditCoverage(t *testing.T) {
	if gaps := keymap.AuditCoverage(NewKeyMap(nil)); len(gaps) != 0 {
		for _, g := range gaps {
			t.Errorf("keymap coverage gap: field=%s kind=%s detail=%s", g.Field, g.Kind, g.Detail)
		}
	}
}

// TestChordKeysAndNamespaceCompleteness pins the canon-60 migration: each
// migrated action carries exactly its chord (E4 — no legacy flat alias), and
// every chord prefix in use is actually DECLARED as a namespace. An undeclared
// prefix is not a cosmetic gap: ProcessChord only arms prefixes it is handed via
// Namespaces(), so the chord would never fire at all.
func TestChordKeysAndNamespaceCompleteness(t *testing.T) {
	km := NewKeyMap(nil)

	for _, tc := range []struct {
		name string
		keys []string
		want string
	}{
		{"ToggleGitLog", km.ToggleGitLog.Keys(), "tg"},
		{"ToggleHold", km.ToggleHold.Keys(), "th"},
		{"ToggleArchived", km.ToggleArchived.Keys(), "ta"},
		{"ToggleColumns", km.ToggleColumns.Keys(), "tc"},
		{"ViewGit", km.ViewGit.Keys(), "vg"},
		{"SetHoldStatus", km.SetHoldStatus.Keys(), "ch"},
	} {
		if len(tc.keys) != 1 || tc.keys[0] != tc.want {
			t.Errorf("%s keys = %v, want single [%q]", tc.name, tc.keys, tc.want)
		}
	}

	declared := map[string]bool{}
	for _, ns := range km.Namespaces() {
		declared[ns.Prefix] = true
	}
	for _, ns := range km.Namespaces() {
		for _, b := range ns.Bindings {
			for _, k := range b.Keys() {
				if len(k) > 1 && !declared[k[:1]] {
					t.Errorf("chord %q uses prefix %q with no declared namespace — it can never arm", k, k[:1])
				}
			}
		}
	}
}

// TestTopAndBottomAreTheCanonicalMotions guards the §7.1 field rename. The keys
// did not move: Bottom keeps end/G and Top gains the fleet-standard gg alongside
// home. Only the FIELD (and therefore the ConfigKey) changed, from Home/End to
// Top/Bottom, which is what clears the reserved-key violation. Do NOT "fix" this
// with an "end"->"bottom" NormalizeAction alias — that breaks the currently-clean
// bottom consistency check.
func TestTopAndBottomAreTheCanonicalMotions(t *testing.T) {
	km := NewKeyMap(nil)
	if got := km.Top.Keys(); len(got) != 2 || got[0] != "gg" || got[1] != "home" {
		t.Errorf("Top keys = %v, want [gg home]", got)
	}
	if got := km.Bottom.Keys(); len(got) != 2 || got[0] != "end" || got[1] != "G" {
		t.Errorf("Bottom keys = %v, want [end G]", got)
	}
}

// TestFlatHomeSurvivesTheChordSeam is the regression guard for the re-synthesis
// bug. Top carries a chord ("gg") AND a flat key ("home"); the seam used to
// rewrite every matched chord to Keys()[0] unconditionally, so a flat press was
// silently replaced by the chord. The guard only re-synthesizes when the pressed
// key is not already one of the matched binding's keys.
func TestFlatHomeSurvivesTheChordSeam(t *testing.T) {
	m := Model{plans: viewportPlans(5), cursor: 4, keys: NewKeyMap(nil)}
	updated, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyHome})
	if got := updated.(Model).cursor; got != 0 {
		t.Fatalf("flat home did not reach Top: cursor = %d, want 0", got)
	}

	m = Model{plans: viewportPlans(5), cursor: 4, keys: NewKeyMap(nil)}
	m, _ = pressChord(t, m, "gg")
	if m.cursor != 0 {
		t.Fatalf("gg did not reach Top: cursor = %d, want 0", m.cursor)
	}
}

// TestStrayKeyClosesAnArmedNamespace pins the which-key idiom: once a namespace
// prefix is armed, a non-continuation key closes the popup and is SWALLOWED
// rather than firing its own flat action ("t" then "q" must not quit).
func TestStrayKeyClosesAnArmedNamespace(t *testing.T) {
	m := Model{plans: viewportPlans(5), cursor: 2, keys: NewKeyMap(nil)}
	m, _ = pressChord(t, m, "t")
	updated, cmd := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd != nil {
		t.Fatal("a stray key while t… was armed fell through to the flat switch")
	}
	if updated.(Model).cursor != 2 {
		t.Fatal("a stray key while t… was armed mutated state")
	}
}
