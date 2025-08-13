package orchestration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/mattsolo1/grove-core/git"
)

// ShellExecutor executes shell commands as orchestration jobs.
type ShellExecutor struct {
	worktreeManager *git.WorktreeManager
}

// NewShellExecutor creates a new shell executor.
func NewShellExecutor() *ShellExecutor {
	return &ShellExecutor{
		worktreeManager: git.NewWorktreeManager(),
	}
}

// Name returns the executor name.
func (e *ShellExecutor) Name() string {
	return "shell"
}

// Execute runs a shell job.
func (e *ShellExecutor) Execute(ctx context.Context, job *Job, plan *Plan) error {
	// Update job status to running
	persister := NewStatePersister()
	job.StartTime = time.Now()
	if err := job.UpdateStatus(persister, JobStatusRunning); err != nil {
		return fmt.Errorf("updating job status: %w", err)
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
	cmd.Env = append(cmd.Environ(), 
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
	
	// Persist the output of the shell command to the job file for easy debugging and audit
	if writeErr := job.AppendOutput(persister, string(output)); writeErr != nil {
		// Log this error, but return the original command's error
		fmt.Printf("Warning: failed to write output for job %s: %v\n", job.ID, writeErr)
	}
	
	// Update job status based on command result
	job.EndTime = time.Now()
	if err != nil {
		job.Status = JobStatusFailed
		job.UpdateStatus(persister, JobStatusFailed)
		// Include more details about the failure
		exitCode := -1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		return fmt.Errorf("shell command failed (exit code: %v): %w\nCommand: %s\nWorking Dir: %s\nOutput: %s", 
			exitCode, err, job.PromptBody, workDir, string(output))
	}
	
	// Update job status to completed
	job.Status = JobStatusCompleted
	if err := job.UpdateStatus(persister, JobStatusCompleted); err != nil {
		return fmt.Errorf("updating job status to completed: %w", err)
	}
	
	return nil
}

// prepareWorktree ensures the worktree exists and is ready.
func (e *ShellExecutor) prepareWorktree(ctx context.Context, job *Job, plan *Plan) (string, error) {
	if job.Worktree == "" {
		return "", fmt.Errorf("job %s has no worktree specified", job.ID)
	}

	// Use the shared method to get or prepare the worktree in the plan's directory
	return e.worktreeManager.GetOrPrepareWorktree(ctx, plan.Directory, job.Worktree, "agent")
}