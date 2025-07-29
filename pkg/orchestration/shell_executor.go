package orchestration

import (
	"context"
	"fmt"
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

	// The PromptBody contains the shell command to run
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", job.PromptBody)
	cmd.Dir = workDir
	
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
		return fmt.Errorf("shell command failed: %w\nOutput: %s", err, string(output))
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

	// Get the git repository root to ensure worktrees are created in a consistent location
	gitRoot, err := GetGitRootSafe(plan.Directory)
	if err != nil {
		return "", fmt.Errorf("could not find git root: %w", err)
	}

	// Use the shared method to get or prepare the worktree
	return e.worktreeManager.GetOrPrepareWorktree(ctx, gitRoot, job.Worktree, "agent")
}