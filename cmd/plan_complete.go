package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/fatih/color"
	"github.com/mattsolo1/grove-core/pkg/workspace"
	"github.com/mattsolo1/grove-core/util/sanitize"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

var planCompleteCmd = &cobra.Command{
	Use:   "complete <job-file>",
	Short: "Mark a job as completed",
	Long: `Mark a job as completed. This is especially useful for chat jobs 
that would otherwise remain in pending_user status indefinitely.

Examples:
  # Complete a chat job
  flow plan complete my-project/plan.md
  
  # Complete any job by its filename
  flow plan complete my-project/01-design-api.md`,
	Args: cobra.ExactArgs(1),
	RunE: runPlanComplete,
}

// completeJob is the shared function that handles job completion logic
// It can be called from both the CLI and TUI
// Set silent=true to suppress output (useful for TUI)
func completeJob(job *orchestration.Job, plan *orchestration.Plan, silent bool) error {
	// Check current status
	alreadyCompleted := job.Status == orchestration.JobStatusCompleted
	if alreadyCompleted && !silent {
		fmt.Printf("Job already completed: %s\n", job.Filename)
		// Still need to clean up associated resources (Claude process, tmux window)
	}

	// Update status (skip if already completed)
	oldStatus := job.Status
	if !alreadyCompleted {
		job.Status = orchestration.JobStatusCompleted

		// Use the state persister to update the job file
		persister := orchestration.NewStatePersister()
		if err := persister.UpdateJobStatus(job, orchestration.JobStatusCompleted); err != nil {
			return fmt.Errorf("update job status: %w", err)
		}

		// Append transcript if it's an interactive agent job
		if job.Type == orchestration.JobTypeInteractiveAgent {
			if !silent {
				fmt.Println("Appending interactive session transcript...")
			}
			if err := orchestration.AppendInteractiveTranscript(job, plan); err != nil {
				// Log warning but don't fail the command
				if !silent {
					fmt.Printf("Warning: failed to append transcript: %v\n", err)
				}
			} else if !silent {
				fmt.Println(color.GreenString("✓") + " Appended session transcript.")
			}
		}

		// Summarize the job content if enabled
		flowCfg, err := loadFlowConfig()
		if err != nil {
			// Don't fail the command, just log a warning
			if !silent {
				fmt.Printf("Warning: could not load flow config for summarization: %v\n", err)
			}
		} else if flowCfg.SummarizeOnComplete {
			summaryCfg := orchestration.SummaryConfig{
				Enabled:  flowCfg.SummarizeOnComplete,
				Model:    flowCfg.SummaryModel,
				Prompt:   flowCfg.SummaryPrompt,
				MaxChars: flowCfg.SummaryMaxChars,
			}

			if !silent {
				fmt.Println("Generating job summary...")
			}
			summary, err := orchestration.SummarizeJobContent(context.Background(), job, plan, summaryCfg)
			if err != nil {
				if !silent {
					fmt.Printf("Warning: failed to generate job summary: %v\n", err)
				}
			} else if summary != "" {
				// Add summary to frontmatter
				if err := orchestration.AddSummaryToJobFile(job, summary); err != nil {
					if !silent {
						fmt.Printf("Warning: failed to add summary to job file: %v\n", err)
					}
				} else if !silent {
					fmt.Println(color.GreenString("✓") + " Added summary to job frontmatter.")
				}
			}
		}
	}

	// If this was an interactive agent, try to kill its associated Claude process and tmux session.
	if job.Type == orchestration.JobTypeInteractiveAgent {
		if !silent {
			fmt.Println("Attempting to clean up associated Claude session...")
		}

		// Kill the Claude process by reading the PID from grove-hooks session metadata
		if err := killClaudeSession(job.ID); err != nil {
			if !silent {
				fmt.Printf("  Note: could not kill Claude session: %v\n", err)
			}
		} else if !silent {
			fmt.Println("  ✓ Claude process killed.")
		}

		// Also kill the tmux window if worktree is specified
		if job.Worktree != "" {
			// Try to read the session metadata to get the working directory
			worktreePath, err := getWorktreePathFromSession(job.ID)
			if err != nil {
				if !silent {
					fmt.Printf("Warning: could not determine worktree path from session: %v\n", err)
				}
			} else {
				projInfo, err := workspace.GetProjectByPath(worktreePath)
				if err != nil {
					if !silent {
						fmt.Printf("Warning: could not get project info to determine session name: %v\n", err)
					}
				} else {
					sessionName := projInfo.Identifier()
					// Replicate window name logic from interactive_agent_executor
					windowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)
					targetWindow := fmt.Sprintf("%s:%s", sessionName, windowName)

					if !silent {
						fmt.Printf("  Closing tmux window: %s\n", targetWindow)
					}
					cmd := exec.Command("tmux", "kill-window", "-t", targetWindow)
					if err := cmd.Run(); err != nil {
						// This is not a fatal error; the window might already be closed.
						if !silent {
							fmt.Printf("  Note: could not close tmux window (it may already be closed): %v\n", err)
						}
					} else if !silent {
						fmt.Println("  ✓ Tmux window closed.")
					}
				}
			}
		}
	}

	// Remove lock file if it exists
	lockFilePath := job.FilePath + ".lock"
	os.Remove(lockFilePath) // Ignore errors - file might not exist

	// Success message
	if !silent {
		if !alreadyCompleted {
			fmt.Printf("%s Job completed: %s\n", color.GreenString("✓"), job.Title)
			fmt.Printf("Status: %s → %s\n", oldStatus, orchestration.JobStatusCompleted)
		} else {
			fmt.Printf("%s Cleaned up resources for: %s\n", color.GreenString("✓"), job.Title)
		}

		// Special message for chat jobs
		if job.Type == orchestration.JobTypeChat && !alreadyCompleted {
			fmt.Printf("\nChat conversation ended. You can transform this chat into executable jobs using:\n")
			fmt.Printf("  flow plan add %s --template generate-plan --prompt-file %s\n",
				plan.Directory, job.Filename)
		}
	}

	return nil
}

