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
)

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
			if err := os.WriteFile(job.FilePath, content, 0644); err != nil {
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
	if err := os.WriteFile(job.FilePath, content, 0644); err != nil {
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
	if err := os.WriteFile(job.FilePath, content, 0644); err != nil {
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
	if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
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
		lock2.Unlock()
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
	lock3.Unlock()
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
	if err := os.WriteFile(validJob.FilePath, validContent, 0644); err != nil {
		t.Fatal(err)
	}
	
	invalidContent := []byte(`---
id: invalid-job
title: "Invalid Job"
---
Job content`)
	if err := os.WriteFile(invalidJob.FilePath, invalidContent, 0644); err != nil {
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