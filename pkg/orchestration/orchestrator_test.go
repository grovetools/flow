package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockRuntime implements the Runtime interface for testing, avoiding all file I/O
// that LocalRuntime does (log files, status persistence, etc.).
type mockRuntime struct {
	executeFunc func(ctx context.Context, job *Job, plan *Plan) error
	mu          sync.Mutex
	calls       int
}

func (m *mockRuntime) ExecuteJob(ctx context.Context, job *Job, plan *Plan) error {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	if m.executeFunc != nil {
		return m.executeFunc(ctx, job, plan)
	}
	job.Status = JobStatusCompleted
	return nil
}

func (m *mockRuntime) StreamLogs(ctx context.Context, jobID string) (<-chan string, error) {
	return nil, nil
}

func (m *mockRuntime) Cancel(ctx context.Context, jobID string) error {
	return nil
}

// createTempJobFile creates a minimal job markdown file on disk so that
// reloadJobStatusesFromDisk can read it. Returns the file path.
func createTempJobFile(t *testing.T, dir, filename string, jobType JobType, status JobStatus) string {
	t.Helper()
	fp := filepath.Join(dir, filename)
	content := fmt.Sprintf("---\ntitle: %s\ntype: %s\nstatus: %s\n---\n\nTest job\n", filename, jobType, status)
	if err := os.WriteFile(fp, []byte(content), 0o600); err != nil {
		t.Fatalf("Failed to create temp job file %s: %v", fp, err)
	}
	return fp
}

// updateJobFileStatus rewrites the status in a job file on disk.
func updateJobFileStatus(t *testing.T, filePath string, newStatus JobStatus) {
	t.Helper()
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read job file: %v", err)
	}
	updated := strings.Replace(string(content), "status: running", "status: "+string(newStatus), 1)
	if err := os.WriteFile(filePath, []byte(updated), 0o600); err != nil {
		t.Fatalf("Failed to write job file: %v", err)
	}
}

// mockExecutor implements Executor for testing.
type mockExecutor struct {
	name         string
	executeFunc  func(ctx context.Context, job *Job, plan *Plan) error
	executeCalls int
}

func (m *mockExecutor) Execute(ctx context.Context, job *Job, plan *Plan) error {
	m.executeCalls++
	if m.executeFunc != nil {
		return m.executeFunc(ctx, job, plan)
	}
	// Default behavior - mark job as completed
	job.Status = JobStatusCompleted
	return nil
}

func (m *mockExecutor) Name() string {
	return m.name
}

func TestNewOrchestrator(t *testing.T) {
	plan := &Plan{
		Name:      "test-plan",
		Directory: "/tmp/test",
		Jobs: []*Job{
			{ID: "job1", Type: JobTypeOneshot, Status: JobStatusPending},
			{ID: "job2", Type: JobTypeAgent, Status: JobStatusPending, DependsOn: []string{"job1"}},
		},
	}

	config := &OrchestratorConfig{
		MaxParallelJobs: 2,
		CheckInterval:   1 * time.Second,
	}

	orch, err := NewOrchestrator(plan, config)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	// Verify orchestrator is properly initialized
	if orch.Plan != plan {
		t.Errorf("Plan not set correctly")
	}

	if orch.config.Runtime == nil {
		t.Errorf("No runtime initialized")
	}

	if orch.dependencyGraph == nil {
		t.Errorf("Dependency graph not created")
	}
}

func TestOrchestrator_GetStatus(t *testing.T) {
	plan := &Plan{
		Name: "test-plan",
		Jobs: []*Job{
			{ID: "job1", Status: JobStatusCompleted},
			{ID: "job2", Status: JobStatusRunning},
			{ID: "job3", Status: JobStatusPending},
			{ID: "job4", Status: JobStatusFailed},
			{ID: "job5", Status: JobStatusPending, DependsOn: []string{"job4"}},
		},
	}

	orch, err := NewOrchestrator(plan, nil)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	status := orch.GetStatus()

	// Verify counts
	if status.Total != 5 {
		t.Errorf("Expected total 5, got %d", status.Total)
	}
	if status.Completed != 1 {
		t.Errorf("Expected completed 1, got %d", status.Completed)
	}
	if status.Running != 1 {
		t.Errorf("Expected running 1, got %d", status.Running)
	}
	if status.Pending != 2 {
		t.Errorf("Expected pending 2, got %d", status.Pending)
	}
	if status.Failed != 1 {
		t.Errorf("Expected failed 1, got %d", status.Failed)
	}

	// Verify progress
	expectedProgress := 20.0 // 1 completed out of 5
	if status.Progress != expectedProgress {
		t.Errorf("Expected progress %.1f, got %.1f", expectedProgress, status.Progress)
	}
}

