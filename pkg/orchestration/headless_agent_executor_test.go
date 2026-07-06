package orchestration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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

// --- J3: headless terminal-status finalizer tests ---

// writeHeadlessJobFixture writes a minimal headless job .md to disk with the
// given frontmatter status and returns the plan + loaded job. It isolates all
// daemon/state side effects to a throwaway GROVE_HOME so the finalizer's
// EndSession/CompleteJob paths never touch real state or spawn a daemon.
func writeHeadlessJobFixture(t *testing.T, status JobStatus) (*Plan, *Job) {
	t.Helper()
	tmpDir := t.TempDir()
	// Fully hermetic daemon isolation for the completed path (CompleteJob →
	// daemon.NewWithAutoStart().EndSession): redirect all grove state to a
	// throwaway GROVE_HOME AND strip grove's bin dir from PATH so autostart
	// cannot find `groved`. With no reachable binary, NewWithAutoStart falls back
	// to a no-op LocalClient — EndSession becomes a harmless error we ignore —
	// so the test never spawns or touches a real daemon. `sh`/`git` stay
	// available via the standard system dirs.
	t.Setenv("GROVE_HOME", filepath.Join(tmpDir, "grovehome"))
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")

	plan := &Plan{
		Name:      "test-plan",
		Directory: tmpDir,
		Jobs:      []*Job{},
		JobsByID:  make(map[string]*Job),
	}
	content := "---\nid: hjob\ntitle: headless job\nstatus: " + string(status) +
		"\ntype: headless_agent\n---\n\nbody\n"
	jobPath := filepath.Join(tmpDir, "hjob.md")
	if err := os.WriteFile(jobPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	job, err := LoadJob(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	job.Filename = filepath.Base(jobPath)
	job.FilePath = jobPath
	// A non-zero StartTime so completed/failed writes get a duration.
	job.StartTime = time.Now().Add(-time.Minute)
	plan.Jobs = append(plan.Jobs, job)
	plan.JobsByID[job.ID] = job
	return plan, job
}

func writeHeadlessStatusFixture(t *testing.T, plan *Plan, job *Job, exitCode int) {
	t.Helper()
	p := headlessStatusPath(plan, job)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(fmt.Sprintf(`{"exit_code":%d,"timestamp":%q,"job_id":%q}`,
		exitCode, time.Now().Format(time.RFC3339), job.ID))
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assertFrontmatterStatus(t *testing.T, path, want string) {
	t.Helper()
	content := readFileString(t, path)
	if !strings.Contains(content, "status: "+want) {
		t.Errorf("expected frontmatter status %q, file:\n%s", want, content)
	}
}

func TestFinalizeHeadlessJob(t *testing.T) {
	t.Run("exit 0 completes", func(t *testing.T) {
		plan, job := writeHeadlessJobFixture(t, JobStatusRunning)
		writeHeadlessStatusFixture(t, plan, job, 0)
		if err := FinalizeHeadlessJob(job, plan); err != nil {
			t.Fatalf("finalize: %v", err)
		}
		assertFrontmatterStatus(t, job.FilePath, "completed")
	})

	t.Run("nonzero exit fails with last_error containing code", func(t *testing.T) {
		// Disk status idle (the strander) — proves idle is reconciled, not kept.
		plan, job := writeHeadlessJobFixture(t, JobStatusIdle)
		writeHeadlessStatusFixture(t, plan, job, 3)
		if err := FinalizeHeadlessJob(job, plan); err != nil {
			t.Fatalf("finalize: %v", err)
		}
		content := readFileString(t, job.FilePath)
		if !strings.Contains(content, "status: failed") {
			t.Errorf("expected status failed, file:\n%s", content)
		}
		if !strings.Contains(content, "code: 3") {
			t.Errorf("expected last_error to contain 'code: 3', file:\n%s", content)
		}
	})

	t.Run("missing status fails with launcher-died message", func(t *testing.T) {
		plan, job := writeHeadlessJobFixture(t, JobStatusRunning)
		// No .status file written.
		if err := FinalizeHeadlessJob(job, plan); err != nil {
			t.Fatalf("finalize: %v", err)
		}
		content := readFileString(t, job.FilePath)
		if !strings.Contains(content, "status: failed") {
			t.Errorf("expected status failed, file:\n%s", content)
		}
		if !strings.Contains(content, "without status file") {
			t.Errorf("expected launcher-died message, file:\n%s", content)
		}
	})

	t.Run("already terminal on disk is a no-op (A3 LoadJob-before-guard)", func(t *testing.T) {
		plan, job := writeHeadlessJobFixture(t, JobStatusCompleted)
		// A non-zero .status would flip a non-terminal job to failed; the guard
		// must read DISK (completed) and no-op, ignoring both the .status and the
		// stale in-memory running status below.
		writeHeadlessStatusFixture(t, plan, job, 3)
		job.Status = JobStatusRunning // the exact stale in-memory strander
		before := readFileString(t, job.FilePath)
		if err := FinalizeHeadlessJob(job, plan); err != nil {
			t.Fatalf("finalize: %v", err)
		}
		after := readFileString(t, job.FilePath)
		if before != after {
			t.Errorf("expected file bytes unchanged for already-terminal disk status\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})
}

func TestWaitAndWriteStatus(t *testing.T) {
	t.Run("clean exit writes status 0 and completes", func(t *testing.T) {
		plan, job := writeHeadlessJobFixture(t, JobStatusRunning)
		e := NewHeadlessAgentExecutor(NewMockLLMClient(), &ExecutorConfig{})
		cmd := exec.Command("sh", "-c", "exit 0")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		e.waitAndWriteStatus(context.Background(), job, plan, cmd)

		sc, err := readHeadlessStatus(headlessStatusPath(plan, job))
		if err != nil {
			t.Fatalf("read status: %v", err)
		}
		if sc.ExitCode != 0 {
			t.Errorf("expected exit_code 0, got %d", sc.ExitCode)
		}
		assertFrontmatterStatus(t, job.FilePath, "completed")
	})

	t.Run("nonzero exit writes status and fails", func(t *testing.T) {
		plan, job := writeHeadlessJobFixture(t, JobStatusRunning)
		e := NewHeadlessAgentExecutor(NewMockLLMClient(), &ExecutorConfig{})
		cmd := exec.Command("sh", "-c", "exit 3")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		e.waitAndWriteStatus(context.Background(), job, plan, cmd)

		sc, err := readHeadlessStatus(headlessStatusPath(plan, job))
		if err != nil {
			t.Fatalf("read status: %v", err)
		}
		if sc.ExitCode != 3 {
			t.Errorf("expected exit_code 3, got %d", sc.ExitCode)
		}
		content := readFileString(t, job.FilePath)
		if !strings.Contains(content, "status: failed") {
			t.Errorf("expected status failed, file:\n%s", content)
		}
		if !strings.Contains(content, "code: 3") {
			t.Errorf("expected last_error to contain 'code: 3', file:\n%s", content)
		}
	})
}
