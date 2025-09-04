package orchestration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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


	// Get git root for worktree creation
	gitRoot, err := GetGitRootSafe(plan.Directory)
	if err != nil {
		// Fallback to plan directory if not in a git repo
		gitRoot = plan.Directory
	}

	// Check if we're already in the worktree
	currentDir, _ := os.Getwd()
	if currentDir != "" && (strings.HasSuffix(currentDir, "/.grove-worktrees/"+job.Worktree) || 
		strings.HasSuffix(gitRoot, "/.grove-worktrees/"+job.Worktree)) {
		// We're already in the worktree
		return currentDir, nil
	}

	// Need to find the actual git root (not a worktree)
	// If gitRoot ends with .grove-worktrees/something, go up to find real root
	realGitRoot := gitRoot
	if idx := strings.Index(gitRoot, "/.grove-worktrees/"); idx != -1 {
		realGitRoot = gitRoot[:idx]
	}

	// Use the shared method to get or prepare the worktree at the git root
	worktreePath, err := e.worktreeManager.GetOrPrepareWorktree(ctx, realGitRoot, job.Worktree, "")
	if err != nil {
		return "", err
	}

	// Check if grove-hooks is available and install hooks in the worktree
	if _, err := exec.LookPath(GetHooksBinaryPath()); err == nil {
		cmd := exec.Command(GetHooksBinaryPath(), "install")
		cmd.Dir = worktreePath
		if output, err := cmd.CombinedOutput(); err != nil {
			fmt.Printf("Warning: grove-hooks install failed: %v (output: %s)\n", err, string(output))
		} else {
			fmt.Printf("✓ Installed grove-hooks in worktree: %s\n", worktreePath)
		}
	}

	// Set up Go workspace if this is a Go project
	if err := SetupGoWorkspaceForWorktree(worktreePath, gitRoot); err != nil {
		// Log a warning but don't fail the job, as this is a convenience feature
		fmt.Printf("Warning: failed to setup Go workspace in worktree: %v\n", err)
	}

	// Automatically initialize state within the new worktree for a better UX.
	groveDir := filepath.Join(worktreePath, ".grove")
	if err := os.MkdirAll(groveDir, 0755); err != nil {
		// Log a warning but don't fail the job, as this is a convenience feature.
		fmt.Printf("Warning: failed to create .grove directory in worktree: %v\n", err)
	} else {
		planName := filepath.Base(plan.Directory)
		stateContent := fmt.Sprintf("active_plan: %s\n", planName)
		statePath := filepath.Join(groveDir, "state.yml")
		// This is a best-effort attempt; failure should not stop the job.
		_ = os.WriteFile(statePath, []byte(stateContent), 0644)
	}

	return worktreePath, nil
}

