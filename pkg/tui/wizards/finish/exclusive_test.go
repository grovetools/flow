package finish

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The two worktree-retirement item IDs, duplicated here rather than imported:
// pkg/plan_finish imports this package, so this package cannot import it back.
const (
	idArchiveWorktree = "archive_worktree"
	idPruneWorktree   = "prune_worktree"
)

// keySelectAll / keyDown / keyUp / keyToggle are the default (vim preset)
// bindings, resolved from an explicit keymap so the test never depends on the
// developer's own config.
var (
	hermeticKeys = NewKeyMap(nil)
	keySelectAll = tea.KeyMsg{Type: tea.KeyCtrlA}
	keyDown      = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}
	keyUp        = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")}
	keyToggle    = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")}
)

// retirementWizard builds a wizard whose checklist offers BOTH worktree
// retirements, in the order the factory emits them (archive first). Nothing is
// pre-ticked, matching the hosted wizard.
func retirementWizard(t *testing.T) Model {
	t.Helper()
	km := hermeticKeys
	return New(Config{
		PlanName: "my-plan",
		KeyMap:   &km,
		Items: []*Item{
			{ID: "env_teardown", Name: "Tear down environment", Status: "Ready", IsAvailable: true},
			{
				ID: idArchiveWorktree, Name: "Archive git worktree", Status: "Exists",
				IsAvailable: true, ExclusiveGroup: GroupWorktreeRetirement,
			},
			{
				ID: idPruneWorktree, Name: "Prune git worktree", Status: "Exists",
				IsAvailable: true, ExclusiveGroup: GroupWorktreeRetirement,
			},
			{ID: "mark_finished", Name: "Mark plan as finished", Status: "Ready", IsAvailable: true},
		},
	})
}

func itemByID(t *testing.T, m Model, id string) *Item {
	t.Helper()
	for _, it := range m.items {
		if it != nil && it.ID == id {
			return it
		}
	}
	t.Fatalf("item %q missing", id)
	return nil
}

// TestSelectAllPicksExactlyOneWorktreeRetirement is the regression test for the
// Select All defect. archive_worktree and prune_worktree both retire the SAME
// container and are mutually exclusive; a single "select all" keystroke used to
// tick both, which runs the archive (moving the container away) and then the
// prune (against a path that no longer exists) in the same run.
func TestSelectAllPicksExactlyOneWorktreeRetirement(t *testing.T) {
	m := retirementWizard(t)
	updated, _ := m.Update(keySelectAll)
	m = updated.(Model)

	archive := itemByID(t, m, idArchiveWorktree)
	prune := itemByID(t, m, idPruneWorktree)

	if archive.IsEnabled && prune.IsEnabled {
		t.Fatal("Select All enabled BOTH archive_worktree and prune_worktree: " +
			"they are mutually exclusive retirements of the same container")
	}
	if !archive.IsEnabled {
		t.Error("Select All must pick the recoverable retirement (archive_worktree)")
	}
	if prune.IsEnabled {
		t.Error("Select All must not pick prune_worktree, which destroys the container")
	}
	// Non-exclusive items are unaffected.
	if !itemByID(t, m, "env_teardown").IsEnabled || !itemByID(t, m, "mark_finished").IsEnabled {
		t.Error("Select All must still enable every non-exclusive available item")
	}
}

// TestTogglingOneRetirementClearsTheOther covers the manual route to the same
// invalid selection: ticking archive and then prune by hand.
func TestTogglingOneRetirementClearsTheOther(t *testing.T) {
	m := retirementWizard(t)
	// Cursor starts on the first available item (env_teardown); walk to archive.
	updated, _ := m.Update(keyDown) // -> archive
	m = updated.(Model)
	updated, _ = m.Update(keyToggle) // tick archive
	m = updated.(Model)
	if !itemByID(t, m, idArchiveWorktree).IsEnabled {
		t.Fatal("space should have ticked archive_worktree")
	}

	updated, _ = m.Update(keyDown) // -> prune
	m = updated.(Model)
	updated, _ = m.Update(keyToggle) // tick prune
	m = updated.(Model)

	if !itemByID(t, m, idPruneWorktree).IsEnabled {
		t.Error("space should have ticked prune_worktree")
	}
	if itemByID(t, m, idArchiveWorktree).IsEnabled {
		t.Error("ticking prune_worktree must untick archive_worktree: only one may run")
	}

	// And back the other way.
	updated, _ = m.Update(keyUp) // -> archive
	m = updated.(Model)
	updated, _ = m.Update(keyToggle)
	m = updated.(Model)
	if !itemByID(t, m, idArchiveWorktree).IsEnabled {
		t.Error("archive_worktree should be ticked again")
	}
	if itemByID(t, m, idPruneWorktree).IsEnabled {
		t.Error("ticking archive_worktree must untick prune_worktree")
	}
}

// TestUntickingAnExclusiveItemLeavesTheGroupEmpty pins that the exclusion is
// "at most one", not "exactly one": the user may decline to retire the worktree.
func TestUntickingAnExclusiveItemLeavesTheGroupEmpty(t *testing.T) {
	m := retirementWizard(t)
	updated, _ := m.Update(keySelectAll)
	m = updated.(Model)
	updated, _ = m.Update(keyDown) // -> archive
	m = updated.(Model)
	updated, _ = m.Update(keyToggle) // untick archive
	m = updated.(Model)

	if itemByID(t, m, idArchiveWorktree).IsEnabled {
		t.Error("unticking archive_worktree must leave it off")
	}
	if itemByID(t, m, idPruneWorktree).IsEnabled {
		t.Error("unticking one exclusive item must not promote its peer")
	}
}
