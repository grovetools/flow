package orchestration

import (
	"context"
	"testing"

	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/mux"
)

// Terminal cleanup during job completion is best-effort. It used to be able to
// kill the whole process: DetectMuxEngine returned an error alongside an
// interface holding a nil *TuimuxEngine, the `engine != nil` guard accepted it,
// and the follow-up ListWindows panicked on the nil receiver — before
// CompleteJob had persisted anything. Children that had already published a
// valid final report were left stranded at idle, and every retry panicked in
// the same place.

func TestCloseAgentWindowSurvivesUnavailableMux(t *testing.T) {
	// tmux selected, nothing on PATH: detection fails and yields no engine.
	t.Setenv("GROVE_MUX", "tmux")
	t.Setenv("PATH", "")
	mux.ResetDetection()
	t.Cleanup(mux.ResetDetection)

	if engine, err := mux.DetectMuxEngine(context.Background()); err == nil || engine != nil {
		t.Fatalf("test precondition: expected detection to fail with no engine, got engine=%v err=%v", engine, err)
	}

	// No engine method may be reached, so returning at all is the assertion.
	closeAgentWindow("some_session", "job-example", true)
}

func TestRunNonFatalCleanupContainsPanicSoCompletionContinues(t *testing.T) {
	logger := grovelogging.NewLogger("test.job.complete")

	ran := false
	runNonFatalCleanup(logger, "panicking cleanup", true, func() {
		ran = true
		// Exactly the historical failure: a nil concrete engine reached
		// through a non-nil MuxEngine interface.
		var engine mux.MuxEngine = (*mux.TmuxEngine)(nil)
		_, _ = engine.ListWindows(context.Background(), "session")
	})

	if !ran {
		t.Fatal("cleanup body never ran")
	}
	// Reaching here is what CompleteJob relies on: the status write and the
	// archival steps that follow cleanup still execute.
}

func TestRunNonFatalCleanupPassesThroughSuccess(t *testing.T) {
	logger := grovelogging.NewLogger("test.job.complete")

	calls := 0
	runNonFatalCleanup(logger, "ordinary cleanup", true, func() { calls++ })
	if calls != 1 {
		t.Fatalf("expected cleanup to run exactly once, ran %d times", calls)
	}
}

// A typed-nil engine must be rejected by the guard rather than called. This
// pins the guard closeAgentWindow uses; mux.IsNilEngine is the only check that
// sees through the interface.
func TestTypedNilEngineIsRejectedByGuard(t *testing.T) {
	var engine mux.MuxEngine = (*mux.TmuxEngine)(nil)

	if engine == nil {
		t.Fatal("expected a typed nil to compare non-nil; this test no longer covers the hazard")
	}
	if !mux.IsNilEngine(engine) {
		t.Fatal("mux.IsNilEngine accepted a typed-nil engine; cleanup would panic on it")
	}
}
