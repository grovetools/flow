package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"

	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/paths"
)

// ClawExecutor executes claw jobs by delegating them to the global claw daemon.
type ClawExecutor struct{}

// NewClawExecutor creates a new claw executor.
func NewClawExecutor() *ClawExecutor {
	return &ClawExecutor{}
}

// Name returns the executor name.
func (e *ClawExecutor) Name() string {
	return "claw"
}

// Execute delegates the job to the claw daemon via Unix socket.
func (e *ClawExecutor) Execute(ctx context.Context, job *Job, plan *Plan) error {
	output := grovelogging.GetWriter(ctx)

	// Create lock file with the current process's PID.
	if err := CreateLockFile(job.FilePath, os.Getpid()); err != nil {
		return fmt.Errorf("failed to create lock file: %w", err)
	}
	defer RemoveLockFile(job.FilePath)

	// Connect to claw daemon Unix socket
	sockPath := filepath.Join(paths.RuntimeDir(), "claw.sock")
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return fmt.Errorf("failed to connect to claw daemon at %s: %w\nIs 'claw daemon' running?", sockPath, err)
	}
	defer conn.Close()

	// Build the payload matching claw's bus.InboundMessage structure
	msg := struct {
		ChatID  string `json:"chat_id"`
		Content string `json:"content"`
		Role    string `json:"role,omitempty"`
	}{
		ChatID:  job.ID,
		Content: fmt.Sprintf("[flow job: %s]\n\n%s\n\nWhen this task is complete, remind the user to run `flow plan complete %s` to unblock downstream jobs.", job.Filename, job.PromptBody, job.Filename),
		Role:    "user",
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	data = append(data, '\n')

	// Send payload to daemon
	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("failed to send message to claw daemon: %w", err)
	}

	successMsg := fmt.Sprintf("Task delegated to claw daemon.\nCheck your configured claw channels (e.g. Signal) for updates.\nRun `flow plan complete %s` when finished.", job.Filename)
	fmt.Fprintln(output, successMsg)

	// Append output to job file
	if err := appendOutputToJobFile(successMsg, job); err != nil {
		return fmt.Errorf("appending output to job file: %w", err)
	}

	// Update job status to pending_user, pausing the DAG
	job.Status = JobStatusPendingUser
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job status to pending_user: %w", err)
	}

	return nil
}

// appendOutputToJobFile appends output to the job markdown file.
func appendOutputToJobFile(text string, job *Job) error {
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("reading job file: %w", err)
	}

	separator := "\n\n---\n\n## Output\n\n"
	newContent := string(content) + separator + text

	if err := os.WriteFile(job.FilePath, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("writing job file: %w", err)
	}

	return nil
}
