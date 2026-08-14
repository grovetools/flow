package cmd

import (
	"strings"
	"testing"

	"github.com/grovetools/flow/pkg/plan_finish"
	"github.com/grovetools/flow/pkg/tui/wizards/finish"
)

// retirementItems builds the subset of the finish checklist that the
// worktree-retirement policy touches. Everything is available, nothing is
// pre-enabled — the shape applyFinishSelection receives from BuildItems.
func retirementItems() []*finish.Item {
	return []*finish.Item{
		// Order mirrors the factory: archive is emitted BEFORE prune.
		{ID: plan_finish.ItemEnvTeardown, Name: "Tear down environment", IsAvailable: true},
		{ID: plan_finish.ItemPruneBuildCaches, Name: "Evict per-worktree build caches", IsAvailable: true},
		{ID: plan_finish.ItemArchiveWorktree, Name: "Archive git worktree", IsAvailable: true},
		{ID: plan_finish.ItemPruneWorktree, Name: "Prune git worktree", IsAvailable: true},
		{ID: plan_finish.ItemClearNavBindings, Name: "Clear sessionizer keymap entries", IsAvailable: true},
		{ID: plan_finish.ItemRebuildBinaries, Name: "Rebuild binaries", IsAvailable: true},
		{ID: plan_finish.ItemMarkFinished, Name: "Mark plan as finished", IsAvailable: true},
	}
}

func enabledByID(t *testing.T, items []*finish.Item, id string) bool {
	t.Helper()
	it := plan_finish.ItemsByID(items, id)
	if it == nil {
		t.Fatalf("item %q not present in the checklist", id)
	}
	return it.IsEnabled
}

// TestYesPrefersArchiveOverPrune is the core of this change. `--yes` is the
// only path that retires a worktree unattended, so its default must be the
// recoverable one: move the container into the worktree archive (with per-repo
// bundles for unpushed history), never `rm -rf` it.
func TestYesPrefersArchiveOverPrune(t *testing.T) {
	items := retirementItems()
	applyFinishSelection(items, plan_finish.Options{Yes: true})

	if !enabledByID(t, items, plan_finish.ItemArchiveWorktree) {
		t.Error("--yes must ARCHIVE the worktree by default; archive_worktree is disabled")
	}
	if enabledByID(t, items, plan_finish.ItemPruneWorktree) {
		t.Error("--yes must NOT prune (delete) the worktree by default; prune_worktree is enabled")
	}
}

// TestYesPruneWorktreeStillPrunes pins the explicit escape hatch: deleting is
// still reachable unattended, it just has to be asked for.
func TestYesPruneWorktreeStillPrunes(t *testing.T) {
	items := retirementItems()
	applyFinishSelection(items, plan_finish.Options{Yes: true, PruneWorktree: true})

	if !enabledByID(t, items, plan_finish.ItemPruneWorktree) {
		t.Error("--yes --prune-worktree must prune; prune_worktree is disabled")
	}
	if enabledByID(t, items, plan_finish.ItemArchiveWorktree) {
		t.Error("--yes --prune-worktree must not also archive; the two race over the same container")
	}
}

// TestYesArchiveWorktreeArchives pins the pre-existing explicit behaviour,
// which this change leaves alone.
func TestYesArchiveWorktreeArchives(t *testing.T) {
	items := retirementItems()
	applyFinishSelection(items, plan_finish.Options{Yes: true, ArchiveWorktree: true})

	if !enabledByID(t, items, plan_finish.ItemArchiveWorktree) {
		t.Error("--yes --archive-worktree must archive")
	}
	if enabledByID(t, items, plan_finish.ItemPruneWorktree) {
		t.Error("--yes --archive-worktree must not prune")
	}
}

// TestYesNeverFallsBackToPruningWhenArchiveIsUnavailable is the anti-regression
// guard for the whole point of this change. If the archive item cannot run, the
// answer is NOT to delete the user's code instead — the worktree is simply left
// alone.
func TestYesNeverFallsBackToPruningWhenArchiveIsUnavailable(t *testing.T) {
	items := retirementItems()
	plan_finish.ItemsByID(items, plan_finish.ItemArchiveWorktree).IsAvailable = false
	applyFinishSelection(items, plan_finish.Options{Yes: true})

	if enabledByID(t, items, plan_finish.ItemArchiveWorktree) {
		t.Error("an unavailable archive item must not be enabled")
	}
	if enabledByID(t, items, plan_finish.ItemPruneWorktree) {
		t.Fatal("an unavailable archive must NEVER silently promote prune_worktree: " +
			"that turns 'archive failed' into 'deleted the worktree'")
	}
}