func TestOrchestrator_RunJob(t *testing.T) {
	tmpDir := t.TempDir()

	// Real job files on disk so LocalRuntime's status persistence works, and
	// resolved Dependencies so dependency gating is exercised.
	job1 := &Job{ID: "job1", Type: JobTypeOneshot, Status: JobStatusPending, Filename: "job1.md", FilePath: createTempJobFile(t, tmpDir, "job1.md", JobTypeOneshot, JobStatusPending)}
	job2 := &Job{ID: "job2", Type: JobTypeOneshot, Status: JobStatusPending, Filename: "job2.md", FilePath: createTempJobFile(t, tmpDir, "job2.md", JobTypeOneshot, JobStatusPending), DependsOn: []string{"job1"}, Dependencies: []*Job{job1}}

	plan := &Plan{
		Name:      "test-plan",
		Directory: tmpDir,
		Jobs:      []*Job{job1, job2},
		JobsByID:  map[string]*Job{"job1": job1, "job2": job2},
	}

	orch, err := NewOrchestrator(plan, nil)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	// Replace executor with mock
	mockExec := &mockExecutor{
		name: "mock",
		executeFunc: func(ctx context.Context, job *Job, plan *Plan) error {
			job.Status = JobStatusCompleted
			return nil
		},
	}
	orch.config.Runtime.(*LocalRuntime).SetExecutor(JobTypeOneshot, mockExec)

	ctx := context.Background()

	// Try to run job2 (should fail - dependencies not met)
	err = orch.RunJob(ctx, "job2.md")
	if err == nil {
		t.Errorf("Expected error running job2 with unmet dependencies")
	}

	// Run job1
	err = orch.RunJob(ctx, "job1.md")
	if err != nil {
		t.Errorf("Failed to run job1: %v", err)
	}

	// Verify job1 was executed
	if mockExec.executeCalls != 1 {
		t.Errorf("Expected 1 execution, got %d", mockExec.executeCalls)
	}
}

func TestOrchestrator_RunNext(t *testing.T) {
	tmpDir := t.TempDir()

	job1 := &Job{ID: "job1", Type: JobTypeOneshot, Status: JobStatusPending, Filename: "job1.md", FilePath: createTempJobFile(t, tmpDir, "job1.md", JobTypeOneshot, JobStatusPending)}
	job2 := &Job{ID: "job2", Type: JobTypeOneshot, Status: JobStatusPending, Filename: "job2.md", FilePath: createTempJobFile(t, tmpDir, "job2.md", JobTypeOneshot, JobStatusPending)}
	job3 := &Job{ID: "job3", Type: JobTypeOneshot, Status: JobStatusPending, Filename: "job3.md", FilePath: createTempJobFile(t, tmpDir, "job3.md", JobTypeOneshot, JobStatusPending), DependsOn: []string{"job1", "job2"}, Dependencies: []*Job{job1, job2}}

	plan := &Plan{
		Name:      "test-plan",
		Directory: tmpDir,
		Jobs:      []*Job{job1, job2, job3},
		JobsByID:  map[string]*Job{"job1": job1, "job2": job2, "job3": job3},
	}

	config := &OrchestratorConfig{
		MaxParallelJobs: 2,
	}

	orch, err := NewOrchestrator(plan, config)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	// Replace executor (job1 and job2 run concurrently, so guard the counter).
	var mu sync.Mutex
	executionCount := 0
	mockExec := &mockExecutor{
		executeFunc: func(ctx context.Context, job *Job, plan *Plan) error {
			mu.Lock()
			executionCount++
			mu.Unlock()
			job.Status = JobStatusCompleted
			return nil
		},
	}
	orch.config.Runtime.(*LocalRuntime).SetExecutor(JobTypeOneshot, mockExec)

	ctx := context.Background()

	// Run next should execute job1 and job2 in parallel
	err = orch.RunNext(ctx)
	if err != nil {
		t.Errorf("RunNext failed: %v", err)
	}

	// Verify both jobs were executed
	if executionCount != 2 {
		t.Errorf("Expected 2 executions, got %d", executionCount)
	}

	// Verify job statuses
	if plan.Jobs[0].Status != JobStatusCompleted || plan.Jobs[1].Status != JobStatusCompleted {
		t.Errorf("Jobs 1 and 2 should be completed")
	}
}

