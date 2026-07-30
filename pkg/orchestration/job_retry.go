package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fatih/color"
	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/models"
	"github.com/grovetools/core/pkg/paths"
	"github.com/sirupsen/logrus"
)

// RetryJob resets a failed or orphaned job back to pending status.
// It can be called from both the CLI and TUI.
// force allows resetting running jobs (with liveness hints).
// autoRun immediately submits the job after resetting if true.
func RetryJob(job *Job, plan *Plan, force bool, autoRun bool) error {
	logger := grovelogging.NewLogger("flow.retry")
	logger.WithFields(logrus.Fields{
		"job_id":     job.ID,
		"job_title":  job.Title,
		"job_status": job.Status,
		"force":      force,
		"auto_run":   autoRun,
	}).Info("RetryJob called")

	// Check current status and handle accordingly
	switch job.Status {
	case JobStatusFailed:
		return retryFailedJob(job, plan, autoRun)

	case JobStatusOrphaned, JobStatusInterrupted:
		// The session-health engine reconciled this job: its process is
		// gone but nothing recorded how it ended. Resetting is exactly
		// what retry is for, so these need no --force — but they get the
		// same liveness veto as a failed job, because "orphaned" means
		// we lost track of the agent, not that we watched it die.
		return retryLostJob(job, plan, autoRun)

	case JobStatusRunning:
		if !force {
			return refuseRunningJobWithoutForce(job)
		}
		return retryRunningJobWithForce(job, plan, autoRun)

	case JobStatusCompleted:
		if isAgentJobType(job.Type) && !JobHasExecutionEvidence(job, plan) {
			return resetJobToPending(job, plan, JobStatusCompleted, autoRun)
		}
		return fmt.Errorf("job already completed: %s. Use 'flow plan run' to re-run or create a fresh job.", job.Filename)

	case JobStatusPendingUser:
		return fmt.Errorf("chat jobs re-run via 'flow plan run'. Nothing to reset.")

	default:
		// For all other states (pending, todo, blocked, etc.)
		return fmt.Errorf("job is already in %s state. Nothing to reset.", job.Status)
	}
}

// retryFailedJob resets a failed job back to pending. For agent jobs the
// frontmatter status is not trusted on its own: terminal states have been
// mislabeled while the agent was still running (daemon session bookkeeping
// lost mid-flight), and re-running such a job would spawn a second agent onto
// the same files. A live agent process vetoes the retry regardless of what
// the markdown says.
func retryFailedJob(job *Job, plan *Plan, autoRun bool) error {
	if isAgentJobType(job.Type) {
		if alive, pid := checkAgentLiveness(job.ID); alive {
			return fmt.Errorf("refusing to retry %s: job is marked failed but its agent process is still alive (pid %d). The status is likely stale — wait for the agent to exit, or kill it with 'flow agent kill' first.", job.Filename, pid)
		}
	}
	return resetJobToPending(job, plan, JobStatusFailed, autoRun)
}

// retryLostJob resets a reconciled job (orphaned/interrupted) back to pending.
// Same shape as retryFailedJob, and for the same reason: the frontmatter says
// the process is gone, but the reconciler only ever had negative evidence. If
// the agent is in fact alive, re-running would put a second one on the same
// files.
func retryLostJob(job *Job, plan *Plan, autoRun bool) error {
	from := job.Status
	if isAgentJobType(job.Type) {
		if alive, pid := checkAgentLiveness(job.ID); alive {
			return fmt.Errorf("refusing to retry %s: job is marked %s but its agent process is still alive (pid %d). The status is likely stale — wait for the agent to exit, or kill it with 'flow agent kill' first.", job.Filename, from, pid)
		}
	}
	return resetJobToPending(job, plan, from, autoRun)
}

