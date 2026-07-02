package plan_finish

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grovetools/core/git"
	"github.com/grovetools/core/pkg/paths"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/pkg/worktreeregistry"
	"github.com/grovetools/core/util/pathutil"

	gexec "github.com/grovetools/flow/pkg/exec"
	"github.com/grovetools/flow/pkg/orchestration"
)

// setGroveHome points GROVE_HOME at a fresh temp dir so paths.WorktreesDir,
// paths.WorktreeArchiveDir and the worktree registry (paths.StateDir) are all
// hermetic for the test.
func setGroveHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GROVE_HOME", home)
	return home
}

// gitRun runs a git command in dir, failing the test on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// buildArchiveItem builds the finish items for a plan with the given repos
// list (nil = single-repo shape) and returns the archive_worktree item.
func buildArchiveItem(t *testing.T, gitRoot, worktreeName string, repos []string, opts Options) *struct {
	Status      string
	IsAvailable bool
	Action      func() error
} {
	t.Helper()
	plan := &orchestration.Plan{
		Config: &orchestration.PlanConfig{Worktree: worktreeName, Status: "review", Repos: repos},
	}
	bctx := BuildContext{
		PlanPath:     t.TempDir(),
		Plan:         plan,
		GitRoot:      gitRoot,
		WorktreeName: worktreeName,
		BranchName:   worktreeName,
		Executor:     &gexec.RealCommandExecutor{},
		WM:           git.NewWorktreeManager(),
	}
	result, err := BuildItems(bctx, opts)
	if err != nil {
		t.Fatalf("BuildItems: %v", err)
	}
	item := ItemsByID(result.Items, ItemArchiveWorktree)
	if item == nil {
		t.Fatal("archive_worktree item not found")
	}
	return &struct {
		Status      string
		IsAvailable bool
		Action      func() error
	}{Status: item.Status, IsAvailable: item.IsAvailable, Action: item.Action}
}

// TestArchiveWorktree_SingleRepo runs the archive action against a real
// linked worktree of a real owner repo and asserts the full contract: the
// container lands under WorktreeArchiveDir, a bundle captured the refs, the
// .git pointer is gone, the owner no longer registers the worktree, and the
// registry entry was re-keyed with ArchivedAt set.
func TestArchiveWorktree_SingleRepo(t *testing.T) {
	setGroveHome(t)
	gitRoot := gitInitForReap(t)
	worktreeName := "feature-arch"
	wPath := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)

	gitRun(t, gitRoot, "worktree", "add", "-q", "-b", worktreeName, wPath)
	// An unpushed commit that only the bundle can preserve once the branch
	// is deleted by later finish items.
	if err := os.WriteFile(filepath.Join(wPath, "work.txt"), []byte("unpushed"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, wPath, "add", "-A")
	gitRun(t, wPath, "commit", "-q", "-m", "unpushed work")

	if err := worktreeregistry.Save(&worktreeregistry.Entry{
		AbsPath: wPath, Owner: gitRoot, Plan: "my-plan",
	}); err != nil {
		t.Fatalf("registry save: %v", err)
	}

	item := buildArchiveItem(t, gitRoot, worktreeName, nil, Options{ArchiveWorktree: true})
	if !item.IsAvailable {
		t.Fatalf("archive item should be available for an existing worktree, status=%q", item.Status)
	}
	if err := item.Action(); err != nil {
		t.Fatalf("Action failed: %v", err)
	}

	destPath := filepath.Join(paths.WorktreeArchiveDir(), workspace.DirIdentifier(gitRoot), worktreeName)

	// Container moved.
	if _, err := os.Stat(destPath); err != nil {
		t.Fatalf("archived container missing at %s: %v", destPath, err)
	}
	if _, err := os.Stat(wPath); !os.IsNotExist(err) {
		t.Errorf("source worktree dir should be gone, stat err=%v", err)
	}

	// Bundle exists and verifies against the owner's object store.
	bundlePath := filepath.Join(destPath, filepath.Base(gitRoot)+".bundle")
	if _, err := os.Stat(bundlePath); err != nil {
		t.Fatalf("bundle missing at %s: %v", bundlePath, err)
	}
	gitRun(t, gitRoot, "bundle", "verify", bundlePath)

	// .git pointer file gone — the archived copy cannot resolve an owner.
	if _, err := os.Stat(filepath.Join(destPath, ".git")); !os.IsNotExist(err) {
		t.Errorf(".git pointer should be removed from archived copy, stat err=%v", err)
	}

	// Owner no longer registers the worktree.
	if worktreeIsRegistered(t, gitRoot, wPath) {
		t.Errorf("owner still registers %s after archive", wPath)
	}

	// Registry re-keyed: old ID gone, new entry archived.
	if _, err := worktreeregistry.Load(pathutil.WorktreeID(wPath)); !os.IsNotExist(err) {
		t.Errorf("old registry entry should be deleted, err=%v", err)
	}
	entry, err := worktreeregistry.Load(pathutil.WorktreeID(destPath))
	if err != nil {
		t.Fatalf("archived registry entry missing: %v", err)
	}
	if !entry.IsArchived() {
		t.Error("archived entry should have ArchivedAt set")
	}
	if entry.AbsPath != destPath {
		t.Errorf("entry.AbsPath = %q, want %q", entry.AbsPath, destPath)
	}
	if entry.OriginalPath != wPath {
		t.Errorf("entry.OriginalPath = %q, want %q", entry.OriginalPath, wPath)
	}
	if entry.Plan != "my-plan" {
		t.Errorf("entry.Plan = %q, want my-plan (history must survive re-key)", entry.Plan)
	}
}

