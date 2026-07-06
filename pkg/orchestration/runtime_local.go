package orchestration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/google/uuid"
	"github.com/grovetools/core/command"
	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/grove-gemini/pkg/gemini"
)

// contextKey is a private type for context keys to avoid collisions with other packages.
type contextKey string

// LocalRuntime executes jobs in-process by invoking the appropriate Executor.
type LocalRuntime struct {
	executors map[JobType]Executor
	config    *ExecutorConfig
	updater   StatusUpdater
	logger    Logger
}

// NewLocalRuntime initializes a new local runtime and registers all executors.
func NewLocalRuntime(config *ExecutorConfig, commandExecutor command.Executor, updater StatusUpdater, logger Logger) *LocalRuntime {
	if commandExecutor == nil {
		commandExecutor = &command.RealExecutor{}
	}

	var llmClient LLMClient
	if os.Getenv("GROVE_MOCK_LLM_RESPONSE_FILE") != "" {
		llmClient = NewMockLLMClient()
	} else {
		llmClient = NewCommandLLMClient(commandExecutor)
	}
	geminiRunner := gemini.NewRequestRunner()

	r := &LocalRuntime{
		executors: make(map[JobType]Executor),
		config:    config,
		updater:   updater,
		logger:    logger,
	}

	// Register executors
	oneshotExecutor := NewOneShotExecutor(llmClient, config)
	r.executors[JobTypeOneshot] = oneshotExecutor
	r.executors[JobTypeChat] = oneshotExecutor

	r.executors[JobTypeHeadlessAgent] = NewHeadlessAgentExecutor(llmClient, config)

	interactiveExecutor := NewInteractiveAgentExecutor(llmClient, geminiRunner, config.SkipInteractive)
	r.executors[JobTypeAgent] = interactiveExecutor
	r.executors[JobTypeInteractiveAgent] = interactiveExecutor

	isolatedExecutor := NewIsolatedAgentExecutor(llmClient, geminiRunner, config.SkipInteractive)
	r.executors[JobTypeIsolatedAgent] = isolatedExecutor

	r.executors[JobTypeShell] = NewShellExecutor(config)
	r.executors[JobTypeGenerateRecipe] = NewGenerateRecipeExecutor(config)

	return r
}

// SetExecutor replaces an executor for a given job type. This is primarily
// used in tests to inject mock executors.
func (r *LocalRuntime) SetExecutor(jobType JobType, executor Executor) {
	r.executors[jobType] = executor
}

