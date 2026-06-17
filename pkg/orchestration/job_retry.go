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

	case JobStatusRunning:
		if !force {
			return refuseRunningJobWithoutForce(job)
		}
		return retryRunningJobWithForce(job, plan, autoRun)

	case JobStatusCompleted:
		return fmt.Errorf("job already completed: %s. Use 'flow plan run' to re-run or create a fresh job.", job.Filename)

	case JobStatusPendingUser:
		return fmt.Errorf("chat jobs re-run via 'flow plan run'. Nothing to reset.")

	default:
		// For all other states (pending, todo, blocked, etc.)
		return fmt.Errorf("job is already in %s state. Nothing to reset.", job.Status)
	}
}

// retryFailedJob resets a failed job back to pending.
func retryFailedJob(job *Job, plan *Plan, autoRun bool) error {
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
	fmt.Printf("Status: %s → %s\n", JobStatusFailed, JobStatusPending)

	if autoRun {
		// Submit to daemon
		daemonClient := daemon.NewWithAutoStart()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		_, err := daemonClient.SubmitJob(ctx, models.JobSubmitRequest{
			PlanDir: plan.Directory,
			JobFile: job.Filename,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not submit job: %v\n", err)
			fmt.Printf("Run manually with: flow plan run --dir %s %s\n", plan.Directory, job.Filename)
		} else {
			fmt.Printf("Job submitted to daemon.\n")
		}
		daemonClient.Close()
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

// retryRunningJobWithForce resets a running job with --force.
func retryRunningJobWithForce(job *Job, plan *Plan, autoRun bool) error {
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
		// Submit to daemon
		daemonClient := daemon.NewWithAutoStart()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		_, err := daemonClient.SubmitJob(ctx, models.JobSubmitRequest{
			PlanDir: plan.Directory,
			JobFile: job.Filename,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not submit job: %v\n", err)
			fmt.Printf("Run manually with: flow plan run --dir %s %s\n", plan.Directory, job.Filename)
		} else {
			fmt.Printf("Job submitted to daemon.\n")
		}
		daemonClient.Close()
	} else {
		fmt.Printf("Run with: flow plan run --dir %s %s\n", plan.Directory, job.Filename)
	}

	return nil
}

// checkAgentLiveness checks if an agent process is still alive.
// Returns (alive, pid). Both values should be interpreted together:
// - (true, pid) means the process is alive with the given PID
// - (false, 0) means no live process was found
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

	return false, 0
}
