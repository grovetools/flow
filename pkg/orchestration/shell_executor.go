package orchestration

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	grovelogging "github.com/mattsolo1/grove-core/logging"
	"github.com/mattsolo1/grove-core/pkg/workspace"
	"github.com/sirupsen/logrus"
)

// ShellExecutor executes shell commands as orchestration jobs.
type ShellExecutor struct {
	log       *logrus.Entry
	prettyLog *grovelogging.PrettyLogger
}

// NewShellExecutor creates a new shell executor.
func NewShellExecutor() *ShellExecutor {
	return &ShellExecutor{
		log:       grovelogging.NewLogger("grove-flow"),
		prettyLog: grovelogging.NewPrettyLogger(),
	}
}

// Name returns the executor name.
func (e *ShellExecutor) Name() string {
	return "shell"
}

// Execute runs a shell job.
func (e *ShellExecutor) Execute(ctx context.Context, job *Job, plan *Plan, output io.Writer) error {
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
	e.log.WithFields(logrus.Fields{
		"request_id": requestID,
		"job_id":  job.ID,
		"command": job.PromptBody,
	}).Debug("Executing shell job")

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

	e.log.WithFields(logrus.Fields{
		"job_id":     job.ID,
		"request_id": requestID,
		"plan_name":  plan.Name,
		"command":    job.PromptBody,
		"work_dir":   workDir,
	}).Info("Executing shell job")

	// Generate context if a rules file is specified
	if job.RulesFile != "" {
		oneShotExec := NewOneShotExecutor(NewCommandLLMClient(), nil) // Use for helper method
		if err := oneShotExec.regenerateContextInWorktree(ctx, workDir, "shell", job, plan); err != nil {
			// Warn but do not fail the job for a context error
			e.log.WithError(err).WithFields(logrus.Fields{"request_id": requestID, "job_id": job.ID}).Warn("Failed to generate job-specific context for shell job")
			e.prettyLog.WarnPretty(fmt.Sprintf("Warning: Failed to generate job-specific context: %v", err))
		}
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

	e.log.WithFields(logrus.Fields{
		"job_id":      job.ID,
		"request_id":  requestID,
		"exit_code":   exitCode,
		"duration_ms": duration.Milliseconds(),
		"success":     execErr == nil,
	}).Info("Shell job execution completed")

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
		e.log.WithFields(logrus.Fields{
			"status": finalStatus,
			"error":  statusUpdateErr,
		}).Warn("Failed to update job status")
		e.prettyLog.WarnPretty(fmt.Sprintf("Warning: failed to update job status to %s: %v", finalStatus, statusUpdateErr))
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

	gitRoot, err := GetGitRootSafe(plan.Directory)
	if err != nil {
		// Fallback to plan directory if not in a git repo
		gitRoot = plan.Directory
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

