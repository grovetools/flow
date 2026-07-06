package orchestration

import (
	"context"
	"testing"
)

// fakeDetachExecutor mimics a detaching agent executor: Execute returns nil
// while deliberately leaving the job in `running` (the real headless executor
// launches the agent and returns before it exits).
type fakeDetachExecutor struct{}

func (f *fakeDetachExecutor) Execute(ctx context.Context, job *Job, plan *Plan) error { return nil }
func (f *fakeDetachExecutor) Name() string                                            { return "fake-detach" }

// TestLocalRuntime_HeadlessKeepsRunning asserts the A1/A6 exclusion: a detached
// headless job whose Execute returns nil must be left at `running` by
// LocalRuntime.ExecuteJob — the exit watcher (FinalizeHeadlessJob) writes the
// real terminal status later. Stamping `completed` here at detach was the
// premature-completion bug.
func TestLocalRuntime_HeadlessKeepsRunning(t *testing.T) {
	plan, job := writeHeadlessJobFixture(t, JobStatusPending)

	rt := NewLocalRuntime(&ExecutorConfig{}, nil, NewStatePersister(), &noopLogger{})
	rt.SetExecutor(JobTypeHeadlessAgent, &fakeDetachExecutor{})

	if err := rt.ExecuteJob(context.Background(), job, plan); err != nil {
		t.Fatalf("ExecuteJob: %v", err)
	}

	// The runtime moved it to running (step 2) and must NOT have stamped a
	// terminal status on the nil-error detach.
	assertFrontmatterStatus(t, job.FilePath, "running")
	if job.Status == JobStatusCompleted {
		t.Errorf("headless job was wrongly stamped completed at detach")
	}
}
