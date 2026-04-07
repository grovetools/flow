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
func VerifyRunningJobStatus(plan *Plan) {
	log := grovelogging.NewLogger("flow.status.verify")

	for _, job := range plan.Jobs {
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
