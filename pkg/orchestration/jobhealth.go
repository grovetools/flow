package orchestration

import (
	"fmt"
	"os"
	"time"

	"github.com/grovetools/core/pkg/sessions/health"
)

// This file supplies the two flow-shaped dependencies that the shared
// session-health engine (core/pkg/sessions/health) deliberately does
// not own: reading a job file's frontmatter status, and rewriting it.
//
// core cannot own them because core must not import flow. Both the
// hooks TUI/CLI and the daemon's reaper need exactly this behavior, so
// it lives here — one implementation, imported by both — rather than
// being reimplemented on each side where the two copies would drift.

// ReadJobFileStatus reports a job file's frontmatter status, satisfying
// health.JobFileStatusReader.
//
// A missing file returns ("", false, nil): absence is not an error, it
// just means the flow perspective has nothing to say about this
// session.
func ReadJobFileStatus(path string) (string, bool, error) {
	content, err := os.ReadFile(path) //nolint:gosec // job file paths come from the daemon's own records
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	fm, _, err := ParseFrontmatter(content)
	if err != nil {
		// The file exists but its claim is unreadable. Say that,
		// rather than reporting a status we do not have.
		return "", true, err
	}
	status, _ := fm["status"].(string)
	return status, true, nil
}

// ReconcileJobFile flips a job file's frontmatter status, satisfying
// health.JobFileReconciler. It is the fix for the "87-commit.md running
// 23m" ghost: a file still claiming to run long after every process
// backing it died.
//
// Three rules keep it from doing damage:
//
//   - Only active statuses (running/in_progress) are rewritten. A job
//     that already recorded how it ended is history, and a cleanup
//     arriving afterwards must not overwrite it with a guess.
//   - The write goes through UpdateFrontmatter, so the body survives.
//   - The file's mtime is re-checked immediately before the write. If
//     something touched it while we were deciding, that something is a
//     live writer and we back off rather than race it.
//
// Returns whether the file was actually changed.
func ReconcileJobFile(path, status string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	before := info.ModTime()

	content, err := os.ReadFile(path) //nolint:gosec // see ReadJobFileStatus
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	fm, _, err := ParseFrontmatter(content)
	if err != nil {
		return false, err
	}
	current, _ := fm["status"].(string)
	if !health.IsJobFileStatusActive(current) {
		return false, nil
	}

	updated, err := UpdateFrontmatter(content, map[string]interface{}{
		"status": status,
	})
	if err != nil {
		return false, err
	}

	// Re-stat immediately before writing. A file that moved under us
	// between the read and the write has a live writer, and clobbering
	// it would turn a diagnostic into data loss.
	if after, err := os.Stat(path); err == nil && !after.ModTime().Equal(before) {
		return false, fmt.Errorf("job file changed while reconciling (mtime %s → %s) — leaving it alone",
			before.Format(time.RFC3339Nano), after.ModTime().Format(time.RFC3339Nano))
	}

	if err := os.WriteFile(path, updated, 0o644); err != nil { //nolint:gosec // job files are not sensitive
		return false, err
	}
	return true, nil
}

// JobFileReconciler adapts ReconcileJobFile to health.JobFileReconciler,
// which does not care whether a write happened.
func JobFileReconciler(path, status string) error {
	_, err := ReconcileJobFile(path, status)
	return err
}

// Compile-time proof that these satisfy the engine's injection points.
var (
	_ health.JobFileStatusReader = ReadJobFileStatus
	_ health.JobFileReconciler   = JobFileReconciler
)
