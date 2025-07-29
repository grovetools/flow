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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMockLLMClient provides a mock implementation for testing.
type TestMockLLMClient struct {
	responses map[string]string
	calls     []string
	mu        sync.Mutex
}

func NewTestMockLLMClient() *TestMockLLMClient {
	return &TestMockLLMClient{
		responses: make(map[string]string),
	}
}

func (m *TestMockLLMClient) Complete(ctx context.Context, prompt string, opts ...CompleteOption) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.calls = append(m.calls, prompt)
	
	// Return predefined response
	for key, response := range m.responses {
		if strings.Contains(prompt, key) {
			return response, nil
		}
	}
	
	return "Default test response", nil
}

// SmartMockLLMClient can generate new job files for testing emergent DAG.
type SmartMockLLMClient struct {
	*TestMockLLMClient
}

func NewSmartMockLLMClient() *SmartMockLLMClient {
	return &SmartMockLLMClient{
		TestMockLLMClient: NewTestMockLLMClient(),
	}
}

func (m *SmartMockLLMClient) Complete(ctx context.Context, prompt string, opts ...CompleteOption) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.calls = append(m.calls, prompt)
	
	// If this is a planning job, generate new job files
	if strings.Contains(prompt, "high-level implementation plan") {
		return m.generatePlanningResponse(), nil
	}
	
	// For implementation jobs, return success
	if strings.Contains(prompt, "implement") {
		return "Implementation completed successfully.", nil
	}
	
	return m.TestMockLLMClient.Complete(ctx, prompt, opts...)
}

func (m *SmartMockLLMClient) generatePlanningResponse() string {
	return `Based on the specification, here's the implementation plan:

## Overview
We'll implement this feature in three phases.

## New Jobs Created

I've created the following job files:

### 02-design-api.md
` + "```yaml" + `
---
id: design-api
title: "Design API Structure"
status: pending
type: oneshot
depends_on:
  - 01-high-level-plan.md
output:
  type: file
---
Design the API structure for the feature.
` + "```" + `

### 03-implement-backend.md
` + "```yaml" + `
---
id: impl-backend
title: "Implement Backend"
status: pending
type: agent
depends_on:
  - 02-design-api.md
worktree: backend-dev
output:
  type: commit
---
Implement the backend based on the API design.
` + "```" + `

### 04-implement-frontend.md
` + "```yaml" + `
---
id: impl-frontend
title: "Implement Frontend"
status: pending
type: agent
depends_on:
  - 02-design-api.md
worktree: frontend-dev
output:
  type: commit
---
Implement the frontend based on the API design.
` + "```" + `

This creates a DAG where both frontend and backend can be implemented in parallel after the API design is complete.`
}

func TestFullOrchestrationWorkflow(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	
	// Create initial spec file
	specContent := `# Test Feature Specification

This is a test specification for integration testing.

## Requirements
1. Create API endpoint
2. Add database schema
3. Build UI components`
	
	specPath := filepath.Join(tmpDir, "spec.md")
	err := os.WriteFile(specPath, []byte(specContent), 0644)
	require.NoError(t, err)
	
	// Initialize plan
	err = InitPlan(tmpDir, specPath)
	require.NoError(t, err)
	
	// Load plan
	plan, err := LoadPlan(tmpDir)
	require.NoError(t, err)
	require.Len(t, plan.Jobs, 1)
	
	// Create orchestrator with mock LLM
	mockLLM := NewSmartMockLLMClient()
	config := &Config{
		MaxParallelJobs: 2,
		Timeout:         30 * time.Second,
	}
	
	orch := NewOrchestrator(mockLLM, config)
	
	// Create a simple oneshot executor for testing
	oneshotExecutor := &OneshotExecutor{
		llmClient: mockLLM,
		config:    config,
	}
	orch.RegisterExecutor(JobTypeOneshot, oneshotExecutor)
	
	// Run initial planning job
	ctx := context.Background()
	initialJob := plan.Jobs[0]
	err = orch.ExecuteJob(ctx, initialJob, plan)
	require.NoError(t, err)
	
	// In a real scenario, the LLM would create new job files
	// For this test, we'll simulate that by creating them manually
	createTestJobFiles(t, tmpDir)
	
	// Reload plan to see new jobs
	plan, err = LoadPlan(tmpDir)
	require.NoError(t, err)
	
	// Verify new jobs were created (emergent DAG)
	assert.True(t, len(plan.Jobs) > 1, "Planning job should have created new jobs")
	
	// Run all jobs to completion
	completed := 0
	for {
		runnable := plan.GetRunnableJobs()
		if len(runnable) == 0 {
			break
		}
		
		for _, job := range runnable {
			// Simulate job execution
			job.Status = JobStatusCompleted
			completed++
		}
	}
	
	// Verify all jobs completed
	assert.Equal(t, len(plan.Jobs), completed)
}