// resetJobToPending is shared by failed jobs and completed agent jobs that have
// no durable execution evidence. The latter case recovers zero-turn provider
// failures that older Flow versions incorrectly stamped completed.
func resetJobToPending(job *Job, plan *Plan, from JobStatus, autoRun bool) error {
	// Create updates map to clear error fields
	updates := map[string]interface{}{
		"status":       string(JobStatusPending),
		"last_error":   nil, // Deletes the field
		"completed_at": nil, // Deletes the field
		"duration":     nil, // Deletes the field
		"updated_at":   time.Now().Format(time.RFC3339),
	}

	// Use StatePersister to update the job file
	persister := NewStatePersister()
	newContent, err := persister.updateFrontmatter([]byte{}, updates)
	if err == nil {
		// We need to actually read the file first
		content, err := os.ReadFile(job.FilePath)
		if err != nil {
			return fmt.Errorf("read job file: %w", err)
		}
		newContent, err = persister.updateFrontmatter(content, updates)
	}
	if err != nil {
		return fmt.Errorf("update frontmatter: %w", err)
	}

	// Write atomically via StatePersister
	if err := persister.writeAtomic(job.FilePath, newContent); err != nil {
		return fmt.Errorf("write job file: %w", err)
	}

	// Update in-memory job state
	job.Status = JobStatusPending
	job.EndTime = time.Time{}

	// Print success message
	fmt.Printf("%s Job reset to pending: %s\n", color.GreenString("*"), job.Filename)
	fmt.Printf("Status: %s → %s\n", from, JobStatusPending)

	if autoRun {
		submitRetriedJob(job, plan)
	} else {
		fmt.Printf("Run with: flow plan run --dir %s %s\n", plan.Directory, job.Filename)
	}

	return nil
}

// refuseRunningJobWithoutForce refuses to reset a running job without --force,
// but provides a liveness hint first.
func refuseRunningJobWithoutForce(job *Job) error {
	// Check if the agent process is alive (best-effort)
	alive, pid := checkAgentLiveness(job.ID)

	if alive {
		return fmt.Errorf("cannot reset running job: %s (agent appears alive, pid %d). Use --force to override.", job.Filename, pid)
	}

	return fmt.Errorf("cannot reset running job: %s (no live agent process found). Use --force to override.", job.Filename)
}

// retryRunningJobWithForce resets a running job with --force. --force exists
// to clear STALE running state (a launcher that died and left the frontmatter
// behind) — it is not a license to double-spawn onto a live agent's files, so
// a demonstrably alive agent process still refuses the reset. Kill the agent
// first if that is really what you want.
func retryRunningJobWithForce(job *Job, plan *Plan, autoRun bool) error {
	if alive, pid := checkAgentLiveness(job.ID); alive {
		return fmt.Errorf("refusing to force-retry %s: its agent process is alive (pid %d). Retrying would spawn a second agent onto the same files. Kill it with 'flow agent kill' first if the agent is truly stuck.", job.Filename, pid)
	}

	// Create updates map to reset to pending
	updates := map[string]interface{}{
		"status":     string(JobStatusPending),
		"updated_at": time.Now().Format(time.RFC3339),
	}

	// Use StatePersister to update the job file
	persister := NewStatePersister()
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("read job file: %w", err)
	}

	newContent, err := persister.updateFrontmatter(content, updates)
	if err != nil {
		return fmt.Errorf("update frontmatter: %w", err)
	}

	// Write atomically
	if err := persister.writeAtomic(job.FilePath, newContent); err != nil {
		return fmt.Errorf("write job file: %w", err)
	}

	// Update in-memory job state
	job.Status = JobStatusPending

	// Print success message
	fmt.Printf("%s Job reset to pending (forced): %s\n", color.GreenString("*"), job.Filename)
	fmt.Printf("Status: %s → %s\n", JobStatusRunning, JobStatusPending)
	fmt.Println("Warning: agent process may still be running. Monitor with 'flow agent list'.")

	if autoRun {
		submitRetriedJob(job, plan)
	} else {
		fmt.Printf("Run with: flow plan run --dir %s %s\n", plan.Directory, job.Filename)
	}

	return nil
}

