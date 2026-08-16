package orchestration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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
include:
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
		if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
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
	var job1 *Job
	for _, j := range plan.Jobs {
		if j.Filename == "01-initial-plan.md" {
			job1 = j
			break
		}
	}
	if job1 == nil {
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
	var job2 *Job
	for _, j := range plan.Jobs {
		if j.Filename == "02-implement-feature.md" {
			job2 = j
			break
		}
	}
	if job2 == nil {
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
			wantErr: "", // Missing deps are now silently resolved as nil
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
			wantErr: "", // Invalid job types are now silently skipped
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
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			// Try to load plan
			_, err := LoadPlan(testDir)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("LoadPlan() unexpected error: %v", err)
				}
			} else if err == nil {
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
		{
			name: "file job is never runnable even with pending status",
			job: &Job{
				ID:     "file-job",
				Type:   JobTypeFile,
				Status: JobStatusPending,
			},
			want: false,
		},
		{
			name: "file job is never runnable even with completed dependencies",
			job: &Job{
				ID:           "file-job-with-deps",
				Type:         JobTypeFile,
				Status:       JobStatusPending,
				Dependencies: []*Job{job1}, // job1 is completed
			},
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
		Jobs: []*Job{
			{
				ID:       "completed",
				Filename: "01-completed.md",
				Status:   JobStatusCompleted,
			},
			{
				ID:       "runnable",
				Filename: "02-runnable.md",
				Status:   JobStatusPending,
				Dependencies: []*Job{
					{ID: "completed", Status: JobStatusCompleted},
				},
			},
			{
				ID:       "blocked",
				Filename: "03-blocked.md",
				Status:   JobStatusPending,
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

func TestLoadFileJobType(t *testing.T) {
	// Create temporary test directory
	tmpDir := t.TempDir()

	// Create a file job
	content := `---
id: context-file-123
title: Context for Feature
status: completed
type: file
---
This is reference content that provides context for downstream jobs.
It is not executed but can be referenced as a dependency.`

	path := filepath.Join(tmpDir, "01-context.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Test loading the plan
	plan, err := LoadPlan(tmpDir)
	if err != nil {
		t.Fatalf("LoadPlan() error = %v", err)
	}

	// Verify job was loaded
	if len(plan.Jobs) != 1 {
		t.Errorf("Plan has %d jobs, want 1", len(plan.Jobs))
	}

	job := plan.Jobs[0]
	if job.Type != JobTypeFile {
		t.Errorf("Job Type = %v, want file", job.Type)
	}
	if job.ID != "context-file-123" {
		t.Errorf("Job ID = %v, want context-file-123", job.ID)
	}
	if job.Status != JobStatusCompleted {
		t.Errorf("Job Status = %v, want completed", job.Status)
	}

	// File jobs should never be runnable
	if job.IsRunnable() {
		t.Error("File job should not be runnable")
	}
}

func TestFileJobAsDependency(t *testing.T) {
	// Create temporary test directory
	tmpDir := t.TempDir()

	// Create a file job and an agent job that depends on it
	tests := []struct {
		filename string
		content  string
	}{
		{
			filename: "01-context.md",
			content: `---
id: context-123
title: Context File
status: completed
type: file
---
Reference content.`,
		},
		{
			filename: "02-impl.md",
			content: `---
id: impl-123
title: Implementation
status: pending
type: agent
depends_on:
  - 01-context.md
---
Use the context file to implement.`,
		},
	}

	// Write test files
	for _, tt := range tests {
		path := filepath.Join(tmpDir, tt.filename)
		if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
			t.Fatalf("Failed to write test file %s: %v", tt.filename, err)
		}
	}

	// Load the plan
	plan, err := LoadPlan(tmpDir)
	if err != nil {
		t.Fatalf("LoadPlan() error = %v", err)
	}

	// Verify both jobs loaded
	if len(plan.Jobs) != 2 {
		t.Errorf("Plan has %d jobs, want 2", len(plan.Jobs))
	}

	// Find the implementation job
	var implJob *Job
	for _, j := range plan.Jobs {
		if j.ID == "impl-123" {
			implJob = j
			break
		}
	}
	if implJob == nil {
		t.Fatal("Implementation job not found")
	}

	// Verify it has the file job as a dependency
	if len(implJob.Dependencies) != 1 {
		t.Errorf("Implementation job has %d dependencies, want 1", len(implJob.Dependencies))
	}
	if implJob.Dependencies[0].Type != JobTypeFile {
		t.Errorf("Dependency Type = %v, want file", implJob.Dependencies[0].Type)
	}

	// The implementation job should be runnable since the file job is completed
	if !implJob.IsRunnable() {
		t.Error("Implementation job should be runnable since file dependency is completed")
	}
}

func TestLoadJobWithSkillField(t *testing.T) {
	tmpDir := t.TempDir()

	content := `---
id: skill-job-123
title: Skill Job
status: pending
type: oneshot
skill: test-skill
---
Job that uses a skill.`

	path := filepath.Join(tmpDir, "01-skill-job.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	plan, err := LoadPlan(tmpDir)
	if err != nil {
		t.Fatalf("LoadPlan() error = %v", err)
	}

	if len(plan.Jobs) != 1 {
		t.Fatalf("Plan has %d jobs, want 1", len(plan.Jobs))
	}

	job := plan.Jobs[0]
	if job.Skill != "test-skill" {
		t.Errorf("Job.Skill = %q, want %q", job.Skill, "test-skill")
	}
	if job.ID != "skill-job-123" {
		t.Errorf("Job.ID = %q, want %q", job.ID, "skill-job-123")
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

// TestPlanConfigSatelliteRoundTrip pins the PlanConfig.Satellite yaml wiring:
// a .grove-plan.yml `satellite:` key loads into Config.Satellite, survives a
// SavePlan→LoadPlan round trip, and is omitted entirely when unset
// (omitempty — older plans must not grow a satellite key on rewrite).
func TestPlanConfigSatelliteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".grove-plan.yml"),
		[]byte("model: test-model\nsatellite: mysat\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	plan, err := LoadPlan(dir)
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	if plan.Config == nil || plan.Config.Satellite != "mysat" {
		t.Fatalf("Config.Satellite = %+v, want mysat", plan.Config)
	}

	// Round trip: SavePlan re-marshals Config; LoadPlan must read it back.
	if err := SavePlan(dir, plan); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	reloaded, err := LoadPlan(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Config.Satellite != "mysat" {
		t.Errorf("round-tripped Satellite = %q, want mysat", reloaded.Config.Satellite)
	}
	if reloaded.Config.Model != "test-model" {
		t.Errorf("round trip dropped Model: %q", reloaded.Config.Model)
	}

	// Unset satellite must not appear in the marshaled file (omitempty).
	reloaded.Config.Satellite = ""
	if err := SavePlan(dir, reloaded); err != nil {
		t.Fatalf("SavePlan (unset): %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".grove-plan.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "satellite") {
		t.Errorf("unset satellite leaked into .grove-plan.yml:\n%s", data)
	}
}

// TestLoadPlanLenientKeepsPlanWithInvalidJob is the loader-level regression
// for the burst-insert index drop: a job file that fails validation (here:
// missing title, the exact shape the TUI pilot's burst fixture wrote) must
// not make the whole plan unloadable for browse/index consumers — LoadPlan
// keeps failing hard for execution paths, LoadPlanLenient reports the bad
// job and returns the plan with its remaining valid jobs.
func TestLoadPlanLenientKeepsPlanWithInvalidJob(t *testing.T) {
	dir := t.TempDir()
	writeFile := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(".grove-plan.yml", "status: active\nworktree: burst-new\nrepos:\n  - beta-repo\n")
	writeFile("01.md", "---\nid: burst\nstatus: pending\ntype: file\n---\nburst\n") // no title
	writeFile("02-good.md", "---\nid: good\ntitle: Good Job\nstatus: pending\ntype: oneshot\n---\nok\n")

	if _, err := LoadPlan(dir); err == nil {
		t.Fatal("strict LoadPlan unexpectedly accepted a title-less job; lenient variant may be redundant")
	}

	plan, problems := LoadPlanLenient(dir)
	if plan == nil {
		t.Fatalf("lenient load returned nil plan (problems: %v)", problems)
	}
	if plan.Config == nil || plan.Config.Status != "active" {
		t.Fatalf("plan config lost in lenient load: %+v", plan.Config)
	}
	if len(plan.Jobs) != 1 || plan.Jobs[0].ID != "good" {
		t.Fatalf("lenient load should keep exactly the valid job, got %d jobs", len(plan.Jobs))
	}
	if len(problems) == 0 || !strings.Contains(problems[0].Error(), "title") {
		t.Fatalf("lenient load should report the skipped job's validation error, got %v", problems)
	}

	// Duplicate IDs: first wins, second is reported, plan survives.
	writeFile("03-dup.md", "---\nid: good\ntitle: Dup\nstatus: pending\ntype: oneshot\n---\ndup\n")
	plan, problems = LoadPlanLenient(dir)
	if plan == nil || len(plan.Jobs) != 1 {
		t.Fatalf("duplicate-ID lenient load: plan=%v jobs=%d", plan != nil, len(plan.Jobs))
	}
	found := false
	for _, problem := range problems {
		if strings.Contains(problem.Error(), "duplicate job ID") {
			found = true
		}
	}
	if !found {
		t.Fatalf("duplicate job ID not reported: %v", problems)
	}
}

// TestLoadJobMetaSkipsBody is the regression for the groved GC storm: job
// files accumulate agent transcripts (hundreds of KB each), and the daemon's
// plan index held every one of them in memory and re-read them on every plan
// file event. Index/browse loads must carry frontmatter only; execution loads
// must still carry the body.
func TestLoadJobMetaSkipsBody(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("transcript line\n", 40000) // ~640 KB, well past the head budget
	path := filepath.Join(dir, "01-big.md")
	content := "---\nid: big\ntitle: Big Job\nstatus: completed\ntype: oneshot\n---\n" + body
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	meta, err := LoadJobMeta(path)
	if err != nil {
		t.Fatalf("LoadJobMeta: %v", err)
	}
	if meta.PromptBody != "" {
		t.Fatalf("metadata load materialized a %d-byte body", len(meta.PromptBody))
	}
	if meta.ID != "big" || meta.Title != "Big Job" || meta.Status != JobStatusCompleted {
		t.Fatalf("frontmatter lost in metadata load: %+v", meta)
	}

	full, err := LoadJob(path)
	if err != nil {
		t.Fatalf("LoadJob: %v", err)
	}
	if full.PromptBody != body {
		t.Fatalf("execution load lost the body: got %d bytes, want %d", len(full.PromptBody), len(body))
	}

	// The index path goes through LoadPlanLenient.
	plan, problems := LoadPlanLenient(dir)
	if plan == nil || len(plan.Jobs) != 1 {
		t.Fatalf("lenient load: plan=%v jobs=%d problems=%v", plan != nil, len(plan.Jobs), problems)
	}
	if plan.Jobs[0].PromptBody != "" {
		t.Fatalf("index load retained a %d-byte body", len(plan.Jobs[0].PromptBody))
	}
}

// TestLoadJobMetaOversizedFrontmatter covers the fallback: frontmatter longer
// than the head budget must still load rather than being reported as broken.
func TestLoadJobMetaOversizedFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "01-wide.md")
	notes := strings.Repeat("x", frontmatterHeadBudget)
	content := "---\nid: wide\ntitle: Wide Job\nstatus: pending\ntype: oneshot\nworktree: " +
		notes + "\n---\nbody\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	job, err := LoadJobMeta(path)
	if err != nil {
		t.Fatalf("LoadJobMeta with oversized frontmatter: %v", err)
	}
	if job.ID != "wide" || len(job.Worktree) != len(notes) {
		t.Fatalf("oversized frontmatter truncated: id=%q worktree=%d", job.ID, len(job.Worktree))
	}
}

// TestLoadJobMetaRejectsNonJob keeps the metadata path's not-a-job signal
// identical to the full loader's, since the index relies on it to skip
// ordinary markdown sitting in a plan directory.
func TestLoadJobMetaRejectsNonJob(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte("# just notes\n\nno frontmatter here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var notAJob ErrNotAJob
	if _, err := LoadJobMeta(path); err == nil || !errors.As(err, &notAJob) {
		t.Fatalf("metadata load of a bodyless markdown file: got %v, want ErrNotAJob", err)
	}
}

// Corrupted job frontmatter shape produced by the historical nil-stringifying
// writers (fmt.Sprint of a nil pointer → literal "<nil>"). Mirrors a real
// corrupted file. Loading must succeed with these fields treated as unset,
// and the surrounding plan must load wholesale.
const nilLiteralJobContent = `---
id: all-agents-crashed-42355510
aliases: []
tags: []
model: claude-fable-5
status: running
title: all-agents-crashed
type: interactive_agent
updated_at: "2026-08-16T11:24:59Z"
completed_at: <nil>
duration: <nil>
last_error: <nil>
attempt_id: 01a00a51-5d06-7157-8acc-d7b5b8989647
started_at: "2026-08-16T11:24:59Z"
---

we just experienced all agents becoming interrupted across our system; any idea how?
`

func TestLoadJobToleratesNilLiterals(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "136-all-agents-crashed.md")
	if err := os.WriteFile(path, []byte(nilLiteralJobContent), 0o600); err != nil {
		t.Fatalf("write test job: %v", err)
	}

	job, err := LoadJob(path)
	if err != nil {
		t.Fatalf("LoadJob() on <nil>-corrupted file: %v", err)
	}
	if !job.CompletedAt.IsZero() {
		t.Errorf("CompletedAt = %v, want zero", job.CompletedAt)
	}
	if job.Duration != 0 {
		t.Errorf("Duration = %v, want 0", job.Duration)
	}
	if job.Metadata.LastError != "" {
		t.Errorf("LastError = %q, want empty", job.Metadata.LastError)
	}
	if job.ID != "all-agents-crashed-42355510" || job.Status != JobStatusRunning {
		t.Errorf("healthy fields damaged: id=%q status=%q", job.ID, job.Status)
	}

	// The metadata-only path shares the tolerance.
	if _, err := LoadJobMeta(path); err != nil {
		t.Errorf("LoadJobMeta() on <nil>-corrupted file: %v", err)
	}
}

func TestLoadPlanToleratesNilLiteralJob(t *testing.T) {
	tmpDir := t.TempDir()
	good := `---
id: good-job-123
title: good-job
status: pending
type: oneshot
---
Do a thing.
`
	if err := os.WriteFile(filepath.Join(tmpDir, "01-good.md"), []byte(good), 0o600); err != nil {
		t.Fatalf("write good job: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "136-all-agents-crashed.md"), []byte(nilLiteralJobContent), 0o600); err != nil {
		t.Fatalf("write corrupted job: %v", err)
	}

	plan, err := LoadPlan(tmpDir)
	if err != nil {
		t.Fatalf("LoadPlan() with a <nil>-corrupted job must not fail wholesale: %v", err)
	}
	if len(plan.Jobs) != 2 {
		t.Errorf("plan has %d jobs, want 2", len(plan.Jobs))
	}
}

// updateJobFile's entering-running transition clears completed_at/duration/
// last_error with nil update values; this is the exact path that used to
// stringify them as "<nil>" and corrupt the file.
func TestUpdateJobFileEnteringRunningWritesNoNilLiterals(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "01-job.md")
	content := `---
id: job-123
title: job
status: failed
type: interactive_agent
completed_at: "2026-08-16T10:00:00Z"
duration: 5m0s
last_error: boom
---
Body.
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write job: %v", err)
	}
	job, err := LoadJob(path)
	if err != nil {
		t.Fatalf("LoadJob: %v", err)
	}
	job.FilePath = path
	job.Status = JobStatusRunning
	if err := updateJobFile(job); err != nil {
		t.Fatalf("updateJobFile: %v", err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "<nil>") {
		t.Errorf("job file contains literal <nil>:\n%s", s)
	}
	for _, key := range []string{"completed_at", "duration", "last_error"} {
		if strings.Contains(s, key) {
			t.Errorf("stale key %q survived the running transition:\n%s", key, s)
		}
	}

	reloaded, err := LoadJob(path)
	if err != nil {
		t.Fatalf("reload after transition: %v", err)
	}
	if reloaded.Status != JobStatusRunning || reloaded.AttemptID == "" {
		t.Errorf("transition incomplete: status=%q attempt=%q", reloaded.Status, reloaded.AttemptID)
	}
}
