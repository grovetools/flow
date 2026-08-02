package plan_finish

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grovetools/core/git"
	"github.com/grovetools/core/pkg/worktreeregistry"
	"github.com/grovetools/core/util/pathutil"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/tui/wizards/finish"
)

// sandboxGroveHome points every registry read/write at a temp dir. Nothing in
// this file may touch the owner's real registry.
func sandboxGroveHome(t *testing.T) {
	t.Helper()
	t.Setenv("GROVE_HOME", t.TempDir())
}

// indexOf returns the position of id in items, or -1 when absent.
func indexOf(t *testing.T, items []*finish.Item, id string) int {
	t.Helper()
	for i, item := range items {
		if item != nil && item.ID == id {
			return i
		}
	}
	return -1
}

// setupProvenancePlan builds a registered worktree container with one real git
// repo inside it, plus a plan directory. It returns the pieces BuildItems needs.
func setupProvenancePlan(t *testing.T) (bctx BuildContext, container, planPath string) {
	t.Helper()
	sandboxGroveHome(t)

	gitRoot := t.TempDir()
	container = filepath.Join(t.TempDir(), "owner-id", "feature")
	repo := filepath.Join(container, "core")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepoWithIdentity(t, repo)
	commitFile(t, repo, "a.txt", "a", "first")

	if err := worktreeregistry.Save(&worktreeregistry.Entry{
		AbsPath:      container,
		Owner:        gitRoot,
		Repos:        []string{"core"},
		Plan:         "my-plan",
		Labels:       map[string]string{"wave": "local-only"},
		SessionState: map[string]any{"active_job": "05", "token": "ephemeral"},
	}); err != nil {
		t.Fatal(err)
	}

	planPath = filepath.Join(t.TempDir(), "my-plan")
	if err := os.MkdirAll(planPath, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCommitsFixture(t, planPath, "job-1", orchestration.JobCommitsRecord{
		Schema: 1, JobID: "job-1", JobFile: "01-job.md",
		StartedAt: "2026-08-02T12:00:00Z", FinishedAt: "2026-08-02T12:10:00Z",
		Repos: []orchestration.JobCommitsRepo{
			{Name: "core", Branch: "feature", StartHead: "aaaa", EndHead: "bbbb", Commits: []string{"c1"}},
		},
	})

	plan := &orchestration.Plan{
		Name:      "my-plan",
		Directory: planPath,
		Config:    &orchestration.PlanConfig{Worktree: "feature", Repos: []string{"core"}, Status: "review"},
		Jobs: []*orchestration.Job{
			{ID: "job-1", Title: "job one", Status: orchestration.JobStatus("completed"), FilePath: filepath.Join(planPath, "01-job.md")},
		},
	}

	bctx = BuildContext{
		PlanPath:     planPath,
		Plan:         plan,
		GitRoot:      gitRoot,
		WorktreeName: "feature",
		BranchName:   "feature",
		WM:           git.NewWorktreeManager(),
		Output:       io.Discard,
	}
	return bctx, container, planPath
}

func TestProvenanceItemsRunBeforeEveryRetirementItem(t *testing.T) {
	bctx, _, _ := setupProvenancePlan(t)
	bctx.LedgerWriter = func(planDir, planName, title, body string) (LedgerNoteResult, error) {
		return LedgerNoteResult{Path: "/notes/completed/x.md"}, nil
	}

	result, err := BuildItems(bctx, Options{})
	if err != nil {
		t.Fatalf("BuildItems: %v", err)
	}

	ledger := indexOf(t, result.Items, ItemLedgerNote)
	tombstone := indexOf(t, result.Items, ItemTombstoneRegistry)
	if ledger < 0 || tombstone < 0 {
		t.Fatalf("provenance items missing: ledger=%d tombstone=%d", ledger, tombstone)
	}
	// Every item that retires the worktree, its branches or the plan dir has
	// to come after the two that record what is about to be retired.
	for _, later := range []string{
		ItemArchiveWorktree,
		ItemPruneWorktree,
		ItemDeleteSubmoduleBranches,
		ItemDeleteLocalBranch,
		ItemDeleteRemoteBranch,
		ItemArchivePlan,
	} {
		at := indexOf(t, result.Items, later)
		if at < 0 {
			t.Fatalf("item %s missing from the built list", later)
		}
		if at < ledger || at < tombstone {
			t.Errorf("%s (index %d) runs before provenance is recorded (ledger=%d tombstone=%d)",
				later, at, ledger, tombstone)
		}
	}
}

func TestTombstoneItemRecordsFinalSHAsAndStripsSessionState(t *testing.T) {
	bctx, container, _ := setupProvenancePlan(t)
	result, err := BuildItems(bctx, Options{})
	if err != nil {
		t.Fatalf("BuildItems: %v", err)
	}

	item := ItemsByID(result.Items, ItemTombstoneRegistry)
	if item == nil {
		t.Fatal("tombstone item not built")
	}
	if !item.IsAvailable {
		t.Fatalf("tombstone item should be available, status = %q", item.Status)
	}
	if err := item.Action(); err != nil {
		t.Fatalf("tombstone action: %v", err)
	}

	entry, err := worktreeregistry.Load(pathutil.WorktreeID(container))
	if err != nil {
		t.Fatalf("registry entry was destroyed instead of tombstoned: %v", err)
	}
	if !entry.IsFinished() {
		t.Errorf("status = %q, want finished", entry.Status)
	}
	if entry.SessionState != nil {
		t.Errorf("SessionState must be stripped, got %v", entry.SessionState)
	}
	if entry.Plan != "my-plan" || len(entry.Repos) != 1 || entry.Labels["wave"] != "local-only" {
		t.Errorf("the binding a tombstone exists to keep was lost: %+v", entry)
	}
	if len(entry.FinalSHAs) != 1 {
		t.Fatalf("final SHAs = %+v, want one per repo", entry.FinalSHAs)
	}
	final := entry.FinalSHAs[0]
	if final.Repo != "core" || final.Source != worktreeregistry.SHASourceBranchHead {
		t.Errorf("final state = %+v, want core from its branch head (no receipts exist)", final)
	}
	head := gitLine(filepath.Join(container, "core"), "rev-parse", "HEAD")
	if final.SHA != head {
		t.Errorf("recorded sha %q != live head %q", final.SHA, head)
	}

	// Re-running finish must not fail on an already-tombstoned entry.
	rebuild, err := BuildItems(bctx, Options{})
	if err != nil {
		t.Fatal(err)
	}
	again := ItemsByID(rebuild.Items, ItemTombstoneRegistry)
	if again.IsAvailable {
		t.Errorf("an already-tombstoned entry should not offer the item again, status = %q", again.Status)
	}
}

func TestLedgerItemWritesRenderedLedgerAndFailsLoudly(t *testing.T) {
	bctx, _, planPath := setupProvenancePlan(t)

	var gotDir, gotPlan, gotTitle, gotBody string
	bctx.LedgerWriter = func(planDir, planName, title, body string) (LedgerNoteResult, error) {
		gotDir, gotPlan, gotTitle, gotBody = planDir, planName, title, body
		return LedgerNoteResult{Path: "/notes/completed/ledger.md"}, nil
	}

	result, err := BuildItems(bctx, Options{})
	if err != nil {
		t.Fatal(err)
	}
	item := ItemsByID(result.Items, ItemLedgerNote)
	if item == nil {
		t.Fatal("ledger item not built")
	}
	if err := item.Action(); err != nil {
		t.Fatalf("ledger action: %v", err)
	}
	if gotDir != planPath {
		t.Errorf("planDir = %q, want the plan dir %q (nb must resolve the PLAN's workspace)", gotDir, planPath)
	}
	if gotPlan != "my-plan" || gotTitle != "Plan ledger: my-plan" {
		t.Errorf("plan/title = %q/%q", gotPlan, gotTitle)
	}
	for _, want := range []string{"# Plan ledger: my-plan", "## Per-job commit ranges", "| core | feature |"} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("ledger body missing %q\n%s", want, gotBody)
		}
	}

	// A write failure must surface as an item failure, not a shrug: this item
	// exists to stop finish destroying the plan's story.
	bctx.LedgerWriter = func(string, string, string, string) (LedgerNoteResult, error) {
		return LedgerNoteResult{}, os.ErrPermission
	}
	failing, err := BuildItems(bctx, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ItemsByID(failing.Items, ItemLedgerNote).Action(); err == nil {
		t.Error("a failed ledger write must fail the item")
	}
}

