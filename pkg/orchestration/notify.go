package orchestration

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	grovelogging "github.com/grovetools/core/logging"
	"github.com/sirupsen/logrus"
)

var notifyLogger = grovelogging.NewLogger("flow.notify")

// FireNotificationOnComplete fires a notification when a job reaches a terminal state.
// It spawns a detached 'notify' process asynchronously and does not wait for it to complete.
// This ensures the notification fires exactly once, regardless of CLI or daemon execution path.
func FireNotificationOnComplete(job *Job, status JobStatus) {
	// Only fire if notify_on_complete is configured
	if job.NotifyOnComplete == "" {
		return
	}

	// Only notify on actual terminal transitions, skip for jobs that were already terminal
	if status != JobStatusCompleted && status != JobStatusFailed && status != JobStatusAbandoned {
		return
	}

	// Spawn notification in a goroutine to avoid blocking
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Build the notify command
		notifyCmd := exec.CommandContext(ctx, "notify", job.NotifyOnComplete,
			fmt.Sprintf("Job %s completed with status: %s", job.Title, status))

		// Run detached (background) - don't wait for completion
		err := notifyCmd.Start()
		if err != nil {
			// Log at debug level since 'notify' command may not exist in test environments
			notifyLogger.WithFields(logrus.Fields{
				"job_id":    job.ID,
				"job_title": job.Title,
				"channel":   job.NotifyOnComplete,
				"status":    string(status),
			}).
				WithError(err).
				Debug("Could not spawn notify process (command may not be available)")
			return
		}

		// Log success (non-blocking spawn)
		notifyLogger.WithFields(logrus.Fields{
			"job_id":    job.ID,
			"job_title": job.Title,
			"channel":   job.NotifyOnComplete,
			"status":    string(status),
		}).
			Debug("Notification spawned for job completion")

		// Optional: wait briefly for process to exit (but don't block the main flow)
		// This is in a separate goroutine so it doesn't affect the main execution path
		go func() {
			// Wait with a short timeout; if it exceeds 5s, log and continue
			waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer waitCancel()

			// Create a channel to get the wait result
			done := make(chan error, 1)
			go func() {
				done <- notifyCmd.Wait()
			}()

			select {
			case notifyErr := <-done:
				if notifyErr != nil {
					notifyLogger.WithFields(logrus.Fields{
						"job_id": job.ID,
					}).
						WithError(notifyErr).
						Debug("Notify process exited with error")
				}
			case <-waitCtx.Done():
				notifyLogger.WithFields(logrus.Fields{
					"job_id": job.ID,
				}).
					Debug("Notify process did not exit within timeout")
			}
		}()
	}()
}

// FireNotification is a generic function to fire a notification command.
// It's a lower-level interface than FireNotificationOnComplete.
func FireNotification(channel string, message string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "notify", channel, message)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting notify command: %w", err)
	}

	// Don't wait for completion - let it run in background
	return nil
}
