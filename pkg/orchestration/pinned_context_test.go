package orchestration

import (
	"os"
	"path/filepath"
	"testing"
)

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
