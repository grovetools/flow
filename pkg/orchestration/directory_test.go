package orchestration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetNextJobNumber(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name     string
		files    []string
		wantNum  int
	}{
		{
			name:    "empty directory",
			files:   []string{},
			wantNum: 1,
		},
		{
			name:    "single job",
			files:   []string{"01-first-job.md"},
			wantNum: 2,
		},
		{
			name:    "multiple jobs",
			files:   []string{"01-first.md", "02-second.md", "03-third.md"},
			wantNum: 4,
		},
		{
			name:    "with gaps",
			files:   []string{"01-first.md", "03-third.md", "07-seventh.md"},
			wantNum: 8,
		},
		{
			name:    "with non-job files",
			files:   []string{"01-job.md", "README.md", "spec.md", "02-job.md"},
			wantNum: 3,
		},
		{
			name:    "high numbers",
			files:   []string{"98-almost.md", "99-last.md"},
			wantNum: 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testDir := filepath.Join(tmpDir, tt.name)
			os.MkdirAll(testDir, 0755)

			// Create test files
			for _, file := range tt.files {
				path := filepath.Join(testDir, file)
				os.WriteFile(path, []byte("test"), 0644)
			}

			got, err := GetNextJobNumber(testDir)
			if err != nil {
				t.Fatalf("GetNextJobNumber() error = %v", err)
			}
			if got != tt.wantNum {
				t.Errorf("GetNextJobNumber() = %v, want %v", got, tt.wantNum)
			}
		})
	}
}

