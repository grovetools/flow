package planops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	coreplan "github.com/grovetools/core/pkg/plan"
)

func TestSingleRepoUpdateAndLand(t *testing.T) {
	main, worktree := repoFixture(t, "master")
	commitFile(t, main, "main.txt", "main advance")
	commitFile(t, worktree, "feature.txt", "feature advance")

	target := targetFor(coreplan.RepoTarget{Name: "single", Path: worktree})
	update, err := Preview(context.Background(), target, OperationUpdateOnly)
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Repos) != 1 || update.Repos[0].Onto != "master" || update.Repos[0].Disposition != DispositionReady {
		t.Fatalf("unexpected update preview: %#v", update.Repos)
	}
	updated := Execute(context.Background(), update)
	if updated.Failed() || updated.Results[0].Outcome != OutcomeSucceeded {
		t.Fatalf("update failed: %#v", updated)
	}

	land, err := Preview(context.Background(), target, OperationLand)
	if err != nil {
		t.Fatal(err)
	}
	if land.Repos[0].Disposition != DispositionReady || !samePath(t, land.Repos[0].MainCheckoutPath, main) {
		t.Fatalf("unexpected land preview: %#v", land.Repos[0])
	}
	landed := Execute(context.Background(), land)
	if landed.Failed() {
		t.Fatalf("land failed: %#v", landed)
	}
	if got, want := gitOut(t, main, "rev-parse", "master"), gitOut(t, worktree, "rev-parse", "HEAD"); got != want {
		t.Fatalf("main was not advanced: got %s want %s", got, want)
	}
}

func TestEcosystemPreflightIsDeterministicAndAllRepo(t *testing.T) {
	_, alpha := repoFixture(t, "main")
	_, beta := repoFixture(t, "main")
	commitFile(t, alpha, "alpha.txt", "alpha")
	commitFile(t, beta, "beta.txt", "beta")
	if err := os.WriteFile(filepath.Join(beta, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}

	target := targetFor(
		coreplan.RepoTarget{Name: "z-beta", Path: beta},
		coreplan.RepoTarget{Name: "a-alpha", Path: alpha},
	)
	preview, err := Preview(context.Background(), target, OperationLand)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Repos[0].Name != "a-alpha" || preview.Repos[1].Name != "z-beta" {
		t.Fatalf("preview order is not deterministic: %#v", preview.Repos)
	}
	before := gitOut(t, alpha, "rev-parse", "HEAD")
	result := Execute(context.Background(), preview)
	if !result.Failed() || result.Results[0].Outcome != OutcomeNotAttempted || result.Results[1].Outcome != OutcomeFailed {
		t.Fatalf("unexpected all-repo refusal: %#v", result)
	}
	if got := gitOut(t, alpha, "rev-parse", "HEAD"); got != before {
		t.Fatalf("eligible repo mutated despite blocked peer: %s -> %s", before, got)
	}
}

func TestPreviewFreshnessAndInProgressChecks(t *testing.T) {
	_, worktree := repoFixture(t, "main")
	commitFile(t, worktree, "feature.txt", "feature")
	target := targetFor(coreplan.RepoTarget{Name: "repo", Path: worktree})
	preview, err := Preview(context.Background(), target, OperationLand)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Repos[0].Disposition != DispositionReady {
		t.Fatalf("expected ready preview: %#v", preview.Repos[0])
	}
	if err := os.WriteFile(filepath.Join(worktree, "later.txt"), []byte("later"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := Execute(context.Background(), preview)
	if !result.Stale || result.Results[0].Outcome != OutcomeNotAttempted {
		t.Fatalf("stale preview was not refused: %#v", result)
	}

	if err := os.Remove(filepath.Join(worktree, "later.txt")); err != nil {
		t.Fatal(err)
	}
	gitPath := gitOut(t, worktree, "rev-parse", "--git-path", "MERGE_HEAD")
	if !filepath.IsAbs(gitPath) {
		gitPath = filepath.Join(worktree, gitPath)
	}
	if err := os.WriteFile(gitPath, []byte(gitOut(t, worktree, "rev-parse", "HEAD")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inProgress, err := Preview(context.Background(), target, OperationUpdateOnly)
	if err != nil {
		t.Fatal(err)
	}
	if inProgress.Repos[0].Disposition != DispositionBlocked || len(inProgress.Repos[0].InProgress) == 0 {
		t.Fatalf("merge-in-progress was not blocked: %#v", inProgress.Repos[0])
	}
}

func targetFor(repos ...coreplan.RepoTarget) coreplan.PlanActionTarget {
	return coreplan.PlanActionTarget{PlanDir: "/plans/test", RegistryID: "registry-test", ContainerPath: filepath.Dir(repos[0].Path), Repos: repos}
}

func repoFixture(t *testing.T, defaultBranch string) (string, string) {
	t.Helper()
	root := t.TempDir()
	main := filepath.Join(root, "main")
	runGit(t, root, "init", "--initial-branch="+defaultBranch, main)
	runGit(t, main, "config", "user.email", "test@example.com")
	runGit(t, main, "config", "user.name", "Planops Test")
	commitFile(t, main, "base.txt", "base")
	worktree := filepath.Join(root, "worktree")
	runGit(t, main, "worktree", "add", "-b", "feature", worktree)
	return main, worktree
}

func commitFile(t *testing.T, repo, name, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(message+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", name)
	runGit(t, repo, "commit", "-m", message)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(bytesTrimSpace(out))
}

func samePath(t *testing.T, a, b string) bool {
	t.Helper()
	aa, err := filepath.EvalSymlinks(a)
	if err != nil {
		t.Fatal(err)
	}
	bb, err := filepath.EvalSymlinks(b)
	if err != nil {
		t.Fatal(err)
	}
	return aa == bb
}

func bytesTrimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && (b[start] == ' ' || b[start] == '\n' || b[start] == '\r' || b[start] == '\t') {
		start++
	}
	for end > start && (b[end-1] == ' ' || b[end-1] == '\n' || b[end-1] == '\r' || b[end-1] == '\t') {
		end--
	}
	return b[start:end]
}
