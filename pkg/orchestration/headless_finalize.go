package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/grovetools/core/pkg/daemon"
)

// headlessStatusFile is the JSON schema of the .status file written by
// waitAndWriteStatus when a headless agent process exits. It MUST match the
// daemon's jobrunner.statusFileContent (daemon/internal/daemon/jobrunner/adoption.go)
// byte-for-byte: flow cannot import the daemon repo (dependency direction), so
// the reader is duplicated here alongside the writer and the two structs are
// kept in lockstep.
type headlessStatusFile struct {
	ExitCode  int    `json:"exit_code"`
	Timestamp string `json:"timestamp"`
	JobID     string `json:"job_id"`
}

// headlessStatusPath returns the path of a headless job's .status file. Both
// the writer (waitAndWriteStatus) and this reader go through this helper so the
// two never drift, and it matches the daemon adoption reader's getStatusFilePath
// (job.PlanDir/.artifacts/<job-id>/.status).
func headlessStatusPath(plan *Plan, job *Job) string {
	return filepath.Join(plan.Directory, ".artifacts", job.ID, ".status")
}

// readHeadlessStatus reads and parses a headless job's .status file.
func readHeadlessStatus(path string) (*headlessStatusFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var content headlessStatusFile
	if err := json.Unmarshal(data, &content); err != nil {
		return nil, err
	}
	return &content, nil
}

// isHeadlessTerminalStatus reports whether a status is a resting terminal state
// for a headless job. `idle` is deliberately NOT terminal here: it is the exact
// state the Stop hook strands headless jobs at, and reconciling it to
// completed/failed is the whole point of the finalizer.
func isHeadlessTerminalStatus(status JobStatus) bool {
	switch status {
	case JobStatusCompleted, JobStatusFailed, JobStatusAbandoned:
		return true
	default:
		return false
	}
}

// FinalizeHeadlessJob drives a detached headless agent job to a terminal
// frontmatter status (completed/failed) once its process has exited. It is the
// single reconciler invoked by whichever watcher observes the exit: the
// in-process waitAndWriteStatus goroutine, or — for jobs whose launcher died —
// the daemon's adoption sweep at the next boot. It is idempotent and safe to
// call more than once.
//
// A3 (binding): the in-memory *Job.Status is UNTRUSTWORTHY here. The runtime
// stamps `completed` at detach and the grove-hooks Stop hook then rewrites the
// frontmatter to `idle`; the in-memory copy still reads `completed`, so a naive
// guard on job.Status would wrongly treat the job as done and skip the real
// status write. This function therefore re-loads the frontmatter from disk with
// LoadJob and applies its idempotence guard against the DISK status, and it
// reconciles the passed job's Status to disk so a downstream CompleteJob sees
// reality rather than the stale stamp.
//
// Branch (per the .status file written by waitAndWriteStatus):
//   - exit_code == 0 → CompleteJob (lock removal, transcript, archive,
//     EndSession("completed")).
//   - exit_code != 0 → superset of the setup-failure defer: last_error +
//     failed status + EndSession("failed") + lock removal.
//   - .status missing → failed, noting the launcher likely died before
//     recording an exit status.
//
// The error strings match the daemon adoption reader's vocabulary verbatim so
// JobInfo and frontmatter never diverge textually.
func FinalizeHeadlessJob(job *Job, plan *Plan) error {
	ctx := context.Background()

	// A3: re-read frontmatter from disk; the in-memory status is the strander.
	if diskJob, err := LoadJob(job.FilePath); err == nil {
		if isHeadlessTerminalStatus(diskJob.Status) {
			// Already terminal on disk — nothing to do. This is the idempotence
			// guard, and it is the reason we read disk rather than job.Status.
			return nil
		}
		// Reconcile the caller's in-memory copy to disk truth so CompleteJob's
		// `alreadyCompleted := job.Status == JobStatusCompleted` guard sees the
		// real (idle/running) state and performs the status write + EndSession.
		job.Status = diskJob.Status
	} else {
		ulog.Warn("[HEADLESS] Finalize: failed to reload job from disk; proceeding with in-memory status").
			Field("job_id", job.ID).
			Field("path", job.FilePath).
			Err(err).
			Log(ctx)
	}

	statusPath := headlessStatusPath(plan, job)
	status, statusErr := readHeadlessStatus(statusPath)

	if statusErr != nil {
		// No .status file: the agent vanished without recording an exit. This
		// is the last-resort backstop (the launcher's cmd.Wait writes .status on
		// exit, and the Stop hook writes a fallback under --local), so a missing
		// file means the launcher process itself died first.
		lastErr := "agent process exited without status file (launcher likely died before recording an exit status)"
		return finalizeHeadlessFailure(ctx, job, lastErr)
	}

	if status.ExitCode == 0 {
		// Clean exit: reuse the unified completion handler.
		return CompleteJob(job, plan, true)
	}

	// Non-zero exit: superset of the setup-failure defer — it also writes
	// last_error, which the defer does not. The message matches daemon
	// adoption's "agent exited with code: N" verbatim.
	lastErr := "agent exited with code: " + strconv.Itoa(status.ExitCode)
	return finalizeHeadlessFailure(ctx, job, lastErr)
}

// finalizeHeadlessFailure stamps a headless job failed via the state persister
// (last_error + failed status, which adds completed_at/duration and fires the
// completion notification), ends the daemon session, and removes the lock file.
// It mirrors — and supersets — the setup-failure defer in
// HeadlessAgentExecutor.Execute (which stamps failed + EndSession but never
// writes last_error).
func finalizeHeadlessFailure(ctx context.Context, job *Job, lastError string) error {
	persister := NewStatePersister()

	job.Metadata.LastError = lastError
	if err := persister.UpdateJobMetadata(job, job.Metadata); err != nil {
		ulog.Warn("[HEADLESS] Finalize: failed to write last_error metadata").
			Field("job_id", job.ID).
			Err(err).
			Log(ctx)
	}

	if err := persister.UpdateJobStatus(job, JobStatusFailed); err != nil {
		return fmt.Errorf("finalize headless failed status: %w", err)
	}

	// End the daemon session so the monitor stops showing a live session for a
	// process that has exited. Use connect-only daemon.New() (never autostart) —
	// mirroring the setup-failure defer in HeadlessAgentExecutor.Execute: if no
	// daemon is running this is a best-effort no-op, never a reason to spawn one.
	daemonClient := daemon.New()
	endCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := daemonClient.EndSession(endCtx, job.ID, string(JobStatusFailed)); err != nil {
		ulog.Debug("[HEADLESS] Finalize: failed to end daemon session").
			Field("job_id", job.ID).
			Err(err).
			Log(ctx)
	}
	daemonClient.Close()

	// Remove the executor's lock file so a retry can re-acquire it.
	if err := RemoveLockFile(job.FilePath); err != nil {
		ulog.Debug("[HEADLESS] Finalize: failed to remove lock file").
			Field("job_id", job.ID).
			Err(err).
			Log(ctx)
	}

	return nil
}
