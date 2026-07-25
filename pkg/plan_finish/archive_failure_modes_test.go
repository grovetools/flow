package plan_finish

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grovetools/core/pkg/paths"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/pkg/worktreeregistry"
)

// blankGroveDataDir makes paths.DataDir() — and therefore
// paths.WorktreeArchiveDir() — resolve to "" while leaving StateDir (the
// worktree registry) pointing at the same place setGroveHome put it. This is
// the "no grove data dir" shape the archive action has to cope with now that
// archiving is the DEFAULT retirement under `flow plan finish --yes`.
func blankGroveDataDir(t *testing.T, groveHome string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", filepath.Join(groveHome, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(groveHome, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(groveHome, "cache"))
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("GROVE_HOME", "")
	t.Setenv("HOME", "")
	if got := paths.WorktreeArchiveDir(); got != "" {
		t.Fatalf("test setup: WorktreeArchiveDir should be empty, got %q", got)
	}
	if got := paths.StateDir(); got == "" {
		t.Fatalf("test setup: StateDir must survive so the registry still resolves")
	}
}

// TestArchiveWorktree_NoArchiveDirRetainsWorktree pins the policy for the first
// of the two archive-only failure modes. `--yes` now archives by default, so an
// unresolvable grove data dir would newly fail a finish that used to succeed.
//
// The answer is NOT to fall back to pruning (that turns "could not archive"
// into "deleted your code") and NOT to hard-fail (that leaves the plan stuck in
// review forever over an environment problem). It is a RetainedWorktreeError:
// a partial success that keeps the container exactly where it is, reports it,
// and still lets mark_finished / archive_plan run.
func TestArchiveWorktree_NoArchiveDirRetainsWorktree(t *testing.T) {
	home := setGroveHome(t)
	gitRoot := gitInitForReap(t)
	worktreeName := "no-data-dir"
	wPath := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
	gitRun(t, gitRoot, "worktree", "add", "-q", "-b", worktreeName, wPath)

	if err := worktreeregistry.Save(&worktreeregistry.Entry{
		AbsPath: wPath, Owner: gitRoot, Plan: "my-plan",
	}); err != nil {
		t.Fatalf("registry save: %v", err)
	}

	item := buildArchiveItem(t, gitRoot, worktreeName, nil, Options{ArchiveWorktree: true})
	blankGroveDataDir(t, home)

	err := item.Action()
	if err == nil {
		t.Fatal("an unarchivable worktree must still be REPORTED, not silently skipped")
	}
	if !IsRetainedWorktree(err) {
		t.Fatalf("expected a retained-worktree partial success so the plan can still be "+
			"marked finished; got a blocking failure: %v", err)
	}
	if !strings.Contains(err.Error(), wPath) {
		t.Errorf("the retention message must name the surviving worktree path %q, got: %v", wPath, err)
	}

	// Nothing was destroyed or moved.
	if _, statErr := os.Stat(filepath.Join(wPath, ".git")); statErr != nil {
		t.Errorf("the worktree must be left completely intact: %v", statErr)
	}
	if !worktreeIsRegistered(t, gitRoot, wPath) {
		t.Error("the owner must still register the retained worktree")
	}
}

