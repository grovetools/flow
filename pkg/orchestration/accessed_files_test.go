package orchestration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/grovetools/core/pkg/workspace"
)

func writeAccessedFixture(t *testing.T, planDir, jobDir, content string) string {
	t.Helper()
	dir := filepath.Join(planDir, ".artifacts", jobDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "accessed_files.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadAccessedFilesDedupAndOrdering(t *testing.T) {
	planDir := t.TempDir()
	fixture := `{"timestamp":"2026-07-18T10:00:00Z","tool":"Read","path":"/a/one.go","action":"read"}
{"timestamp":"2026-07-18T10:00:01Z","tool":"Read","path":"/a/two.go","action":"read"}
not json at all
{"timestamp":"2026-07-18T10:00:02Z","tool":"Edit","path":"/a/one.go","action":"modified"}

{"timestamp":"2026-07-18T10:00:03Z","tool":"Read","path":"/a/three.go","action":"read"}
`
	path := writeAccessedFixture(t, planDir, "job-1", fixture)

	files, err := ReadAccessedFiles(path, "")
	if err != nil {
		t.Fatalf("ReadAccessedFiles: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("expected 3 deduped files, got %d: %+v", len(files), files)
	}
	// Ordered by last access: two.go (10:00:01), one.go (10:00:02), three.go (10:00:03).
	wantOrder := []string{"/a/two.go", "/a/one.go", "/a/three.go"}
	for i, want := range wantOrder {
		if files[i].Path != want {
			t.Errorf("order[%d] = %s, want %s", i, files[i].Path, want)
		}
	}
	one := files[1]
	if one.Count != 2 {
		t.Errorf("one.go count = %d, want 2", one.Count)
	}
	if one.Action != "modified" {
		t.Errorf("one.go action = %s, want modified (last action wins)", one.Action)
	}
	if one.LastTimestamp != "2026-07-18T10:00:02Z" {
		t.Errorf("one.go last timestamp = %s", one.LastTimestamp)
	}
}

func TestReadAccessedFilesMissing(t *testing.T) {
	files, err := ReadAccessedFiles(filepath.Join(t.TempDir(), "nope", "accessed_files.jsonl"), "")
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("missing file should yield empty list, got %+v", files)
	}

	files, err = ReadAccessedFiles("", "")
	if err != nil || len(files) != 0 {
		t.Fatalf("empty path should yield empty list, got %v / %+v", err, files)
	}
}

func TestReadAccessedFilesAbsolutizes(t *testing.T) {
	planDir := t.TempDir()
	base := t.TempDir()

	// A worktree-root-relative file, the same file via a ./ spelling and via
	// its absolute path (all must collapse), plus a file that only exists in
	// a sub-repo of the (ecosystem) base — the agent had cd'd into it.
	for _, f := range []string{
		filepath.Join(base, "flow", "pkg", "a.go"),
		filepath.Join(base, "cx", "pkg", "context", "cache.go"),
	} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	fixture := `{"timestamp":"2026-07-18T10:00:00Z","tool":"Read","path":"flow/pkg/a.go","action":"read"}
{"timestamp":"2026-07-18T10:00:01Z","tool":"Read","path":"./flow/pkg/a.go","action":"read"}
{"timestamp":"2026-07-18T10:00:02Z","tool":"Edit","path":"` + filepath.Join(base, "flow", "pkg", "a.go") + `","action":"modified"}
{"timestamp":"2026-07-18T10:00:03Z","tool":"Read","path":"pkg/context/cache.go","action":"read"}
{"timestamp":"2026-07-18T10:00:04Z","tool":"Read","path":"pkg/context/nowhere.go","action":"read"}
`
	path := writeAccessedFixture(t, planDir, "job-abs", fixture)

	files, err := ReadAccessedFiles(path, base)
	if err != nil {
		t.Fatalf("ReadAccessedFiles: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("expected 3 deduped files, got %d: %+v", len(files), files)
	}

	// Three spellings of a.go collapse into one absolute row.
	a := files[0]
	if a.Path != filepath.Join(base, "flow", "pkg", "a.go") {
		t.Errorf("a.go path = %s", a.Path)
	}
	if a.Count != 3 || a.Action != "modified" {
		t.Errorf("a.go count/action = %d/%s, want 3/modified", a.Count, a.Action)
	}

	// A sub-repo-relative row resolves through the unique sub-repo probe.
	if files[1].Path != filepath.Join(base, "cx", "pkg", "context", "cache.go") {
		t.Errorf("sub-repo path = %s, want under %s/cx", files[1].Path, base)
	}

	// A row that exists nowhere still absolutizes against base (best effort).
	if files[2].Path != filepath.Join(base, "pkg", "context", "nowhere.go") {
		t.Errorf("nonexistent path = %s", files[2].Path)
	}
}

func TestReadAccessedFilesNoBaseKeepsRelative(t *testing.T) {
	planDir := t.TempDir()
	fixture := `{"timestamp":"2026-07-18T10:00:00Z","tool":"Read","path":"pkg/b.go","action":"read"}
{"timestamp":"2026-07-18T10:00:01Z","tool":"Read","path":"./pkg/b.go","action":"read"}
`
	path := writeAccessedFixture(t, planDir, "job-rel", fixture)

	// No base (worktree gone): entries stay relative but are still cleaned
	// and deduped across spellings.
	files, err := ReadAccessedFiles(path, "")
	if err != nil {
		t.Fatalf("ReadAccessedFiles: %v", err)
	}
	if len(files) != 1 || files[0].Path != filepath.Join("pkg", "b.go") || files[0].Count != 2 {
		t.Fatalf("expected one cleaned relative row with count 2, got %+v", files)
	}
}

func TestAccessedFilesPathCandidates(t *testing.T) {
	planDir := t.TempDir()
	job := &Job{ID: "20260101-000000-my-job", Filename: "01-my-job.md"}

	if got := AccessedFilesPath(planDir, job); got != "" {
		t.Fatalf("no trace should yield empty path, got %s", got)
	}

	// Filename-without-extension dir is found when the ID dir is absent.
	byName := writeAccessedFixture(t, planDir, "01-my-job", "")
	if got := AccessedFilesPath(planDir, job); got != byName {
		t.Errorf("expected filename-based path %s, got %s", byName, got)
	}

	// The job-ID dir wins when both exist.
	byID := writeAccessedFixture(t, planDir, job.ID, "")
	if got := AccessedFilesPath(planDir, job); got != byID {
		t.Errorf("expected id-based path %s, got %s", byID, got)
	}
}

func TestWorkspaceRootedPath(t *testing.T) {
	root := t.TempDir()
	repoPath := filepath.Join(root, "flow")
	worktreePath := filepath.Join(root, "worktrees", "my-feature", "flow")
	// The files must exist: path normalization resolves symlinks (e.g. macOS
	// /var/folders -> /private/var) only for paths that are on disk.
	for _, f := range []string{
		filepath.Join(repoPath, "pkg", "a.go"),
		filepath.Join(worktreePath, "cmd", "b.go"),
	} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	provider := workspace.NewProviderFromNodes([]*workspace.WorkspaceNode{
		{Name: "flow", Path: repoPath, Kind: workspace.KindStandaloneProject},
		{
			Name:              "my-feature",
			Path:              worktreePath,
			Kind:              workspace.KindStandaloneProjectWorktree,
			ParentProjectPath: repoPath,
		},
	})

	// A file directly in the repo.
	got := WorkspaceRootedPath(provider, filepath.Join(repoPath, "pkg", "a.go"))
	if got != filepath.Join("flow", "pkg", "a.go") {
		t.Errorf("repo file = %s, want flow/pkg/a.go", got)
	}

	// A file in a worktree is named by the parent repo (worktree-unrooted).
	got = WorkspaceRootedPath(provider, filepath.Join(worktreePath, "cmd", "b.go"))
	if got != filepath.Join("flow", "cmd", "b.go") {
		t.Errorf("worktree file = %s, want flow/cmd/b.go", got)
	}

	// A path outside any workspace falls back to the input.
	outside := filepath.Join(root, "elsewhere", "c.go")
	if got := WorkspaceRootedPath(provider, outside); got != outside {
		t.Errorf("outside file = %s, want %s", got, outside)
	}

	// Nil provider falls back to the input.
	if got := WorkspaceRootedPath(nil, outside); got != outside {
		t.Errorf("nil provider = %s, want %s", got, outside)
	}
}