// TestArchiveWorktree_Ecosystem exercises the ecosystem-container shape: a
// plain container dir under the XDG worktrees base holding a linked worktree
// of a separate owner repo, driven by plan.Config.Repos.
func TestArchiveWorktree_Ecosystem(t *testing.T) {
	setGroveHome(t)
	ecoRoot := gitInitForReap(t)
	repoOwner := gitInitForReap(t) // the sub-repo's source checkout
	worktreeName := "eco-arch"
	repoName := "corelib"

	containerPath := filepath.Join(paths.WorktreesDir(), workspace.DirIdentifier(ecoRoot), worktreeName)
	if err := os.MkdirAll(containerPath, 0o755); err != nil {
		t.Fatal(err)
	}
	repoWorktree := filepath.Join(containerPath, repoName)
	gitRun(t, repoOwner, "worktree", "add", "-q", "-b", worktreeName, repoWorktree)

	// Owner marker that must be scrubbed from the archived copy.
	if err := os.MkdirAll(filepath.Join(repoWorktree, ".grove"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoWorktree, ".grove", "workspace"), []byte("owner: "+repoOwner+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := worktreeregistry.Save(&worktreeregistry.Entry{
		AbsPath: containerPath, Owner: ecoRoot, Repos: []string{repoName},
	}); err != nil {
		t.Fatalf("registry save: %v", err)
	}

	item := buildArchiveItem(t, ecoRoot, worktreeName, []string{repoName}, Options{ArchiveWorktree: true})
	if !item.IsAvailable {
		t.Fatalf("archive item should be available, status=%q", item.Status)
	}
	if err := item.Action(); err != nil {
		t.Fatalf("Action failed: %v", err)
	}

	destPath := filepath.Join(paths.WorktreeArchiveDir(), workspace.DirIdentifier(ecoRoot), worktreeName)
	if _, err := os.Stat(destPath); err != nil {
		t.Fatalf("archived container missing at %s: %v", destPath, err)
	}
	if _, err := os.Stat(containerPath); !os.IsNotExist(err) {
		t.Errorf("source container should be gone, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(destPath, repoName+".bundle")); err != nil {
		t.Errorf("per-repo bundle missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destPath, repoName, ".git")); !os.IsNotExist(err) {
		t.Errorf("repo .git pointer should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(destPath, repoName, ".grove", "workspace")); !os.IsNotExist(err) {
		t.Errorf("workspace marker should be removed, stat err=%v", err)
	}
	if worktreeIsRegistered(t, repoOwner, repoWorktree) {
		t.Errorf("sub-repo owner still registers %s after archive", repoWorktree)
	}
	entry, err := worktreeregistry.Load(pathutil.WorktreeID(destPath))
	if err != nil {
		t.Fatalf("archived registry entry missing: %v", err)
	}
	if !entry.IsArchived() || entry.OriginalPath != containerPath {
		t.Errorf("entry archived=%v originalPath=%q, want archived with original %q",
			entry.IsArchived(), entry.OriginalPath, containerPath)
	}
}

// TestArchiveWorktree_MutuallyExclusiveWithPrune asserts the factory-level
// exclusion: when both archive and prune are enabled in Options, the archive
// item is unavailable and its Action refuses to run.
func TestArchiveWorktree_MutuallyExclusiveWithPrune(t *testing.T) {
	setGroveHome(t)
	gitRoot := gitInitForReap(t)
	worktreeName := "both-flags"
	wPath := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
	gitRun(t, gitRoot, "worktree", "add", "-q", "-b", worktreeName, wPath)

	item := buildArchiveItem(t, gitRoot, worktreeName, nil, Options{ArchiveWorktree: true, PruneWorktree: true})
	if item.IsAvailable {
		t.Error("archive item must not be available when prune is also enabled")
	}
	err := item.Action()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("Action should refuse with a mutual-exclusion error, got: %v", err)
	}
	// Nothing was touched.
	if _, statErr := os.Stat(filepath.Join(wPath, ".git")); statErr != nil {
		t.Errorf("worktree must be untouched after refusal: %v", statErr)
	}
}

// TestArchiveWorktree_DestinationExists asserts the action refuses (before
// doing anything destructive) when the archive destination already exists.
func TestArchiveWorktree_DestinationExists(t *testing.T) {
	setGroveHome(t)
	gitRoot := gitInitForReap(t)
	worktreeName := "dup-arch"
	wPath := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
	gitRun(t, gitRoot, "worktree", "add", "-q", "-b", worktreeName, wPath)

	destPath := filepath.Join(paths.WorktreeArchiveDir(), workspace.DirIdentifier(gitRoot), worktreeName)
	if err := os.MkdirAll(destPath, 0o755); err != nil {
		t.Fatal(err)
	}

	item := buildArchiveItem(t, gitRoot, worktreeName, nil, Options{ArchiveWorktree: true})
	err := item.Action()
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Action should refuse when destination exists, got: %v", err)
	}
	// Refusal happened before any mutation: the .git pointer survives.
	if _, statErr := os.Stat(filepath.Join(wPath, ".git")); statErr != nil {
		t.Errorf("worktree must be untouched after refusal: %v", statErr)
	}
	if worktreeIsRegistered(t, gitRoot, wPath) == false {
		t.Error("owner registration must survive a refused archive")
	}
}
