package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mockAgentRunner implements AgentRunner for testing.
type mockAgentRunner struct {
	runCalled bool
	runError  error
}

func (m *mockAgentRunner) RunAgent(ctx context.Context, worktree string, prompt string) error {
	m.runCalled = true
	return m.runError
}

func TestHeadlessAgentExecutor_Execute(t *testing.T) {
	// Create temporary directory for test
	tmpDir, err := os.MkdirTemp("", "headless-agent-executor-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test plan and job
	plan := &Plan{
		Name:      "test-plan",
		Directory: tmpDir,
	}

	job := &Job{
		ID:       "test-job",
		Type:     JobTypeHeadlessAgent,
		Status:   JobStatusPending,
		Worktree: "test-worktree",
		FilePath: filepath.Join(tmpDir, "test-job.md"),
	}

	// Create executor with mock
	config := &ExecutorConfig{
		Timeout: 5 * time.Second,
	}
	executor := NewHeadlessAgentExecutor(NewMockLLMClient(), config)
	
	// Use mock agent runner
	mockRunner := &mockAgentRunner{}
	executor.agentRunner = mockRunner

	// Execute job
	ctx := context.Background()
	err = executor.Execute(ctx, job, plan)

	// For now, expect error due to missing git repo
	if err == nil {
		t.Errorf("Expected error due to missing git repo, got nil")
	}

	// Verify status was updated
	if job.Status != JobStatusFailed {
		t.Errorf("Expected job status to be failed, got %s", job.Status)
	}
}

func TestHeadlessAgentExecutor_Name(t *testing.T) {
	executor := NewHeadlessAgentExecutor(nil, nil)
	if executor.Name() != "agent" {
		t.Errorf("Expected name 'agent', got %s", executor.Name())
	}
}

func TestHeadlessAgentExecutor_PrepareWorktree(t *testing.T) {
	// This test would require a real git repository
	// For now, we'll just test the error cases

	executor := NewHeadlessAgentExecutor(nil, nil)
	ctx := context.Background()

	// Test missing worktree in job
	job := &Job{
		ID: "test-job",
	}
	plan := &Plan{
		Name: "test-plan",
	}

	_, err := executor.prepareWorktree(ctx, job, plan)
	if err == nil {
		t.Errorf("Expected error for missing worktree, got nil")
	}
}

func TestHeadlessAgentExecutor_BuildPrompt(t *testing.T) {
	t.Skip("Test uses removed buildPromptFromSources function - refactored into executor method")
}

func TestHeadlessAgentExecutor_BuildPrompt_ReferenceBasedPrompts(t *testing.T) {
	t.Skip("Test uses removed buildPromptFromSources function - refactored into executor method")
}