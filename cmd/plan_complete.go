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
	Short: "Mark a job as completed (use: flow complete)",
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

// NewCompleteCmd creates the top-level `complete` command.
func NewCompleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "complete <job-file>",
		Short: "Mark a job as completed",
		Long: `Mark a job as completed. This is especially useful for chat jobs
that would otherwise remain in pending_user status indefinitely.

Examples:
  # Complete a chat job
  flow complete my-project/plan.md`,
		Args: cobra.ExactArgs(1),
		RunE: runPlanComplete,
	}
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

		// Archive session artifacts if it's an interactive agent job
		if job.Type == orchestration.JobTypeInteractiveAgent || job.Type == orchestration.JobTypeHeadlessAgent {
			if !silent {
				fmt.Println("Archiving session artifacts...")
			}
			if err := orchestration.ArchiveInteractiveSession(job, plan); err != nil {
				// Log a warning but don't fail the entire completion process.
				if !silent {
					fmt.Printf("Warning: failed to archive session artifacts: %v\n", err)
				}
			} else if !silent {
				fmt.Println(color.GreenString("✓") + " Session artifacts archived.")
			}
		}

		// Append transcript if it's an agent job
		if job.Type == orchestration.JobTypeInteractiveAgent || job.Type == orchestration.JobTypeHeadlessAgent {
			if !silent {
				fmt.Println("Appending agent session transcript...")
			}
			if err := orchestration.AppendAgentTranscript(job, plan); err != nil {
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

	// If this was an interactive agent, try to kill its associated agent process and tmux session.
	if job.Type == orchestration.JobTypeInteractiveAgent {
		if !silent {
			fmt.Println("Attempting to clean up associated agent session...")
		}

		// Kill the agent process by reading the PID from grove-hooks session metadata
		if err := killAgentSession(job.ID); err != nil {
			if !silent {
				fmt.Printf("  Note: could not kill agent session: %v\n", err)
			}
		} else if !silent {
			fmt.Println("  ✓ Agent process killed.")
		}

		// Also kill the tmux window for any interactive_agent job
		// First try to read the session metadata to get the working directory
		worktreePath, err := getWorktreePathFromSession(job.ID)

		// If we can't find the session (e.g., resumed job), fall back to using the job's worktree
		if err != nil && job.Worktree != "" {
			// Determine worktree path from the plan's git root
			gitRoot, gitErr := orchestration.GetGitRootSafe(plan.Directory)
			if gitErr == nil {
				// If gitRoot is itself a worktree, find the actual main repository root
				gitRootInfo, gitRootErr := workspace.GetProjectByPath(gitRoot)
				if gitRootErr == nil && gitRootInfo.IsWorktree() && gitRootInfo.ParentProjectPath != "" {
					gitRoot = gitRootInfo.ParentProjectPath
				}
				worktreePath = filepath.Join(gitRoot, ".grove-worktrees", job.Worktree)
				err = nil // Clear the error since we found it via worktree
			}
		}

		if err != nil {
			if !silent {
				fmt.Printf("  Note: could not determine working directory: %v\n", err)
			}
		} else {
			projInfo, err := workspace.GetProjectByPath(worktreePath)
			if err != nil {
				if !silent {
					fmt.Printf("  Note: could not get project info to determine session name: %v\n", err)
				}
			} else {
				sessionName := projInfo.Identifier()
				// Replicate window name logic from interactive_agent_executor
				windowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)

				// First, try exact match
				targetWindow := fmt.Sprintf("%s:%s", sessionName, windowName)
				if !silent {
					fmt.Printf("  Closing tmux window: %s\n", targetWindow)
				}
				cmd := exec.Command("tmux", "kill-window", "-t", targetWindow)
				err := cmd.Run()

				// If exact match fails, try to find windows with this prefix
				// (tmux may add numeric suffixes like "job-hi5-" for duplicate names)
				if err != nil {
					listCmd := exec.Command("tmux", "list-windows", "-t", sessionName, "-F", "#{window_name}")
					output, listErr := listCmd.Output()
					if listErr == nil {
						windows := strings.Split(strings.TrimSpace(string(output)), "\n")
						for _, win := range windows {
							if strings.HasPrefix(win, windowName) {
								targetWindow = fmt.Sprintf("%s:%s", sessionName, win)
								if !silent {
									fmt.Printf("  Found window with prefix: %s\n", targetWindow)
								}
								killCmd := exec.Command("tmux", "kill-window", "-t", targetWindow)
								if killErr := killCmd.Run(); killErr == nil {
									if !silent {
										fmt.Println("  ✓ Tmux window closed.")
									}
									err = nil // Clear the error
									break
								}
							}
						}
					}
				}

				if err != nil {
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

// findAgentSessionInfo finds the PID and session directory for an agent session associated with a job ID.
func findAgentSessionInfo(jobID string) (pid int, sessionDir string, err error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return 0, "", fmt.Errorf("get home directory: %w", err)
	}

	sessionsDir := filepath.Join(homeDir, ".grove", "hooks", "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, "", fmt.Errorf("sessions directory not found: %s", sessionsDir)
		}
		return 0, "", fmt.Errorf("read sessions directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		currentSessionDir := filepath.Join(sessionsDir, entry.Name())
		metadataFile := filepath.Join(currentSessionDir, "metadata.json")

		metadataBytes, err := os.ReadFile(metadataFile)
		if err != nil {
			continue
		}

		var metadata struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			continue
		}

		if metadata.SessionID == jobID {
			// Found the session, now get the PID
			pidFile := filepath.Join(currentSessionDir, "pid.lock")
			pidBytes, err := os.ReadFile(pidFile)
			if err != nil {
				return 0, "", fmt.Errorf("read pid file for session %s: %w", jobID, err)
			}

			pidStr := strings.TrimSpace(string(pidBytes))
			pid, err := strconv.Atoi(pidStr)
			if err != nil {
				return 0, "", fmt.Errorf("parse pid for session %s: %w", jobID, err)
			}

			return pid, currentSessionDir, nil
		}
	}

	return 0, "", fmt.Errorf("no session found for job ID: %s", jobID)
}

// getWorktreePathFromSession reads the working_directory from the session metadata.
func getWorktreePathFromSession(jobID string) (string, error) {
	_, sessionDir, err := findAgentSessionInfo(jobID)
	if err != nil {
		return "", err
	}

	metadataFile := filepath.Join(sessionDir, "metadata.json")
	metadataBytes, err := os.ReadFile(metadataFile)
	if err != nil {
		return "", fmt.Errorf("could not read metadata from found session: %w", err)
	}

	var metadata struct {
		WorkingDirectory string `json:"working_directory"`
	}
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		return "", fmt.Errorf("could not parse metadata from found session: %w", err)
	}

	return metadata.WorkingDirectory, nil
}

// killAgentSession kills the agent process associated with the given job ID
// by reading the PID from grove-hooks session metadata.
func killAgentSession(jobID string) error {
	pid, _, err := findAgentSessionInfo(jobID)
	if err != nil {
		return err // The error from findAgentSessionInfo is already descriptive
	}

	// Check if process exists
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process: %w", err)
	}

	// Send SIGTERM to gracefully terminate
	if err := process.Signal(syscall.SIGTERM); err != nil {
		// Process might already be dead, which is fine
		if !strings.Contains(err.Error(), "process already finished") {
			return fmt.Errorf("kill process: %w", err)
		}
	}

	return nil
}
