package orchestration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// ShellExecutor executes shell commands as orchestration jobs.
type ShellExecutor struct {
}

// NewShellExecutor creates a new shell executor.
func NewShellExecutor() *ShellExecutor {
	return &ShellExecutor{}
}

// Name returns the executor name.
func (e *ShellExecutor) Name() string {
	return "shell"
}

// Execute runs a shell job.
func (e *ShellExecutor) Execute(ctx context.Context, job *Job, plan *Plan) error {
	// Notify grove-hooks about job start
	notifyJobStart(job, plan)

	// Ensure we notify completion/failure when we exit
	var execErr error
	defer func() {
		notifyJobComplete(job, execErr)
	}()

	// Update job status to running
	persister := NewStatePersister()
	job.StartTime = time.Now()
	if err := job.UpdateStatus(persister, JobStatusRunning); err != nil {
		execErr = fmt.Errorf("updating job status: %w", err)
		return execErr
	}
	
	// Log execution details for debugging
	fmt.Printf("[Shell Executor] Executing job %s\n", job.ID)
	fmt.Printf("[Shell Executor] Command: %s\n", job.PromptBody)

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
	
	fmt.Printf("[Shell Executor] Working directory resolved to: %s\n", workDir)

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
	
	output, err := cmd.CombinedOutput()
	execErr = err // Capture the execution error for the deferred hook

	// Persist the output of the shell command to the job file for easy debugging and audit
	if writeErr := job.AppendOutput(persister, string(output)); writeErr != nil {
		// Log this error, but return the original command's error
		fmt.Printf("Warning: failed to write output for job %s: %v\n", job.ID, writeErr)
	}

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
		fmt.Printf("Warning: failed to update job status to %s: %v\n", finalStatus, statusUpdateErr)
	}
	
	if execErr != nil {
		exitCode := -1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		execErr = fmt.Errorf("shell command failed (exit code: %v): %w\nCommand: %s\nWorking Dir: %s\nOutput: %s",
			exitCode, execErr, job.PromptBody, workDir, string(output))
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

	// Use the new centralized worktree preparation function with repos filter
	var repos []string
	if plan.Config != nil && len(plan.Config.Repos) > 0 {
		repos = plan.Config.Repos
	}
	return PrepareWorktreeWithRepos(ctx, gitRoot, job.Worktree, plan.Name, repos)
}

