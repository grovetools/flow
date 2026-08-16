package orchestration

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestStatePersisterRunningTransitionMintsUUIDv7ExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "job.md")
	if err := os.WriteFile(path, []byte("---\nid: job\nstatus: pending\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	job := &Job{ID: "job", Status: JobStatusPending, FilePath: path}
	sp := NewStatePersister()
	if err := sp.UpdateJobStatus(job, JobStatusRunning); err != nil {
		t.Fatal(err)
	}
	first := job.AttemptID
	parsed, err := uuid.Parse(first)
	if err != nil || parsed.Version() != 7 {
		t.Fatalf("attempt_id = %q, want UUIDv7 (parse err %v)", first, err)
	}
	if err := sp.UpdateJobStatus(job, JobStatusRunning); err != nil {
		t.Fatal(err)
	}
	if job.AttemptID != first {
		t.Fatalf("redundant running write replaced attempt %q with %q", first, job.AttemptID)
	}
	content, _ := os.ReadFile(path)
	fm, _, _ := ParseFrontmatter(content)
	if fm["attempt_id"] != first {
		t.Fatalf("persisted attempt_id = %v, want %q", fm["attempt_id"], first)
	}
	if err := sp.UpdateJobStatus(job, JobStatusPending); err != nil {
		t.Fatal(err)
	}
	if job.AttemptID != "" {
		t.Fatalf("pending retained attempt %q", job.AttemptID)
	}
	if err := sp.UpdateJobStatus(job, JobStatusRunning); err != nil {
		t.Fatal(err)
	}
	if job.AttemptID == "" || job.AttemptID == first {
		t.Fatalf("retry attempt = %q, want fresh id distinct from %q", job.AttemptID, first)
	}
}

func TestStatePersister_UpdateJobStatus(t *testing.T) {
	tests := []struct {
		name      string
		oldStatus JobStatus
		newStatus JobStatus
		wantErr   bool
	}{
		{
			name:      "pending to running",
			oldStatus: JobStatusPending,
			newStatus: JobStatusRunning,
			wantErr:   false,
		},
		{
			name:      "running to completed",
			oldStatus: JobStatusRunning,
			newStatus: JobStatusCompleted,
			wantErr:   false,
		},
		{
			name:      "running to failed",
			oldStatus: JobStatusRunning,
			newStatus: JobStatusFailed,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp directory
			dir := t.TempDir()

			// Create job file
			job := &Job{
				ID:       "test-job",
				Title:    "Test Job",
				Status:   tt.oldStatus,
				FilePath: filepath.Join(dir, "test-job.md"),
			}

			// Write initial job file
			content := createJobFile(job)
			if err := os.WriteFile(job.FilePath, content, 0o600); err != nil {
				t.Fatal(err)
			}

			// Create state manager
			sp := NewStatePersister()

			// Update status
			err := sp.UpdateJobStatus(job, tt.newStatus)
			if (err != nil) != tt.wantErr {
				t.Errorf("UpdateJobStatus() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !tt.wantErr {
				// Verify file was updated
				updatedContent, err := os.ReadFile(job.FilePath)
				if err != nil {
					t.Fatal(err)
				}

				// Check status in file
				if !strings.Contains(string(updatedContent), "status: "+string(tt.newStatus)) {
					t.Errorf("Expected status %s in file, got:\n%s", tt.newStatus, updatedContent)
				}

				// Check updated_at is present
				if !strings.Contains(string(updatedContent), "updated_at:") {
					t.Error("Expected updated_at field in frontmatter")
				}
			}
		})
	}
}

func TestStatePersister_ConcurrentUpdates(t *testing.T) {
	// Create temp directory
	dir := t.TempDir()

	// Create job file
	job := &Job{
		ID:       "concurrent-job",
		Title:    "Concurrent Test Job",
		Status:   JobStatusPending,
		FilePath: filepath.Join(dir, "concurrent-job.md"),
	}

	// Write initial job file
	content := createJobFile(job)
	if err := os.WriteFile(job.FilePath, content, 0o600); err != nil {
		t.Fatal(err)
	}

	// Create state manager
	sp := NewStatePersister()

	// Run concurrent updates
	var wg sync.WaitGroup
	numGoroutines := 10

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			// Alternate between different statuses
			status := JobStatusRunning
			if i%2 == 0 {
				status = JobStatusCompleted
			}

			if err := sp.UpdateJobStatus(job, status); err != nil {
				// Lock contention is expected, but not other errors
				if !strings.Contains(err.Error(), "file is locked") {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		}(i)
	}

	wg.Wait()

	// Verify file is still valid
	finalContent, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}

	// Should be able to parse frontmatter
	parser := &FrontmatterParser{}
	_, _, err = parser.ParseFrontmatter(finalContent)
	if err != nil {
		t.Errorf("File corrupted after concurrent updates: %v", err)
	}
}

