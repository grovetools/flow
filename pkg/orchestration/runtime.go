package orchestration

import (
	"context"
)

// Runtime defines how jobs are executed. The Orchestrator uses a Runtime
// to dispatch jobs, allowing different execution strategies (local, daemon, remote).
type Runtime interface {
	// ExecuteJob runs a single job. It blocks until completion or error.
	// The Runtime is responsible for log capture and status updates.
	ExecuteJob(ctx context.Context, job *Job, plan *Plan) error

	// StreamLogs returns a channel of log lines for a running or completed job.
	StreamLogs(ctx context.Context, jobID string) (<-chan string, error)

	// Cancel stops a running job.
	Cancel(ctx context.Context, jobID string) error
}

// StatusUpdater allows runtimes to report status changes back to the orchestrator
// to keep the in-memory state and dependency graph synchronized.
type StatusUpdater interface {
	UpdateJobStatus(job *Job, status JobStatus) error
}
