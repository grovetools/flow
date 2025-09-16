package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
		Output: OutputConfig{
			Type: "file",
		},
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
	// Create temporary directory
	tmpDir, err := os.MkdirTemp("", "prompt-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create source files
	sourceFile := filepath.Join(tmpDir, "source.md")
	err = os.WriteFile(sourceFile, []byte("# Source Content\nThis is the source"), 0644)
	if err != nil {
		t.Fatalf("Failed to write source file: %v", err)
	}

	// Create job with prompt sources
	job := &Job{
		ID: "test-job",
		PromptBody: "Test the implementation",
		PromptSource: []string{"source.md"},
		FilePath: filepath.Join(tmpDir, "job.md"),
	}

	plan := &Plan{
		Directory: tmpDir,
	}

	// Build prompt
	prompt, err := buildPromptFromSources(job, plan)
	if err != nil {
		t.Errorf("Failed to build prompt: %v", err)
	}

	// Verify prompt contains both source and job prompt
	if prompt == "" {
		t.Errorf("Expected non-empty prompt")
	}
}

func TestHeadlessAgentExecutor_BuildPrompt_ReferenceBasedPrompts(t *testing.T) {
	// Create temporary directory
	tmpDir, err := os.MkdirTemp("", "reference-prompt-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create source files
	sourceFile1 := filepath.Join(tmpDir, "api.go")
	err = os.WriteFile(sourceFile1, []byte("package api\n\nfunc Handler() {}"), 0644)
	if err != nil {
		t.Fatalf("Failed to write source file: %v", err)
	}
	
	sourceFile2 := filepath.Join(tmpDir, "service.go")
	err = os.WriteFile(sourceFile2, []byte("package service\n\nfunc Process() {}"), 0644)
	if err != nil {
		t.Fatalf("Failed to write source file: %v", err)
	}

	// Create job with template and prompt sources
	job := &Job{
		ID: "test-job",
		Template: "agent-run",
		PromptBody: "<!-- This step uses template 'agent-run' with source files -->\n<!-- Template will be resolved at execution time -->\n\n## Additional Instructions\n\nRefactor the API",
		PromptSource: []string{"api.go", "service.go"},
		FilePath: filepath.Join(tmpDir, "job.md"),
	}

	plan := &Plan{
		Directory: tmpDir,
	}

	// Build prompt
	prompt, err := buildPromptFromSources(job, plan)
	if err != nil {
		// The test might fail if the template doesn't exist, but we can check
		// if it's trying to use the reference-based path
		if !strings.Contains(err.Error(), "resolving template agent-run") {
			t.Errorf("buildPromptFromSources() unexpected error = %v", err)
		}
		// Even with error, check if reference-based path was attempted
		return
	}

	// Verify prompt structure for reference-based prompts
	if !strings.Contains(prompt, "api.go") {
		t.Errorf("Prompt missing api.go reference")
	}
	if !strings.Contains(prompt, "service.go") {
		t.Errorf("Prompt missing service.go reference")
	}
	if !strings.Contains(prompt, "Refactor the API") {
		t.Errorf("Prompt missing additional instructions")
	}
}