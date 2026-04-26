package orchestration

import (
	"context"
	"fmt"

	"github.com/grovetools/core/pkg/daemon"
	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/models"
)

var submitLog = grovelogging.NewUnifiedLogger("flow.submit")

// DaemonRuntime delegates job execution to the groved daemon via the daemon.Client API.
// It submits jobs, streams logs back through the context writer, and blocks until completion.
type DaemonRuntime struct {
	client  daemon.Client
	updater StatusUpdater
	logger  Logger
}

// NewDaemonRuntime creates a runtime that delegates to the daemon for job execution.
func NewDaemonRuntime(client daemon.Client, updater StatusUpdater, logger Logger) *DaemonRuntime {
	return &DaemonRuntime{
		client:  client,
		updater: updater,
		logger:  logger,
	}
}

// Client returns the underlying daemon client for direct use by TUI/CLI.
func (r *DaemonRuntime) Client() daemon.Client {
	return r.client
}

// ExecuteJob submits a job to the daemon, streams logs to the context writer,
// and blocks until the job reaches a terminal state.
func (r *DaemonRuntime) ExecuteJob(ctx context.Context, job *Job, plan *Plan) error {
	// Update status to running locally
	if r.updater != nil {
		if err := r.updater.UpdateJobStatus(job, JobStatusRunning); err != nil {
			r.logger.Error("Failed to update local status to running", "error", err)
		}
	}

	// Propagate agent target from orchestration config
	agentTarget := ""
	if plan.Orchestration != nil {
		agentTarget = plan.Orchestration.AgentTarget
	}

	submitLog.Info("submitting job to daemon").
		Field("job", job.Filename).
		Field("type", string(job.Type)).
		Field("agent_target", agentTarget).
		Field("plan_dir", plan.Directory).
		StructuredOnly().Log(ctx)

	// Submit job to daemon
	info, err := r.client.SubmitJob(ctx, models.JobSubmitRequest{
		PlanDir:     plan.Directory,
		JobFile:     job.Filename,
		AgentTarget: agentTarget,
	})
	if err != nil {
		if r.updater != nil {
			job.Metadata.LastError = fmt.Sprintf("submit job to daemon: %s", err)
			_ = r.updater.UpdateJobMetadata(job, job.Metadata)
			_ = r.updater.UpdateJobStatus(job, JobStatusFailed)
		}
		return fmt.Errorf("submit job to daemon: %w", err)
	}

	jobID := info.ID
	r.logger.Info("Job submitted to daemon", "job_id", jobID, "status", info.Status)

	// Stream logs and wait for completion
	ch, err := r.client.StreamJobLogs(ctx, jobID)
	if err != nil {
		// If streaming fails, fall back to polling for completion
		r.logger.Error("Failed to stream logs, polling for completion", "error", err)
		return r.waitForCompletion(ctx, job, jobID)
	}

	writer := grovelogging.GetWriter(ctx)
	for event := range ch {
		switch event.Event {
		case "log":
			if event.Line != nil {
				fmt.Fprintln(writer, event.Line.Line)
			}
		case "status":
			return r.handleTerminalStatus(job, event.Status, event.Error)
		}
	}

	// Channel closed without terminal status — check final state
	return r.waitForCompletion(ctx, job, jobID)
}

// handleTerminalStatus updates job status and returns the appropriate error.
func (r *DaemonRuntime) handleTerminalStatus(job *Job, status, errMsg string) error {
	switch status {
	case "completed":
		if r.updater != nil {
			_ = r.updater.UpdateJobStatus(job, JobStatusCompleted)
		}
		return nil
	case "running":
		// Interactive agent jobs stay in "running" after the executor returns.
		// The job was already set to running before submission; preserve that state
		// so the orchestrator knows to keep polling for completion.
		return nil
	case "pending_user":
		if r.updater != nil {
			_ = r.updater.UpdateJobStatus(job, JobStatusPendingUser)
		}
		return nil
	case "failed":
		if r.updater != nil {
			if errMsg != "" {
				job.Metadata.LastError = errMsg
			} else {
				job.Metadata.LastError = "job failed"
			}
			_ = r.updater.UpdateJobMetadata(job, job.Metadata)
			r.updater.UpdateJobStatus(job, JobStatusFailed)
		}
		if errMsg != "" {
			return fmt.Errorf("job failed: %s", errMsg)
		}
		return fmt.Errorf("job failed")
	case "cancelled":
		if r.updater != nil {
			job.Metadata.LastError = "job cancelled"
			_ = r.updater.UpdateJobMetadata(job, job.Metadata)
			r.updater.UpdateJobStatus(job, JobStatusFailed)
		}
		return fmt.Errorf("job cancelled")
	default:
		return nil
	}
}

// waitForCompletion polls the daemon for the job's final status.
func (r *DaemonRuntime) waitForCompletion(ctx context.Context, job *Job, jobID string) error {
	finalInfo, err := r.client.GetJob(ctx, jobID)
	if err != nil {
		return fmt.Errorf("get final job status: %w", err)
	}
	return r.handleTerminalStatus(job, finalInfo.Status, finalInfo.Error)
}

// StreamLogs returns a channel of log lines for a running or completed job.
func (r *DaemonRuntime) StreamLogs(ctx context.Context, jobID string) (<-chan string, error) {
	ch, err := r.client.StreamJobLogs(ctx, jobID)
	if err != nil {
		return nil, err
	}

	out := make(chan string, 100)
	go func() {
		defer close(out)
		for event := range ch {
			if event.Event == "log" && event.Line != nil {
				select {
				case out <- event.Line.Line:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return out, nil
}

// Cancel stops a running job via the daemon.
func (r *DaemonRuntime) Cancel(ctx context.Context, jobID string) error {
	return r.client.CancelJob(ctx, jobID)
}