func TestStatePersister_AppendJobOutput(t *testing.T) {
	// Create temp directory
	dir := t.TempDir()

	// Create job file
	job := &Job{
		ID:       "output-job",
		Title:    "Output Test Job",
		Status:   JobStatusRunning,
		FilePath: filepath.Join(dir, "output-job.md"),
	}

	// Write initial job file
	content := createJobFile(job)
	if err := os.WriteFile(job.FilePath, content, 0o600); err != nil {
		t.Fatal(err)
	}

	// Create state manager
	sp := NewStatePersister()

	// Append output multiple times
	outputs := []string{
		"Starting job execution",
		"Processing data",
		"Job completed successfully",
	}

	for _, output := range outputs {
		if err := sp.AppendJobOutput(job, output); err != nil {
			t.Errorf("Failed to append output: %v", err)
		}
		// Small delay to ensure different timestamps
		time.Sleep(10 * time.Millisecond)
	}

	// Read final content
	finalContent, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}

	// Verify all outputs are present
	contentStr := string(finalContent)
	if !strings.Contains(contentStr, "## Output") {
		t.Error("Expected Output section in file")
	}

	for _, output := range outputs {
		if !strings.Contains(contentStr, output) {
			t.Errorf("Expected output '%s' in file", output)
		}
	}

	// Verify timestamps are present
	if !strings.Contains(contentStr, "[") || !strings.Contains(contentStr, "]") {
		t.Error("Expected timestamps in output")
	}
}

func TestStatePersister_WriteAtomic(t *testing.T) {
	// Create temp directory
	dir := t.TempDir()
	path := filepath.Join(dir, "atomic-test.txt")

	// Create state manager
	sp := NewStatePersister()

	// Write initial content
	content1 := []byte("initial content")
	if err := sp.writeAtomic(path, content1); err != nil {
		t.Fatalf("Failed to write initial content: %v", err)
	}

	// Verify content
	read1, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(read1, content1) {
		t.Error("Initial content mispatch")
	}

	// Update content
	content2 := []byte("updated content")
	if err := sp.writeAtomic(path, content2); err != nil {
		t.Fatalf("Failed to write updated content: %v", err)
	}

	// Verify updated content
	read2, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(read2, content2) {
		t.Error("Updated content mispatch")
	}

	// Verify no temp files left
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Errorf("Temp file not cleaned up: %s", entry.Name())
		}
	}
}

func TestStatePersister_FileLocking(t *testing.T) {
	// Create temp directory
	dir := t.TempDir()
	path := filepath.Join(dir, "lock-test.md")

	// Create file
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Create state manager
	sp := NewStatePersister()

	// Acquire lock
	lock1, err := sp.lockFile(path)
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}

	// Try to acquire lock again (should fail)
	lock2, err := sp.lockFile(path)
	if err == nil {
		_ = lock2.Unlock()
		t.Error("Expected error when acquiring lock on already locked file")
	}

	// Release first lock
	if err := lock1.Unlock(); err != nil {
		t.Errorf("Failed to unlock: %v", err)
	}

	// Should be able to lock again
	lock3, err := sp.lockFile(path)
	if err != nil {
		t.Errorf("Failed to acquire lock after unlock: %v", err)
	}
	_ = lock3.Unlock()
}