func TestLedgerItemSkippedByOption(t *testing.T) {
	bctx, _, _ := setupProvenancePlan(t)
	called := false
	bctx.LedgerWriter = func(string, string, string, string) (LedgerNoteResult, error) {
		called = true
		return LedgerNoteResult{}, nil
	}
	result, err := BuildItems(bctx, Options{NoLedger: true})
	if err != nil {
		t.Fatal(err)
	}
	item := ItemsByID(result.Items, ItemLedgerNote)
	if item.IsAvailable {
		t.Errorf("--no-ledger must make the item unavailable, status = %q", item.Status)
	}
	if err := item.Action(); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("--no-ledger must not write a note")
	}
}

// The prune item is the teardown step that historically ended in
// worktreeregistry.Delete. Once a tombstone exists it must survive it.
func TestPruneWorktreeKeepsTombstonedRegistryEntry(t *testing.T) {
	sandboxGroveHome(t)
	gitRoot, worktreeName, wPath := setupPruneTest(t, false)
	planPath := t.TempDir()

	if err := worktreeregistry.Save(&worktreeregistry.Entry{AbsPath: wPath, Owner: gitRoot, Plan: "my-plan"}); err != nil {
		t.Fatal(err)
	}
	if _, err := worktreeregistry.Tombstone(pathutil.WorktreeID(wPath),
		[]worktreeregistry.RepoFinalState{{Repo: "core", SHA: "abc123"}}); err != nil {
		t.Fatal(err)
	}

	prune := buildPruneItem(t, &recordingExecutor{}, gitRoot, worktreeName, planPath)
	if err := prune.Action(); err != nil {
		t.Fatalf("prune action: %v", err)
	}

	entry, err := worktreeregistry.Load(pathutil.WorktreeID(wPath))
	if err != nil {
		t.Fatalf("prune destroyed the tombstone: %v", err)
	}
	if !entry.IsFinished() || len(entry.FinalSHAs) != 1 {
		t.Errorf("tombstone was degraded by prune: %+v", entry)
	}
}

// An ACTIVE entry is still deleted by prune, exactly as before.
func TestPruneWorktreeStillDeletesActiveRegistryEntry(t *testing.T) {
	sandboxGroveHome(t)
	gitRoot, worktreeName, wPath := setupPruneTest(t, false)
	planPath := t.TempDir()

	if err := worktreeregistry.Save(&worktreeregistry.Entry{AbsPath: wPath, Owner: gitRoot, Plan: "my-plan"}); err != nil {
		t.Fatal(err)
	}

	prune := buildPruneItem(t, &recordingExecutor{}, gitRoot, worktreeName, planPath)
	if err := prune.Action(); err != nil {
		t.Fatalf("prune action: %v", err)
	}
	if _, err := worktreeregistry.Load(pathutil.WorktreeID(wPath)); !os.IsNotExist(err) {
		t.Errorf("an untombstoned entry must still be deleted by prune, got %v", err)
	}
}
