package orchestration

import (
	"os"
	"path/filepath"
	"testing"

	grovecontext "github.com/grovetools/cx/pkg/context"
)

func sliceContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// TestCollectContextFiles_JobCtxAuthoritative locks in the strip_comments fix:
// once a job-scoped context was requested (jobCtx != nil), collectContextFiles
// must NEVER substitute the shared plan-level context/generated/context — that
// shared file is written by a manager that did not honor this job's
// strip_comments, so falling back to it uploads unstripped context. Only jobless
// callers (jobCtx == nil) keep the legacy shared fallback.
func TestCollectContextFiles_JobCtxAuthoritative(t *testing.T) {
	dir := t.TempDir()
	e := &OneShotExecutor{}
	job := &Job{}
	plan := &Plan{Directory: dir}

	// Seed the shared plan-level context where NewManager resolves it, plus a
	// CLAUDE.md in the context dir.
	sharedPath := grovecontext.NewManager(dir).ResolveContextPath()
	if err := os.MkdirAll(filepath.Dir(sharedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sharedPath, []byte("SHARED-UNSTRIPPED"), 0o600); err != nil {
		t.Fatal(err)
	}
	claudePath := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte("CLAUDE"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Case 1 (legacy): jobCtx == nil → shared fallback IS used.
	got := e.collectContextFiles(job, plan, dir, nil)
	if !sliceContains(got, sharedPath) {
		t.Fatalf("nil jobCtx should include the shared context %q, got %v", sharedPath, got)
	}

	// Case 2 (the fix): jobCtx != nil but its files don't exist on disk (e.g. a
	// regen that produced nothing) → NO shared fallback; only CLAUDE.md.
	jobCtx := &jobContextPaths{
		Hot:  filepath.Join(dir, ".artifacts", "job1", "context", "context"),
		Cold: filepath.Join(dir, ".artifacts", "job1", "context", "cached-context"),
	}
	got = e.collectContextFiles(job, plan, dir, jobCtx)
	if sliceContains(got, sharedPath) {
		t.Fatalf("non-nil jobCtx must NOT substitute the shared context %q, got %v", sharedPath, got)
	}
	if !sliceContains(got, claudePath) {
		t.Fatalf("expected CLAUDE.md %q to be included, got %v", claudePath, got)
	}

	// Case 3: jobCtx != nil with an existing job-scoped hot file → that file is
	// attached and the shared file still never is.
	if err := os.MkdirAll(filepath.Dir(jobCtx.Hot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jobCtx.Hot, []byte("JOB-SCOPED-STRIPPED"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = e.collectContextFiles(job, plan, dir, jobCtx)
	if !sliceContains(got, jobCtx.Hot) {
		t.Fatalf("expected job-scoped hot file %q to be attached, got %v", jobCtx.Hot, got)
	}
	if sliceContains(got, sharedPath) {
		t.Fatalf("non-nil jobCtx must never include the shared context, got %v", got)
	}
}