func TestStatePersister_ValidateJobStates(t *testing.T) {
	// Create temp directory
	dir := t.TempDir()

	// Create valid job
	validJob := &Job{
		ID:       "valid-job",
		Title:    "Valid Job",
		Status:   JobStatusPending,
		FilePath: filepath.Join(dir, "valid-job.md"),
		Filename: "valid-job.md",
	}

	// Create invalid job (missing status)
	invalidJob := &Job{
		ID:       "invalid-job",
		Title:    "Invalid Job",
		FilePath: filepath.Join(dir, "invalid-job.md"),
		Filename: "invalid-job.md",
	}

	// Write job files
	validContent := createJobFile(validJob)
	if err := os.WriteFile(validJob.FilePath, validContent, 0o600); err != nil {
		t.Fatal(err)
	}

	invalidContent := []byte(`---
id: invalid-job
title: "Invalid Job"
---
Job content`)
	if err := os.WriteFile(invalidJob.FilePath, invalidContent, 0o600); err != nil {
		t.Fatal(err)
	}

	// Create plan
	plan := &Plan{
		Directory: dir,
		Jobs: []*Job{
			validJob,
			invalidJob,
			{
				ID:       "missing-job",
				FilePath: filepath.Join(dir, "missing.md"),
				Filename: "missing.md",
			},
		},
	}

	// Create state manager
	sp := NewStatePersister()

	// Validate
	errors := sp.ValidateJobStates(plan)

	// Should have 2 errors (missing status, missing file)
	if len(errors) != 2 {
		t.Errorf("Expected 2 validation errors, got %d", len(errors))
		for _, err := range errors {
			t.Logf("Error: %v", err)
		}
	}
}

func TestStatePersister_UpdateJobMetadata(t *testing.T) {
	dir := t.TempDir()

	job := &Job{
		ID:       "meta-job",
		Title:    "Metadata Test Job",
		Status:   JobStatusFailed,
		FilePath: filepath.Join(dir, "meta-job.md"),
	}

	// Write initial job file
	content := createJobFile(job)
	if err := os.WriteFile(job.FilePath, content, 0o600); err != nil {
		t.Fatal(err)
	}

	sp := NewStatePersister()

	// Update metadata with a last_error
	err := sp.UpdateJobMetadata(job, JobMetadata{LastError: "dependency failed"})
	if err != nil {
		t.Fatalf("UpdateJobMetadata() error = %v", err)
	}

	// Read updated file content
	updatedContent, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}

	contentStr := string(updatedContent)

	// Verify last_error is persisted in the frontmatter
	if !strings.Contains(contentStr, "last_error: dependency failed") {
		t.Errorf("Expected 'last_error: dependency failed' in frontmatter, got:\n%s", contentStr)
	}

	// Verify in-memory state was also updated
	if job.Metadata.LastError != "dependency failed" {
		t.Errorf("Expected in-memory LastError to be 'dependency failed', got '%s'", job.Metadata.LastError)
	}
}

