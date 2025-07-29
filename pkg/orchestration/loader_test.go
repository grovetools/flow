package orchestration

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPlan(t *testing.T) {
	// Create temporary test directory
	tmpDir := t.TempDir()

	// Create test files
	tests := []struct {
		filename string
		content  string
	}{
		{
			filename: "spec.md",
			content: `# Test Specification

This is the spec file.`,
		},
		{
			filename: "01-initial-plan.md",
			content: `---
id: initial-plan-123
title: initial-plan
status: pending
type: oneshot
prompt_source:
  - spec.md
---
Create the initial plan.`,
		},
		{
			filename: "02-implement-feature.md",
			content: `---
id: implement-123
title: Implement Feature
status: pending
type: agent
depends_on:
  - 01-initial-plan.md
worktree: feature-branch
output:
  type: commit
  message: "feat: implement feature"
---
Implement the feature.`,
		},
		{
			filename: "not-a-job.txt",
			content:  "This should be ignored",
		},
	}

	// Write test files
	for _, tt := range tests {
		path := filepath.Join(tmpDir, tt.filename)
		if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
			t.Fatalf("Failed to write test file %s: %v", tt.filename, err)
		}
	}

	// Test loading the plan
	plan, err := LoadPlan(tmpDir)
	if err != nil {
		t.Fatalf("LoadPlan() error = %v", err)
	}

	// Verify plan structure
	if plan.Directory != tmpDir {
		t.Errorf("Plan directory = %v, want %v", plan.Directory, tmpDir)
	}

	if plan.SpecFile != filepath.Join(tmpDir, "spec.md") {
		t.Errorf("Plan spec file = %v, want %v", plan.SpecFile, filepath.Join(tmpDir, "spec.md"))
	}

	// Verify jobs were loaded
	if len(plan.Jobs) != 2 {
		t.Errorf("Plan has %d jobs, want 2", len(plan.Jobs))
	}

	// Verify job 1
	job1, exists := plan.Jobs["01-initial-plan.md"]
	if !exists {
		t.Fatal("Job 01-initial-plan.md not found")
	}
	if job1.ID != "initial-plan-123" {
		t.Errorf("Job1 ID = %v, want initial-plan-123", job1.ID)
	}
	if job1.Type != JobTypeOneshot {
		t.Errorf("Job1 Type = %v, want oneshot", job1.Type)
	}
	if len(job1.Dependencies) != 0 {
		t.Errorf("Job1 has %d dependencies, want 0", len(job1.Dependencies))
	}

	// Verify job 2
	job2, exists := plan.Jobs["02-implement-feature.md"]
	if !exists {
		t.Fatal("Job 02-implement-feature.md not found")
	}
	if job2.Type != JobTypeAgent {
		t.Errorf("Job2 Type = %v, want agent", job2.Type)
	}
	if len(job2.Dependencies) != 1 {
		t.Errorf("Job2 has %d dependencies, want 1", len(job2.Dependencies))
	}
	if job2.Dependencies[0] != job1 {
		t.Errorf("Job2 dependency is not job1")
	}

	// Verify JobsByID
	if job, exists := plan.GetJobByID("initial-plan-123"); !exists || job != job1 {
		t.Errorf("GetJobByID failed for initial-plan-123")
	}
}

func TestLoadPlanErrors(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name    string
		files   map[string]string
		wantErr string
	}{
		{
			name: "missing dependency",
			files: map[string]string{
				"01-job.md": `---
id: job-1
title: Job 1
status: pending
type: oneshot
depends_on:
  - 99-missing.md
---
Body`,
			},
			wantErr: "non-existent job",
		},
		{
			name: "circular dependency",
			files: map[string]string{
				"01-job-a.md": `---
id: job-a
title: Job A
status: pending
type: oneshot
depends_on:
  - 02-job-b.md
---
Body`,
				"02-job-b.md": `---
id: job-b
title: Job B
status: pending
type: oneshot
depends_on:
  - 01-job-a.md
---
Body`,
			},
			wantErr: "circular dependency",
		},
		{
			name: "duplicate ID",
			files: map[string]string{
				"01-job.md": `---
id: same-id
title: Job 1
status: pending
type: oneshot
---
Body`,
				"02-job.md": `---
id: same-id
title: Job 2
status: pending
type: oneshot
---
Body`,
			},
			wantErr: "duplicate job ID",
		},
		{
			name: "missing required field",
			files: map[string]string{
				"01-job.md": `---
title: Job without ID
status: pending
type: oneshot
---
Body`,
			},
			wantErr: "missing required field: id",
		},
		{
			name: "invalid job type",
			files: map[string]string{
				"01-job.md": `---
id: job-1
title: Job 1
status: pending
type: invalid
---
Body`,
			},
			wantErr: "invalid job type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test directory
			testDir := filepath.Join(tmpDir, tt.name)
			if err := os.MkdirAll(testDir, 0o755); err != nil {
				t.Fatal(err)
			}

			// Write test files
			for filename, content := range tt.files {
				path := filepath.Join(testDir, filename)
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			// Try to load plan
			_, err := LoadPlan(testDir)
			if err == nil {
				t.Errorf("LoadPlan() expected error containing %q, got nil", tt.wantErr)
			} else if !contains(err.Error(), tt.wantErr) {
				t.Errorf("LoadPlan() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestJobIsRunnable(t *testing.T) {
	// Create test jobs
	job1 := &Job{
		ID:     "job-1",
		Status: JobStatusCompleted,
	}

	job2 := &Job{
		ID:           "job-2",
		Status:       JobStatusPending,
		Dependencies: []*Job{job1},
	}

	job3 := &Job{
		ID:           "job-3",
		Status:       JobStatusPending,
		Dependencies: []*Job{job2},
	}

	// Test cases
	tests := []struct {
		name string
		job  *Job
		want bool
	}{
		{
			name: "completed job is not runnable",
			job:  job1,
			want: false,
		},
		{
			name: "pending job with completed dependencies is runnable",
			job:  job2,
			want: true,
		},
		{
			name: "pending job with pending dependencies is not runnable",
			job:  job3,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.job.IsRunnable(); got != tt.want {
				t.Errorf("IsRunnable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetRunnableJobs(t *testing.T) {
	// Create a simple plan
	plan := &Plan{
		Jobs: map[string]*Job{
			"01-completed.md": {
				ID:     "completed",
				Status: JobStatusCompleted,
			},
			"02-runnable.md": {
				ID:     "runnable",
				Status: JobStatusPending,
				Dependencies: []*Job{
					{ID: "completed", Status: JobStatusCompleted},
				},
			},
			"03-blocked.md": {
				ID:     "blocked",
				Status: JobStatusPending,
				Dependencies: []*Job{
					{ID: "runnable", Status: JobStatusPending},
				},
			},
		},
	}

	runnable := plan.GetRunnableJobs()
	if len(runnable) != 1 {
		t.Errorf("GetRunnableJobs() returned %d jobs, want 1", len(runnable))
	}
	if runnable[0].ID != "runnable" {
		t.Errorf("GetRunnableJobs() returned job %s, want runnable", runnable[0].ID)
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

