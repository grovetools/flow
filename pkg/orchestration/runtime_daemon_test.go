package orchestration

import (
	"testing"
)

// mockStatusUpdater tracks calls to UpdateJobStatus for testing.
type mockStatusUpdater struct {
	calls []struct {
		job    *Job
		status JobStatus
	}
}

func (m *mockStatusUpdater) UpdateJobStatus(job *Job, status JobStatus) error {
	m.calls = append(m.calls, struct {
		job    *Job
		status JobStatus
	}{job, status})
	job.Status = status
	return nil
}

func (m *mockStatusUpdater) UpdateJobMetadata(job *Job, meta JobMetadata) error {
	return nil
}

type noopLogger struct{}

func (l *noopLogger) Info(msg string, keysAndValues ...interface{})  {}
func (l *noopLogger) Error(msg string, keysAndValues ...interface{}) {}
func (l *noopLogger) Debug(msg string, keysAndValues ...interface{}) {}

func TestHandleTerminalStatus_Running(t *testing.T) {
	updater := &mockStatusUpdater{}
	rt := &DaemonRuntime{
		updater: updater,
		logger:  &noopLogger{},
	}

	job := &Job{
		ID:     "test-job",
		Status: JobStatusRunning,
	}

	err := rt.handleTerminalStatus(job, "running", "")
	if err != nil {
		t.Errorf("handleTerminalStatus('running') should return nil, got: %v", err)
	}

	// The updater should NOT have been called — running status is preserved as-is
	if len(updater.calls) != 0 {
		t.Errorf("Expected no status updates, got %d calls", len(updater.calls))
	}

	// Job status should remain running
	if job.Status != JobStatusRunning {
		t.Errorf("Job status should remain running, got %s", job.Status)
	}
}

func TestHandleTerminalStatus_Completed(t *testing.T) {
	updater := &mockStatusUpdater{}
	rt := &DaemonRuntime{
		updater: updater,
		logger:  &noopLogger{},
	}

	job := &Job{
		ID:     "test-job",
		Status: JobStatusRunning,
	}

	err := rt.handleTerminalStatus(job, "completed", "")
	if err != nil {
		t.Errorf("handleTerminalStatus('completed') should return nil, got: %v", err)
	}

	if len(updater.calls) != 1 {
		t.Fatalf("Expected 1 status update, got %d", len(updater.calls))
	}
	if updater.calls[0].status != JobStatusCompleted {
		t.Errorf("Expected status update to completed, got %s", updater.calls[0].status)
	}
}

func TestHandleTerminalStatus_Failed(t *testing.T) {
	updater := &mockStatusUpdater{}
	rt := &DaemonRuntime{
		updater: updater,
		logger:  &noopLogger{},
	}

	job := &Job{
		ID:     "test-job",
		Status: JobStatusRunning,
	}

	err := rt.handleTerminalStatus(job, "failed", "something went wrong")
	if err == nil {
		t.Errorf("handleTerminalStatus('failed') should return an error")
	}

	if len(updater.calls) != 1 {
		t.Fatalf("Expected 1 status update, got %d", len(updater.calls))
	}
	if updater.calls[0].status != JobStatusFailed {
		t.Errorf("Expected status update to failed, got %s", updater.calls[0].status)
	}
}

func TestHandleTerminalStatus_PendingUser(t *testing.T) {
	updater := &mockStatusUpdater{}
	rt := &DaemonRuntime{
		updater: updater,
		logger:  &noopLogger{},
	}

	job := &Job{
		ID:     "test-job",
		Status: JobStatusRunning,
	}

	err := rt.handleTerminalStatus(job, "pending_user", "")
	if err != nil {
		t.Errorf("handleTerminalStatus('pending_user') should return nil, got: %v", err)
	}

	if len(updater.calls) != 1 {
		t.Fatalf("Expected 1 status update, got %d", len(updater.calls))
	}
	if updater.calls[0].status != JobStatusPendingUser {
		t.Errorf("Expected status update to pending_user, got %s", updater.calls[0].status)
	}
}
