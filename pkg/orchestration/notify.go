package orchestration

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	grovelogging "github.com/grovetools/core/logging"
	notifications "github.com/grovetools/notify"
	notificationsconfig "github.com/grovetools/notify/pkg/config"
	"github.com/sirupsen/logrus"
)

var notifyLogger = grovelogging.NewLogger("flow.notify")

// FireNotificationOnComplete fires notifications when a job reaches a terminal state.
//
// It does two independent things on a genuine terminal transition:
//  1. By default (config-driven), it sends an ntfy push with a rich message
//     (agent name + what happened + plan/worktree/repo), matching the
//     "waiting-on-you" style fired by the hooks repo. This is the completion
//     half of the notify scope; the Claude Stop hook cannot fire it for
//     headless/agent jobs because their end-of-turn resolves to "idle" and the
//     job is marked completed out-of-band by the flow executor.
//  2. If the job has notify_on_complete configured, it also execs the legacy
//     `notify <channel> <msg>` command (a separate channel from the ntfy default).
//
// Both fire asynchronously and never block the caller. The single terminal
// guard below applies to BOTH paths so completion fires exactly once per
// transition (UpdateJobStatus is the single chokepoint, and the daemon path
// guards against terminal status before calling it).
func FireNotificationOnComplete(job *Job, status JobStatus) {
	// Only notify on actual terminal transitions, skip otherwise.
	if status != JobStatusCompleted && status != JobStatusFailed && status != JobStatusAbandoned {
		return
	}

	// 1. Default ntfy push (config-driven).
	fireCompletionNtfy(job, status)

	// 2. Legacy per-job notify_on_complete channel (exec `notify`).
	if job.NotifyOnComplete != "" {
		fireNotifyOnCompleteChannel(job, status)
	}
}

// fireCompletionNtfy sends the default completion ntfy push if ntfy is enabled
// in the merged grove config. Send happens in a goroutine so it never blocks.
func fireCompletionNtfy(job *Job, status JobStatus) {
	cfg := notificationsconfig.Load()
	if cfg == nil || !cfg.Ntfy.Enabled || cfg.Ntfy.Topic == "" {
		return
	}

	title, message, tags := buildCompletionNtfyMessage(job, status)

	go func() {
		if err := notifications.SendNtfy(cfg.Ntfy.URL, cfg.Ntfy.Topic, title, message, "default", tags); err != nil {
			notifyLogger.WithFields(logrus.Fields{
				"job_id":    job.ID,
				"job_title": job.Title,
				"status":    string(status),
			}).
				WithError(err).
				Debug("Failed to send completion ntfy notification")
			return
		}
		notifyLogger.WithFields(logrus.Fields{
			"job_id":    job.ID,
			"job_title": job.Title,
			"status":    string(status),
		}).
			Debug("Completion ntfy notification sent")
	}()
}

// buildCompletionNtfyMessage builds the title, body, and tags for a completion
// ntfy push. It is a pure function (no I/O) so it can be unit-tested without
// touching the network or config. The message mirrors the hooks "waiting-on-you"
// style: agent name in the title, plan/worktree/repo (and duration) in the body.
func buildCompletionNtfyMessage(job *Job, status JobStatus) (title, message string, tags []string) {
	if status == JobStatusCompleted {
		title = fmt.Sprintf("✅ %s — completed", job.Title)
		tags = []string{"white_check_mark"}
	} else {
		// failed / abandoned
		title = fmt.Sprintf("❌ %s — failed", job.Title)
		tags = []string{"rotating_light"}
	}

	var lines []string

	// Plan name is not a field on Job; derive it from the job file's parent
	// directory name (which is the plan directory), matching how flow derives
	// plan names elsewhere (filepath.Base(plan.Directory)).
	planName := ""
	if job.FilePath != "" {
		planName = filepath.Base(filepath.Dir(job.FilePath))
		if planName == "." || planName == string(filepath.Separator) {
			planName = ""
		}
	}
	if planName != "" {
		lines = append(lines, fmt.Sprintf("📂 plan: %s", planName))
	}

	// Worktree: only show when it adds information beyond the plan name (they're
	// often identical for flow plans).
	if job.Worktree != "" && job.Worktree != planName {
		lines = append(lines, fmt.Sprintf("🌿 worktree: %s", job.Worktree))
	}

	if job.Repository != "" {
		lines = append(lines, fmt.Sprintf("📦 repo: %s", job.Repository))
	}

	// Duration, if available. Prefer the recorded field; fall back to
	// start/end times captured during this transition.
	dur := job.Duration
	if dur == 0 && !job.StartTime.IsZero() && !job.EndTime.IsZero() {
		dur = job.EndTime.Sub(job.StartTime)
	}
	if dur > 0 {
		lines = append(lines, fmt.Sprintf("⏱ duration: %s", dur.Round(time.Second)))
	}

	message = strings.Join(lines, "\n")
	if message == "" {
		message = fmt.Sprintf("Job reached status: %s", status)
	}
	return title, message, tags
}

// fireNotifyOnCompleteChannel spawns a detached `notify <channel> <msg>` process
// asynchronously and does not wait for it to complete. This preserves the legacy
// per-job notify_on_complete behavior.
func fireNotifyOnCompleteChannel(job *Job, status JobStatus) {
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