// retrySubmitRequest builds the daemon submission for a retried job. It is split
// out from submitRetriedJob so the request's contents — above all its routing —
// are unit-testable without a live daemon.
//
// A retry is a full submission, not an amendment of the previous one: the daemon
// builds a fresh JobInfo from this request, so anything it omits is simply gone.
// Both retry paths used to omit AgentTarget, which meant `flow plan retry --run`
// queued the job with no routing and the executor failed it immediately
// ("agent_target not set: job submitted without routing context"), seconds after
// the CLI had printed "Job submitted to daemon."
func retrySubmitRequest(job *Job, plan *Plan) models.JobSubmitRequest {
	return models.JobSubmitRequest{
		PlanDir:     plan.Directory,
		JobFile:     job.Filename,
		AgentTarget: AgentTargetForSubmission(plan),
	}
}

// submitRetriedJob submits a job that was just reset to pending, shared by the
// failed-job and forced-running-job paths. A submission failure is a warning,
// not an error: the reset itself already succeeded and is the durable part, so
// the user is told how to run the job by hand instead of being told the retry
// failed.
func submitRetriedJob(job *Job, plan *Plan) {
	daemonClient := daemon.NewWithAutoStart()
	defer func() { _ = daemonClient.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := daemonClient.SubmitJob(ctx, retrySubmitRequest(job, plan)); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not submit job: %v\n", err)
		fmt.Printf("Run manually with: flow plan run --dir %s %s\n", plan.Directory, job.Filename)
		return
	}
	fmt.Printf("Job submitted to daemon.\n")
}

// checkAgentLiveness checks if an agent process is still alive.
// Returns (alive, pid). Both values should be interpreted together:
// - (true, pid) means the process is alive with the given PID
// - (false, 0) means no live process was found
// AgentProcessAlive reports whether a live agent process can be found for the
// job, checking the daemon session registry, the runtime pidfile, the
// grove-hooks session record, and finally the process table by job ID.
func AgentProcessAlive(jobID string) (bool, int) {
	return checkAgentLiveness(jobID)
}

func checkAgentLiveness(jobID string) (bool, int) {
	// Try daemon session lookup first (most reliable)
	daemonClient := daemon.New()
	if daemonClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Try to get session status from daemon
		session, err := daemonClient.GetSession(ctx, jobID)
		if err == nil && session != nil {
			// Check if process is still alive
			if process, err := os.FindProcess(session.PID); err == nil {
				// Process exists; try a kill with signal 0 to check liveness
				// (on Unix, signal 0 is a no-op that only checks if process exists)
				if err := process.Signal(nil); err == nil {
					return true, session.PID
				}
			}
		}
		daemonClient.Close()
	}

	// Fallback to pidfile check
	pidFile := filepath.Join(paths.RuntimeDir(), fmt.Sprintf("grove-agent-%s.pid", jobID))
	data, err := os.ReadFile(pidFile)
	if err == nil && len(data) > 0 {
		pidStr := strings.TrimSpace(string(data))
		if pid, err := strconv.Atoi(pidStr); err == nil && pid > 0 {
			// Check if process exists
			if process, err := os.FindProcess(pid); err == nil {
				// Try signal 0 to check liveness
				if err := process.Signal(nil); err == nil {
					return true, pid
				}
			}
		}
	}

	// Fallback to the grove-hooks session record (daemon bookkeeping can be
	// lost while the agent and its hook-registered session survive).
	if pid, _, err := findAgentSessionInfo(jobID); err == nil && pid > 0 {
		if proc, err := os.FindProcess(pid); err == nil && proc.Signal(nil) == nil {
			return true, pid
		}
	}

	// Last resort: a live process carrying the job ID on its command line
	// (Claude's node process can fork away from every recorded PID).
	if pid := findProcessByJobID(jobID); pid > 0 {
		return true, pid
	}

	return false, 0
}
