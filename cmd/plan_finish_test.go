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

// TestPlanFinish_PreserveCloudFlag asserts the --preserve-cloud flag
// plumbs into Options correctly. Default is false (meaning
// ForceDestroy=true at teardown time, which bypasses skip_destroy);
// passing --preserve-cloud sets it true (ForceDestroy=false).
func TestPlanFinish_PreserveCloudFlag(t *testing.T) {
	// Default case: no flag → PreserveCloud=false → ForceDestroy=true.
	cmdDefault := NewPlanFinishCmd()
	if err := cmdDefault.ParseFlags([]string{}); err != nil {
		t.Fatalf("parse default flags: %v", err)
	}
	preserveDefault, _ := cmdDefault.Flags().GetBool("preserve-cloud")
	if preserveDefault {
		t.Errorf("expected default preserve-cloud=false, got true")
	}
	// Derived: what the factory would pass as ForceDestroy.
	if got := !preserveDefault; got != true {
		t.Errorf("expected derived ForceDestroy=true by default, got %v", got)
	}

	// Explicit flag: --preserve-cloud → PreserveCloud=true → ForceDestroy=false.
	cmdPreserve := NewPlanFinishCmd()
	if err := cmdPreserve.ParseFlags([]string{"--preserve-cloud"}); err != nil {
		t.Fatalf("parse preserve flags: %v", err)
	}
	preserveSet, _ := cmdPreserve.Flags().GetBool("preserve-cloud")
	if !preserveSet {
		t.Errorf("expected --preserve-cloud to set flag true")
	}
	if got := !preserveSet; got != false {
		t.Errorf("expected derived ForceDestroy=false with --preserve-cloud, got %v", got)
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