// TestYesClearsNavBindings pins that the sessionizer keymap entries are cleared
// on the --yes path. Archiving MOVES the container out from under its owner
// repos, so every keymap entry pointing into it is as dead as it would be after
// a prune.
func TestYesClearsNavBindings(t *testing.T) {
	items := retirementItems()
	applyFinishSelection(items, plan_finish.Options{Yes: true})
	if !enabledByID(t, items, plan_finish.ItemClearNavBindings) {
		t.Error("--yes must clear nav bindings: the archived container's paths are gone")
	}
}

// TestArchiveWorktreeFlagClearsNavBindings covers the explicit-flag path, which
// gated nav-binding cleanup on --prune-worktree only. Archiving leaves exactly
// the same dangling keymap entries.
func TestArchiveWorktreeFlagClearsNavBindings(t *testing.T) {
	items := retirementItems()
	applyFinishSelection(items, plan_finish.Options{ArchiveWorktree: true})

	if !enabledByID(t, items, plan_finish.ItemArchiveWorktree) {
		t.Fatal("--archive-worktree must enable archive_worktree")
	}
	if enabledByID(t, items, plan_finish.ItemPruneWorktree) {
		t.Error("--archive-worktree must not prune")
	}
	if !enabledByID(t, items, plan_finish.ItemClearNavBindings) {
		t.Error("--archive-worktree must clear nav bindings: archiving moves the container, " +
			"so its sessionizer keymap entries point at paths that no longer exist")
	}
}

// TestPruneWorktreeFlagStillClearsNavBindings pins the pre-existing behaviour
// the case above generalises.
func TestPruneWorktreeFlagStillClearsNavBindings(t *testing.T) {
	items := retirementItems()
	applyFinishSelection(items, plan_finish.Options{PruneWorktree: true})
	if !enabledByID(t, items, plan_finish.ItemClearNavBindings) {
		t.Error("--prune-worktree must still clear nav bindings")
	}
	if enabledByID(t, items, plan_finish.ItemArchiveWorktree) {
		t.Error("--prune-worktree must not archive")
	}
}

// TestNoRetirementFlagsLeavesNavBindingsAlone pins that the nav-binding cleanup
// is still tied to the worktree actually being retired, not enabled blanket.
func TestNoRetirementFlagsLeavesNavBindingsAlone(t *testing.T) {
	items := retirementItems()
	applyFinishSelection(items, plan_finish.Options{DeleteBranch: true})
	if enabledByID(t, items, plan_finish.ItemClearNavBindings) {
		t.Error("nav bindings must stay put when the worktree is not retired")
	}
}

// TestBothWorktreeFlagsStillRejected pins the mutual exclusion at the CLI
// boundary. Nothing in this change may weaken it.
func TestBothWorktreeFlagsStillRejected(t *testing.T) {
	for _, name := range []string{"plan finish", "finish"} {
		c := NewPlanFinishCmd()
		if name == "finish" {
			c = NewFinishCmd()
		}
		c.SetArgs([]string{"--prune-worktree", "--archive-worktree"})
		c.SetOut(nopWriter{})
		c.SetErr(nopWriter{})
		c.SilenceUsage = true
		c.SilenceErrors = true
		err := c.Execute()
		if err == nil {
			t.Fatalf("%s: passing both --prune-worktree and --archive-worktree must fail", name)
		}
		if !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("%s: expected a mutual-exclusion error, got: %v", name, err)
		}
	}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// TestYesKeepsRebuildBinariesOptIn guards the behaviour the --yes loop already
// had, which the retirement rework must not disturb.
func TestYesKeepsRebuildBinariesOptIn(t *testing.T) {
	items := retirementItems()
	applyFinishSelection(items, plan_finish.Options{Yes: true})
	if enabledByID(t, items, plan_finish.ItemRebuildBinaries) {
		t.Error("rebuild_binaries must stay opt-in under --yes")
	}

	items = retirementItems()
	applyFinishSelection(items, plan_finish.Options{Yes: true, RebuildBinaries: true})
	if !enabledByID(t, items, plan_finish.ItemRebuildBinaries) {
		t.Error("--yes --rebuild-binaries must enable the rebuild")
	}
	// And the retirement policy is unchanged by it.
	if !enabledByID(t, items, plan_finish.ItemArchiveWorktree) {
		t.Error("--rebuild-binaries must not change the archive-by-default policy")
	}
}
