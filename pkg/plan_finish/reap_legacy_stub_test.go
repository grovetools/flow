package plan_finish

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitInitForReap creates a minimal real git repo with one commit so that
// `git worktree add` works. Returns the repo root.
func gitInitForReap(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	return root
}

func worktreeIsRegistered(t *testing.T, gitRoot, path string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", gitRoot, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	return worktreeListContainsPath(string(out), path)
}

// TestReapLegacyStubWorktree_RemovesStrayStub asserts the defensive cleanup
// removes a stray legacy `<gitRoot>/.grove-worktrees/<name>` superproject git
// worktree that the registry-first prune leaves behind (it has no registry
// entry).
func TestReapLegacyStubWorktree_RemovesStrayStub(t *testing.T) {
	gitRoot := gitInitForReap(t)
	name := "feature-x"
	legacyPath := filepath.Join(gitRoot, ".grove-worktrees", name)

	// Create the stray legacy worktree the way the old legacy-only path would:
	// a bare `git worktree add` of the superproject.
	add := exec.Command("git", "-C", gitRoot, "worktree", "add", "-q", "-b", name, legacyPath)
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	if !worktreeIsRegistered(t, gitRoot, legacyPath) {
		t.Fatalf("precondition: legacy stub not registered at %s", legacyPath)
	}

	reapLegacyStubWorktree(context.Background(), io.Discard, gitRoot, name, false)

	if worktreeIsRegistered(t, gitRoot, legacyPath) {
		t.Errorf("legacy stub still registered after reap: %s", legacyPath)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Errorf("legacy stub dir still on disk after reap: %s (err=%v)", legacyPath, err)
	}
}

// TestReapLegacyStubWorktree_NoStubNoOp asserts the cleanup is a safe no-op when
// no stray legacy stub exists (the common case once worktree-prep is anchor
// aware).
func TestReapLegacyStubWorktree_NoStubNoOp(t *testing.T) {
	gitRoot := gitInitForReap(t)
	// No worktree added; must not error or create anything.
	reapLegacyStubWorktree(context.Background(), io.Discard, gitRoot, "absent", false)
	if _, err := os.Stat(filepath.Join(gitRoot, ".grove-worktrees", "absent")); !os.IsNotExist(err) {
		t.Errorf("reap should not create the legacy path; stat err=%v", err)
	}
}

// TestReapLegacyStubWorktree_LeavesUnregisteredDir asserts the cleanup does NOT
// `git worktree remove` (nor delete) a directory that merely sits at the legacy
// location but is not a registered git worktree of gitRoot — the registration
// check guards against clobbering unrelated content.
func TestReapLegacyStubWorktree_LeavesUnregisteredDir(t *testing.T) {
	gitRoot := gitInitForReap(t)
	name := "manual"
	strayDir := filepath.Join(gitRoot, ".grove-worktrees", name)
	if err := os.MkdirAll(strayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(strayDir, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	reapLegacyStubWorktree(context.Background(), io.Discard, gitRoot, name, false)

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("unregistered dir contents must be preserved, but marker is gone: %v", err)
	}
}