// ExecuteJob handles logging setup, status transitions, and delegates to the executor.
func (r *LocalRuntime) ExecuteJob(ctx context.Context, job *Job, plan *Plan) error {
	// 1. Context and logging setup
	requestID, _ := ctx.Value(contextKey("request_id")).(string)
	if requestID == "" {
		requestID = "req-" + uuid.New().String()[:8]
		ctx = context.WithValue(ctx, contextKey("request_id"), requestID)
	}

	logFields := map[string]interface{}{
		"request_id": requestID,
		"job_id":     job.ID,
		"job_type":   job.Type,
		"job_title":  job.Title,
		"job_file":   job.FilePath,
		"plan_name":  plan.Name,
		"plan_dir":   plan.Directory,
		"status":     job.Status,
	}

	// Add optional fields if present
	if job.Model != "" {
		logFields["model"] = job.Model
	}
	if job.Template != "" {
		logFields["template"] = job.Template
	}
	if job.RulesFile != "" {
		logFields["rules_file"] = job.RulesFile
	}
	if job.Repository != "" {
		logFields["repository"] = job.Repository
	}
	if job.Worktree != "" {
		logFields["worktree"] = job.Worktree
	}
	if len(job.Dependencies) > 0 {
		depFiles := make([]string, len(job.Dependencies))
		for i, dep := range job.Dependencies {
			if dep != nil {
				depFiles[i] = dep.Filename
			}
		}
		logFields["dependencies"] = depFiles
		logFields["dependency_count"] = len(job.Dependencies)
	}
	if job.PrependDependencies {
		logFields["prepend_dependencies"] = job.PrependDependencies
	}

	// Debug: the user-facing lifecycle event is the single event=job.launched
	// line emitted by UpdateJobStatus on the pending->running transition.
	if job.Type == JobTypeInteractiveAgent {
		r.logger.Debug("Starting interactive job", logFieldsToKeyVals(logFields)...)
	} else {
		r.logger.Debug("Executing job", logFieldsToKeyVals(logFields)...)
	}

	// 2. Update status to running
	if err := r.updater.UpdateJobStatus(job, JobStatusRunning); err != nil {
		return fmt.Errorf("update status to running: %w", err)
	}

	executor, ok := r.executors[job.Type]
	if !ok {
		return fmt.Errorf("no executor for job type: %s", job.Type)
	}

	// 3. Set up the file logger and MultiWriter
	logFilePath, err := GetJobLogPath(plan, job)
	if err != nil {
		return fmt.Errorf("failed to get log path: %w", err)
	}

	logFile, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer logFile.Close()

	// Extract the base writer provided by the caller (Stdout or TUI stream)
	baseWriter := grovelogging.GetWriter(ctx)

	isAgentJob := job.Type == JobTypeInteractiveAgent || job.Type == JobTypeHeadlessAgent || job.Type == JobTypeIsolatedAgent

	var multiWriter io.Writer
	if isAgentJob {
		// Agent jobs use separate log streaming via aglogs, so only write to log file directly
		multiWriter = logFile
	} else {
		// Non-agent jobs write to both log file and the current context writer
		multiWriter = io.MultiWriter(baseWriter, logFile)
	}

	jobCtx := grovelogging.WithWriter(ctx, multiWriter)

	// 4. Delegate to executor
	execErr := executor.Execute(jobCtx, job, plan)

	// 5. Update final status (skip for chat and interactive agent jobs - they
	// manage their own status). Headless agents are also excluded (A6): they
	// detach — Execute returns nil while the agent is still running — so the
	// exit watcher (waitAndWriteStatus → FinalizeHeadlessJob) writes the real
	// terminal status once the process exits. Stamping `completed` here at
	// detach was the premature-completion bug. Isolated agents are deliberately
	// NOT excluded: they have no headless-style finalizer, so they keep the
	// existing auto-complete-at-detach behavior.
	if job.Type != JobTypeChat && job.Type != JobTypeInteractiveAgent && job.Type != JobTypeAgent && job.Type != JobTypeHeadlessAgent {
		finalStatus := JobStatusCompleted
		if execErr != nil {
			finalStatus = JobStatusFailed
			r.logger.Error("Job execution failed", "request_id", requestID, "id", job.ID, "error", execErr)

			// Record the error in metadata
			job.Metadata.LastError = execErr.Error()
			if err := r.updater.UpdateJobMetadata(job, job.Metadata); err != nil {
				r.logger.Error("Failed to update job metadata", "error", err)
			}
		}

		if err := r.updater.UpdateJobStatus(job, finalStatus); err != nil {
			return fmt.Errorf("update final status: %w", err)
		}
	} else if execErr != nil {
		// For chat jobs, only update status on error
		r.logger.Error("Job execution failed", "request_id", requestID, "id", job.ID, "error", execErr)

		// Record the error in metadata
		job.Metadata.LastError = execErr.Error()
		if err := r.updater.UpdateJobMetadata(job, job.Metadata); err != nil {
			r.logger.Error("Failed to update job metadata", "error", err)
		}

		if err := r.updater.UpdateJobStatus(job, JobStatusFailed); err != nil {
			return fmt.Errorf("update final status: %w", err)
		}
	}

	return execErr
}

// StreamLogs returns a channel of log lines for a running or completed job.
// Not implemented in Phase 0 — will be implemented when DaemonRuntime needs it.
func (r *LocalRuntime) StreamLogs(ctx context.Context, jobID string) (<-chan string, error) {
	return nil, errors.New("StreamLogs not implemented for LocalRuntime")
}

// Cancel stops a running job.
// Not implemented in Phase 0 — will be implemented when DaemonRuntime needs it.
func (r *LocalRuntime) Cancel(ctx context.Context, jobID string) error {
	return errors.New("Cancel not implemented for LocalRuntime")
}
