package orchestration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// createHeadlessTestJob writes a headless_agent job file with the given status
// and returns the loaded Job. The updatedAt time is stamped into frontmatter so
// verifyHeadlessJobStatus's grace-period logic sees a controlled clock.
func createHeadlessTestJob(t *testing.T, dir, jobID, status string, updatedAt time.Time) (*Job, *Plan) {
	t.Helper()
	frontmatter := fmt.Sprintf(`---
id: %s
status: %s
title: Headless Test Job
type: headless_agent
updated_at: %q
---

# Headless Test Job
`, jobID, status, updatedAt.Format(time.RFC3339))

	jobPath := filepath.Join(dir, "headless-test.md")
	if err := os.WriteFile(jobPath, []byte(frontmatter), 0o644); err != nil {
		t.Fatalf("write job file: %v", err)
	}

	job := &Job{
		ID:        jobID,
		Filename:  "headless-test.md",
		FilePath:  jobPath,
		Title:     "Headless Test Job",
		Type:      JobTypeHeadlessAgent,
		Status:    JobStatus(status),
		UpdatedAt: updatedAt,
	}
	plan := &Plan{Directory: dir, Jobs: []*Job{job}}
	return job, plan
}

// writeHooksSession fabricates a grove-hooks session record under the
// (sandboxed) state dir so findAgentSessionInfo's filesystem fallback finds it.
func writeHooksSession(t *testing.T, jobID string, pid int) {
	t.Helper()
	sessionDir := filepath.Join(os.Getenv("GROVE_HOME"), "state", "grove", "hooks", "sessions", "sess-"+jobID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	meta := map[string]any{"session_id": jobID, "provider": "claude", "pid": pid}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(sessionDir, "metadata.json"), data, 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "pid.lock"), []byte(fmt.Sprint(pid)), 0o644); err != nil {
		t.Fatalf("write pid.lock: %v", err)
	}
}

func writeHeadlessSidecar(t *testing.T, plan *Plan, job *Job, exitCode int, mtime time.Time) {
	t.Helper()
	statusPath := headlessStatusPath(plan, job)
	if err := os.MkdirAll(filepath.Dir(statusPath), 0o755); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	data, _ := json.Marshal(map[string]any{
		"exit_code": exitCode,
		"timestamp": mtime.Format(time.RFC3339),
		"job_id":    job.ID,
	})
	if err := os.WriteFile(statusPath, data, 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	if err := os.Chtimes(statusPath, mtime, mtime); err != nil {
		t.Fatalf("chtimes sidecar: %v", err)
	}
}

func TestVerifyHeadless_LiveSessionKeepsRunning(t *testing.T) {
	t.Setenv("GROVE_HOME", t.TempDir())
	dir := t.TempDir()
	// Well past the startup grace: only the live-process evidence keeps it running.
	job, plan := createHeadlessTestJob(t, dir, "vh-live-4f9a2c81", "running", time.Now().Add(-10*time.Minute))
	// Our own test process is alive by definition.
	writeHooksSession(t, job.ID, os.Getpid())

	VerifyRunningJobStatus(plan)

	if job.Status != JobStatusRunning {
		t.Errorf("expected running, got %s", job.Status)
	}
}

func TestVerifyHeadless_DeadNoEvidenceMarksInterrupted(t *testing.T) {
	t.Setenv("GROVE_HOME", t.TempDir())
	dir := t.TempDir()
	job, plan := createHeadlessTestJob(t, dir, "vh-dead-7b3e5d92", "running", time.Now().Add(-10*time.Minute))

	VerifyRunningJobStatus(plan)

	if job.Status != JobStatusInterrupted {
		t.Errorf("expected interrupted, got %s", job.Status)
	}
	// Display-only: the frontmatter must be untouched.
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatalf("read job file: %v", err)
	}
	if !strings.Contains(string(content), "status: running") {
		t.Errorf("expected disk status running to be untouched, got:\n%s", content)
	}
}

func TestVerifyHeadless_StartupGraceKeepsRunning(t *testing.T) {
	t.Setenv("GROVE_HOME", t.TempDir())
	dir := t.TempDir()
	// No session, no process — but the job just started.
	job, plan := createHeadlessTestJob(t, dir, "vh-fresh-1c8d6e43", "running", time.Now())

	VerifyRunningJobStatus(plan)

	if job.Status != JobStatusRunning {
		t.Errorf("expected running within startup grace, got %s", job.Status)
	}
}

func TestVerifyHeadless_FreshSidecarDefersToExitWatcher(t *testing.T) {
	t.Setenv("GROVE_HOME", t.TempDir())
	dir := t.TempDir()
	job, plan := createHeadlessTestJob(t, dir, "vh-sidecar-fresh-9e2b7a54", "running", time.Now().Add(-10*time.Minute))
	writeHeadlessSidecar(t, plan, job, 0, time.Now())

	VerifyRunningJobStatus(plan)

	// Inside the watcher window nothing is touched: the launcher's own
	// finalize is presumed in flight.
	if job.Status != JobStatusRunning {
		t.Errorf("expected running inside watcher window, got %s", job.Status)
	}
	content, _ := os.ReadFile(job.FilePath)
	if !strings.Contains(string(content), "status: running") {
		t.Errorf("expected disk status running, got:\n%s", content)
	}
}

func TestVerifyHeadless_StaleSidecarNonZeroExitFinalizesFailed(t *testing.T) {
	t.Setenv("GROVE_HOME", t.TempDir())
	dir := t.TempDir()
	job, plan := createHeadlessTestJob(t, dir, "vh-sidecar-stale-6a1f3c28", "running", time.Now().Add(-10*time.Minute))
	writeHeadlessSidecar(t, plan, job, 3, time.Now().Add(-time.Minute))

	VerifyRunningJobStatus(plan)

	if job.Status != JobStatusFailed {
		t.Errorf("expected failed after finalize, got %s", job.Status)
	}
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatalf("read job file: %v", err)
	}
	if !strings.Contains(string(content), "status: failed") {
		t.Errorf("expected disk status failed, got:\n%s", content)
	}
	if !strings.Contains(string(content), "agent exited with code: 3") {
		t.Errorf("expected last_error with exit code, got:\n%s", content)
	}
}

func TestVerifyHeadless_IdleWithLiveProcessLeftAlone(t *testing.T) {
	t.Setenv("GROVE_HOME", t.TempDir())
	dir := t.TempDir()
	job, plan := createHeadlessTestJob(t, dir, "vh-idle-live-2d7c9b16", "idle", time.Now().Add(-10*time.Minute))
	writeHooksSession(t, job.ID, os.Getpid())

	VerifyRunningJobStatus(plan)

	if job.Status != JobStatusIdle {
		t.Errorf("expected idle to be left alone while process lives, got %s", job.Status)
	}
}

func TestVerifyHeadless_TerminalStatusesUntouched(t *testing.T) {
	t.Setenv("GROVE_HOME", t.TempDir())
	dir := t.TempDir()
	job, plan := createHeadlessTestJob(t, dir, "vh-done-8c4a1e37", "completed", time.Now().Add(-10*time.Minute))

	VerifyRunningJobStatus(plan)

	if job.Status != JobStatusCompleted {
		t.Errorf("expected completed untouched, got %s", job.Status)
	}
}
