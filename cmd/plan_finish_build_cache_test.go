package cmd

import (
	"testing"

	"github.com/grovetools/flow/pkg/plan_finish"
	"github.com/grovetools/flow/pkg/tui/wizards/finish"
)

// Per-worktree build caches under ~/.cache/grove are keyed by the container's
// ABSOLUTE PATH. Both retirements invalidate that key — prune deletes the path,
// archive moves the container elsewhere — so both strand the entries with no
// way left to name them. These tests pin that the eviction item is enabled on
// EITHER route, the same rule the nav-binding cleanup follows.

func TestArchiveWorktreeFlagEvictsBuildCaches(t *testing.T) {
	items := retirementItems()
	applyFinishSelection(items, plan_finish.Options{ArchiveWorktree: true})

	if !enabledByID(t, items, plan_finish.ItemPruneBuildCaches) {
		t.Error("--archive-worktree must evict build caches: archiving moves the container, " +
			"so its path-keyed cache entries are stranded exactly as a prune would strand them")
	}
}

func TestPruneWorktreeFlagEvictsBuildCaches(t *testing.T) {
	items := retirementItems()
	applyFinishSelection(items, plan_finish.Options{PruneWorktree: true})

	if !enabledByID(t, items, plan_finish.ItemPruneBuildCaches) {
		t.Error("--prune-worktree must evict build caches; the container path is about to be deleted")
	}
}

func TestYesEvictsBuildCaches(t *testing.T) {
	items := retirementItems()
	applyFinishSelection(items, plan_finish.Options{Yes: true})

	if !enabledByID(t, items, plan_finish.ItemPruneBuildCaches) {
		t.Error("--yes archives the worktree by default, so it must also evict the caches keyed by its path")
	}
}

// TestBuildCacheEvictionNeedsARetirement pins the other half: a finish that
// keeps the worktree must not evict its caches, which would only force a
// needless full rebuild in a worktree the user is still using.
func TestBuildCacheEvictionNeedsARetirement(t *testing.T) {
	items := retirementItems()
	applyFinishSelection(items, plan_finish.Options{CloseSession: true})

	if enabledByID(t, items, plan_finish.ItemPruneBuildCaches) {
		t.Error("a finish that retires nothing must leave the worktree's build caches alone")
	}
}

// TestBuildCacheEvictionRunsBeforeRetirementOnTheCLIHost is the host-side half
// of the ordering guarantee. The factory emits prune_build_caches ahead of both
// retirement items; this pins that executeFinishActions — the CLI host — runs
// enabled actions in that slice order, so eviction happens while the container
// (and the make target inside it) still exists.
func TestBuildCacheEvictionRunsBeforeRetirementOnTheCLIHost(t *testing.T) {
	var order []string
	items := []*finish.Item{
		{
			ID: plan_finish.ItemPruneBuildCaches, Name: "Evict per-worktree build caches",
			IsEnabled: true, IsAvailable: true,
			Action: func() error { order = append(order, "evict"); return nil },
		},
		{
			ID: plan_finish.ItemArchiveWorktree, Name: "Archive git worktree",
			IsEnabled: true, IsAvailable: true,
			Action: func() error { order = append(order, "archive"); return nil },
		},
		{
			ID: plan_finish.ItemPruneWorktree, Name: "Prune git worktree",
			IsEnabled: true, IsAvailable: true,
			Action: func() error { order = append(order, "prune"); return nil },
		},
	}
	if err := executeFinishActions(items); err != nil {
		t.Fatalf("executeFinishActions: %v", err)
	}
	if len(order) != 3 || order[0] != "evict" {
		t.Fatalf("action order was %v, want the eviction first — after either retirement the "+
			"container is gone or moved and the make target cannot be invoked", order)
	}
}