func TestOrchestrator_RunAll(t *testing.T) {
	tmpDir := t.TempDir()

	job1 := &Job{ID: "job1", Type: JobTypeOneshot, Status: JobStatusPending, Filename: "job1.md", FilePath: createTempJobFile(t, tmpDir, "job1.md", JobTypeOneshot, JobStatusPending)}
	job2 := &Job{ID: "job2", Type: JobTypeOneshot, Status: JobStatusPending, Filename: "job2.md", FilePath: createTempJobFile(t, tmpDir, "job2.md", JobTypeOneshot, JobStatusPending), DependsOn: []string{"job1"}, Dependencies: []*Job{job1}}
	job3 := &Job{ID: "job3", Type: JobTypeOneshot, Status: JobStatusPending, Filename: "job3.md", FilePath: createTempJobFile(t, tmpDir, "job3.md", JobTypeOneshot, JobStatusPending), DependsOn: []string{"job2"}, Dependencies: []*Job{job2}}

	plan := &Plan{
		Name:      "test-plan",
		Directory: tmpDir,
		Jobs:      []*Job{job1, job2, job3},
		JobsByID:  map[string]*Job{"job1": job1, "job2": job2, "job3": job3},
	}

	config := &OrchestratorConfig{
		MaxParallelJobs: 1,
		CheckInterval:   10 * time.Millisecond,
	}

	orch, err := NewOrchestrator(plan, config)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	// Replace executor
	var mu sync.Mutex
	executionOrder := []string{}
	mockExec := &mockExecutor{
		executeFunc: func(ctx context.Context, job *Job, plan *Plan) error {
			mu.Lock()
			executionOrder = append(executionOrder, job.ID)
			mu.Unlock()
			job.Status = JobStatusCompleted
			return nil
		},
	}
	orch.config.Runtime.(*LocalRuntime).SetExecutor(JobTypeOneshot, mockExec)

	ctx := context.Background()

	// Run all jobs
	err = orch.RunAll(ctx)
	if err != nil {
		t.Errorf("RunAll failed: %v", err)
	}

	// Verify execution order
	if len(executionOrder) != 3 {
		t.Errorf("Expected 3 executions, got %d", len(executionOrder))
	}

	// Verify jobs were executed in dependency order
	for i, jobID := range executionOrder {
		expectedID := fmt.Sprintf("job%d", i+1)
		if jobID != expectedID {
			t.Errorf("Expected job %s at position %d, got %s", expectedID, i, jobID)
		}
	}

	// Verify all jobs completed
	for _, job := range plan.Jobs {
		if job.Status != JobStatusCompleted {
			t.Errorf("Job %s should be completed, got %s", job.ID, job.Status)
		}
	}
}

func TestOrchestrator_UpdateJobStatus(t *testing.T) {
	tmpDir := t.TempDir()

	job1 := &Job{ID: "job1", Type: JobTypeOneshot, Status: JobStatusPending, Filename: "job1.md", FilePath: createTempJobFile(t, tmpDir, "job1.md", JobTypeOneshot, JobStatusPending)}

	plan := &Plan{
		Name:      "test-plan",
		Directory: tmpDir,
		Jobs:      []*Job{job1},
		JobsByID:  map[string]*Job{"job1": job1},
	}

	orch, err := NewOrchestrator(plan, nil)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	job := plan.Jobs[0]

	// Update to running
	err = orch.UpdateJobStatus(job, JobStatusRunning)
	if err != nil {
		t.Errorf("Failed to update status: %v", err)
	}

	if job.Status != JobStatusRunning {
		t.Errorf("Expected status running, got %s", job.Status)
	}

	// Verify timestamp was set
	if job.StartTime.IsZero() {
		t.Errorf("Start time should be set")
	}

	// Update to completed
	err = orch.UpdateJobStatus(job, JobStatusCompleted)
	if err != nil {
		t.Errorf("Failed to update status: %v", err)
	}

	if job.Status != JobStatusCompleted {
		t.Errorf("Expected status completed, got %s", job.Status)
	}

	// Verify end time was set
	if job.EndTime.IsZero() {
		t.Errorf("End time should be set")
	}
}