// TestArchiveWorktree_DestinationCollisionDisambiguates pins the policy for the
// second failure mode. Finishing two worktrees that share a name under the same
// owner is ordinary (a slug reused after an earlier finish), and it is a naming
// accident, not a safety signal.
//
// Refusing would fail the DEFAULT --yes retirement over something only a manual
// `mv` can fix — and would nudge the operator toward `--prune-worktree`, i.e.
// deleting the very code the archive exists to preserve. So the destination is
// disambiguated with a numeric suffix instead: both archives survive and
// nothing is ever overwritten.
func TestArchiveWorktree_DestinationCollisionDisambiguates(t *testing.T) {
	setGroveHome(t)
	gitRoot := gitInitForReap(t)
	worktreeName := "dup-arch"
	wPath := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
	gitRun(t, gitRoot, "worktree", "add", "-q", "-b", worktreeName, wPath)

	// An earlier finish already archived a worktree of this name.
	occupied := filepath.Join(paths.WorktreeArchiveDir(), workspace.DirIdentifier(gitRoot), worktreeName)
	if err := os.MkdirAll(occupied, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(occupied, "earlier-archive.txt")
	if err := os.WriteFile(marker, []byte("do not clobber"), 0o600); err != nil {
		t.Fatal(err)
	}

	item := buildArchiveItem(t, gitRoot, worktreeName, nil, Options{ArchiveWorktree: true})
	if err := item.Action(); err != nil {
		t.Fatalf("a name collision must not fail the archive: %v", err)
	}

	// The earlier archive is untouched.
	body, err := os.ReadFile(marker)
	if err != nil || string(body) != "do not clobber" {
		t.Fatalf("the pre-existing archive was clobbered (body=%q err=%v)", body, err)
	}
	// The new one landed beside it under a disambiguated name.
	disambiguated := occupied + "-2"
	if _, err := os.Stat(disambiguated); err != nil {
		t.Fatalf("archived container missing at %s: %v", disambiguated, err)
	}
	if _, err := os.Stat(filepath.Join(disambiguated, filepath.Base(gitRoot)+".bundle")); err != nil {
		t.Errorf("the disambiguated archive must still carry its bundle: %v", err)
	}
	if _, err := os.Stat(wPath); !os.IsNotExist(err) {
		t.Errorf("source worktree dir should be gone, stat err=%v", err)
	}
	if worktreeIsRegistered(t, gitRoot, wPath) {
		t.Errorf("owner still registers %s after archive", wPath)
	}
}

// TestUniqueArchiveDest_WalksPastEveryTakenName pins the suffix search itself,
// including the third-and-beyond collisions the disambiguation test above only
// reaches once.
func TestUniqueArchiveDest_WalksPastEveryTakenName(t *testing.T) {
	base := filepath.Join(t.TempDir(), "wt")

	got, err := uniqueArchiveDest(base)
	if err != nil {
		t.Fatalf("free name should resolve: %v", err)
	}
	if got != base {
		t.Errorf("a free destination must be used as-is: got %q want %q", got, base)
	}

	for _, occupied := range []string{base, base + "-2", base + "-3"} {
		if err := os.MkdirAll(occupied, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err = uniqueArchiveDest(base)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := base + "-4"; got != want {
		t.Errorf("uniqueArchiveDest = %q, want %q", got, want)
	}
}

// TestArchiveWorktree_MutualExclusionGuardIsInertInWizardMode documents WHY the
// Select All fix had to live in the wizard's selection model.
//
// The factory's archive/prune exclusion keys off Options.ArchiveWorktree &&
// Options.PruneWorktree — the CLI flags. The hosted finish wizard builds its
// items with plan_finish.Options{ForceSwitch: ...}, so BOTH are false and the
// guard can never fire there no matter which items the user ticked. Enabling
// both items in the wizard would therefore have run both actions, not been
// refused.
func TestArchiveWorktree_MutualExclusionGuardIsInertInWizardMode(t *testing.T) {
	setGroveHome(t)
	gitRoot := gitInitForReap(t)
	worktreeName := "wizard-mode"
	wPath := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
	gitRun(t, gitRoot, "worktree", "add", "-q", "-b", worktreeName, wPath)

	// Exactly the Options the hosted wizard passes: no flags at all.
	item := buildArchiveItem(t, gitRoot, worktreeName, nil, Options{ForceSwitch: &ForceSwitch{}})
	if !item.IsAvailable {
		t.Fatalf("archive item unexpectedly unavailable in wizard mode: %q", item.Status)
	}
	if strings.Contains(item.Status, "Conflicts") {
		t.Fatalf("guard fired in wizard mode, which it cannot do: %q", item.Status)
	}
	if err := item.Action(); err != nil {
		t.Fatalf("the factory does not refuse in wizard mode; the wizard must not rely on it: %v", err)
	}
}
