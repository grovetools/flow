package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

	// Use an invalid model so the executor fails deterministically at model
	// validation — the first thing Execute does, before any worktree prep, agent
	// launch, or daemon contact. This keeps the test fully hermetic (no git repo,
	// no groved, no network) while still exercising the "job marked failed on
	// error" path.
	job := &Job{
		ID:       "test-job",
		Type:     JobTypeHeadlessAgent,
		Status:   JobStatusPending,
		Model:    "not-a-real-model",
		Worktree: "test-worktree",
		FilePath: filepath.Join(tmpDir, "test-job.md"),
	}

	// Create executor with mock
	config := &ExecutorConfig{
		Timeout: 5 * time.Second,
	}
	executor := NewHeadlessAgentExecutor(NewMockLLMClient(), config)

	// Execute job
	ctx := context.Background()
	err = executor.Execute(ctx, job, plan)

	// Expect an error due to the invalid model.
	if err == nil {
		t.Errorf("Expected error due to invalid model, got nil")
	}

	// Verify status was updated
	if job.Status != JobStatusFailed {
		t.Errorf("Expected job status to be failed, got %s", job.Status)
	}
}

func TestHeadlessAgentExecutor_Name(t *testing.T) {
	executor := NewHeadlessAgentExecutor(nil, nil)
	if executor.Name() != "headless_agent" {
		t.Errorf("Expected name 'headless_agent', got %s", executor.Name())
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

func TestBuildHeadlessCommand(t *testing.T) {
	ctx := context.Background()

	t.Run("claude pipes prompt via stdin", func(t *testing.T) {
		cmd, err := buildHeadlessCommand(ctx, "claude", "do the task", []string{"--model", "opus"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := filepath.Base(cmd.Path); got != "claude" && cmd.Args[0] != "claude" {
			t.Errorf("expected claude binary, got path=%s args[0]=%s", cmd.Path, cmd.Args[0])
		}
		// The bypass flag is no longer hardcoded here — flags come solely from
		// the agentArgs passed in (resolved upstream from providers.claude.args).
		wantArgs := []string{"claude", "--model", "opus"}
		if len(cmd.Args) != len(wantArgs) {
			t.Fatalf("expected args %v, got %v", wantArgs, cmd.Args)
		}
		for i, a := range wantArgs {
			if cmd.Args[i] != a {
				t.Errorf("arg %d: expected %q, got %q", i, a, cmd.Args[i])
			}
		}
		if cmd.Stdin == nil {
			t.Errorf("expected prompt on stdin for claude")
		}
	})

	t.Run("opencode uses run subcommand with prompt arg", func(t *testing.T) {
		cmd, err := buildHeadlessCommand(ctx, "opencode", "do the task", []string{"--log-level", "DEBUG"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantArgs := []string{"opencode", "run", "--log-level", "DEBUG", "do the task"}
		if len(cmd.Args) != len(wantArgs) {
			t.Fatalf("expected args %v, got %v", wantArgs, cmd.Args)
		}
		for i, a := range wantArgs {
			if cmd.Args[i] != a {
				t.Errorf("arg %d: expected %q, got %q", i, a, cmd.Args[i])
			}
		}
		if cmd.Stdin != nil {
			t.Errorf("expected no stdin for opencode (prompt is an argument)")
		}
	})

	t.Run("codex returns actionable error", func(t *testing.T) {
		_, err := buildHeadlessCommand(ctx, "codex", "do the task", nil)
		if err == nil {
			t.Fatal("expected error for codex headless, got nil")
		}
		for _, want := range []string{"codex", "headless", "claude", "opencode"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error should mention %q, got: %v", want, err)
			}
		}
	})

	t.Run("unknown provider returns error naming it", func(t *testing.T) {
		_, err := buildHeadlessCommand(ctx, "gemini", "do the task", nil)
		if err == nil {
			t.Fatal("expected error for unknown provider, got nil")
		}
		if !strings.Contains(err.Error(), "gemini") {
			t.Errorf("error should name the provider, got: %v", err)
		}
	})
}

func TestHeadlessAgentExecutor_BuildPrompt(t *testing.T) {
	t.Skip("Test uses removed buildPromptFromSources function - refactored into executor method")
}

func TestHeadlessAgentExecutor_BuildPrompt_ReferenceBasedPrompts(t *testing.T) {
	t.Skip("Test uses removed buildPromptFromSources function - refactored into executor method")
}
