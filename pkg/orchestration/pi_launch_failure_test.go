package orchestration

import (
	"context"
	"testing"
)

func TestEndFailedPiLaunchAttemptUsesExactAttemptOnce(t *testing.T) {
	ender := &recordingSessionEnder{}
	job := &Job{ID: "job-1", AttemptID: "attempt-new"}

	if err := endFailedPiLaunchAttempt(context.Background(), ender, job, true); err != nil {
		t.Fatal(err)
	}
	if ender.calls != 1 {
		t.Fatalf("EndSession calls = %d, want exactly 1", ender.calls)
	}
	if ender.jobID != job.ID || ender.attemptID != job.AttemptID || ender.outcome != string(JobStatusFailed) {
		t.Fatalf("EndSession = (%q, %q, %q), want (%q, %q, %q)",
			ender.jobID, ender.attemptID, ender.outcome, job.ID, job.AttemptID, JobStatusFailed)
	}
}

func TestEndFailedPiLaunchAttemptNeverUsesBroadJobAlias(t *testing.T) {
	for _, tc := range []struct {
		name             string
		job              *Job
		intentRegistered bool
	}{
		{name: "missing attempt", job: &Job{ID: "job-1"}, intentRegistered: true},
		{name: "intent not registered", job: &Job{ID: "job-1", AttemptID: "attempt-new"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ender := &recordingSessionEnder{}
			if err := endFailedPiLaunchAttempt(context.Background(), ender, tc.job, tc.intentRegistered); err != nil {
				t.Fatal(err)
			}
			if ender.calls != 0 {
				t.Fatalf("EndSession calls = %d, want 0 to protect another attempt", ender.calls)
			}
		})
	}
}