func TestGenerateJobFilename(t *testing.T) {
	tests := []struct {
		name    string
		number  int
		title   string
		want    string
	}{
		{
			name:   "simple title",
			number: 1,
			title:  "Simple Job",
			want:   "01-simple-job.md",
		},
		{
			name:   "complex title",
			number: 10,
			title:  "Implement User Authentication & Authorization",
			want:   "10-implement-user-authentication-authorization.md",
		},
		{
			name:   "special characters",
			number: 5,
			title:  "Fix bug #123: User's profile!",
			want:   "05-fix-bug-123-users-profile.md",
		},
		{
			name:   "very long title",
			number: 99,
			title:  "This is a very long job title that should be truncated to fit within reasonable filename limits",
			want:   "99-this-is-a-very-long-job-title-that-should-be-trunc.md",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GenerateJobFilename(tt.number, tt.title)
			if got != tt.want {
				t.Errorf("GenerateJobFilename() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAddJob(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a test plan
	plan := &Plan{
		Directory: tmpDir,
		Jobs:      []*Job{},
		JobsByID:  make(map[string]*Job),
	}

	// Add first job
	job1 := &Job{
		ID:     "test-job-1",
		Title:  "First Test Job",
		Type:   JobTypeOneshot,
		Status: JobStatusPending,
		PromptBody: "Do something",
	}

	if _, err := AddJob(plan, job1); err != nil {
		t.Fatalf("AddJob() error = %v", err)
	}

	// Verify job was added to plan
	if _, exists := plan.JobsByID["test-job-1"]; !exists {
		t.Errorf("Job not added to JobsByID")
	}

	// Verify file was created
	expectedFile := filepath.Join(tmpDir, "01-first-test-job.md")
	if _, err := os.Stat(expectedFile); err != nil {
		t.Errorf("Job file not created: %v", err)
	}

	// Add second job with dependencies
	job2 := &Job{
		ID:         "test-job-2",
		Title:      "Second Test Job",
		Type:       JobTypeAgent,
		Status:     JobStatusPending,
		DependsOn:  []string{"01-first-test-job.md"},
		Worktree:   "feature-branch",
		PromptBody: "Implement the feature",
	}

	if _, err := AddJob(plan, job2); err != nil {
		t.Fatalf("AddJob() second job error = %v", err)
	}

	// Verify second job file
	expectedFile2 := filepath.Join(tmpDir, "02-second-test-job.md")
	if content, err := os.ReadFile(expectedFile2); err != nil {
		t.Errorf("Second job file not created: %v", err)
	} else {
		contentStr := string(content)
		if !strings.Contains(contentStr, "depends_on:\n  - 01-first-test-job.md") {
			t.Errorf("Second job missing dependencies")
		}
		if !strings.Contains(contentStr, "worktree: feature-branch") {
			t.Errorf("Second job missing worktree")
		}
	}
}

func TestAddJobErrors(t *testing.T) {
	tmpDir := t.TempDir()

	plan := &Plan{
		Directory: tmpDir,
		Jobs:      []*Job{},
		JobsByID:  make(map[string]*Job),
	}

	// Add a job first
	existingJob := &Job{
		ID:    "existing-id",
		Title: "Existing Job",
	}
	_, _ = AddJob(plan, existingJob)

	tests := []struct {
		name    string
		job     *Job
		wantErr string
	}{
		{
			name: "missing ID",
			job: &Job{
				Title: "No ID Job",
			},
			wantErr: "job ID is required",
		},
		{
			name: "missing title",
			job: &Job{
				ID: "no-title",
			},
			wantErr: "job title is required",
		},
		{
			name: "duplicate ID",
			job: &Job{
				ID:    "existing-id",
				Title: "Duplicate ID Job",
			},
			wantErr: "already exists",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := AddJob(plan, tt.job)
			if err == nil {
				t.Errorf("AddJob() expected error containing %q, got nil", tt.wantErr)
			} else if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("AddJob() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestCreateJobFromTemplate(t *testing.T) {
	tests := []struct {
		name     string
		jobType  JobType
		title    string
		opts     JobOptions
		validate func(t *testing.T, job *Job)
	}{
		{
			name:    "oneshot job defaults",
			jobType: JobTypeOneshot,
			title:   "Test Oneshot Job",
			opts:    JobOptions{},
			validate: func(t *testing.T, job *Job) {
				if job.Type != JobTypeOneshot {
					t.Errorf("Job type = %v, want oneshot", job.Type)
				}
			},
		},
		{
			name:    "agent job defaults",
			jobType: JobTypeAgent,
			title:   "Test Agent Job",
			opts:    JobOptions{},
			validate: func(t *testing.T, job *Job) {
				if job.Type != JobTypeAgent {
					t.Errorf("Job type = %v, want agent", job.Type)
				}
			},
		},
		{
			name:    "with options",
			jobType: JobTypeAgent,
			title:   "Complex Job",
			opts: JobOptions{
				DependsOn: []string{"01-first.md", "02-second.md"},
				Include:   []string{"spec.md", "context.md"},
				Worktree:  "feature-xyz",
				Prompt:    "Do complex stuff",
			},
			validate: func(t *testing.T, job *Job) {
				if len(job.DependsOn) != 2 {
					t.Errorf("DependsOn length = %v, want 2", len(job.DependsOn))
				}
				if job.Worktree != "feature-xyz" {
					t.Errorf("Worktree = %v, want feature-xyz", job.Worktree)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := CreateJobFromTemplate(tt.jobType, tt.title, tt.opts)

			// Common validations
			if job.ID == "" {
				t.Errorf("Job ID is empty")
			}
			if job.Title != tt.title {
				t.Errorf("Job title = %v, want %v", job.Title, tt.title)
			}
			if job.Status != JobStatusPending {
				t.Errorf("Job status = %v, want pending", job.Status)
			}

			// Test-specific validations
			tt.validate(t, job)
		})
	}
}

func TestListJobs(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test files
	files := []string{
		"01-first.md",
		"02-second.md",
		"03-third.md",
		"spec.md",        // Should be ignored
		"README.md",      // Should be ignored
		"99-last.md",
		"10-middle.md",
	}

	for _, file := range files {
		path := filepath.Join(tmpDir, file)
		os.WriteFile(path, []byte("test"), 0644)
	}

	// List jobs
	jobs, err := ListJobs(tmpDir)
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}

	// Expected jobs in sorted order
	expected := []string{
		"01-first.md",
		"02-second.md",
		"03-third.md",
		"10-middle.md",
		"99-last.md",
	}

	if len(jobs) != len(expected) {
		t.Errorf("ListJobs() returned %d jobs, want %d", len(jobs), len(expected))
	}

	for i, job := range jobs {
		if job != expected[i] {
			t.Errorf("ListJobs()[%d] = %v, want %v", i, job, expected[i])
		}
	}
}