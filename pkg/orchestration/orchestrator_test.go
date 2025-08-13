package orchestration

import (
	"context"
	"fmt"
	"testing"
	"time"
)

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

	orch, err := NewOrchestrator(plan, config, nil)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	// Verify orchestrator is properly initialized
	if orch.plan != plan {
		t.Errorf("Plan not set correctly")
	}

	if len(orch.executors) == 0 {
		t.Errorf("No executors registered")
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

	orch, err := NewOrchestrator(plan, nil, nil)
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
	plan := &Plan{
		Name:      "test-plan",
		Directory: "/tmp/test",
		Jobs: []*Job{
			{ID: "job1", Type: JobTypeOneshot, Status: JobStatusPending, FilePath: "job1.md"},
			{ID: "job2", Type: JobTypeOneshot, Status: JobStatusPending, FilePath: "job2.md", DependsOn: []string{"job1"}},
		},
	}

	orch, err := NewOrchestrator(plan, nil, nil)
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
	orch.executors[JobTypeOneshot] = mockExec

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
	plan := &Plan{
		Name: "test-plan",
		Jobs: []*Job{
			{ID: "job1", Type: JobTypeOneshot, Status: JobStatusPending},
			{ID: "job2", Type: JobTypeOneshot, Status: JobStatusPending},
			{ID: "job3", Type: JobTypeOneshot, Status: JobStatusPending, DependsOn: []string{"job1", "job2"}},
		},
	}

	config := &OrchestratorConfig{
		MaxParallelJobs: 2,
	}

	orch, err := NewOrchestrator(plan, config, nil)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	// Replace executor
	executionCount := 0
	mockExec := &mockExecutor{
		executeFunc: func(ctx context.Context, job *Job, plan *Plan) error {
			executionCount++
			job.Status = JobStatusCompleted
			return nil
		},
	}
	orch.executors[JobTypeOneshot] = mockExec

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
	plan := &Plan{
		Name: "test-plan",
		Jobs: []*Job{
			{ID: "job1", Type: JobTypeOneshot, Status: JobStatusPending},
			{ID: "job2", Type: JobTypeOneshot, Status: JobStatusPending, DependsOn: []string{"job1"}},
			{ID: "job3", Type: JobTypeOneshot, Status: JobStatusPending, DependsOn: []string{"job2"}},
		},
	}

	config := &OrchestratorConfig{
		MaxParallelJobs: 1,
		CheckInterval:   10 * time.Millisecond,
	}

	orch, err := NewOrchestrator(plan, config, nil)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	// Replace executor
	executionOrder := []string{}
	mockExec := &mockExecutor{
		executeFunc: func(ctx context.Context, job *Job, plan *Plan) error {
			executionOrder = append(executionOrder, job.ID)
			job.Status = JobStatusCompleted
			return nil
		},
	}
	orch.executors[JobTypeOneshot] = mockExec

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
	plan := &Plan{
		Name: "test-plan",
		Jobs: []*Job{
			{ID: "job1", Status: JobStatusPending},
		},
	}

	orch, err := NewOrchestrator(plan, nil, nil)
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
	plan := &Plan{
		Name: "test-plan",
		Jobs: []*Job{
			{ID: "job1", Type: JobTypeOneshot, Status: JobStatusPending},
			{ID: "job2", Type: JobTypeOneshot, Status: JobStatusPending, DependsOn: []string{"job1"}},
		},
	}

	orch, err := NewOrchestrator(plan, nil, nil)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}

	// Replace executor to simulate failure
	mockExec := &mockExecutor{
		executeFunc: func(ctx context.Context, job *Job, plan *Plan) error {
			return fmt.Errorf("simulated failure")
		},
	}
	orch.executors[JobTypeOneshot] = mockExec

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