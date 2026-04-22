package plan_finish

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grovetools/core/git"
	gexec "github.com/grovetools/flow/pkg/exec"
	"github.com/grovetools/flow/pkg/orchestration"
)

// recordingExecutor wraps MockCommandExecutor with scripted responses
// indexed by call order so tests can simulate git failures.
type recordingExecutor struct {
	Calls     []string
	Responses []error
}

func (r *recordingExecutor) LookPath(file string) (string, error) {
	return "/path/to/" + file, nil
}

func (r *recordingExecutor) Execute(name string, arg ...string) error {
	cmd := name
	if len(arg) > 0 {
		cmd = name + " " + strings.Join(arg, " ")
	}
	r.Calls = append(r.Calls, cmd)
	idx := len(r.Calls) - 1
	if idx < len(r.Responses) {
		return r.Responses[idx]
	}
	return nil
}

func setupPruneTest(t *testing.T, withSubmodules bool) (gitRoot, worktreeName, wPath string) {
	t.Helper()
	gitRoot = t.TempDir()
	worktreeName = "feature"
	wPath = filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
	if err := os.MkdirAll(wPath, 0755); err != nil {
		t.Fatal(err)
	}
	if withSubmodules {
		gm := `[submodule "fake"]
	path = fake
	url = https://example.invalid/fake.git
`
		if err := os.WriteFile(filepath.Join(wPath, ".gitmodules"), []byte(gm), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return gitRoot, worktreeName, wPath
}

func buildPruneItem(t *testing.T, exec gexec.CommandExecutor, gitRoot, worktreeName, planPath string) *struct {
	Action func() error
} {
	t.Helper()
	plan := &orchestration.Plan{
		Config: &orchestration.PlanConfig{Worktree: worktreeName, Status: "review"},
	}
	bctx := BuildContext{
		PlanPath:     planPath,
		Plan:         plan,
		GitRoot:      gitRoot,
		WorktreeName: worktreeName,
		BranchName:   worktreeName,
		Executor:     exec,
		WM:           git.NewWorktreeManager(),
	}
	result, err := BuildItems(bctx, Options{PruneWorktree: true})
	if err != nil {
		t.Fatalf("BuildItems: %v", err)
	}
	item := ItemsByID(result.Items, ItemPruneWorktree)
	if item == nil {
		t.Fatal("prune_worktree item not found")
	}
	return &struct{ Action func() error }{Action: item.Action}
}

// TestPruneWorktree_ParentBeforeSubmodules asserts parent worktree
// removal is attempted FIRST — before any submodule worktree pruning.
// Historically this was inverted, causing the parent's tracked
// submodule checkouts to vanish and git to refuse removal.
func TestPruneWorktree_ParentBeforeSubmodules(t *testing.T) {
	gitRoot, worktreeName, wPath := setupPruneTest(t, false)
	planPath := t.TempDir()
	exec := &recordingExecutor{}
	prune := buildPruneItem(t, exec, gitRoot, worktreeName, planPath)

	if err := prune.Action(); err != nil {
		t.Fatalf("Action failed: %v", err)
	}
	if len(exec.Calls) == 0 {
		t.Fatal("expected at least one executor call")
	}
	first := exec.Calls[0]
	if !strings.Contains(first, "worktree remove") {
		t.Fatalf("first call should be parent worktree remove, got %q", first)
	}
	if !strings.HasSuffix(first, wPath) {
		t.Fatalf("first call should target parent path %s, got %q", wPath, first)
	}
}

// TestPruneWorktree_FallbackOnSubmoduleRefusal asserts that when git
// initially refuses parent removal with the "working trees containing
// submodules" message, the factory prunes linked submodule worktrees
// and retries the parent — still BEFORE any other work.
func TestPruneWorktree_FallbackOnSubmoduleRefusal(t *testing.T) {
	gitRoot, worktreeName, wPath := setupPruneTest(t, true)
	planPath := t.TempDir()

	submoduleErr := &fakeExecError{msg: "fatal: working trees containing submodules"}
	exec := &recordingExecutor{
		Responses: []error{submoduleErr, nil, nil, nil},
	}
	prune := buildPruneItem(t, exec, gitRoot, worktreeName, planPath)

	if err := prune.Action(); err != nil {
		t.Fatalf("Action should succeed after retry, got: %v", err)
	}
	if len(exec.Calls) < 2 {
		t.Fatalf("expected at least 2 executor calls (initial + retry), got %d: %v", len(exec.Calls), exec.Calls)
	}
	first := exec.Calls[0]
	last := exec.Calls[len(exec.Calls)-1]
	if !strings.Contains(first, "worktree remove") || !strings.HasSuffix(first, wPath) {
		t.Fatalf("first call should be parent worktree remove for %s, got %q", wPath, first)
	}
	// Retry must target the same parent path.
	retryFound := false
	for _, c := range exec.Calls[1:] {
		if strings.Contains(c, "worktree remove") && strings.HasSuffix(c, wPath) {
			retryFound = true
			break
		}
	}
	if !retryFound {
		t.Fatalf("expected retry of parent worktree remove, got calls: %v (last=%q)", exec.Calls, last)
	}
}

type fakeExecError struct{ msg string }

func (e *fakeExecError) Error() string { return e.msg }
