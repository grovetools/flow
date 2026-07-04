package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestResolveGitRootForWorktree covers the notebook-aware git-root resolution
// used by worktree creation: notebook plans must NEVER fall back to the plan
// directory (the silent fallback fabricated a bogus worktree container at the
// notebook plan dir during transient discovery races), while the historical
// plan-dir fallback is preserved for non-notebook, non-git plan dirs.
func TestResolveGitRootForWorktree(t *testing.T) {
	ctx := context.Background()
	discoveryErr := errors.New("could not find git root from plan directory or current directory")

	t.Run("success passes through without notebook check", func(t *testing.T) {
		calls := 0
		root, err := resolveGitRootForWorktreeWith(ctx, "/plans/p",
			func(string) (string, error) { calls++; return "/repo/root", nil },
			func(string) bool { t.Fatal("underNotebook must not be called on success"); return false },
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if root != "/repo/root" || calls != 1 {
			t.Errorf("root = %q (calls=%d), want /repo/root (1)", root, calls)
		}
	})

	t.Run("non-notebook failure keeps plan-dir fallback", func(t *testing.T) {
		calls := 0
		root, err := resolveGitRootForWorktreeWith(ctx, "/tmp/bare-plan",
			func(string) (string, error) { calls++; return "", discoveryErr },
			func(string) bool { return false },
		)
		if err != nil {
			t.Fatalf("non-notebook fallback must not error, got: %v", err)
		}
		if root != "/tmp/bare-plan" {
			t.Errorf("root = %q, want the plan dir fallback", root)
		}
		// Deterministic failure — no retries for non-notebook dirs.
		if calls != 1 {
			t.Errorf("resolver called %d times, want 1 (no retry outside notebooks)", calls)
		}
	})

	t.Run("notebook transient failure recovers via retry", func(t *testing.T) {
		calls := 0
		root, err := resolveGitRootForWorktreeWith(ctx, "/notebooks/ws/plans/p",
			func(string) (string, error) {
				calls++
				if calls < 2 {
					return "", discoveryErr // first call races, retry clears it
				}
				return "/repo/root", nil
			},
			func(string) bool { return true },
		)
		if err != nil {
			t.Fatalf("retry should have recovered, got: %v", err)
		}
		if root != "/repo/root" || calls != 2 {
			t.Errorf("root = %q (calls=%d), want /repo/root (2)", root, calls)
		}
	})

	t.Run("notebook persistent failure hard-fails, never plan dir", func(t *testing.T) {
		calls := 0
		root, err := resolveGitRootForWorktreeWith(ctx, "/notebooks/ws/plans/p",
			func(string) (string, error) { calls++; return "", discoveryErr },
			func(string) bool { return true },
		)
		if err == nil {
			t.Fatalf("want hard error for unresolvable notebook plan, got root %q", root)
		}
		if root != "" {
			t.Errorf("root = %q, must be empty on error", root)
		}
		if !strings.Contains(err.Error(), "notebook plan") || !strings.Contains(err.Error(), "retry the job") {
			t.Errorf("error should explain the notebook/transient situation, got: %v", err)
		}
		if !errors.Is(err, discoveryErr) {
			t.Errorf("error should wrap the underlying resolution error, got: %v", err)
		}
		// 1 initial + 2 bounded retries.
		if calls != 3 {
			t.Errorf("resolver called %d times, want 3", calls)
		}
	})

	t.Run("cancelled context aborts the retry backoff", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := resolveGitRootForWorktreeWith(cancelled, "/notebooks/ws/plans/p",
			func(string) (string, error) { return "", discoveryErr },
			func(string) bool { return true },
		)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("want context.Canceled, got: %v", err)
		}
	})
}
