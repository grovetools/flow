package cmd

import (
	"errors"
	"testing"

	"github.com/grovetools/flow/pkg/plan_finish"
	"github.com/grovetools/flow/pkg/tui/wizards/finish"
)

// TestExecuteFinishActions_SkipsArchiveOnEarlierFailure simulates a
// failing prune step and asserts the archive_plan + mark_finished
// actions are SKIPPED (their Action closures never fire) and that
// executeFinishActions returns the first error so the caller can
// refuse to unset the active plan / exit non-zero.
func TestExecuteFinishActions_SkipsArchiveOnEarlierFailure(t *testing.T) {
	sentinel := errors.New("prune failed")

	pruneRan := false
	archiveRan := false
	markRan := false

	items := []*finish.Item{
		{
			ID:          plan_finish.ItemPruneWorktree,
			Name:        "Prune git worktree",
			IsEnabled:   true,
			IsAvailable: true,
			Action: func() error {
				pruneRan = true
				return sentinel
			},
		},
		{
			ID:          plan_finish.ItemMarkFinished,
			Name:        "Mark plan as finished",
			IsEnabled:   true,
			IsAvailable: true,
			Action: func() error {
				markRan = true
				return nil
			},
		},
		{
			ID:          plan_finish.ItemArchivePlan,
			Name:        "Archive plan directory",
			IsEnabled:   true,
			IsAvailable: true,
			Action: func() error {
				archiveRan = true
				return nil
			},
		},
	}

	err := executeFinishActions(items)
	if err == nil {
		t.Fatal("expected non-nil error after prune failure")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got: %v", err)
	}
	if !pruneRan {
		t.Error("prune should have run")
	}
	if archiveRan {
		t.Error("archive_plan must be SKIPPED after earlier failure; it ran")
	}
	if markRan {
		t.Error("mark_finished must be SKIPPED after earlier failure; it ran")
	}
}

// TestExecuteFinishActions_NoFailureRunsTerminals asserts the normal
// path: with no failures, archive_plan and mark_finished run.
func TestExecuteFinishActions_NoFailureRunsTerminals(t *testing.T) {
	archiveRan := false
	markRan := false
	items := []*finish.Item{
		{
			ID:        plan_finish.ItemMarkFinished,
			Name:      "Mark finished",
			IsEnabled: true,
			Action:    func() error { markRan = true; return nil },
		},
		{
			ID:        plan_finish.ItemArchivePlan,
			Name:      "Archive",
			IsEnabled: true,
			Action:    func() error { archiveRan = true; return nil },
		},
	}
	if err := executeFinishActions(items); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !markRan || !archiveRan {
		t.Errorf("terminal items should have run: mark=%v archive=%v", markRan, archiveRan)
	}
}