// TestStatePersister_UpdateJobStatus_ClearsStaleLastError covers the
// failed-then-rerun path: a job whose frontmatter carries last_error from an
// earlier failed run must shed it (file and in-memory) when it completes
// successfully, but keep it when it fails again without a new error.
func TestStatePersister_UpdateJobStatus_ClearsStaleLastError(t *testing.T) {
	tests := []struct {
		name          string
		newStatus     JobStatus
		wantLastError bool
	}{
		{name: "completed clears stale last_error", newStatus: JobStatusCompleted, wantLastError: false},
		{name: "failed keeps last_error", newStatus: JobStatusFailed, wantLastError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()

			job := &Job{
				ID:       "rerun-job",
				Title:    "Rerun Test Job",
				Status:   JobStatusRunning,
				FilePath: filepath.Join(dir, "rerun-job.md"),
			}

			// Simulate a job file left behind by a failed run (loader carries
			// last_error into job.Metadata).
			staleError := "job not found: old-failure"
			job.Metadata.LastError = staleError
			content := createJobFile(job)
			if err := os.WriteFile(job.FilePath, content, 0o600); err != nil {
				t.Fatal(err)
			}

			sp := NewStatePersister()
			if err := sp.UpdateJobMetadata(job, job.Metadata); err != nil {
				t.Fatalf("UpdateJobMetadata() error = %v", err)
			}

			if err := sp.UpdateJobStatus(job, tt.newStatus); err != nil {
				t.Fatalf("UpdateJobStatus() error = %v", err)
			}

			updatedContent, err := os.ReadFile(job.FilePath)
			if err != nil {
				t.Fatal(err)
			}
			contentStr := string(updatedContent)

			if tt.wantLastError {
				if !strings.Contains(contentStr, "last_error:") {
					t.Errorf("Expected last_error to survive %s, got:\n%s", tt.newStatus, contentStr)
				}
				if job.Metadata.LastError != staleError {
					t.Errorf("Expected in-memory LastError %q, got %q", staleError, job.Metadata.LastError)
				}
			} else {
				if strings.Contains(contentStr, "last_error:") {
					t.Errorf("Expected last_error cleared from frontmatter on %s, got:\n%s", tt.newStatus, contentStr)
				}
				if job.Metadata.LastError != "" {
					t.Errorf("Expected in-memory LastError cleared, got %q", job.Metadata.LastError)
				}
			}
		})
	}
}

func TestStatePersister_UpdateJobModel(t *testing.T) {
	dir := t.TempDir()
	job := &Job{
		ID:       "model-job",
		Title:    "Model Test Job",
		Status:   JobStatusRunning,
		FilePath: filepath.Join(dir, "model-job.md"),
	}

	// A curated job file with a specific key order and a comment to ensure the
	// order/comment-preserving writer is used (not the map-based rewrite).
	content := []byte(`---
id: model-job
title: "Model Test Job"
status: running
type: headless_agent
model: gemini-3.1-pro-preview
---

<!-- grove: keep me -->

# Model Test Job
`)
	if err := os.WriteFile(job.FilePath, content, 0o600); err != nil {
		t.Fatal(err)
	}

	sp := NewStatePersister()
	if err := sp.UpdateJobModel(job, "claude-sonnet-4-6"); err != nil {
		t.Fatalf("UpdateJobModel() error = %v", err)
	}

	updated, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(updated)

	if !strings.Contains(s, "model: claude-sonnet-4-6") {
		t.Errorf("expected model rewritten to claude-sonnet-4-6, got:\n%s", s)
	}
	if strings.Contains(s, "gemini-3.1-pro-preview") {
		t.Errorf("old model should be gone, got:\n%s", s)
	}
	if !strings.Contains(s, "updated_at:") {
		t.Errorf("expected updated_at to be set, got:\n%s", s)
	}
	// Order/comment preservation: id stays first, the HTML comment survives.
	if idIdx, statusIdx := strings.Index(s, "id: model-job"), strings.Index(s, "status: running"); idIdx == -1 || statusIdx == -1 || idIdx > statusIdx {
		t.Errorf("expected original key order preserved (id before status), got:\n%s", s)
	}
	if !strings.Contains(s, "<!-- grove: keep me -->") {
		t.Errorf("expected body comment preserved, got:\n%s", s)
	}
	if job.Model != "claude-sonnet-4-6" {
		t.Errorf("expected in-memory job.Model updated, got %q", job.Model)
	}

	// Empty model clears the explicit value, restoring the provider default.
	if err := sp.UpdateJobModel(job, ""); err != nil {
		t.Fatalf("UpdateJobModel(\"\") should clear, got error %v", err)
	}
	if job.Model != "" {
		t.Errorf("empty UpdateJobModel must clear job.Model, got %q", job.Model)
	}
}

// Helper function to create job file content
func createJobFile(job *Job) []byte {
	return []byte(fmt.Sprintf(`---
id: %s
title: "%s"
status: %s
type: oneshot
---

# %s

Job content goes here.
`, job.ID, job.Title, job.Status, job.Title))
}