func runPlanComplete(cmd *cobra.Command, args []string) error {
	jobPath := args[0]

	// Determine if it's a file or needs to be resolved
	var planDir string
	var jobFile string

	// Check if it's an absolute path or contains a separator
	if filepath.IsAbs(jobPath) || filepath.Dir(jobPath) != "." {
		// Extract directory and filename
		planDir = filepath.Dir(jobPath)
		jobFile = filepath.Base(jobPath)
	} else {
		// Just a filename, use current directory
		planDir = "."
		jobFile = jobPath
	}

	// Load the plan
	plan, err := orchestration.LoadPlan(planDir)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}

	// Find the job
	job, found := plan.GetJobByFilename(jobFile)
	if !found {
		return fmt.Errorf("job not found: %s", jobFile)
	}

	// Use the shared completion function (not silent for CLI)
	return completeJob(job, plan, false)
}

// getWorktreePathFromSession reads the working_directory from the session metadata.
func getWorktreePathFromSession(jobID string) (string, error) {
	// Expand home directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home directory: %w", err)
	}

	sessionsDir := filepath.Join(homeDir, ".grove", "hooks", "sessions")

	// Look for any session directory that contains this job ID in its metadata
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return "", fmt.Errorf("read sessions directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		sessionDir := filepath.Join(sessionsDir, entry.Name())
		metadataFile := filepath.Join(sessionDir, "metadata.json")

		// Read metadata to check if it matches our job ID
		metadataBytes, err := os.ReadFile(metadataFile)
		if err != nil {
			continue // Skip if we can't read metadata
		}

		var metadata struct {
			SessionID        string `json:"session_id"`
			WorkingDirectory string `json:"working_directory"`
		}
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			continue // Skip if metadata is invalid
		}

		// Check if this session matches our job ID
		if metadata.SessionID == jobID {
			return metadata.WorkingDirectory, nil
		}
	}

	return "", fmt.Errorf("no session found for job ID: %s", jobID)
}

// killClaudeSession kills the Claude process associated with the given job ID
// by reading the PID from grove-hooks session metadata.
func killClaudeSession(jobID string) error {
	// Expand home directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home directory: %w", err)
	}

	sessionsDir := filepath.Join(homeDir, ".grove", "hooks", "sessions")

	// Look for any session directory that contains this job ID in its metadata
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return fmt.Errorf("read sessions directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		sessionDir := filepath.Join(sessionsDir, entry.Name())
		metadataFile := filepath.Join(sessionDir, "metadata.json")

		// Read metadata to check if it matches our job ID
		metadataBytes, err := os.ReadFile(metadataFile)
		if err != nil {
			continue // Skip if we can't read metadata
		}

		var metadata struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			continue // Skip if metadata is invalid
		}

		// Check if this session matches our job ID
		if metadata.SessionID == jobID {
			// Read the PID
			pidFile := filepath.Join(sessionDir, "pid.lock")
			pidBytes, err := os.ReadFile(pidFile)
			if err != nil {
				return fmt.Errorf("read pid file: %w", err)
			}

			pidStr := strings.TrimSpace(string(pidBytes))
			pid, err := strconv.Atoi(pidStr)
			if err != nil {
				return fmt.Errorf("parse pid: %w", err)
			}

			// Check if process exists
			process, err := os.FindProcess(pid)
			if err != nil {
				return fmt.Errorf("find process: %w", err)
			}

			// Send SIGTERM to gracefully terminate
			if err := process.Signal(syscall.SIGTERM); err != nil {
				// Process might already be dead, which is fine
				return fmt.Errorf("kill process: %w", err)
			}

			return nil
		}
	}

	return fmt.Errorf("no session found for job ID: %s", jobID)
}
