package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/core/tui/theme"
	grovecontext "github.com/grovetools/cx/pkg/context"
)

// pinnedOrderFileName is the artifact that records the observed order of a
// chat job's pinned_context files. Persisting the order lets the executor keep
// prior members in the same document positions across turns even if the user
// reorders the frontmatter list — a precondition for the Anthropic pinned
// cache_control breakpoint to keep hitting the cache. It lives next to the
// per-job context under <plan>/.artifacts/<job-id>/.
const pinnedOrderFileName = "pinned-context-order"

// resolvePinnedSource resolves a pinned_context source path with worktree-first
// semantics. pinned_context paths are a WORKTREE-ROOT-relative contract (see the
// oracle-chats concept), but the daemon executes chat turns with a cwd that is
// NOT the worktree, so ResolvePromptSource's plan-dir/cwd strategies miss the
// file entirely (the "could not find pinned_context file core/logging/..." live
// failure). Try, in order: an absolute path as-is; the worktree-root join (the
// documented contract); then fall back to ResolvePromptSource's plan-dir/cwd
// strategies so plan-relative pins and existing callers keep working.
//
// worktreeRoot MUST be the pre-scope worktree ROOT, not a sub-project-scoped
// path, so ecosystem-relative pins like "core/logging/logger_test.go" resolve
// against the superrepo root rather than a scoped sub-project directory.
func resolvePinnedSource(source, worktreeRoot string, plan *Plan) (string, error) {
	// Absolute path: honor as given (matches ResolvePromptSource's contract for
	// its other callers — prompts, includes, templates).
	if filepath.IsAbs(source) {
		return source, nil
	}

	// Worktree-root-relative: the documented pinned_context contract, tried
	// before the shared fallbacks because the daemon's cwd is not the worktree.
	if worktreeRoot != "" {
		if candidate := filepath.Join(worktreeRoot, source); fileExistsAt(candidate) {
			return candidate, nil
		}
	}

	// Fall back to the shared resolution strategies (plan dir, plan parent, cwd)
	// so plan-relative pins still resolve. ResolvePromptSource is left untouched
	// for its other callers.
	return ResolvePromptSource(source, plan)
}

// fileExistsAt reports whether a regular path exists (best-effort stat).
func fileExistsAt(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// reconcileOrder computes the emission order for pinned files given the order
// persisted from prior turns and the set resolved this turn. Rules:
//   - persisted entries that still appear in current keep their persisted order;
//   - persisted entries no longer in current are dropped (removal shifts later
//     blocks → busts the pinned region onward, but the stable prefix survives);
//   - current entries not seen before are appended at the end (new pinned files
//     get one unavoidable cache write, then cache durably on later turns).
//
// It is a pure function so it can be unit-tested without touching disk.
func reconcileOrder(persisted, current []string) []string {
	inCurrent := make(map[string]bool, len(current))
	for _, c := range current {
		inCurrent[c] = true
	}

	result := make([]string, 0, len(current))
	emitted := make(map[string]bool, len(current))

	// Keep persisted entries that are still present, in persisted order.
	for _, p := range persisted {
		if inCurrent[p] && !emitted[p] {
			result = append(result, p)
			emitted[p] = true
		}
	}
	// Append any current entries not already emitted, in declared order.
	for _, c := range current {
		if !emitted[c] {
			result = append(result, c)
			emitted[c] = true
		}
	}
	return result
}

// reconcilePinnedOrder loads the persisted pinned order for this job, reconciles
// it against the paths resolved this turn (see reconcileOrder), rewrites the
// artifact, and returns the reconciled order. All disk operations are
// best-effort: on any error it logs and falls back to the caller's order so a
// chat turn never fails over pinned-order bookkeeping.
func reconcilePinnedOrder(ctx context.Context, plan *Plan, job *Job, current []string) []string {
	if plan == nil || job == nil || job.ID == "" || len(current) == 0 {
		return current
	}

	orderPath := filepath.Join(plan.Directory, ".artifacts", job.ID, pinnedOrderFileName)

	var persisted []string
	if data, err := os.ReadFile(orderPath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				persisted = append(persisted, line)
			}
		}
	}

	reconciled := reconcileOrder(persisted, current)

	// Persist the new order (best-effort).
	if err := os.MkdirAll(filepath.Dir(orderPath), 0o755); err == nil {
		_ = os.WriteFile(orderPath, []byte(strings.Join(reconciled, "\n")+"\n"), 0o600)
	} else {
		ulog.Warn("Failed to persist pinned-context order").
			Field("job_id", job.ID).
			Err(err).
			Log(ctx)
	}

	return reconciled
}

// warnPinnedContextOverlap warns when a pinned file is ALSO swept into the
// cx-generated hot context (via the job's rules). That double-places the file
// (wasted tokens) and, worse, reintroduces edit-sensitivity to the stable
// prefix — the very problem pinning avoids — because the cold/hot blobs are
// regenerated every turn. Best-effort and read-only; it never fails the turn.
func warnPinnedContextOverlap(ctx context.Context, contextDir string, jobCtx *jobContextPaths, pinned []string) {
	if jobCtx == nil || jobCtx.FilesList == "" || len(pinned) == 0 {
		return
	}

	mgr := grovecontext.NewManager(contextDir)
	files, err := mgr.ReadFilesList(jobCtx.FilesList)
	if err != nil || len(files) == 0 {
		return
	}

	// Build a lookup of the cx-context files by absolute path (entries are
	// repo-root-relative) and by basename as a lenient fallback.
	absSet := make(map[string]bool, len(files))
	baseSet := make(map[string]bool, len(files))
	for _, f := range files {
		abs := f
		if !filepath.IsAbs(f) {
			abs = filepath.Join(contextDir, f)
		}
		if a, aerr := filepath.Abs(abs); aerr == nil {
			absSet[a] = true
		}
		baseSet[filepath.Base(f)] = true
	}

	for _, p := range pinned {
		abs := p
		if a, aerr := filepath.Abs(p); aerr == nil {
			abs = a
		}
		if absSet[abs] || baseSet[filepath.Base(p)] {
			ulog.Warn("pinned_context file also appears in the cx-generated context; it will be uploaded twice and its edits will bust the stable prefix — exclude it from the rules file").
				Field("file", p).
				Icon(theme.IconWarning).
				Log(ctx)
		}
	}
}
