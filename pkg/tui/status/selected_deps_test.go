package status

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/tui/keymap"
)

// TestSelectedJobFilenamesIsPlanOrdered pins the accessor hosts read when they
// open the add-job wizard: the space-selection must come back in plan order,
// not the random order of the Selected map.
func TestSelectedJobFilenamesIsPlanOrdered(t *testing.T) {
	m := newDisplayTestModel(testJob("a"), testJob("b"), testJob("c"))
	m.Selected["c"] = true
	m.Selected["a"] = true

	got := m.SelectedJobFilenames()
	want := []string{"a.md", "c.md"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SelectedJobFilenames() = %v, want %v", got, want)
	}
}

func TestSelectedJobFilenamesEmptyWithoutSelection(t *testing.T) {
	m := newDisplayTestModel(testJob("a"), testJob("b"))
	if got := m.SelectedJobFilenames(); len(got) != 0 {
		t.Errorf("SelectedJobFilenames() = %v, want empty", got)
	}
}

// TestSelectedJobFilenamesSkipsFilelessJobs guards the dependency picker from
// blank entries: deps are matched by filename, so a job without one can't be
// depended on.
func TestSelectedJobFilenamesSkipsFilelessJobs(t *testing.T) {
	fileless := testJob("b")
	fileless.Filename = ""
	m := newDisplayTestModel(testJob("a"), fileless)
	m.Selected["a"] = true
	m.Selected["b"] = true

	got := m.SelectedJobFilenames()
	want := []string{"a.md"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SelectedJobFilenames() = %v, want %v", got, want)
	}
}

// TestSpaceSelectionFeedsSelectedJobFilenames walks the real key path: space
// toggles the row under the cursor, and that toggle is what the add-job wizard
// reads as its initial dependencies.
func TestSpaceSelectionFeedsSelectedJobFilenames(t *testing.T) {
	m := newDisplayTestModel(testJob("a"), testJob("b"), testJob("c"))
	m.KeyMap = NewKeyMap(nil)
	m.WhichKey = keymap.NewWhichKeyHost(nil, m.KeyMap.Namespaces()...)
	m.Cursor = 1

	space := tea.KeyMsg{Type: tea.KeySpace}
	updated, _ := m.Update(space)
	sm, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want status.Model", updated)
	}

	got := sm.SelectedJobFilenames()
	want := []string{"b.md"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after space on row 1: SelectedJobFilenames() = %v, want %v", got, want)
	}

	// Space again clears it — no stale dependency left behind.
	updated, _ = sm.Update(space)
	sm = updated.(Model)
	if got := sm.SelectedJobFilenames(); len(got) != 0 {
		t.Errorf("after second space: SelectedJobFilenames() = %v, want empty", got)
	}
}
