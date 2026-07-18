package status

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/flow/pkg/orchestration"
)

// TestChangeNamespaceComplete asserts the Change (c…) namespace is chord-only and
// complete: every member is bound to a single chord under the "c" prefix, the
// second chars are unique (so SequenceState prefix matching is unambiguous), and
// the migrated flat mutators "R" (rename) and "ctrl+o" (edit deps) appear nowhere
// in the registry.
func TestChangeNamespaceComplete(t *testing.T) {
	km := NewKeyMap(nil)
	ns := km.Namespaces()
	change := ns[1]
	if change.Prefix != "c" {
		t.Fatalf("expected Change namespace at index 1, got prefix %q", change.Prefix)
	}

	seen := map[string]bool{}
	for _, b := range change.Bindings {
		keys := b.Keys()
		if len(keys) != 1 {
			t.Errorf("Change member %v has %d keys, want exactly 1 (chord-only)", keys, len(keys))
			continue
		}
		k := keys[0]
		if !strings.HasPrefix(k, "c") || len(k) != 2 {
			t.Errorf("Change member key %q is not a two-char chord under the c prefix", k)
		}
		if seen[k] {
			t.Errorf("duplicate Change chord %q", k)
		}
		seen[k] = true
	}

	// No flat "R" or "ctrl+o" anywhere in the registry.
	for _, b := range allBindings(t) {
		for _, k := range b.Keys {
			if k == "R" || k == "ctrl+o" {
				t.Errorf("binding %q (%s) still carries retired flat key %q — must be chord-only", b.Name, b.ConfigKey, k)
			}
		}
	}
}

// changeDispatchModel builds a real Model over a one-job plan and forces the
// which-key show-delay to 0, so chord dispatch through Update() resolves the c…
// namespace and falls through to the flat action switch.
func changeDispatchModel(t *testing.T) Model {
	t.Helper()
	job := &orchestration.Job{ID: "j1", Filename: "j1.md", Title: "job one", Status: orchestration.JobStatusPending, Type: orchestration.JobTypeChat}
	plan := &orchestration.Plan{
		Name:     "t",
		Jobs:     []*orchestration.Job{job},
		JobsByID: map[string]*orchestration.Job{job.ID: job},
	}
	graph, err := orchestration.BuildDependencyGraph(plan)
	if err != nil {
		t.Fatalf("BuildDependencyGraph: %v", err)
	}
	m := New(Config{Plan: plan, Graph: graph})
	mdl, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = mdl.(Model)
	m.WhichKey.Delay = 0
	return m
}

// driveRune feeds one rune-key (single- or multi-char) through Update.
func driveRune(t *testing.T, m Model, s string) (Model, tea.Cmd) {
	t.Helper()
	mdl, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	return mdl.(Model), cmd
}

// TestChordDispatchOpensFieldEditor: the cm chord arms on "c" then resolves on
// "m" into the schema-driven field editor for the model field.
func TestChordDispatchOpensFieldEditor(t *testing.T) {
	m := changeDispatchModel(t)
	m, _ = driveRune(t, m, "c")
	if !m.WhichKey.IsPending() {
		t.Fatalf("'c' should arm the Change namespace")
	}
	m, _ = driveRune(t, m, "m")
	if m.fieldEditor == nil {
		t.Fatalf("cm should open the field editor")
	}
	if m.fieldEditor.desc.Key != "model" {
		t.Errorf("cm opened editor for %q, want model", m.fieldEditor.desc.Key)
	}
}

// TestChordDispatchToggleDirect: the cM chord toggles memory directly without
// opening the field editor (toggles dispatch straight to setJobFieldCmd).
func TestChordDispatchToggleDirect(t *testing.T) {
	m := changeDispatchModel(t)
	m, _ = driveRune(t, m, "c")
	m, cmd := driveRune(t, m, "M")
	if m.fieldEditor != nil {
		t.Errorf("cM must not open the field editor — it toggles in place")
	}
	if cmd == nil {
		t.Errorf("cM should dispatch a toggle command")
	}
}

// TestChordDispatchRename: the cn chord enters the bespoke Renaming flow.
func TestChordDispatchRename(t *testing.T) {
	m := changeDispatchModel(t)
	m, _ = driveRune(t, m, "c")
	m, _ = driveRune(t, m, "n")
	if !m.Renaming {
		t.Errorf("cn should enter the Renaming flow")
	}
	if m.fieldEditor != nil {
		t.Errorf("cn must not open the field editor — rename stays bespoke")
	}
}

// TestChordDispatchEditDeps: the cd chord enters the bespoke EditingDeps editor.
func TestChordDispatchEditDeps(t *testing.T) {
	m := changeDispatchModel(t)
	m, _ = driveRune(t, m, "c")
	m, _ = driveRune(t, m, "d")
	if !m.EditingDeps {
		t.Errorf("cd should enter the EditingDeps editor")
	}
	if m.fieldEditor != nil {
		t.Errorf("cd must not open the field editor — deps stays bespoke")
	}
}
