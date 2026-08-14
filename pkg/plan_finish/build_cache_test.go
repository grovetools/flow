package plan_finish

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grovetools/core/git"
	"github.com/grovetools/core/pkg/paths"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/pkg/worktreeregistry"

	gexec "github.com/grovetools/flow/pkg/exec"
	"github.com/grovetools/flow/pkg/orchestration"
)

// requireMake skips the test when make is absent. Every assertion in this file
// is about detecting and invoking a real make target, so a stub would only
// test the stub.
func requireMake(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not on PATH")
	}
}

// writeCacheEvictMakefile drops a Makefile in dir defining cache-evict. The
// recipe appends the WORKTREE it was handed to logPath (so tests can assert
// both THAT it ran and WITH WHAT), then exits with exitCode.
func writeCacheEvictMakefile(t *testing.T, dir, logPath string, exitCode int) {
	t.Helper()
	body := "cache-evict:\n" +
		"\t@echo \"$(WORKTREE)\" >> " + logPath + "\n"
	if exitCode != 0 {
		body += "\t@exit " + itoa(exitCode) + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeInertMakefile drops a Makefile with no cache-evict target, which is the
// shape of every repo that has not opted in.
func writeInertMakefile(t *testing.T, dir string) {
	t.Helper()
	body := "build:\n\t@true\n"
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// setupCacheEvictPlan registers a container holding one plain directory per
// repo (no git needed: the item only shells out to make) and returns a
// BuildContext plus the container path and the captured output buffer.
func setupCacheEvictPlan(t *testing.T, repos []string) (bctx BuildContext, container string, out *bytes.Buffer) {
	t.Helper()
	sandboxGroveHome(t)

	gitRoot := t.TempDir()
	container = filepath.Join(t.TempDir(), "owner-id", "feature")
	if err := os.MkdirAll(container, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, repo := range repos {
		if err := os.MkdirAll(filepath.Join(container, repo), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := worktreeregistry.Save(&worktreeregistry.Entry{
		AbsPath: container,
		Owner:   gitRoot,
		Repos:   repos,
		Plan:    "my-plan",
	}); err != nil {
		t.Fatal(err)
	}

	planPath := filepath.Join(t.TempDir(), "my-plan")
	if err := os.MkdirAll(planPath, 0o755); err != nil {
		t.Fatal(err)
	}
	plan := &orchestration.Plan{
		Name:      "my-plan",
		Directory: planPath,
		Config:    &orchestration.PlanConfig{Worktree: "feature", Repos: repos, Status: "review"},
	}
	out = &bytes.Buffer{}
	bctx = BuildContext{
		PlanPath:     planPath,
		Plan:         plan,
		GitRoot:      gitRoot,
		WorktreeName: "feature",
		BranchName:   "feature",
		WM:           git.NewWorktreeManager(),
		Output:       out,
	}
	return bctx, container, out
}

// makeCommands filters a MockCommandExecutor's log down to the make
// invocations, so unrelated commands from other items cannot confuse the
// assertions.
func makeCommands(recorded []string) []string {
	var got []string
	for _, cmd := range recorded {
		if strings.HasPrefix(cmd, "make ") {
			got = append(got, cmd)
		}
	}
	return got
}

// TestPruneBuildCachesRunsBeforeWorktreeRetirement pins the load-bearing
// ordering in the built list: eviction shells out to a make target INSIDE the
// container, so it has to precede both retirements — prune deletes that
// directory and archive moves it somewhere the target cannot be invoked from
// (and where the cache key, which is the container's absolute path, no longer
// names anything).
func TestPruneBuildCachesRunsBeforeWorktreeRetirement(t *testing.T) {
	bctx, container, _ := setupCacheEvictPlan(t, []string{"core"})
	writeCacheEvictMakefile(t, filepath.Join(container, "core"), filepath.Join(t.TempDir(), "evict.log"), 0)

	result, err := BuildItems(bctx, Options{})
	if err != nil {
		t.Fatalf("BuildItems: %v", err)
	}
	evict := indexOf(t, result.Items, ItemPruneBuildCaches)
	if evict < 0 {
		t.Fatal("prune_build_caches missing from the built list")
	}
	for _, later := range []string{ItemArchiveWorktree, ItemPruneWorktree} {
		at := indexOf(t, result.Items, later)
		if at < 0 {
			t.Fatalf("item %s missing from the built list", later)
		}
		if at < evict {
			t.Errorf("%s (index %d) runs before prune_build_caches (index %d): the container is gone/moved by the time the caches would be evicted",
				later, at, evict)
		}
	}
}

// TestPruneBuildCachesEvictsBeforeTheArchiveMovesTheContainer is the RUNTIME
// half of the ordering guarantee: list position only matters because both
// hosts (cmd.executeFinishActions and the view's runEmbeddedFinishActions)
// range over the slice in order. This drives the real items in that order
// against a real container and asserts the eviction actually happened while
// the path was still live, and that the archive then moved it.
func TestPruneBuildCachesEvictsBeforeTheArchiveMovesTheContainer(t *testing.T) {
	requireMake(t)
	setGroveHome(t)

	ecoRoot := gitInitForReap(t)
	repoOwner := gitInitForReap(t)
	worktreeName := "eco-evict"
	repoName := "corelib"

	containerPath := filepath.Join(paths.WorktreesDir(), workspace.DirIdentifier(ecoRoot), worktreeName)
	if err := os.MkdirAll(containerPath, 0o755); err != nil {
		t.Fatal(err)
	}
	repoWorktree := filepath.Join(containerPath, repoName)
	gitRun(t, repoOwner, "worktree", "add", "-q", "-b", worktreeName, repoWorktree)

	// The log lives OUTSIDE the container so it survives the archive move.
	logPath := filepath.Join(t.TempDir(), "evict.log")
	writeCacheEvictMakefile(t, repoWorktree, logPath, 0)

	if err := worktreeregistry.Save(&worktreeregistry.Entry{
		AbsPath: containerPath, Owner: ecoRoot, Repos: []string{repoName},
	}); err != nil {
		t.Fatalf("registry save: %v", err)
	}

	out := &bytes.Buffer{}
	bctx := BuildContext{
		PlanPath:     t.TempDir(),
		Plan:         &orchestration.Plan{Config: &orchestration.PlanConfig{Worktree: worktreeName, Status: "review", Repos: []string{repoName}}},
		GitRoot:      ecoRoot,
		WorktreeName: worktreeName,
		BranchName:   worktreeName,
		Executor:     &gexec.RealCommandExecutor{},
		WM:           git.NewWorktreeManager(),
		Output:       out,
	}
	result, err := BuildItems(bctx, Options{ArchiveWorktree: true})
	if err != nil {
		t.Fatalf("BuildItems: %v", err)
	}

	// Run exactly what a host runs: the enabled items, in slice order.
	var ran []string
	for _, item := range result.Items {
		if item == nil || item.Action == nil {
			continue
		}
		if item.ID != ItemPruneBuildCaches && item.ID != ItemArchiveWorktree {
			continue
		}
		ran = append(ran, item.ID)
		if err := item.Action(); err != nil {
			t.Fatalf("%s action failed: %v", item.ID, err)
		}
	}
	if len(ran) != 2 || ran[0] != ItemPruneBuildCaches || ran[1] != ItemArchiveWorktree {
		t.Fatalf("host order was %v, want [%s %s]", ran, ItemPruneBuildCaches, ItemArchiveWorktree)
	}

	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("cache-evict never ran (no log at %s): %v", logPath, err)
	}
	if got := strings.TrimSpace(string(logged)); got != repoWorktree {
		t.Errorf("cache-evict got WORKTREE=%q, want the live container repo path %q", got, repoWorktree)
	}
	// And the archive really did move the container out from under that path,
	// which is what makes the ordering load-bearing rather than cosmetic.
	destPath := filepath.Join(paths.WorktreeArchiveDir(), workspace.DirIdentifier(ecoRoot), worktreeName)
	if _, err := os.Stat(destPath); err != nil {
		t.Fatalf("archived container missing at %s: %v", destPath, err)
	}
	if _, err := os.Stat(containerPath); !os.IsNotExist(err) {
		t.Errorf("source container should be gone after archive, stat err=%v", err)
	}
}

// TestPruneBuildCachesSkipsReposWithoutTheTarget is the "detect, don't
// hardcode" contract: only repos whose Makefile defines cache-evict are
// invoked. Repos with no Makefile at all, and repos with an unrelated one, are
// silently skipped — that is most repos.
func TestPruneBuildCachesSkipsReposWithoutTheTarget(t *testing.T) {
	requireMake(t)
	bctx, container, _ := setupCacheEvictPlan(t, []string{"agent", "core", "flow"})
	logPath := filepath.Join(t.TempDir(), "evict.log")
	writeCacheEvictMakefile(t, filepath.Join(container, "agent"), logPath, 0)
	writeInertMakefile(t, filepath.Join(container, "core"))
	// flow gets no Makefile at all.

	mock := &gexec.MockCommandExecutor{}
	bctx.Executor = mock
	result, err := BuildItems(bctx, Options{})
	if err != nil {
		t.Fatalf("BuildItems: %v", err)
	}
	item := ItemsByID(result.Items, ItemPruneBuildCaches)
	if item == nil {
		t.Fatal("prune_build_caches item not found")
	}
	if !item.IsAvailable {
		t.Fatalf("item should be available with one cache-evict repo present, status=%q", item.Status)
	}
	if !strings.Contains(stripANSIForTest(item.Status), "1 repo(s)") {
		t.Errorf("Check status = %q, want an honest count of 1 repo", stripANSIForTest(item.Status))
	}
	if err := item.Action(); err != nil {
		t.Fatalf("Action returned an error: %v", err)
	}

	got := makeCommands(mock.Commands)
	agentDir := filepath.Join(container, "agent")
	want := "make -C " + agentDir + " cache-evict WORKTREE=" + agentDir
	if len(got) != 1 {
		t.Fatalf("make invocations = %v, want exactly one (only the repo exposing the target)", got)
	}
	if got[0] != want {
		t.Errorf("invocation = %q, want %q", got[0], want)
	}
}

// TestPruneBuildCachesReportsNAWithoutAnyTarget covers the common case: no repo
// in the container opts in, so the item reports N/A and is unavailable rather
// than offering a no-op action.
func TestPruneBuildCachesReportsNAWithoutAnyTarget(t *testing.T) {
	requireMake(t)
	bctx, container, _ := setupCacheEvictPlan(t, []string{"core"})
	writeInertMakefile(t, filepath.Join(container, "core"))

	result, err := BuildItems(bctx, Options{})
	if err != nil {
		t.Fatalf("BuildItems: %v", err)
	}
	item := ItemsByID(result.Items, ItemPruneBuildCaches)
	if item == nil {
		t.Fatal("prune_build_caches item not found")
	}
	plain := stripANSIForTest(item.Status)
	if !strings.HasPrefix(plain, "N/A") {
		t.Errorf("status = %q, want an N/A status when nothing exposes cache-evict", plain)
	}
	if item.IsAvailable {
		t.Error("item must not be available when no repo exposes cache-evict")
	}
}

// TestPruneBuildCachesFailureDoesNotFailTheFinish pins the tolerance rule: a
// broken or failing cache-evict recipe is a warning on the output, never an
// error return. An error would become the run's firstErr and skip
// mark_finished / archive_plan, i.e. a stale cache entry would strand the plan.
func TestPruneBuildCachesFailureDoesNotFailTheFinish(t *testing.T) {
	requireMake(t)
	bctx, container, out := setupCacheEvictPlan(t, []string{"core"})
	bctx.Executor = &gexec.RealCommandExecutor{}
	writeCacheEvictMakefile(t, filepath.Join(container, "core"), filepath.Join(t.TempDir(), "evict.log"), 1)

	result, err := BuildItems(bctx, Options{})
	if err != nil {
		t.Fatalf("BuildItems: %v", err)
	}
	item := ItemsByID(result.Items, ItemPruneBuildCaches)
	if item == nil {
		t.Fatal("prune_build_caches item not found")
	}
	if err := item.Action(); err != nil {
		t.Fatalf("a failing cache-evict must not fail the finish, got: %v", err)
	}
	if !strings.Contains(out.String(), "cache eviction failed") {
		t.Errorf("failure must be reported to the user; output was:\n%s", out.String())
	}
}

// TestPruneBuildCachesSingleRepoPlanUsesTheContainer covers legacy single-repo
// plans, which carry no Repos at all: there the container IS the checkout, so
// it is probed and evicted directly rather than reported N/A. The cache key is
// the worktree path either way.
func TestPruneBuildCachesSingleRepoPlanUsesTheContainer(t *testing.T) {
	requireMake(t)
	bctx, container, _ := setupCacheEvictPlan(t, nil)
	writeCacheEvictMakefile(t, container, filepath.Join(t.TempDir(), "evict.log"), 0)

	mock := &gexec.MockCommandExecutor{}
	bctx.Executor = mock
	result, err := BuildItems(bctx, Options{})
	if err != nil {
		t.Fatalf("BuildItems: %v", err)
	}
	item := ItemsByID(result.Items, ItemPruneBuildCaches)
	if item == nil {
		t.Fatal("prune_build_caches item not found")
	}
	if !item.IsAvailable {
		t.Fatalf("single-repo container exposing cache-evict must be actionable, status=%q", stripANSIForTest(item.Status))
	}
	if err := item.Action(); err != nil {
		t.Fatalf("Action returned an error: %v", err)
	}
	got := makeCommands(mock.Commands)
	want := "make -C " + container + " cache-evict WORKTREE=" + container
	if len(got) != 1 || got[0] != want {
		t.Errorf("invocations = %v, want [%q]", got, want)
	}
}

// TestCacheEvictCandidateDirsSkipsMissingRepos guards the resolution helper
// directly: a repo listed in the plan whose subdir is not on disk (partial
// container, repo added to the plan after the worktree was made) must be
// skipped, not passed to make as a nonexistent -C.
func TestCacheEvictCandidateDirsSkipsMissingRepos(t *testing.T) {
	container := t.TempDir()
	if err := os.MkdirAll(filepath.Join(container, "core"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := cacheEvictCandidateDirs(container, []string{"core", "ghost"})
	want := []string{filepath.Join(container, "core")}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("cacheEvictCandidateDirs = %v, want %v", got, want)
	}
	if dirs := cacheEvictCandidateDirs("", []string{"core"}); dirs != nil {
		t.Errorf("an unresolved container must yield no dirs, got %v", dirs)
	}
}
