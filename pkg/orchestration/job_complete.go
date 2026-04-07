package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fatih/color"
	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/tmux"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/util/sanitize"
	"github.com/sirupsen/logrus"
)

// CompleteJob is the shared function that handles job completion logic.
// It can be called from both the CLI and TUI.
// Set silent=true to suppress output (useful for TUI).
func CompleteJob(job *Job, plan *Plan, silent bool) error {
	// Log entry to trace who is calling completeJob
	logger := grovelogging.NewLogger("flow.complete")
	logger.WithFields(logrus.Fields{
		"job_id":     job.ID,
		"job_title":  job.Title,
		"job_status": job.Status,
		"silent":     silent,
	}).Info("CompleteJob called - tracing caller")

	// Check current status
	alreadyCompleted := job.Status == JobStatusCompleted
	if alreadyCompleted && !silent {
		fmt.Printf("Job already completed: %s\n", job.Filename)
		// Still need to clean up associated resources (Claude process, tmux window)
	}

	// For isolated agents, kill the agent FIRST before updating status.
	// The agent holds a lock on the job file, so we must terminate it first.
	if job.Type == JobTypeIsolatedAgent && !alreadyCompleted {
		if !silent {
			fmt.Println("Cleaning up isolated agent tmux server...")
		}

		if err := KillIsolatedAgentServer(job.ID); err != nil {
			if !silent {
				fmt.Printf("  Note: could not kill isolated tmux server (it may already be closed): %v\n", err)
			}
		} else if !silent {
			fmt.Println("  * Isolated tmux server terminated.")
		}

		// Also try to kill the agent process via session metadata
		if err := killAgentSession(job.ID); err != nil {
			if !silent {
				fmt.Printf("  Note: could not kill agent session: %v\n", err)
			}
		} else if !silent {
			fmt.Println("  * Agent process killed.")
		}

		// Give the process a moment to terminate, then remove the stale lock file
		time.Sleep(200 * time.Millisecond)
		if err := RemoveLockFile(job.FilePath); err != nil {
			if !silent {
				fmt.Printf("  Note: could not remove lock file: %v\n", err)
			}
		}
	}

	// Update status (skip if already completed)
	oldStatus := job.Status
	if !alreadyCompleted {
		job.Status = JobStatusCompleted

		// Use the state persister to update the job file
		persister := NewStatePersister()
		if err := persister.UpdateJobStatus(job, JobStatusCompleted); err != nil {
			return fmt.Errorf("update job status: %w", err)
		}

		// Notify the daemon that the session has ended
		daemonClient := daemon.NewWithAutoStart()
		if err := daemonClient.EndSession(context.Background(), job.ID, "completed"); err != nil {
			logger.WithError(err).Debug("Failed to notify daemon of session end")
		}
		daemonClient.Close()

		// Archive session artifacts if it's an agent job
		if job.Type == JobTypeInteractiveAgent || job.Type == JobTypeHeadlessAgent || job.Type == JobTypeIsolatedAgent {
			if !silent {
				fmt.Println("Archiving session artifacts...")
			}
			if err := ArchiveInteractiveSession(job, plan); err != nil {
				// Log a warning but don't fail the entire completion process.
				if !silent {
					fmt.Printf("Warning: failed to archive session artifacts: %v\n", err)
				}
			} else if !silent {
				fmt.Println(color.GreenString("*") + " Session artifacts archived.")
			}
		}

		// Append transcript if it's an agent job
		if job.Type == JobTypeInteractiveAgent || job.Type == JobTypeHeadlessAgent || job.Type == JobTypeIsolatedAgent {
			if !silent {
				fmt.Println("Appending agent session transcript...")
			}
			if err := AppendAgentTranscript(job, plan); err != nil {
				// Log warning but don't fail the command
				if !silent {
					fmt.Printf("Warning: failed to append transcript: %v\n", err)
				}
			} else if !silent {
				fmt.Println(color.GreenString("*") + " Appended session transcript.")
			}
		}
	}

	// If this was an interactive agent, try to kill its associated agent process and tmux session.
	if job.Type == JobTypeInteractiveAgent {
		if !silent {
			fmt.Println("Attempting to clean up associated agent session...")
		}

		// Kill the agent process by reading the PID from grove-hooks session metadata
		if err := killAgentSession(job.ID); err != nil {
			if !silent {
				fmt.Printf("  Note: could not kill agent session: %v\n", err)
			}
		} else if !silent {
			fmt.Println("  * Agent process killed.")
		}

		// Also kill the tmux window for any interactive_agent job
		// First try to read the session metadata to get the working directory
		worktreePath, err := getWorktreePathFromSession(job.ID)

		// If we can't find the session (e.g., resumed job), fall back to using the job's worktree
		if err != nil && job.Worktree != "" {
			// Determine worktree path from the plan's project git root (notebook-aware)
			gitRoot, gitErr := GetProjectGitRoot(plan.Directory)
			if gitErr == nil {
				// Skip worktree operations if this is a notebook repo
				if workspace.IsNotebookRepo(gitRoot) {
					// Don't error here - just skip the worktree resolution
					gitRoot = ""
					gitErr = fmt.Errorf("skipping notebook repo")
				}
			}
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
			projInfo, err := ResolveProjectForSessionNaming(worktreePath)
			if err != nil {
				if !silent {
					fmt.Printf("  Note: could not get project info to determine session name: %v\n", err)
				}
			} else {
				sessionName := projInfo.Identifier("_")
				// Replicate window name logic from interactive_agent_executor
				windowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)

				// First, try exact match
				targetWindow := fmt.Sprintf("%s:%s", sessionName, windowName)
				if !silent {
					fmt.Printf("  Closing tmux window: %s\n", targetWindow)
				}
				cmd := tmux.Command("kill-window", "-t", targetWindow)
				err := cmd.Run()

				// If exact match fails, try to find windows with this prefix
				// (tmux may add numeric suffixes like "job-hi5-" for duplicate names)
				if err != nil {
					listCmd := tmux.Command("list-windows", "-t", sessionName, "-F", "#{window_name}")
					output, listErr := listCmd.Output()
					if listErr == nil {
						windows := strings.Split(strings.TrimSpace(string(output)), "\n")
						for _, win := range windows {
							if strings.HasPrefix(win, windowName) {
								targetWindow = fmt.Sprintf("%s:%s", sessionName, win)
								if !silent {
									fmt.Printf("  Found window with prefix: %s\n", targetWindow)
								}
								killCmd := tmux.Command("kill-window", "-t", targetWindow)
								if killErr := killCmd.Run(); killErr == nil {
									if !silent {
										fmt.Println("  * Tmux window closed.")
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
					fmt.Println("  * Tmux window closed.")
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
			fmt.Printf("%s Job completed: %s\n", color.GreenString("*"), job.Title)
			fmt.Printf("Status: %s → %s\n", oldStatus, JobStatusCompleted)
		} else {
			fmt.Printf("%s Cleaned up resources for: %s\n", color.GreenString("*"), job.Title)
		}

		// Special message for chat jobs
		if job.Type == JobTypeChat && !alreadyCompleted {
			fmt.Printf("\nChat conversation ended. You can transform this chat into executable jobs using:\n")
			fmt.Printf("  flow plan add %s --template generate-plan --prompt-file %s\n",
				plan.Directory, job.Filename)
		}
	}

	return nil
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
