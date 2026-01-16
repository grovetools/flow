package orchestration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/workspace"
)

// ShellExecutor executes shell commands as orchestration jobs.
type ShellExecutor struct {
	config *ExecutorConfig
}

// NewShellExecutor creates a new shell executor.
func NewShellExecutor(config *ExecutorConfig) *ShellExecutor {
	return &ShellExecutor{
		config: config,
	}
}

// Name returns the executor name.
func (e *ShellExecutor) Name() string {
	return "shell"
}

// Execute runs a shell job.
func (e *ShellExecutor) Execute(ctx context.Context, job *Job, plan *Plan) error {
	// Get the output writer from context
	output := grovelogging.GetWriter(ctx)

	// Generate a unique request ID for tracing
	requestID, _ := ctx.Value("request_id").(string)

	// Create lock file with the current process's PID.
	if err := CreateLockFile(job.FilePath, os.Getpid()); err != nil {
		return fmt.Errorf("failed to create lock file: %w", err)
	}
	// Ensure lock file is removed when execution finishes.
	defer RemoveLockFile(job.FilePath)

	// Update job status to running
	persister := NewStatePersister()
	job.StartTime = time.Now()
	if err := job.UpdateStatus(persister, JobStatusRunning); err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}

	// Log execution details for debugging
	ulog.Debug("Executing shell job").
		Field("request_id", requestID).
		Field("job_id", job.ID).
		Field("command", job.PromptBody).
		Log(ctx)

	// Determine the working directory
	var workDir string
	if job.Worktree != "" {
		// If job has a worktree, prepare and use it
		worktreePath, err := e.prepareWorktree(ctx, job, plan)
		if err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("prepare worktree: %w", err)
		}
		workDir = worktreePath
	} else {
		// Otherwise use the resolved working directory
		workDir = ResolveWorkingDirectory(plan)
	}

	// Scope to sub-project if job.Repository is set (for ecosystem worktrees)
	workDir = ScopeToSubProject(workDir, job)

	ulog.Info("Executing shell job").
		Field("job_id", job.ID).
		Field("request_id", requestID).
		Field("plan_name", plan.Name).
		Field("command", job.PromptBody).
		Field("work_dir", workDir).
		Log(ctx)

	// Always regenerate context to ensure shell job has latest view, similar to oneshot executor
	oneShotExec := NewOneShotExecutor(NewCommandLLMClient(nil), e.config) // Pass config for SkipInteractive
	if err := oneShotExec.regenerateContextInWorktree(ctx, workDir, "shell", job, plan); err != nil {
		// Warn but do not fail the job for a context error
		ulog.Warn("Failed to generate context for shell job").
			Err(err).
			Field("request_id", requestID).
			Field("job_id", job.ID).
			Log(ctx)
	}

	// The PromptBody contains the shell command to run
	// Use "sh" instead of "/bin/sh" to be more portable
	cmd := exec.CommandContext(ctx, "sh", "-c", job.PromptBody)
	cmd.Dir = workDir

	// Set up environment for better debugging
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("GROVE_PLAN_DIR=%s", plan.Directory),
		fmt.Sprintf("GROVE_WORK_DIR=%s", workDir),
	)

	// Ensure the working directory exists
	if err := os.MkdirAll(workDir, 0755); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to create working directory %s: %w", workDir, err)
	}

	// Stream output directly to the provided writer
	cmd.Stdout = output
	cmd.Stderr = output
	execErr := cmd.Run()

	// Log execution result
	duration := time.Since(job.StartTime)
	exitCode := 0
	if execErr != nil {
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		} else {
			exitCode = -1 // Unknown exit code
		}
	}

	ulog.Info("Shell job execution completed").
		Field("job_id", job.ID).
		Field("request_id", requestID).
		Field("exit_code", exitCode).
		Field("duration_ms", duration.Milliseconds()).
		Field("success", execErr == nil).
		Log(ctx)

	// Output is streamed directly, so we don't need to persist it separately
	// The TUI or other consumers will handle the output via the io.Writer

	// Update job status based on command result
	job.EndTime = time.Now()
	finalStatus := JobStatusCompleted
	if execErr != nil {
		finalStatus = JobStatusFailed
	}

	if statusUpdateErr := job.UpdateStatus(persister, finalStatus); statusUpdateErr != nil {
		// If the original command succeeded but status update failed, return the status error
		if execErr == nil {
			execErr = fmt.Errorf("updating job status to %s: %w", finalStatus, statusUpdateErr)
			return execErr
		}
		// Otherwise, log the status update error and return the original execution error
		ulog.Warn("Failed to update job status").
			Field("status", finalStatus).
			Err(statusUpdateErr).
			Log(ctx)
	}

	if execErr != nil {
		exitCode := -1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		execErr = fmt.Errorf("shell command failed (exit code: %v): %w\nCommand: %s\nWorking Dir: %s",
			exitCode, execErr, job.PromptBody, workDir)
		return execErr
	}

	return nil
}

// prepareWorktree ensures the worktree exists and is ready.
func (e *ShellExecutor) prepareWorktree(ctx context.Context, job *Job, plan *Plan) (string, error) {
	if job.Worktree == "" {
		return "", fmt.Errorf("job %s has no worktree specified", job.ID)
	}

	gitRoot, err := GetProjectGitRoot(plan.Directory)
	if err != nil {
		// Fallback to plan directory if not in a git repo
		gitRoot = plan.Directory
	}

	// Check if the worktree directory already exists. If so, skip preparation.
	worktreePath := filepath.Join(gitRoot, ".grove-worktrees", job.Worktree)
	if _, err := os.Stat(worktreePath); err == nil {
		return worktreePath, nil
	}

	// The new logic:
	opts := workspace.PrepareOptions{
		GitRoot:      gitRoot,
		WorktreeName: job.Worktree,
		BranchName:   job.Worktree,
		PlanName:     plan.Name,
	}

	if plan.Config != nil && len(plan.Config.Repos) > 0 {
		opts.Repos = plan.Config.Repos
	}

	return workspace.Prepare(ctx, opts)
}
