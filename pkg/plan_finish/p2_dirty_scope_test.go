package plan_finish

import (
	"context"
	"io"
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

// ecosystemFixture is a two-repo ecosystem worktree: an ecosystem root, two
// source repos as direct children of it (the resolution fallback that works
// without workspace discovery), and a container under the XDG worktrees base
// holding one linked worktree per repo. dirtyRepo carries an uncommitted change
// so `git worktree remove` refuses it without --force.
type ecosystemFixture struct {
	ecoRoot    string
	container  string
	repos      []string
	cleanRepo  string
	dirtyRepo  string
	stalePath  string // a registration in ecoRoot whose dir was deleted
	staleBranc string
}

func newEcosystemFixture(t *testing.T) ecosystemFixture {
	t.Helper()
	setGroveHome(t)
	ecoRoot := gitInitForReap(t)
	worktreeName := "eco-dirty"

	container := filepath.Join(paths.WorktreesDir(), workspace.DirIdentifier(ecoRoot), worktreeName)
	if err := os.MkdirAll(container, 0o755); err != nil {
		t.Fatal(err)
	}

	mkRepo := func(name string) string {
		src := filepath.Join(ecoRoot, name)
		if err := os.MkdirAll(src, 0o755); err != nil {
			t.Fatal(err)
		}
		gitRun(t, src, "init", "-q")
		gitRun(t, src, "config", "user.email", "t@t")
		gitRun(t, src, "config", "user.name", "t")
		if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("v1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitRun(t, src, "add", "-A")
		gitRun(t, src, "commit", "-q", "-m", "init")
		gitRun(t, src, "worktree", "add", "-q", "-b", worktreeName, filepath.Join(container, name))
		return src
	}
	cleanRepo := mkRepo("cleanrepo")
	dirtyRepo := mkRepo("dirtyrepo")

	// Uncommitted work in exactly one repo.
	if err := os.WriteFile(filepath.Join(container, "dirtyrepo", "f.txt"), []byte("uncommitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A stale ecosystem-level registration: the only observable proof that the
	// gitRoot `worktree prune` ran.
	stalePath := filepath.Join(t.TempDir(), "stale-wt")
	gitRun(t, ecoRoot, "worktree", "add", "-q", "-b", "stale-branch", stalePath)
	if !worktreeListMentions(t, ecoRoot, "stale-wt") {
		t.Fatalf("precondition: stale registration missing for %s", stalePath)
	}
	if err := os.RemoveAll(stalePath); err != nil {
		t.Fatal(err)
	}

	if err := worktreeregistry.Save(&worktreeregistry.Entry{
		AbsPath: container, Owner: ecoRoot, Repos: []string{"cleanrepo", "dirtyrepo"},
	}); err != nil {
		t.Fatalf("registry save: %v", err)
	}

	return ecosystemFixture{
		ecoRoot:    ecoRoot,
		container:  container,
		repos:      []string{"cleanrepo", "dirtyrepo"},
		cleanRepo:  cleanRepo,
		dirtyRepo:  dirtyRepo,
		stalePath:  stalePath,
		staleBranc: "stale-branch",
	}
}

func (f ecosystemFixture) buildPrune(t *testing.T, opts Options) *finishItemView {
	t.Helper()
	plan := &orchestration.Plan{
		Config: &orchestration.PlanConfig{Worktree: "eco-dirty", Status: "review", Repos: f.repos},
	}
	bctx := BuildContext{
		PlanPath:     t.TempDir(),
		Plan:         plan,
		GitRoot:      f.ecoRoot,
		WorktreeName: "eco-dirty",
		BranchName:   "eco-dirty",
		Executor:     &gexec.RealCommandExecutor{},
		WM:           git.NewWorktreeManager(),
		Output:       io.Discard,
	}
	result, err := BuildItems(bctx, opts)
	if err != nil {
		t.Fatalf("BuildItems: %v", err)
	}
	item := ItemsByID(result.Items, ItemPruneWorktree)
	if item == nil {
		t.Fatal("prune_worktree item not found")
	}
	return &finishItemView{Status: item.Status, IsAvailable: item.IsAvailable, Action: item.Action}
}

// worktreeListMentions reports whether repo's worktree registrations still
// mention needle. Used instead of a path comparison because the registration
// under test points at a directory that has been deleted, which defeats
// symlink-resolving path matching.
func worktreeListMentions(t *testing.T, repo, needle string) bool {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	return strings.Contains(string(out), needle)
}

type finishItemView struct {
	Status      string
	IsAvailable bool
	Action      func() error
}

// TestPruneWorktreeCheck_EcosystemWarnsAboutDirtyRepo pins the pre-flight
// signal. The ecosystem branch of the Check used to be a bare os.Stat, so
// exactly the plan shape that hits the dirty-repo failure got NO warning: the
// checklist said "Exists" and the obstacle only surfaced after the destructive
// run had already half-completed.
func TestPruneWorktreeCheck_EcosystemWarnsAboutDirtyRepo(t *testing.T) {
	f := newEcosystemFixture(t)
	item := f.buildPrune(t, Options{PruneWorktree: true})

	plain := stripANSIForTest(item.Status)
	if !strings.Contains(plain, "dirtyrepo") || !strings.Contains(plain, "force") {
		t.Fatalf("Check status = %q, want it to name the dirty repo and mention force", plain)
	}
	// The availability switch is the trap: a new status string that is not
	// recognised silently makes the whole item unavailable.
	if !item.IsAvailable {
		t.Fatalf("prune item became unavailable with status %q — the new status must stay actionable", plain)
	}
}

// TestPruneWorktreeCheck_EcosystemCleanStillExists pins that a clean ecosystem
// container is unchanged: status "Exists", still available.
func TestPruneWorktreeCheck_EcosystemCleanStillExists(t *testing.T) {
	f := newEcosystemFixture(t)
	// Make the dirty repo clean again.
	gitRun(t, filepath.Join(f.container, "dirtyrepo"), "checkout", "--", "f.txt")

	item := f.buildPrune(t, Options{PruneWorktree: true})
	plain := stripANSIForTest(item.Status)
	if plain != "Exists" {
		t.Fatalf("clean ecosystem container status = %q, want \"Exists\"", plain)
	}
	if !item.IsAvailable {
		t.Fatal("clean ecosystem container must remain available")
	}
}

// TestCleanupEcosystemWorktree_DirtyRepoDoesNotVetoTheRest is the core P2
// assertion: one repo holding uncommitted work must not stop the teardown of
// everything unrelated to it. The clean repo's worktree is removed, the dirty
// repo's survives, the container survives (it legitimately still holds work),
// and the ecosystem-level `git worktree prune` still runs.
func TestCleanupEcosystemWorktree_DirtyRepoDoesNotVetoTheRest(t *testing.T) {
	f := newEcosystemFixture(t)

	err := cleanupEcosystemWorktree(context.Background(), io.Discard, f.ecoRoot, "eco-dirty", f.repos, nil, false)
	if err == nil {
		t.Fatal("expected an error reporting the retained dirty repo")
	}
	if !strings.Contains(err.Error(), "dirtyrepo") {
		t.Errorf("error must name the offending repo, got: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(f.container, "cleanrepo")); !os.IsNotExist(statErr) {
		t.Errorf("clean repo's worktree should have been removed, stat err=%v", statErr)
	}
	if worktreeIsRegistered(t, f.cleanRepo, filepath.Join(f.container, "cleanrepo")) {
		t.Error("clean repo should no longer register its worktree")
	}
	if _, statErr := os.Stat(filepath.Join(f.container, "dirtyrepo", "f.txt")); statErr != nil {
		t.Errorf("dirty repo's uncommitted work must survive: %v", statErr)
	}
	if _, statErr := os.Stat(f.container); statErr != nil {
		t.Errorf("container must survive while it holds retained work: %v", statErr)
	}
	if worktreeListMentions(t, f.ecoRoot, "stale-wt") {
		t.Error("ecosystem-level `git worktree prune` did not run (stale registration survived)")
	}
}

// TestPruneWorktreeAction_DeletesRegistryEntryDespiteDirtyRepo pins that the
// registry entry describing a container being torn down does not survive
// because one repo inside it was dirty.
func TestPruneWorktreeAction_DeletesRegistryEntryDespiteDirtyRepo(t *testing.T) {
	f := newEcosystemFixture(t)
	id := pathutil.WorktreeID(f.container)
	if _, err := worktreeregistry.Load(id); err != nil {
		t.Fatalf("precondition: registry entry missing: %v", err)
	}

	item := f.buildPrune(t, Options{PruneWorktree: true})
	if err := item.Action(); err == nil {
		t.Fatal("expected the action to report the retained dirty repo")
	}

	if _, err := worktreeregistry.Load(id); err == nil {
		t.Error("registry entry survived a dirty-repo failure; it must be deleted with the rest of the teardown")
	}
}

// TestCleanupEcosystemWorktree_AllCleanStillRemovesContainer pins the
// unchanged happy path: with no retained work the container is removed
// entirely and no error is returned.
func TestCleanupEcosystemWorktree_AllCleanStillRemovesContainer(t *testing.T) {
	f := newEcosystemFixture(t)
	gitRun(t, filepath.Join(f.container, "dirtyrepo"), "checkout", "--", "f.txt")

	if err := cleanupEcosystemWorktree(context.Background(), io.Discard, f.ecoRoot, "eco-dirty", f.repos, nil, false); err != nil {
		t.Fatalf("clean teardown should succeed: %v", err)
	}
	if _, statErr := os.Stat(f.container); !os.IsNotExist(statErr) {
		t.Errorf("container should be gone, stat err=%v", statErr)
	}
}
