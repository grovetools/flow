package orchestration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// hookWaitGroup tracks pending hook operations
var hookWaitGroup sync.WaitGroup

// WaitForHooks waits for all pending hook operations to complete
func WaitForHooks() {
	hookWaitGroup.Wait()
}

// OneshotHookInput defines the JSON payload for starting a job
type OneshotHookInput struct {
	JobID         string `json:"job_id"`
	PlanName      string `json:"plan_name"`
	PlanDirectory string `json:"plan_directory"`
	JobTitle      string `json:"job_title"`
	JobFilePath   string `json:"job_file_path"`
	Repository    string `json:"repository,omitempty"`
	Branch        string `json:"branch,omitempty"`
	Status        string `json:"status,omitempty"`
}

// OneshotHookStopInput defines the JSON payload for stopping a job
type OneshotHookStopInput struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"` // "completed" or "failed"
	Error  string `json:"error,omitempty"`
}

// callGroveHook calls grove-hooks commands for job lifecycle tracking
// This is a non-blocking operation - errors are logged but don't fail the job
func callGroveHook(subcommand string, payload interface{}) {
	callGroveHookWithSync(subcommand, payload, false)
}

// callGroveHookWithSync calls grove-hooks with optional synchronous execution
func callGroveHookWithSync(subcommand string, payload interface{}, synchronous bool) {
	// Check if grove-hooks is available
	_, err := exec.LookPath("grove-hooks")
	if err != nil {
		// grove-hooks not installed, silently skip
		return
	}

	data, err := json.Marshal(payload)
	if err != nil {
		// Log but don't fail
		fmt.Printf("Warning: failed to marshal grove-hooks payload: %v\n", err)
		return
	}

	executeHook := func() {
		cmd := exec.Command("grove-hooks", "oneshot", subcommand)
		cmd.Stdin = bytes.NewReader(data)
		
		// Run with timeout to prevent hanging
		done := make(chan error, 1)
		go func() {
			done <- cmd.Run()
		}()

		select {
		case err := <-done:
			if err != nil {
				// Log but don't fail - hooks are optional
				fmt.Printf("Warning: grove-hooks %s failed: %v\n", subcommand, err)
			}
		case <-time.After(5 * time.Second):
			// Timeout after 5 seconds
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
			fmt.Printf("Warning: grove-hooks %s timed out\n", subcommand)
		}
	}

	if synchronous {
		// Execute synchronously
		executeHook()
	} else {
		// Run in background to avoid blocking job execution
		hookWaitGroup.Add(1)
		go func() {
			defer hookWaitGroup.Done()
			executeHook()
		}()
	}
}

// notifyJobStart sends a start notification to grove-hooks
func notifyJobStart(job *Job, plan *Plan) {
	if job == nil || plan == nil {
		return
	}

	// Get git information from the working directory
	repo := ""
	branch := ""
	
	workDir := plan.Directory
	if workDir == "" {
		workDir = "."
	}
	
	// Try to get git info
	gitCmd := exec.Command("git", "-C", workDir, "rev-parse", "--show-toplevel")
	if output, err := gitCmd.Output(); err == nil {
		repoPath := strings.TrimSpace(string(output))
		repo = filepath.Base(repoPath)
		
		// Get branch name
		branchCmd := exec.Command("git", "-C", workDir, "rev-parse", "--abbrev-ref", "HEAD")
		if branchOutput, err := branchCmd.Output(); err == nil {
			branch = strings.TrimSpace(string(branchOutput))
		}
	}

	payload := OneshotHookInput{
		JobID:         job.ID,
		PlanName:      plan.Name,
		PlanDirectory: plan.Directory,
		JobTitle:      job.Title,
		JobFilePath:   job.FilePath,
		Repository:    repo,
		Branch:        branch,
		Status:        "running",
	}

	// Call synchronously to ensure status is updated before job execution begins
	callGroveHookWithSync("start", payload, true)
}

// notifyJobComplete sends a completion notification to grove-hooks
func notifyJobComplete(job *Job, jobErr error) {
	NotifyJobCompleteExternal(job, jobErr)
}

// NotifyJobCompleteExternal sends a completion notification to grove-hooks
// This is exported for use by external commands like 'flow plan complete'
func NotifyJobCompleteExternal(job *Job, jobErr error) {
	if job == nil {
		return
	}

	status := "completed"
	errorMsg := ""
	
	if jobErr != nil {
		status = "failed"
		errorMsg = jobErr.Error()
	} else if job.Status == JobStatusFailed {
		status = "failed"
		errorMsg = "Job failed"
	} else if job.Status == JobStatusPendingUser {
		status = "pending_user"
	} else if job.Status == JobStatusRunning {
		// Job is still running, this shouldn't happen in normal flow
		// but can occur if there's an early return or error
		status = "running"
	}

	payload := OneshotHookStopInput{
		JobID:  job.ID,
		Status: status,
		Error:  errorMsg,
	}

	callGroveHookWithSync("stop", payload, true)
}

// GroveHooksSessionStatus represents the response from grove-hooks sessions get --json
type GroveHooksSessionStatus struct {
	SessionID string    `json:"session_id"`
	JobID     string    `json:"job_id"`
	Status    string    `json:"status"`
	Type      string    `json:"type"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// queryGroveHooksStatus queries grove-hooks for the status of a job/session
func queryGroveHooksStatus(jobID string) (*GroveHooksSessionStatus, error) {
	// Check if grove-hooks is available
	_, err := exec.LookPath("grove-hooks")
	if err != nil {
		return nil, fmt.Errorf("grove-hooks not available")
	}

	cmd := exec.Command("grove-hooks", "sessions", "get", jobID, "--json")
	output, err := cmd.Output()
	if err != nil {
		// If command failed, it might mean the session doesn't exist yet
		return nil, fmt.Errorf("grove-hooks query failed: %w", err)
	}

	var status GroveHooksSessionStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return nil, fmt.Errorf("failed to parse grove-hooks response: %w", err)
	}

	return &status, nil
}