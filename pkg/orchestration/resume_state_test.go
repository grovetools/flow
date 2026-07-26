package orchestration

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestStatePersister_BeginResumedAttemptStartsFreshAttempt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resume-job.md")
	original := []byte(`---
id: resume-job
title: Resume Job
status: completed
started_at: 2025-01-02T03:04:05Z
updated_at: 2025-01-02T03:09:05Z
completed_at: 2025-01-02T03:09:05Z
duration: 5m0s
last_error: stale failure
---

# Resume Job
`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	oldStart := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	oldEnd := oldStart.Add(5 * time.Minute)
	job := &Job{
		ID:          "resume-job",
		Title:       "Resume Job",
		Status:      JobStatusCompleted,
		FilePath:    path,
		StartTime:   oldStart,
		EndTime:     oldEnd,
		UpdatedAt:   oldEnd,
		CompletedAt: oldEnd,
		Duration:    5 * time.Minute,
		Metadata:    JobMetadata{LastError: "stale failure"},
	}

	before := time.Now().UTC().Truncate(time.Second)
	rollback, err := NewStatePersister().BeginResumedAttempt(job)
	if err != nil {
		t.Fatalf("BeginResumedAttempt() error = %v", err)
	}
	if rollback == nil {
		t.Fatal("BeginResumedAttempt() returned a nil rollback")
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	frontmatter, _, err := ParseFrontmatter(content)
	if err != nil {
		t.Fatal(err)
	}
	if got := frontmatter["status"]; got != string(JobStatusRunning) {
		t.Errorf("status = %v, want running", got)
	}
	for _, key := range []string{"completed_at", "duration", "last_error"} {
		if _, ok := frontmatter[key]; ok {
			t.Errorf("stale terminal field %q was not cleared", key)
		}
	}

	startedAt, err := time.Parse(time.RFC3339, frontmatter["started_at"].(string))
	if err != nil {
		t.Fatalf("parse started_at: %v", err)
	}
	updatedAt, err := time.Parse(time.RFC3339, frontmatter["updated_at"].(string))
	if err != nil {
		t.Fatalf("parse updated_at: %v", err)
	}
	if startedAt.Before(before) {
		t.Errorf("started_at = %v, want fresh attempt at or after %v", startedAt, before)
	}
	if !updatedAt.Equal(startedAt) {
		t.Errorf("updated_at = %v, want attempt time %v", updatedAt, startedAt)
	}
	if job.Status != JobStatusRunning || !job.StartTime.Equal(startedAt) {
		t.Errorf("in-memory attempt state = (%s, %v), want (running, %v)", job.Status, job.StartTime, startedAt)
	}
	if !job.EndTime.IsZero() || !job.CompletedAt.IsZero() || job.Duration != 0 || job.Metadata.LastError != "" {
		t.Errorf("in-memory terminal metadata was not cleared: EndTime=%v CompletedAt=%v Duration=%v LastError=%q", job.EndTime, job.CompletedAt, job.Duration, job.Metadata.LastError)
	}
}

func TestStatePersister_BeginResumedAttemptRollbackRestoresExactState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resume-job.md")
	original := []byte("---\n# keep this comment\nid: resume-job\nstatus: completed\nstarted_at: 2025-01-02T03:04:05Z\nupdated_at: 2025-01-02T03:09:05Z\ncompleted_at: 2025-01-02T03:09:05Z\nduration: 5m0s\nlast_error: stale failure\n---\nbody without trailing newline")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	oldStart := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	job := &Job{
		ID:          "resume-job",
		Status:      JobStatusCompleted,
		FilePath:    path,
		StartTime:   oldStart,
		EndTime:     oldStart.Add(5 * time.Minute),
		UpdatedAt:   oldStart.Add(5 * time.Minute),
		CompletedAt: oldStart.Add(5 * time.Minute),
		Duration:    5 * time.Minute,
		Metadata: JobMetadata{
			ExecutionTime: 5 * time.Minute,
			RetryCount:    2,
			LastError:     "stale failure",
		},
	}
	originalJob := *job

	rollback, err := NewStatePersister().BeginResumedAttempt(job)
	if err != nil {
		t.Fatalf("BeginResumedAttempt() error = %v", err)
	}
	if err := rollback(); err != nil {
		t.Fatalf("rollback() error = %v", err)
	}

	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, original) {
		t.Errorf("rollback rewrote prior file state\ngot:  %q\nwant: %q", restored, original)
	}
	if !reflect.DeepEqual(*job, originalJob) {
		t.Errorf("rollback did not restore exact in-memory state\ngot:  %#v\nwant: %#v", *job, originalJob)
	}
}

func TestStatePersister_BeginResumedAttemptRejectsPersistedNonCompletedState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resume-job.md")
	original := []byte("---\nid: resume-job\nstatus: running\n---\nbody\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	// Simulate a concurrent caller that loaded the job while it was completed,
	// before another resume persisted running.
	job := &Job{ID: "resume-job", Status: JobStatusCompleted, FilePath: path}
	rollback, err := NewStatePersister().BeginResumedAttempt(job)
	if err == nil {
		t.Fatal("BeginResumedAttempt() error = nil, want persisted-status conflict")
	}
	if rollback != nil {
		t.Fatal("BeginResumedAttempt() returned rollback after rejected transition")
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(content, original) || job.Status != JobStatusCompleted {
		t.Fatal("rejected concurrent transition changed file or in-memory state")
	}
}

func TestStatePersister_BeginResumedAttemptParseFailureIsTransactional(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resume-job.md")
	original := []byte("---\nstatus: [invalid\n---\nbody\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	job := &Job{ID: "resume-job", Status: JobStatusCompleted, FilePath: path}
	originalJob := *job

	rollback, err := NewStatePersister().BeginResumedAttempt(job)
	if err == nil {
		t.Fatal("BeginResumedAttempt() error = nil, want malformed frontmatter error")
	}
	if rollback != nil {
		t.Fatal("BeginResumedAttempt() returned rollback after failed transition")
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(content, original) || !reflect.DeepEqual(*job, originalJob) {
		t.Fatal("failed transition changed file or in-memory state")
	}
}
