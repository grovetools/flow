package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/grovetools/core/pkg/worktreeregistry"
)

// mustEvalTempDir returns a symlink-resolved temp dir so paths built from it
// compare equal to the registry/resolver's EvalSymlinks-normalized forms
// (macOS /var → /private/var).
func mustEvalTempDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if r, err := filepath.EvalSymlinks(d); err == nil {
		return r
	}
	return d
}

// TestResolveOrPrepareWorktree_ResolvesExistingBeforeCreate is the regression
// test for the generator bug (site A): when an anchored container already
// exists — recorded in the per-worktree registry under a sub-repo owner and
// living OUTSIDE gitRoot's own .grove-worktrees base — resolveOrPrepareWorktree
// MUST return that existing path and MUST NOT create a duplicate legacy
// `<gitRoot>/.grove-worktrees/<name>` superproject stub.
func TestResolveOrPrepareWorktree_ResolvesExistingBeforeCreate(t *testing.T) {
	// Sandbox all grove dirs (registry lives under StateDir()) so the test
	// neither reads nor writes the real ~/.local/state/grove.
	groveHome := t.TempDir()
	t.Setenv("GROVE_HOME", groveHome)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	gitRoot := mustEvalTempDir(t)
	name := "anchored-wt"

	// The anchored container lives under a DIFFERENT base than gitRoot's
	// .grove-worktrees — mimicking an `--anchor <sub-repo>` XDG location.
	anchoredParent := mustEvalTempDir(t)
	anchoredPath := filepath.Join(anchoredParent, name)
	if err := os.MkdirAll(anchoredPath, 0o755); err != nil {
		t.Fatal(err)
	}

	// Seed the per-worktree registry: Owner is a sub-repo of the ecosystem at
	// gitRoot (the only owner an --anchor in this ecosystem could name), so the
	// resolver accepts it under the ecosystem scope.
	owner := filepath.Join(gitRoot, "treemux")
	if err := worktreeregistry.Save(&worktreeregistry.Entry{
		AbsPath: anchoredPath,
		Owner:   owner,
		Repos:   []string{"treemux"},
		Plan:    "p",
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	plan := &Plan{
		Directory: filepath.Join(gitRoot, ".grove", "plans", "p"),
		Config:    &PlanConfig{Repos: []string{"treemux"}},
	}

	got, err := resolveOrPrepareWorktree(context.Background(), gitRoot, name, plan)
	if err != nil {
		t.Fatalf("resolveOrPrepareWorktree: %v", err)
	}

	if filepath.Clean(got) != filepath.Clean(anchoredPath) {
		t.Errorf("resolved %q, want existing anchored path %q", got, anchoredPath)
	}

	// The crux: NO duplicate legacy stub was created under gitRoot.
	legacy := filepath.Join(gitRoot, ".grove-worktrees", name)
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("must NOT create legacy stub at %q (stat err=%v)", legacy, err)
	}
}

// TestResolveWorktreeLayout_EnvAndDefault covers the layout resolution used by
// the create fallback: env override wins, otherwise ecosystems default to xdg
// and single repos to legacy.
func TestResolveWorktreeLayout_EnvAndDefault(t *testing.T) {
	t.Setenv("GROVE_WORKTREE_LAYOUT", "legacy")
	if got := resolveWorktreeLayout("", true); got != "legacy" {
		t.Errorf("env override: got %q, want legacy", got)
	}

	t.Setenv("GROVE_WORKTREE_LAYOUT", "")
	if got := resolveWorktreeLayout("/nonexistent/root", true); got != "xdg" {
		t.Errorf("ecosystem default: got %q, want xdg", got)
	}
	if got := resolveWorktreeLayout("/nonexistent/root", false); got != "legacy" {
		t.Errorf("single-repo default: got %q, want legacy", got)
	}
}
