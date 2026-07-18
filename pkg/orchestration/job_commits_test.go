package orchestration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// initTestGitRepo creates a git repo at dir with one initial commit and
// returns its HEAD SHA.
func initTestGitRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, dir, "init", "-q", "-b", "main")
	runTestGit(t, dir, "config", "user.email", "test@example.com")
	runTestGit(t, dir, "config", "user.name", "Test")
	commitTestFile(t, dir, "README.md", "initial\n", "initial commit")
	head, err := gitOutput(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return head
}

func commitTestFile(t *testing.T, dir, name, content, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, dir, "add", "-A")
	runTestGit(t, dir, "commit", "-q", "-m", msg)
	head, err := gitOutput(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return head
}

func runTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func testCommitsFixture(t *testing.T) (*Plan, *Job, string) {
	t.Helper()
	tmp := t.TempDir()
	planDir := filepath.Join(tmp, "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	container := filepath.Join(tmp, "container")
	plan := &Plan{Directory: planDir}
	job := &Job{
		ID:        "test-job-abc12345",
		FilePath:  filepath.Join(planDir, "01-test-job.md"),
		Worktree:  "test-wt",
		StartTime: time.Now(),
	}
	return plan, job, container
}

func TestJobCommitsCaptureAndFinalize(t *testing.T) {
	plan, job, container := testCommitsFixture(t)
	repoA := filepath.Join(container, "alpha")
	repoB := filepath.Join(container, "beta")
	startA := initTestGitRepo(t, repoA)
	initTestGitRepo(t, repoB)
	// A non-repo subdir must be skipped silently.
	if err := os.MkdirAll(filepath.Join(container, "not-a-repo"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := CaptureJobCommitsStart(job, plan, container); err != nil {
		t.Fatalf("CaptureJobCommitsStart: %v", err)
	}

	startRec, err := ReadJobCommits(plan, job)
	if err != nil {
		t.Fatalf("ReadJobCommits after start: %v", err)
	}
	if startRec.FinishedAt != "" {
		t.Errorf("start record should have no finished_at, got %q", startRec.FinishedAt)
	}
	if len(startRec.Repos) != 2 {
		t.Fatalf("start record repos = %d, want 2", len(startRec.Repos))
	}
	if startRec.Repos[0].Name != "alpha" || startRec.Repos[0].StartHead != startA {
		t.Errorf("start record alpha = %+v, want start_head %s", startRec.Repos[0], startA)
	}

	// The "job" commits twice in alpha, leaves beta untouched but dirty.
	c1 := commitTestFile(t, repoA, "one.txt", "1\n", "first")
	c2 := commitTestFile(t, repoA, "two.txt", "2\n", "second")
	if err := os.WriteFile(filepath.Join(repoB, "untracked.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := FinalizeJobCommits(job, plan); err != nil {
		t.Fatalf("FinalizeJobCommits: %v", err)
	}

	rec, err := ReadJobCommits(plan, job)
	if err != nil {
		t.Fatalf("ReadJobCommits after finalize: %v", err)
	}
	if rec.Schema != jobCommitsSchemaVersion {
		t.Errorf("schema = %d, want %d", rec.Schema, jobCommitsSchemaVersion)
	}
	if rec.JobID != job.ID || rec.JobFile != "01-test-job.md" || rec.Worktree != container {
		t.Errorf("record identity fields wrong: %+v", rec)
	}
	if rec.FinishedAt == "" {
		t.Error("finalized record missing finished_at")
	}
	if len(rec.Repos) != 2 {
		t.Fatalf("finalized repos = %d, want 2", len(rec.Repos))
	}

	alpha, beta := rec.Repos[0], rec.Repos[1]
	if alpha.Name != "alpha" || beta.Name != "beta" {
		t.Fatalf("repo order = %s,%s, want alpha,beta", alpha.Name, beta.Name)
	}
	if alpha.StartHead != startA || alpha.EndHead != c2 {
		t.Errorf("alpha range = %s..%s, want %s..%s", alpha.StartHead, alpha.EndHead, startA, c2)
	}
	if len(alpha.Commits) != 2 || alpha.Commits[0] != c1 || alpha.Commits[1] != c2 {
		t.Errorf("alpha commits = %v, want oldest-first [%s %s]", alpha.Commits, c1, c2)
	}
	if alpha.Branch != "main" {
		t.Errorf("alpha branch = %q, want main", alpha.Branch)
	}
	if alpha.DirtyAtEnd {
		t.Error("alpha should be clean at end")
	}
	if beta.Commits == nil || len(beta.Commits) != 0 {
		t.Errorf("beta commits = %v, want empty (non-null) array", beta.Commits)
	}
	if !beta.DirtyAtEnd {
		t.Error("beta should be dirty at end (untracked file)")
	}

	// The empty-vs-null distinction must survive JSON round-tripping.
	raw, err := os.ReadFile(JobCommitsPath(plan, job))
	if err != nil {
		t.Fatal(err)
	}
	if !containsJSONKeyValue(string(raw), `"commits": []`) {
		t.Errorf("beta's empty commits should serialize as [], got:\n%s", raw)
	}
}

func TestFinalizeJobCommitsIdempotent(t *testing.T) {
	plan, job, container := testCommitsFixture(t)
	repo := filepath.Join(container, "alpha")
	initTestGitRepo(t, repo)

	if err := CaptureJobCommitsStart(job, plan, container); err != nil {
		t.Fatal(err)
	}
	if err := FinalizeJobCommits(job, plan); err != nil {
		t.Fatal(err)
	}
	first, err := ReadJobCommits(plan, job)
	if err != nil {
		t.Fatal(err)
	}

	// A commit landing AFTER finalize (e.g. another job in the shared
	// worktree) must not be attributed by a repeat finalize.
	commitTestFile(t, repo, "later.txt", "x\n", "post-job commit")
	if err := FinalizeJobCommits(job, plan); err != nil {
		t.Fatal(err)
	}
	second, err := ReadJobCommits(plan, job)
	if err != nil {
		t.Fatal(err)
	}
	if second.FinishedAt != first.FinishedAt || len(second.Repos[0].Commits) != len(first.Repos[0].Commits) {
		t.Errorf("repeat finalize modified the record: first=%+v second=%+v", first, second)
	}
}

func TestFinalizeJobCommitsMissingStart(t *testing.T) {
	plan, job, container := testCommitsFixture(t)
	repo := filepath.Join(container, "alpha")
	head := initTestGitRepo(t, repo)

	// No start capture ever ran: the builder must still record the repo, with
	// null commits and the contractual note.
	rec, err := buildFinalizedJobCommits(job, container, nil)
	if err != nil {
		t.Fatalf("buildFinalizedJobCommits: %v", err)
	}
	if len(rec.Repos) != 1 {
		t.Fatalf("repos = %d, want 1", len(rec.Repos))
	}
	got := rec.Repos[0]
	if got.Commits != nil {
		t.Errorf("commits = %v, want null", got.Commits)
	}
	if got.Note != jobCommitsNoteStartMissing {
		t.Errorf("note = %q, want %q", got.Note, jobCommitsNoteStartMissing)
	}
	if got.EndHead != head {
		t.Errorf("end_head = %q, want %q", got.EndHead, head)
	}
	if err := writeJobCommits(plan, job, rec); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(JobCommitsPath(plan, job))
	if err != nil {
		t.Fatal(err)
	}
	if !containsJSONKeyValue(string(raw), `"commits": null`) {
		t.Errorf("missing-start commits should serialize as null, got:\n%s", raw)
	}

	// A start record that lacks one repo (added to the worktree mid-job) gets
	// the same treatment for that repo only.
	repoB := filepath.Join(container, "beta")
	initTestGitRepo(t, repoB)
	partial := &JobCommitsRecord{
		Schema:    jobCommitsSchemaVersion,
		JobID:     job.ID,
		Worktree:  container,
		StartedAt: time.Now().Format(time.RFC3339),
		Repos:     []JobCommitsRepo{{Name: "alpha", Path: repo, StartHead: head}},
	}
	rec, err = buildFinalizedJobCommits(job, container, partial)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Repos) != 2 {
		t.Fatalf("repos = %d, want 2", len(rec.Repos))
	}
	if rec.Repos[0].Commits == nil || rec.Repos[0].Note != "" {
		t.Errorf("alpha (captured at start) = %+v, want computed commits", rec.Repos[0])
	}
	if rec.Repos[1].Commits != nil || rec.Repos[1].Note != jobCommitsNoteStartMissing {
		t.Errorf("beta (added mid-job) = %+v, want null commits + note", rec.Repos[1])
	}
}

func TestFinalizeJobCommitsNoRecordNoWorktree(t *testing.T) {
	plan, job, _ := testCommitsFixture(t)
	job.Worktree = ""
	if err := FinalizeJobCommits(job, plan); err != nil {
		t.Fatalf("finalize without record or worktree should be a no-op, got %v", err)
	}
	if _, err := ReadJobCommits(plan, job); !os.IsNotExist(err) {
		t.Errorf("no sidecar should be written, got err=%v", err)
	}
}

func TestGitCommitRangeUnbornStart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	head := initTestGitRepo(t, dir)

	commits, err := gitCommitRange(dir, "", head)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 1 || commits[0] != head {
		t.Errorf("unborn-start range = %v, want [%s]", commits, head)
	}

	commits, err = gitCommitRange(dir, head, head)
	if err != nil {
		t.Fatal(err)
	}
	if commits == nil || len(commits) != 0 {
		t.Errorf("identical endpoints = %v, want empty non-null", commits)
	}
}

// containsJSONKeyValue reports whether the rendered JSON contains the given
// fragment (MarshalIndent output with two-space indentation).
func containsJSONKeyValue(doc, fragment string) bool {
	return strings.Contains(doc, fragment)
}