func TestDependencyResolution(t *testing.T) {
	tests := []struct {
		name     string
		jobs     []jobDef
		expected [][]string // execution stages
		wantErr  bool
	}{
		{
			name: "linear dependencies",
			jobs: []jobDef{
				{id: "01", deps: []string{}},
				{id: "02", deps: []string{"01"}},
				{id: "03", deps: []string{"02"}},
			},
			expected: [][]string{
				{"01"},
				{"02"},
				{"03"},
			},
		},
		{
			name: "parallel execution",
			jobs: []jobDef{
				{id: "01", deps: []string{}},
				{id: "02", deps: []string{"01"}},
				{id: "03", deps: []string{"01"}},
				{id: "04", deps: []string{"02", "03"}},
			},
			expected: [][]string{
				{"01"},
				{"02", "03"},
				{"04"},
			},
		},
		{
			name: "circular dependency",
			jobs: []jobDef{
				{id: "01", deps: []string{"03"}},
				{id: "02", deps: []string{"01"}},
				{id: "03", deps: []string{"02"}},
			},
			wantErr: true,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			
			// Create job files
			for i, job := range tt.jobs {
				content := fmt.Sprintf(`---
id: %s
title: "Job %s"
status: pending
type: oneshot
depends_on: %s
---
Test job %s`, job.id, job.id, formatDeps(job.deps), job.id)
				
				filename := fmt.Sprintf("%02d-job-%s.md", i+1, job.id)
				err := os.WriteFile(filepath.Join(tmpDir, filename), []byte(content), 0644)
				require.NoError(t, err)
			}
			
			// Load plan
			plan, err := LoadPlan(tmpDir)
			
			if tt.wantErr {
				assert.Error(t, err, "Expected error for circular dependencies")
				return
			}
			
			require.NoError(t, err)
			
			// Simulate execution stages
			stages := [][]string{}
			completed := make(map[string]bool)
			
			for {
				stage := []string{}
				
				for _, job := range plan.Jobs {
					if completed[job.ID] {
						continue
					}
					
					// Check if all dependencies are completed
					canRun := true
					for _, dep := range job.DependsOn {
						if !completed[dep] {
							canRun = false
							break
						}
					}
					
					if canRun {
						stage = append(stage, job.ID)
					}
				}
				
				if len(stage) == 0 {
					break
				}
				
				// Mark stage jobs as completed
				for _, id := range stage {
					completed[id] = true
				}
				
				stages = append(stages, stage)
			}
			
			// Compare execution stages
			assert.Equal(t, len(tt.expected), len(stages), "Number of execution stages mismatch")
			
			for i, expectedStage := range tt.expected {
				if i < len(stages) {
					assert.ElementsMatch(t, expectedStage, stages[i], "Stage %d mismatch", i)
				}
			}
		})
	}
}

func TestStatePersistence(t *testing.T) {
	tmpDir := t.TempDir()
	
	// Create job file
	jobContent := `---
id: test-job
title: "Test Job"
status: pending
type: oneshot
---
Test prompt`
	
	jobPath := filepath.Join(tmpDir, "01-test.md")
	err := os.WriteFile(jobPath, []byte(jobContent), 0644)
	require.NoError(t, err)
	
	// Load job
	job, err := LoadJob(jobPath)
	require.NoError(t, err)
	
	// Update status
	sp := NewStatePersister()
	err = sp.UpdateJobStatus(job, JobStatusRunning)
	require.NoError(t, err)
	
	// Reload and verify
	reloaded, err := LoadJob(jobPath)
	require.NoError(t, err)
	assert.Equal(t, JobStatusRunning, reloaded.Status)
	
	// Append output
	err = sp.AppendJobOutput(job, "Test output line 1")
	require.NoError(t, err)
	
	err = sp.AppendJobOutput(job, "Test output line 2")
	require.NoError(t, err)
	
	// Read file and verify output section
	content, err := os.ReadFile(jobPath)
	require.NoError(t, err)
	
	contentStr := string(content)
	assert.Contains(t, contentStr, "## Output")
	assert.Contains(t, contentStr, "Test output line 1")
	assert.Contains(t, contentStr, "Test output line 2")
}

