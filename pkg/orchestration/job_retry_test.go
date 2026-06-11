package orchestration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func createTestJobFile(t *testing.T, dir, filename, status string) *Job {
	// Build frontmatter
	frontmatter := `---
id: test-job-1
status: ` + status + `
title: Test Job
type: interactive_agent
template: generic
created: 2026-01-01T00:00:00Z
modified: 2026-01-01T00:00:00Z
`

	// Add error fields if status is failed
	if status == string(JobStatusFailed) {
		frontmatter += `last_error: "transient API error"
completed_at: 2026-01-02T00:00:00Z
duration: 5m30s
`
	}

	// Add completed_at if status is completed
	if status == string(JobStatusCompleted) {
		frontmatter += `completed_at: 2026-01-02T00:00:00Z
started_at: 2026-01-02T00:00:00Z
`
	}

	frontmatter += `---

# Test Job

This is a test job file.
`

	jobPath := filepath.Join(dir, filename)
	if err := os.WriteFile(jobPath, []byte(frontmatter), 0o644); err != nil {
		t.Fatalf("failed to create test job file: %v", err)
	}

	job := &Job{
		ID:       "test-job-1",
		Filename: filename,
		FilePath: jobPath,
		Title:    "Test Job",
		Type:     JobTypeInteractiveAgent,
		Status:   JobStatus(status),
		Template: "generic",
	}

	return job
}

func TestRetryJob_FromFailed(t *testing.T) {
	dir := t.TempDir()
	job := createTestJobFile(t, dir, "test-job.md", string(JobStatusFailed))

	plan := &Plan{
		Directory: dir,
	}

	// Retry the job
	err := RetryJob(job, plan, false, false)
	if err != nil {
		t.Fatalf("RetryJob failed: %v", err)
	}

	// Verify job status was updated
	if job.Status != JobStatusPending {
		t.Errorf("expected status %s, got %s", JobStatusPending, job.Status)
	}

	// Verify file was updated
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatalf("failed to read job file: %v", err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "status: pending") {
		t.Errorf("expected status: pending in file, got: %s", contentStr)
	}

	// Verify error fields were cleared
	if strings.Contains(contentStr, "last_error") {
		t.Errorf("expected last_error to be cleared, but found it in file")
	}
	if strings.Contains(contentStr, "completed_at") {
		t.Errorf("expected completed_at to be cleared, but found it in file")
	}
	if strings.Contains(contentStr, "duration") {
		t.Errorf("expected duration to be cleared, but found it in file")
	}
}

func TestRetryJob_FromRunning_RefusesWithoutForce(t *testing.T) {
	dir := t.TempDir()
	job := createTestJobFile(t, dir, "test-job.md", string(JobStatusRunning))

	plan := &Plan{
		Directory: dir,
	}

	// Try to retry without --force
	err := RetryJob(job, plan, false, false)
	if err == nil {
		t.Fatal("expected RetryJob to return error for running job without --force")
	}

	// Verify the job status was not changed
	if job.Status != JobStatusRunning {
		t.Errorf("expected status to remain %s, got %s", JobStatusRunning, job.Status)
	}

	// Verify error message mentions --force
	if !strings.Contains(err.Error(), "force") {
		t.Errorf("expected error message to mention --force, got: %s", err)
	}
}

func TestRetryJob_FromRunning_WithForce(t *testing.T) {
	dir := t.TempDir()
	job := createTestJobFile(t, dir, "test-job.md", string(JobStatusRunning))

	plan := &Plan{
		Directory: dir,
	}

	// Retry with --force
	err := RetryJob(job, plan, true, false)
	if err != nil {
		t.Fatalf("RetryJob failed with --force: %v", err)
	}

	// Verify job status was updated to pending
	if job.Status != JobStatusPending {
		t.Errorf("expected status %s, got %s", JobStatusPending, job.Status)
	}

	// Verify file was updated
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatalf("failed to read job file: %v", err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "status: pending") {
		t.Errorf("expected status: pending in file, got: %s", contentStr)
	}
}

func TestRetryJob_FromCompleted_Refuses(t *testing.T) {
	dir := t.TempDir()
	job := createTestJobFile(t, dir, "test-job.md", string(JobStatusCompleted))

	plan := &Plan{
		Directory: dir,
	}

	// Try to retry a completed job
	err := RetryJob(job, plan, false, false)
	if err == nil {
		t.Fatal("expected RetryJob to return error for completed job")
	}

	// Verify error message is helpful
	if !strings.Contains(err.Error(), "already completed") {
		t.Errorf("expected error to mention 'already completed', got: %s", err)
	}

	// Verify job status was not changed
	if job.Status != JobStatusCompleted {
		t.Errorf("expected status to remain %s, got %s", JobStatusCompleted, job.Status)
	}
}

func TestRetryJob_FromPendingUser_Refuses(t *testing.T) {
	dir := t.TempDir()
	job := createTestJobFile(t, dir, "test-job.md", string(JobStatusPendingUser))

	plan := &Plan{
		Directory: dir,
	}

	// Try to retry a pending_user job (chat job)
	err := RetryJob(job, plan, false, false)
	if err == nil {
		t.Fatal("expected RetryJob to return error for pending_user job")
	}

	// Verify error message mentions chat
	if !strings.Contains(err.Error(), "chat") {
		t.Errorf("expected error to mention 'chat', got: %s", err)
	}
}

func TestRetryJob_AlreadyPending_Refuses(t *testing.T) {
	dir := t.TempDir()
	job := createTestJobFile(t, dir, "test-job.md", string(JobStatusPending))

	plan := &Plan{
		Directory: dir,
	}

	// Try to retry a job that's already pending
	err := RetryJob(job, plan, false, false)
	if err == nil {
		t.Fatal("expected RetryJob to return error for already-pending job")
	}

	// Verify error message mentions state
	if !strings.Contains(err.Error(), "state") {
		t.Errorf("expected error to mention 'state', got: %s", err)
	}
}
