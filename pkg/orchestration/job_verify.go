package orchestration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/process"
	"github.com/sirupsen/logrus"
)

// JobStatusInterrupted is a display-only status used to mark jobs whose process
// has died unexpectedly. It is not persisted to disk.
const JobStatusInterrupted = JobStatus("interrupted")

// VerifyRunningJobStatus checks the PID liveness for jobs marked as running.
// If a job's process is dead, its status is updated in-memory to "interrupted".
// Headless agent jobs are handled separately: they detach from their launcher,
// so liveness is judged from the agent process and its exit evidence rather
// than the launcher's lock file (see verifyHeadlessJobStatus).
func VerifyRunningJobStatus(plan *Plan) {
	log := grovelogging.NewLogger("flow.status.verify")

	for _, job := range plan.Jobs {
		if job.Type == JobTypeHeadlessAgent {
			verifyHeadlessJobStatus(plan, job)
			continue
		}

		if job.Status != JobStatusRunning {
			continue
		}

		log.WithFields(logrus.Fields{
			"job_id":     job.ID,
			"job_title":  job.Title,
			"job_type":   job.Type,
			"updated_at": job.UpdatedAt,
		}).Debug("Verifying running job status")

		// Special handling for interactive agent jobs
		if job.Type == JobTypeInteractiveAgent || job.Type == JobTypeAgent {
			log.WithField("job_id", job.ID).Debug("Job is interactive/agent type, looking up session info")

			pid, sessionDir, err := findAgentSessionInfo(job.ID)
			if err != nil {
				// Session bookkeeping can be lost (daemon restart, reaped
				// registry entry) while the agent itself is fine. A live
				// process whose command line carries the job ID is stronger
				// evidence than a missing session record, so check it before
				// concluding anything from the lookup failure.
				if actualPID := findProcessByJobID(job.ID); actualPID > 0 && process.IsProcessAlive(actualPID) {
					log.WithFields(logrus.Fields{
						"job_id":     job.ID,
						"actual_pid": actualPID,
					}).Debug("Session lookup failed but process found via pgrep, status remains running")
					continue
				}

				// Give agent jobs a grace period to register with grove-hooks
				// Agents don't register until their first hook call, which can take 5-30 seconds
				gracePeriod := 30 * time.Second
				// If UpdatedAt is zero (not set in frontmatter), fall back to file mod time
				// or treat as just-started to avoid immediately marking as interrupted
				updatedAt := job.UpdatedAt
				if updatedAt.IsZero() {
					if job.FilePath != "" {
						if fi, statErr := os.Stat(job.FilePath); statErr == nil {
							updatedAt = fi.ModTime()
						}
					}
					// If still zero (no file path or stat failed), assume just started
					if updatedAt.IsZero() {
						updatedAt = time.Now()
					}
				}
				timeSinceUpdate := time.Since(updatedAt)

				log.WithFields(logrus.Fields{
					"job_id":              job.ID,
					"error":               err.Error(),
					"time_since_update":   timeSinceUpdate.String(),
					"grace_period":        gracePeriod.String(),
					"within_grace_period": timeSinceUpdate < gracePeriod,
					"updated_at_zero":     job.UpdatedAt.IsZero(),
				}).Debug("Session lookup failed for job")

				if timeSinceUpdate < gracePeriod {
					// Job just started, give it time to register
					log.WithFields(logrus.Fields{
						"job_id":         job.ID,
						"time_remaining": (gracePeriod - timeSinceUpdate).String(),
					}).Debug("Job within grace period, skipping status check")
					continue
				}
				// Grace period expired, mark as interrupted
				log.WithFields(logrus.Fields{
					"job_id":            job.ID,
					"reason":            "session_not_found_grace_expired",
					"time_since_update": timeSinceUpdate.String(),
				}).Info("Marking job as interrupted - session not found after grace period")
				job.Status = JobStatusInterrupted
				continue
			}

			log.WithFields(logrus.Fields{
				"job_id":      job.ID,
				"pid":         pid,
				"session_dir": sessionDir,
			}).Debug("Found session info for job")

			// Read provider from session metadata to handle opencode specially
			provider := ""
			sessionStatus := ""
			metadataPath := filepath.Join(sessionDir, "metadata.json")
			if metadataBytes, err := os.ReadFile(metadataPath); err == nil {
				var metadata struct {
					Provider string `json:"provider"`
					Status   string `json:"status"`
				}
				if err := json.Unmarshal(metadataBytes, &metadata); err == nil {
					provider = metadata.Provider
					sessionStatus = metadata.Status
				}
			}

			log.WithFields(logrus.Fields{
				"job_id":         job.ID,
				"pid":            pid,
				"provider":       provider,
				"session_status": sessionStatus,
			}).Debug("Session metadata read")

			// For opencode, don't use PID liveness - opencode exits after each turn
			// Instead, check session status
			if provider == "opencode" {
				// Opencode exits after each turn, so PID being dead is normal
				// Only mark as interrupted if session status explicitly indicates failure
				if sessionStatus == "failed" || sessionStatus == "error" {
					log.WithFields(logrus.Fields{
						"job_id":         job.ID,
						"session_status": sessionStatus,
						"reason":         "session_status_failed",
					}).Info("Marking opencode job as interrupted - session failed")
					job.Status = JobStatusInterrupted
				} else {
					// Session is idle or running - keep job as running
					log.WithFields(logrus.Fields{
						"job_id":         job.ID,
						"session_status": sessionStatus,
						"provider":       provider,
					}).Debug("Opencode job remains running - session is idle/active")
				}
			} else if pid == 0 {
				// PID=0 means session intent registered but not yet confirmed
				// Keep job as running - it's still starting up
				log.WithFields(logrus.Fields{
					"job_id": job.ID,
					"reason": "pending_confirmation",
				}).Debug("Job session pending confirmation, status remains running")
			} else if !process.IsProcessAlive(pid) {
				// Stored PID is dead, but Claude may have forked/respawned
				// Try to find a process with this job ID in its command line
				actualPID := findProcessByJobID(job.ID)
				if actualPID > 0 && process.IsProcessAlive(actualPID) {
					log.WithFields(logrus.Fields{
						"job_id":     job.ID,
						"stored_pid": pid,
						"actual_pid": actualPID,
					}).Debug("Found alive process via pgrep, status remains running")
				} else {
					log.WithFields(logrus.Fields{
						"job_id":     job.ID,
						"stored_pid": pid,
						"actual_pid": actualPID,
						"reason":     "process_not_alive",
					}).Info("Marking job as interrupted - process is dead")
					job.Status = JobStatusInterrupted
				}
			} else {
				log.WithFields(logrus.Fields{
					"job_id": job.ID,
					"pid":    pid,
				}).Debug("Job process is alive, status remains running")
			}
		} else {
			// Original logic for other job types
			pid, err := ReadLockFile(job.FilePath)
			if err != nil || !process.IsProcessAlive(pid) {
				// Lock file missing or process is dead, mark as interrupted.
				log.WithFields(logrus.Fields{
					"job_id":        job.ID,
					"pid":           pid,
					"lock_file_err": err != nil,
					"reason":        "lock_file_or_process_dead",
				}).Debug("Marking non-agent job as interrupted")
				job.Status = JobStatusInterrupted
			}
		}
	}
}

