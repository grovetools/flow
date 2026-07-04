package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolvePinnedSource covers the worktree-first resolution contract that the
// bare ResolvePromptSource missed: a worktree-root-relative pin must resolve even
// when the daemon's cwd is not the worktree, an absolute path is honored as-is,
// the plan-dir fallback still resolves plan-relative pins, and a genuinely
// missing file returns an error (so the chat path can hard-fail visibly).
func TestResolvePinnedSource(t *testing.T) {
	worktreeRoot := t.TempDir()
	planDir := t.TempDir()

	// Worktree-root-relative file (e.g. "core/logging/logger_test.go").
	rel := filepath.Join("core", "logging", "logger_test.go")
	wtFile := filepath.Join(worktreeRoot, rel)
	if err := os.MkdirAll(filepath.Dir(wtFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wtFile, []byte("package logging\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Plan-relative file, to exercise the ResolvePromptSource fallback.
	planRel := "spec.md"
	if err := os.WriteFile(filepath.Join(planDir, planRel), []byte("spec"), 0o600); err != nil {
		t.Fatal(err)
	}

	plan := &Plan{Directory: planDir}

	t.Run("worktree-relative hit", func(t *testing.T) {
		got, err := resolvePinnedSource(rel, worktreeRoot, plan)
		if err != nil {
			t.Fatalf("resolvePinnedSource(%q) error = %v", rel, err)
		}
		if got != wtFile {
			t.Errorf("resolvePinnedSource(%q) = %q, want %q", rel, got, wtFile)
		}
	})

	t.Run("absolute path as-is", func(t *testing.T) {
		got, err := resolvePinnedSource(wtFile, worktreeRoot, plan)
		if err != nil {
			t.Fatalf("resolvePinnedSource(abs) error = %v", err)
		}
		if got != wtFile {
			t.Errorf("resolvePinnedSource(abs) = %q, want %q", got, wtFile)
		}
	})

	t.Run("plan-dir fallback still works", func(t *testing.T) {
		got, err := resolvePinnedSource(planRel, worktreeRoot, plan)
		if err != nil {
			t.Fatalf("resolvePinnedSource(%q) error = %v", planRel, err)
		}
		want := filepath.Join(planDir, planRel)
		if got != want {
			t.Errorf("resolvePinnedSource(%q) = %q, want %q", planRel, got, want)
		}
	})

	t.Run("missing file errors", func(t *testing.T) {
		if _, err := resolvePinnedSource("does/not/exist.go", worktreeRoot, plan); err == nil {
			t.Error("resolvePinnedSource(missing) = nil error, want error")
		}
	})
}

// TestExecuteChatJob_UnresolvablePinFailsJob is the Bug 2 regression: a chat job
// whose pinned_context names a file that resolves nowhere must end with the job
// FILE at status: failed (not stuck at running). Resolution happens before the
// LLM call, so the mock client is never invoked.
func TestExecuteChatJob_UnresolvablePinFailsJob(t *testing.T) {
	tmpDir := t.TempDir()

	plan := &Plan{
		Name:      "test-plan",
		Directory: tmpDir,
		Jobs:      []*Job{},
		JobsByID:  make(map[string]*Job),
	}

	jobContent := `---
id: pin-fail-1
title: pin-fail
status: pending
type: chat
template: chat
pinned_context:
  - core/logging/does-not-exist.go
---

Please answer using the pinned reference file.
`
	jobPath := filepath.Join(tmpDir, "01-pin-fail.md")
	if err := os.WriteFile(jobPath, []byte(jobContent), 0o600); err != nil {
		t.Fatal(err)
	}

	job, err := LoadJob(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	job.Filename = "01-pin-fail.md"
	job.FilePath = jobPath

	executor := NewOneShotExecutor(NewMockLLMClient(), nil)

	err = executor.Execute(context.Background(), job, plan)
	if err == nil {
		t.Fatal("Execute() = nil error, want an unresolvable-pin error")
	}
	if !strings.Contains(err.Error(), "pinned_context") {
		t.Errorf("Execute() error = %v, want it to mention pinned_context", err)
	}

	// The in-memory job status must be terminal-failed...
	if job.Status != JobStatusFailed {
		t.Errorf("job.Status = %v, want failed", job.Status)
	}

	// ...and, crucially, the on-disk .md must not be stuck at running.
	after, err := os.ReadFile(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "status: failed") {
		t.Errorf("job file not written to failed status:\n%s", string(after))
	}
	if strings.Contains(string(after), "status: running") {
		t.Errorf("job file stuck at running status:\n%s", string(after))
	}
}

// TestLoadJobWithPinnedContext verifies the pinned_context frontmatter field
// round-trips through the YAML loader into Job.PinnedContext in declared order.
func TestLoadJobWithPinnedContext(t *testing.T) {
	tmpDir := t.TempDir()

	content := `---
id: pinned-job-123
title: Pinned Job
status: pending
type: chat
pinned_context:
  - docs/spec.md
  - notes/reference.md
---
Chat that pins reference files.`

	path := filepath.Join(tmpDir, "01-pinned-job.md")
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
	want := []string{"docs/spec.md", "notes/reference.md"}
	if len(job.PinnedContext) != len(want) {
		t.Fatalf("Job.PinnedContext = %v, want %v", job.PinnedContext, want)
	}
	for i := range want {
		if job.PinnedContext[i] != want[i] {
			t.Errorf("Job.PinnedContext[%d] = %q, want %q", i, job.PinnedContext[i], want[i])
		}
	}
}

// TestLoadJobWithoutPinnedContext confirms the field defaults to nil (empty)
// when absent — the zero-pinned path that must behave exactly as before.
func TestLoadJobWithoutPinnedContext(t *testing.T) {
	tmpDir := t.TempDir()
	content := `---
id: plain-job-123
title: Plain Job
status: pending
type: chat
---
No pinned context here.`

	path := filepath.Join(tmpDir, "01-plain-job.md")
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
	if len(plan.Jobs[0].PinnedContext) != 0 {
		t.Errorf("Job.PinnedContext = %v, want empty", plan.Jobs[0].PinnedContext)
	}
}

func eqSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestReconcileOrder covers the pinned-order reconciliation contract: persisted
// members keep their positions, new members append, removed members drop.
func TestReconcileOrder(t *testing.T) {
	tests := []struct {
		name      string
		persisted []string
		current   []string
		want      []string
	}{
		{
			name:      "no persisted order — declared order preserved",
			persisted: nil,
			current:   []string{"a", "b", "c"},
			want:      []string{"a", "b", "c"},
		},
		{
			name:      "new entry appended at end even if declared earlier",
			persisted: []string{"a", "b"},
			current:   []string{"x", "a", "b"}, // x is new, declared first
			want:      []string{"a", "b", "x"}, // but a,b keep their persisted positions
		},
		{
			name:      "removed entry dropped, others hold position",
			persisted: []string{"a", "b", "c"},
			current:   []string{"a", "c"},
			want:      []string{"a", "c"},
		},
		{
			name:      "reordered frontmatter ignored — persisted order wins",
			persisted: []string{"a", "b", "c"},
			current:   []string{"c", "b", "a"},
			want:      []string{"a", "b", "c"},
		},
		{
			name:      "mix: keep persisted, drop missing, append new",
			persisted: []string{"a", "b", "c"},
			current:   []string{"b", "c", "d", "e"},
			want:      []string{"b", "c", "d", "e"},
		},
		{
			name:      "duplicates in current collapse to first emission",
			persisted: nil,
			current:   []string{"a", "a", "b"},
			want:      []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reconcileOrder(tt.persisted, tt.current)
			if !eqSlice(got, tt.want) {
				t.Errorf("reconcileOrder(%v, %v) = %v, want %v", tt.persisted, tt.current, got, tt.want)
			}
		})
	}
}

// TestReconcilePinnedOrderPersistsAndReloads exercises the disk round-trip: the
// first turn persists declared order; a later turn with a new file appends it
// after the persisted members regardless of frontmatter position.
func TestReconcilePinnedOrderPersistsAndReloads(t *testing.T) {
	tmpDir := t.TempDir()
	plan := &Plan{Directory: tmpDir}
	job := &Job{ID: "job-xyz"}

	// Turn 1: establish order.
	got := reconcilePinnedOrder(nil, plan, job, []string{"/p/a.md", "/p/b.md"})
	if !eqSlice(got, []string{"/p/a.md", "/p/b.md"}) {
		t.Fatalf("turn 1 = %v", got)
	}

	orderPath := filepath.Join(tmpDir, ".artifacts", "job-xyz", pinnedOrderFileName)
	if _, err := os.Stat(orderPath); err != nil {
		t.Fatalf("order artifact not written: %v", err)
	}

	// Turn 2: user prepends a new file in frontmatter; it must land LAST.
	got = reconcilePinnedOrder(nil, plan, job, []string{"/p/c.md", "/p/a.md", "/p/b.md"})
	if !eqSlice(got, []string{"/p/a.md", "/p/b.md", "/p/c.md"}) {
		t.Fatalf("turn 2 = %v, want a,b,c", got)
	}
}