func TestOrchestrator_HandleFailures(t *testing.T) {
	tmpDir := t.TempDir()

	job1 := &Job{ID: "job1", Type: JobTypeOneshot, Status: JobStatusPending, Filename: "job1.md", FilePath: createTempJobFile(t, tmpDir, "job1.md", JobTypeOneshot, JobStatusPending)}
	job2 := &Job{ID: "job2", Type: JobTypeOneshot, Status: JobStatusPending, Filename: "job2.md", FilePath: createTempJobFile(t, tmpDir, "job2.md", JobTypeOneshot, JobStatusPending), DependsOn: []string{"job1"}, Dependencies: []*Job{job1}}

	plan := &Plan{
		Name:      "test-plan",
		Directory: tmpDir,
		Jobs:      []*Job{job1, job2},
		JobsByID:  map[string]*Job{"job1": job1, "job2": job2},
	}

	orch, err := NewOrchestrator(plan, nil)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	// Replace executor to simulate failure
	mockExec := &mockExecutor{
		executeFunc: func(ctx context.Context, job *Job, plan *Plan) error {
			return fmt.Errorf("simulated failure")
		},
	}
	orch.config.Runtime.(*LocalRuntime).SetExecutor(JobTypeOneshot, mockExec)

	ctx := context.Background()

	// Run all should handle the failure
	err = orch.RunAll(ctx)
	if err == nil {
		t.Errorf("Expected error due to failed jobs")
	}

	// Verify job1 is marked as failed
	if plan.Jobs[0].Status != JobStatusFailed {
		t.Errorf("Job1 should be marked as failed")
	}

	// Verify job2 is still pending (blocked by failed dependency)
	if plan.Jobs[1].Status != JobStatusPending {
		t.Errorf("Job2 should still be pending")
	}
}

func TestOrchestrator_RunAll_WaitsForRunningJobs(t *testing.T) {
	tmpDir := t.TempDir()

	job1 := &Job{
		ID:       "01-interactive.md",
		Type:     JobTypeInteractiveAgent,
		Status:   JobStatusPending,
		Filename: "01-interactive.md",
		FilePath: createTempJobFile(t, tmpDir, "01-interactive.md", JobTypeInteractiveAgent, JobStatusPending),
	}
	job2 := &Job{
		ID:           "02-followup.md",
		Type:         JobTypeOneshot,
		Status:       JobStatusPending,
		Filename:     "02-followup.md",
		FilePath:     createTempJobFile(t, tmpDir, "02-followup.md", JobTypeOneshot, JobStatusPending),
		DependsOn:    []string{"01-interactive.md"},
		Dependencies: []*Job{job1},
	}

	plan := &Plan{
		Name:      "test-wait-plan",
		Directory: tmpDir,
		Jobs:      []*Job{job1, job2},
		JobsByID:  map[string]*Job{"01-interactive.md": job1, "02-followup.md": job2},
	}

	config := &OrchestratorConfig{
		MaxParallelJobs: 2,
		CheckInterval:   10 * time.Millisecond,
	}

	orch, err := NewOrchestrator(plan, config)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	// Replace runtime with mock that simulates interactive agent behavior
	var mu sync.Mutex
	executionOrder := []string{}
	rt := &mockRuntime{
		executeFunc: func(ctx context.Context, job *Job, plan *Plan) error {
			mu.Lock()
			executionOrder = append(executionOrder, job.ID)
			mu.Unlock()
			if job.ID == "01-interactive.md" {
				// Interactive agent: stays running after executor returns
				job.Status = JobStatusRunning
				return nil
			}
			// Oneshot: completes normally
			job.Status = JobStatusCompleted
			return nil
		},
	}
	orch.config.Runtime = rt

	// In a goroutine, simulate external completion of the interactive job after a delay
	go func() {
		time.Sleep(80 * time.Millisecond)
		// Update both in-memory and on-disk status
		job1.Status = JobStatusCompleted
		updateJobFileStatus(t, job1.FilePath, JobStatusCompleted)
		orch.dependencyGraph.UpdateJobStatus(job1.ID, JobStatusCompleted)
	}()

	ctx := context.Background()
	err = orch.RunAll(ctx)
	if err != nil {
		t.Errorf("RunAll should complete successfully, got error: %v", err)
	}

	// Both jobs should be completed
	if job1.Status != JobStatusCompleted {
		t.Errorf("job1 should be completed, got %s", job1.Status)
	}
	if job2.Status != JobStatusCompleted {
		t.Errorf("job2 should be completed, got %s", job2.Status)
	}

	// Both jobs should have been executed
	mu.Lock()
	defer mu.Unlock()
	if len(executionOrder) != 2 {
		t.Errorf("Expected 2 executions, got %d: %v", len(executionOrder), executionOrder)
	}
	if len(executionOrder) >= 2 {
		if executionOrder[0] != "01-interactive.md" {
			t.Errorf("Expected job1 first, got %s", executionOrder[0])
		}
		if executionOrder[1] != "02-followup.md" {
			t.Errorf("Expected job2 second, got %s", executionOrder[1])
		}
	}
}