// headlessExitFinalizeGrace is how long after the .status sidecar appears the
// status path defers to the launcher's in-process exit watcher
// (waitAndWriteStatus → FinalizeHeadlessJob) before reconciling the job
// itself. Within the window the watcher is presumed live and about to
// finalize; past it the launcher has almost certainly died (the CLI-spawned
// case) and the sidecar would otherwise sit unreconciled until the daemon's
// next adoption sweep at boot.
const headlessExitFinalizeGrace = 15 * time.Second

// headlessStartupGrace mirrors the interactive-agent grace period: a headless
// agent registers no liveness evidence until its process is up and its first
// hook fires, so a fresh job with no session record is presumed starting, not
// dead.
const headlessStartupGrace = 30 * time.Second

// verifyHeadlessJobStatus reconciles a headless job's status against durable
// evidence. Headless agents DETACH from their launcher: the executor's lock
// file is removed the moment Execute returns (at detach, agent still
// running), so the lock-file liveness check used for other job types reads
// every live headless job as interrupted — that was the "job reported
// interrupted while its agent was demonstrably alive" bug. And the
// grove-hooks Stop hook parks finished headless jobs at `idle`; when the
// launcher's exit watcher died with its CLI process, nothing reconciled the
// job to completed/failed until the daemon's next boot — the "settled on idle
// after committing, had to flow complete it by hand" bug.
//
// Evidence order:
//  1. `.status` sidecar present → the agent process has exited. Give the
//     in-process exit watcher a short window, then invoke the idempotent
//     FinalizeHeadlessJob (the designated reconciler for whichever watcher
//     observes the exit) and reflect the disk status.
//  2. No sidecar → the job is live iff its agent process is: daemon/hooks
//     session PID first, pgrep by job ID as the fork/bookkeeping-loss
//     fallback.
//  3. No evidence either way → startup grace, then display `interrupted`.
//
// Jobs at `idle` with a live process are left alone (end-of-turn shutdown in
// progress); all other statuses are none of our business.
func verifyHeadlessJobStatus(plan *Plan, job *Job) {
	if job.Status != JobStatusRunning && job.Status != JobStatusIdle {
		return
	}
	log := grovelogging.NewLogger("flow.status.verify")

	statusPath := headlessStatusPath(plan, job)
	if fi, statErr := os.Stat(statusPath); statErr == nil {
		if time.Since(fi.ModTime()) < headlessExitFinalizeGrace {
			// The exit watcher's window: it writes the sidecar first, then
			// finalizes. Don't race it from a status read.
			return
		}
		// The launcher never finalized (its process died with the CLI, or the
		// finalize failed). FinalizeHeadlessJob is idempotent and guards on
		// the DISK status, so a concurrent finalize elsewhere is safe.
		if err := FinalizeHeadlessJob(job, plan); err != nil {
			log.WithFields(logrus.Fields{
				"job_id": job.ID,
				"error":  err.Error(),
			}).Warn("Failed to finalize exited headless job from status sweep")
			return
		}
		if diskJob, err := LoadJob(job.FilePath); err == nil {
			job.Status = diskJob.Status
		}
		log.WithField("job_id", job.ID).Info("Reconciled exited headless job from status sweep")
		return
	}

	// No exit evidence — judge by the agent process itself.
	if pid, _, err := findAgentSessionInfo(job.ID); err == nil {
		// pid == 0 means the session intent is registered but not yet
		// confirmed: the agent is still starting up.
		if pid == 0 || process.IsProcessAlive(pid) {
			return
		}
	}
	if actualPID := findProcessByJobID(job.ID); actualPID > 0 && process.IsProcessAlive(actualPID) {
		log.WithFields(logrus.Fields{
			"job_id":     job.ID,
			"actual_pid": actualPID,
		}).Debug("Headless session lookup failed but process found via pgrep, status remains running")
		return
	}

	// No process and no exit record. Within the startup window that just
	// means the agent hasn't registered yet.
	updatedAt := job.UpdatedAt
	if updatedAt.IsZero() && job.FilePath != "" {
		if fi, err := os.Stat(job.FilePath); err == nil {
			updatedAt = fi.ModTime()
		}
	}
	if updatedAt.IsZero() || time.Since(updatedAt) < headlessStartupGrace {
		return
	}

	// `idle` means the Stop hook fired: the agent finished its turn, and a
	// headless agent has exactly one. If its transcript verifies, the work is
	// done — the launcher just died before recording the exit, which used to
	// strand the job at idle until someone ran `flow complete` by hand.
	if job.Status == JobStatusIdle && successfulExecutionEvidence(job, plan) == nil {
		if err := CompleteJob(job, plan, true); err != nil {
			log.WithFields(logrus.Fields{
				"job_id": job.ID,
				"error":  err.Error(),
			}).Warn("Failed to complete evidence-backed idle headless job from status sweep")
			return
		}
		log.WithField("job_id", job.ID).Info("Completed idle headless job with verified transcript from status sweep")
		return
	}

	log.WithFields(logrus.Fields{
		"job_id": job.ID,
		"status": job.Status,
		"reason": "no_process_no_exit_record",
	}).Info("Marking headless job as interrupted - no live agent process and no exit record")
	job.Status = JobStatusInterrupted
}