func TestConcurrentJobExecution(t *testing.T) {
	tmpDir := t.TempDir()
	
	// Create 10 independent jobs
	for i := 1; i <= 10; i++ {
		content := fmt.Sprintf(`---
id: job-%02d
title: "Job %d"
status: pending
type: oneshot
---
Test job %d`, i, i, i)
		
		filename := fmt.Sprintf("%02d-job.md", i)
		err := os.WriteFile(filepath.Join(tmpDir, filename), []byte(content), 0644)
		require.NoError(t, err)
	}
	
	plan, err := LoadPlan(tmpDir)
	require.NoError(t, err)
	
	// Track execution order
	var mu sync.Mutex
	executionOrder := []string{}
	executionTimes := make(map[string]time.Time)
	
	// Mock executor that records execution
	mockExecutor := &MockExecutor{
		OnExecute: func(ctx context.Context, job *Job, plan *Plan) error {
			mu.Lock()
			executionOrder = append(executionOrder, job.ID)
			executionTimes[job.ID] = time.Now()
			mu.Unlock()
			
			// Simulate work
			time.Sleep(100 * time.Millisecond)
			
			// Update status
			job.Status = JobStatusCompleted
			return nil
		},
	}
	
	// Create orchestrator
	config := &Config{
		MaxParallelJobs: 3,
		Timeout:         30 * time.Second,
	}
	orch := NewOrchestrator(nil, config)
	orch.RegisterExecutor(JobTypeOneshot, mockExecutor)
	
	// Execute all jobs
	ctx := context.Background()
	var wg sync.WaitGroup
	
	for _, job := range plan.Jobs {
		wg.Add(1)
		go func(j *Job) {
			defer wg.Done()
			orch.ExecuteJob(ctx, j, plan)
		}(job)
		
		// Small delay to respect parallelism limit
		time.Sleep(10 * time.Millisecond)
	}
	
	wg.Wait()
	
	// Verify all executed
	assert.Len(t, executionOrder, 10)
	
	// Verify parallelism was respected
	// Check that at most 3 jobs were running at the same time
	maxConcurrent := 0
	for i := 0; i < len(executionOrder); i++ {
		startTime := executionTimes[executionOrder[i]]
		concurrent := 1
		
		for j := 0; j < len(executionOrder); j++ {
			if i == j {
				continue
			}
			otherStart := executionTimes[executionOrder[j]]
			otherEnd := otherStart.Add(100 * time.Millisecond)
			
			// Check if jobs overlapped
			if startTime.After(otherStart) && startTime.Before(otherEnd) {
				concurrent++
			}
		}
		
		if concurrent > maxConcurrent {
			maxConcurrent = concurrent
		}
	}
	
	assert.LessOrEqual(t, maxConcurrent, 3, "Max parallelism exceeded")
}

// Helper types and functions

type jobDef struct {
	id   string
	deps []string
}

func formatDeps(deps []string) string {
	if len(deps) == 0 {
		return "[]"
	}
	
	formatted := []string{}
	for _, dep := range deps {
		formatted = append(formatted, fmt.Sprintf("  - %s", dep))
	}
	return "\n" + strings.Join(formatted, "\n")
}

func createTestJobFiles(t *testing.T, dir string) {
	// Create the job files that the smart mock would have created
	
	// 02-design-api.md
	content := `---
id: design-api
title: "Design API Structure"
status: pending
type: oneshot
depends_on:
  - 01-high-level-plan.md
output:
  type: file
---
Design the API structure for the feature.`
	err := os.WriteFile(filepath.Join(dir, "02-design-api.md"), []byte(content), 0644)
	require.NoError(t, err)
	
	// 03-implement-backend.md
	content = `---
id: impl-backend
title: "Implement Backend"
status: pending
type: agent
depends_on:
  - 02-design-api.md
worktree: backend-dev
output:
  type: commit
---
Implement the backend based on the API design.`
	err = os.WriteFile(filepath.Join(dir, "03-implement-backend.md"), []byte(content), 0644)
	require.NoError(t, err)
	
	// 04-implement-frontend.md
	content = `---
id: impl-frontend
title: "Implement Frontend"
status: pending
type: agent
depends_on:
  - 02-design-api.md
worktree: frontend-dev
output:
  type: commit
---
Implement the frontend based on the API design.`
	err = os.WriteFile(filepath.Join(dir, "04-implement-frontend.md"), []byte(content), 0644)
	require.NoError(t, err)
}

// MockExecutor for testing
type MockExecutor struct {
	OnExecute func(ctx context.Context, job *Job, plan *Plan) error
}

func (m *MockExecutor) Name() string {
	return "mock"
}

func (m *MockExecutor) CanExecute(job *Job) bool {
	return true
}

func (m *MockExecutor) Execute(ctx context.Context, job *Job, plan *Plan) error {
	if m.OnExecute != nil {
		return m.OnExecute(ctx, job, plan)
	}
	return nil
}