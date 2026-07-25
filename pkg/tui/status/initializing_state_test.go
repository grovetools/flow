package status

import (
	"testing"
	"time"

	"github.com/grovetools/flow/pkg/orchestration"
)

func TestIsInitializing(t *testing.T) {
	job := &orchestration.Job{ID: "job-1", Status: orchestration.JobStatusPending}

	m := Model{}
	if m.isInitializing(job) {
		t.Error("unmarked job should not be initializing")
	}

	m.InitializingJobs = map[string]time.Time{"job-1": time.Now()}
	if !m.isInitializing(job) {
		t.Error("freshly submitted pending job should be initializing")
	}

	// Once the store reports the launch, the real status supersedes the marker.
	job.Status = orchestration.JobStatusRunning
	if m.isInitializing(job) {
		t.Error("running job should not render as initializing")
	}
	job.Status = orchestration.JobStatusCompleted
	if m.isInitializing(job) {
		t.Error("completed job should not render as initializing")
	}

	// A marker older than the grace window expires even if the status never moved.
	job.Status = orchestration.JobStatusPending
	m.InitializingJobs["job-1"] = time.Now().Add(-initializingGrace - time.Second)
	if m.isInitializing(job) {
		t.Error("marker past the grace window should expire")
	}
}

func TestJobStatusIconInitializingOverridesPending(t *testing.T) {
	job := &orchestration.Job{ID: "job-1", Status: orchestration.JobStatusPending}

	m := Model{}
	pendingIcon := m.jobStatusIcon(job)

	m.InitializingJobs = map[string]time.Time{"job-1": time.Now()}
	initializingIcon := m.jobStatusIcon(job)

	if initializingIcon == pendingIcon {
		t.Error("initializing job should render a distinct icon from pending")
	}

	job.Status = orchestration.JobStatusRunning
	if got := m.jobStatusIcon(job); got != m.getStatusIcon(orchestration.JobStatusRunning) {
		t.Errorf("running job should use the real status icon, got %q", got)
	}
}