func TestOrchestrator_RunAll_InteractiveJobDoesNotTimeout(t *testing.T) {
	tmpDir := t.TempDir()

	job1 := &Job{
		ID:       "01-interactive.md",
		Type:     JobTypeInteractiveAgent,
		Status:   JobStatusPending,
		Filename: "01-interactive.md",
		FilePath: createTempJobFile(t, tmpDir, "01-interactive.md", JobTypeInteractiveAgent, JobStatusPending),
	}
	job2 := &Job{
		ID:           "02-followup.md",
		Type:         JobTypeOneshot,
		Status:       JobStatusPending,
		Filename:     "02-followup.md",
		FilePath:     createTempJobFile(t, tmpDir, "02-followup.md", JobTypeOneshot, JobStatusPending),
		DependsOn:    []string{"01-interactive.md"},
		Dependencies: []*Job{job1},
	}

	plan := &Plan{
		Name:      "test-timeout-plan",
		Directory: tmpDir,
		Jobs:      []*Job{job1, job2},
		JobsByID:  map[string]*Job{"01-interactive.md": job1, "02-followup.md": job2},
	}

	config := &OrchestratorConfig{
		MaxParallelJobs:     2,
		CheckInterval:       5 * time.Millisecond,
		MaxConsecutiveSteps: 5, // Low limit - would be hit if wait loops count as steps
	}

	orch, err := NewOrchestrator(plan, config)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	rt := &mockRuntime{
		executeFunc: func(ctx context.Context, job *Job, plan *Plan) error {
			if job.ID == "01-interactive.md" {
				job.Status = JobStatusRunning
				return nil
			}
			job.Status = JobStatusCompleted
			return nil
		},
	}
	orch.config.Runtime = rt

	// Complete the interactive job after 100ms — enough time for >5 polling cycles (5ms each)
	go func() {
		time.Sleep(100 * time.Millisecond)
		job1.Status = JobStatusCompleted
		updateJobFileStatus(t, job1.FilePath, JobStatusCompleted)
		orch.dependencyGraph.UpdateJobStatus(job1.ID, JobStatusCompleted)
	}()

	ctx := context.Background()
	err = orch.RunAll(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "maximum consecutive step limit") {
			t.Errorf("RunAll should NOT hit step limit while waiting for interactive jobs, but got: %v", err)
		} else {
			t.Errorf("RunAll should succeed, got unexpected error: %v", err)
		}
	}

	if job1.Status != JobStatusCompleted {
		t.Errorf("job1 should be completed, got %s", job1.Status)
	}
	if job2.Status != JobStatusCompleted {
		t.Errorf("job2 should be completed, got %s", job2.Status)
	}
}

func TestOrchestrator_RunAll_NoChangeWithoutRunningJobs(t *testing.T) {
	tmpDir := t.TempDir()

	job1 := &Job{
		ID:       "01-job.md",
		Type:     JobTypeOneshot,
		Status:   JobStatusPending,
		Filename: "01-job.md",
		FilePath: createTempJobFile(t, tmpDir, "01-job.md", JobTypeOneshot, JobStatusPending),
	}

	plan := &Plan{
		Name:      "test-blocked-plan",
		Directory: tmpDir,
		Jobs:      []*Job{job1},
		JobsByID:  map[string]*Job{"01-job.md": job1},
	}

	config := &OrchestratorConfig{
		MaxParallelJobs: 1,
		CheckInterval:   10 * time.Millisecond,
	}

	orch, err := NewOrchestrator(plan, config)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	// Mock runtime that fails the job
	rt := &mockRuntime{
		executeFunc: func(ctx context.Context, job *Job, plan *Plan) error {
			job.Status = JobStatusFailed
			return fmt.Errorf("simulated failure")
		},
	}
	orch.config.Runtime = rt

	ctx := context.Background()
	err = orch.RunAll(ctx)

	if err == nil {
		t.Errorf("Expected error when no runnable jobs and no running jobs")
	}

	// The error should be about failed jobs, not about "no runnable jobs and no jobs running"
	// since the one job failed and there are no other pending jobs
	if err != nil && !strings.Contains(err.Error(), "failed") {
		t.Errorf("Expected failure-related error, got: %v", err)
	}
}
