package orchestration

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattsolo1/grove-core/config"
	"github.com/mattsolo1/grove-core/git"
	"github.com/mattsolo1/grove-core/pkg/tmux"
	"github.com/mattsolo1/grove-flow/pkg/exec"
)

// InteractiveAgentExecutor executes interactive agent jobs in tmux sessions.
type InteractiveAgentExecutor struct {
	skipInteractive bool
}

// NewInteractiveAgentExecutor creates a new interactive agent executor.
func NewInteractiveAgentExecutor(skipInteractive bool) *InteractiveAgentExecutor {
	return &InteractiveAgentExecutor{
		skipInteractive: skipInteractive,
	}
}

// Name returns the executor name.
func (e *InteractiveAgentExecutor) Name() string {
	return "interactive_agent"
}

// Execute runs an interactive agent job in a tmux session and blocks until completion.
func (e *InteractiveAgentExecutor) Execute(ctx context.Context, job *Job, plan *Plan) error {
	// Check if interactive jobs should be skipped
	if e.skipInteractive {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("interactive agent job skipped due to --skip-interactive flag")
	}

	// Always run in host mode - no container dependencies
	return e.executeHostMode(ctx, job, plan)
}


// buildAgentCommand constructs the agent command for the interactive session.
func (e *InteractiveAgentExecutor) buildAgentCommand(job *Job, plan *Plan, worktreePath string, agentArgs []string) (string, error) {
	// Build instruction for the agent
	instruction := fmt.Sprintf("Read the file %s and execute the agent job defined there. ", job.FilePath)

	// Add dependency files if the job has dependencies
	if len(job.Dependencies) > 0 {
		instruction += "For additional context from previous jobs, also read: "
		var depFiles []string
		for _, dep := range job.Dependencies {
			if dep != nil && dep.FilePath != "" {
				depFiles = append(depFiles, dep.FilePath)
			}
		}
		instruction += strings.Join(depFiles, ", ")
		instruction += ". "
	}

	// Add context files if specified
	if len(job.PromptSource) > 0 {
		instruction += "Also read these context files: "
		var contextFiles []string
		for _, source := range job.PromptSource {
			resolved, err := ResolvePromptSource(source, plan)
			relPath := source // fallback
			if err == nil {
				if p, err := filepath.Rel(worktreePath, resolved); err == nil {
					relPath = p
				} else {
					relPath = resolved
				}
			}
			contextFiles = append(contextFiles, relPath)
		}
		instruction += strings.Join(contextFiles, ", ")
	}

	// Shell escape the instruction
	escapedInstruction := "'" + strings.ReplaceAll(instruction, "'", "'\\''") + "'"

	// Build command with agent args
	cmdParts := []string{"claude"}
	if job.AgentContinue {
		cmdParts = append(cmdParts, "--continue")
		cmdParts = append(cmdParts, agentArgs...)
		return fmt.Sprintf("echo %s | %s", escapedInstruction, strings.Join(cmdParts, " ")), nil
	}
	
	cmdParts = append(cmdParts, agentArgs...)
	cmdParts = append(cmdParts, escapedInstruction)

	return strings.Join(cmdParts, " "), nil
}

// generateSessionName creates a unique session name for the interactive job.
func (e *InteractiveAgentExecutor) generateSessionName(plan *Plan, job *Job) string {
	// Use worktree name if available, otherwise fall back to plan name and job title
	var sessionName string
	if job.Worktree != "" {
		// Use just the worktree name
		sessionName = job.Worktree
	} else {
		// Fall back to original behavior: plan name + job title
		sanitized := strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
				return r
			}
			return '-'
		}, job.Title)

		// Limit length and remove leading/trailing dashes
		if len(sanitized) > 50 {
			sanitized = sanitized[:50]
		}
		sanitized = strings.Trim(sanitized, "-")

		sessionName = fmt.Sprintf("%s__%s", plan.Name, sanitized)
	}

	// Sanitize the final session name for tmux compatibility
	sessionName = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, sessionName)

	// Limit length and remove leading/trailing dashes
	if len(sessionName) > 100 {
		sessionName = sessionName[:100]
	}
	sessionName = strings.Trim(sessionName, "-")

	return sessionName
}

// executeHostMode runs the job directly on the host machine with tmux
func (e *InteractiveAgentExecutor) executeHostMode(ctx context.Context, job *Job, plan *Plan) error {
	// Update job status to running
	job.Status = JobStatusRunning
	job.StartTime = time.Now()
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}

	// Notify grove-hooks about job start
	notifyJobStart(job, plan)

	// Ensure we notify completion/failure when we exit
	var execErr error
	defer func() {
		notifyJobComplete(job, execErr)
	}()


	// Determine the working directory for the job on the host
	var workDir string
	gitRoot, err := GetGitRootSafe(plan.Directory)
	if err != nil {
		return fmt.Errorf("could not find git root: %w", err)
	}

	if job.Worktree != "" {
		// Check if we're already in the worktree
		currentDir, _ := os.Getwd()
		
		// Check if current directory is already a worktree for this job
		// This handles cases where gitRoot might be the worktree itself
		if currentDir != "" && (strings.HasSuffix(currentDir, "/.grove-worktrees/"+job.Worktree) || 
			strings.HasSuffix(gitRoot, "/.grove-worktrees/"+job.Worktree)) {
			// We're already in the worktree
			workDir = currentDir
		} else {
			// Need to find the actual git root (not a worktree)
			// If gitRoot ends with .grove-worktrees/something, go up to find real root
			realGitRoot := gitRoot
			if idx := strings.Index(gitRoot, "/.grove-worktrees/"); idx != -1 {
				realGitRoot = gitRoot[:idx]
			}
			
			expectedWorktreePath := filepath.Join(realGitRoot, ".grove-worktrees", job.Worktree)
			
			// A worktree is specified, so create/use it on the host
			wm := git.NewWorktreeManager()
			worktreePath, err := wm.GetOrPrepareWorktree(ctx, realGitRoot, job.Worktree, "")
			if err != nil {
				// Check if it's because the worktree already exists
				if strings.Contains(err.Error(), "already checked out") || strings.Contains(err.Error(), "already exists") {
					// Worktree exists, just use it
					workDir = expectedWorktreePath
					
				} else {
					job.Status = JobStatusFailed
					job.EndTime = time.Now()
					return fmt.Errorf("failed to prepare host worktree: %w", err)
				}
			} else {
				workDir = worktreePath
				
				// Check if grove-hooks is available and install hooks in the new worktree
				if _, err := osexec.LookPath(GetHooksBinaryPath()); err == nil {
					cmd := osexec.Command(GetHooksBinaryPath(), "install")
					cmd.Dir = worktreePath
					if output, err := cmd.CombinedOutput(); err != nil {
						fmt.Printf("Warning: grove-hooks install failed: %v (output: %s)\n", err, string(output))
					} else {
						fmt.Printf("✓ Installed grove-hooks in worktree: %s\n", worktreePath)
					}
				}
				
				// Set up Go workspace if this is a Go project
				if err := SetupGoWorkspaceForWorktree(workDir, realGitRoot); err != nil {
					// Log a warning but don't fail the job, as this is a convenience feature
					fmt.Printf("Warning: failed to setup Go workspace in worktree: %v\n", err)
				}
			}
		}

		// Automatically initialize state within the new worktree for a better UX.
		groveDir := filepath.Join(workDir, ".grove")
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
	} else {
		// No worktree, use the main git repository root
		workDir = gitRoot
	}

	// Create tmux client
	tmuxClient, err := tmux.NewClient()
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("tmux not available: %w", err)
	}

	// Check if job has a worktree - if so, create a new session; otherwise use existing behavior
	if job.Worktree != "" {
		// For jobs with worktrees, create a new isolated session
		sessionName := e.generateSessionName(plan, job)
		
		// Check if session already exists
		sessionExists, _ := tmuxClient.SessionExists(ctx, sessionName)
		if sessionExists {
			fmt.Printf("✓ Session '%s' already exists.\n", sessionName)
			
			// Build agent command
			fullCfg, _ := config.LoadFrom(".")
			agentCommand, err := e.buildAgentCommand(job, plan, workDir, fullCfg.Agent.Args)
			if err != nil {
				job.Status = JobStatusFailed
				job.EndTime = time.Now()
				return fmt.Errorf("failed to build agent command: %w", err)
			}

			// Create a new window for this agent job in the existing session
			executor := &exec.RealCommandExecutor{}
			agentWindowName := sanitizeForTmuxSession(job.Title)
			
			// Create new window for the agent
			fmt.Printf("Creating window '%s' for agent...\n", agentWindowName)
			if err := executor.Execute("tmux", "new-window", "-t", sessionName, "-n", agentWindowName, "-c", workDir); err != nil {
				fmt.Printf("Warning: failed to create agent window: %v\n", err)
			} else {
				// Send the agent command to the new window
				targetPane := fmt.Sprintf("%s:%s", sessionName, agentWindowName)
				if err := executor.Execute("tmux", "send-keys", "-t", targetPane, agentCommand, "C-m"); err != nil {
					fmt.Printf("Warning: failed to send agent command: %v\n", err)
				}
				
				// Switch to the agent window
				if os.Getenv("TMUX") != "" {
					if err := executor.Execute("tmux", "select-window", "-t", targetPane); err != nil {
						fmt.Printf("Warning: failed to switch to agent window: %v\n", err)
					}
				}
			}
			
			fmt.Printf("\n👉 Exit the tmux session when done to continue the plan.\n")
			fmt.Printf("💡 To mark as complete without closing, run in another terminal:\n")
			fmt.Printf("   flow plan complete %s\n", job.FilePath)

			// Wait for the existing session to close
			err = tmuxClient.WaitForSessionClose(ctx, sessionName, 2*time.Second)

			// Handle interruption
			if err != nil {
				if ctx.Err() != nil {
					fmt.Printf("\n⚠️  Interrupted. Session '%s' will continue running.\n", sessionName)
					job.Status = JobStatusFailed
					job.EndTime = time.Now()
					return fmt.Errorf("interrupted by user")
				}
				job.Status = JobStatusFailed
				job.EndTime = time.Now()
				return fmt.Errorf("wait for session failed: %w", err)
			}

			// Success - prompt for job status
			status := e.promptForJobStatus(sessionName)

			switch status {
			case "c":
				fmt.Printf("✅ Session '%s' marked as completed. Continuing plan...\n", sessionName)
				job.Status = JobStatusCompleted
			case "f":
				fmt.Printf("❌ Session '%s' marked as failed.\n", sessionName)
				job.Status = JobStatusFailed
			case "q":
				fmt.Printf("⏸️  Session '%s' closed with no status change.\n", sessionName)
				// Keep current status (should still be Running)
			}

			job.EndTime = time.Now()
			return nil
		}

		// Build agent command
		fullCfg, _ := config.LoadFrom(".")
		agentCommand, err := e.buildAgentCommand(job, plan, workDir, fullCfg.Agent.Args)
		if err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to build agent command: %w", err)
		}

		// Launch a NEW tmux session with two windows
		// First window is a blank shell in the worktree
		opts := tmux.LaunchOptions{
			SessionName:      sessionName,
			WorkingDirectory: workDir,
			WindowName:       "workspace",  // First window for working in the worktree
			Panes: []tmux.PaneOptions{
				{
					Command: "", // Empty command = default shell
				},
			},
		}

		fmt.Printf("🚀 Launching interactive session '%s'...\n", sessionName)
		if err := tmuxClient.Launch(ctx, opts); err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to launch tmux session: %w", err)
		}

		// Create second window for the agent
		executor := &exec.RealCommandExecutor{}
		agentWindowName := sanitizeForTmuxSession(job.Title)
		
		// Create new window for the agent
		if err := executor.Execute("tmux", "new-window", "-t", sessionName, "-n", agentWindowName, "-c", workDir); err != nil {
			fmt.Printf("Warning: failed to create agent window: %v\n", err)
		} else {
			// Send the agent command to the new window
			targetPane := fmt.Sprintf("%s:%s", sessionName, agentWindowName)
			if err := executor.Execute("tmux", "send-keys", "-t", targetPane, agentCommand, "C-m"); err != nil {
				fmt.Printf("Warning: failed to send agent command: %v\n", err)
			}
			
			// Switch to the agent window
			if err := executor.Execute("tmux", "select-window", "-t", targetPane); err != nil {
				fmt.Printf("Warning: failed to switch to agent window: %v\n", err)
			}
		}

		// Check if we're already in a tmux session
		if os.Getenv("TMUX") != "" {
			// We're already in tmux, so switch to the new session
			fmt.Printf("✓ Switching to session '%s'...\n", sessionName)
			executor := &exec.RealCommandExecutor{}
			if err := executor.Execute("tmux", "switch-client", "-t", sessionName); err != nil {
				// If switch fails, just print instructions
				fmt.Printf("   Could not switch to session. Attach with: tmux attach -t %s\n", sessionName)
			}
		} else {
			// Not in tmux, just print instructions
			fmt.Printf("   Attach with: tmux attach -t %s\n", sessionName)
		}

		fmt.Printf("\n👉 When your task is complete, run the following in any terminal:\n")
		fmt.Printf("   flow plan complete %s\n", job.FilePath)
		fmt.Printf("\n   The session can remain open - the plan will continue automatically.\n")

		// Return immediately - the orchestrator will poll for completion
		// Set execErr to nil to indicate successful launch
		execErr = nil
		return nil
	}

	// Original behavior for jobs without worktrees - use repository-based session with new window
	repoName := filepath.Base(gitRoot)
	sessionName := sanitizeForTmuxSession(repoName)
	windowName := "job-" + sanitizeForTmuxSession(job.Title)

	// Ensure session exists
	sessionExists, _ := tmuxClient.SessionExists(ctx, sessionName)
	if !sessionExists {
		fmt.Printf("✓ Tmux session '%s' not found, creating it...\n", sessionName)
		opts := tmux.LaunchOptions{
			SessionName:      sessionName,
			WorkingDirectory: gitRoot,
		}
		if err := tmuxClient.Launch(ctx, opts); err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to create tmux session: %w", err)
		}
	}

	// Create new window using RealCommandExecutor pattern (like chat and plan launch)
	fmt.Printf("Creating tmux window: session=%s, window=%s, workdir=%s\n", sessionName, windowName, workDir)
	
	// Create a RealCommandExecutor like chat/plan launch do
	executor := &exec.RealCommandExecutor{}
	
	// Create the window with the same pattern as chat/plan launch
	if err := executor.Execute("tmux", "new-window", "-t", sessionName, "-n", windowName, "-c", workDir); err != nil {
		// Check if it's because window already exists
		if strings.Contains(err.Error(), "duplicate window") {
			fmt.Printf("Window '%s' already exists, attempting to kill it first\n", windowName)
			// Kill the existing window
			executor.Execute("tmux", "kill-window", "-t", fmt.Sprintf("%s:%s", sessionName, windowName))
			time.Sleep(100 * time.Millisecond)
			
			// Try again
			if err := executor.Execute("tmux", "new-window", "-t", sessionName, "-n", windowName, "-c", workDir); err != nil {
				job.Status = JobStatusFailed
				job.EndTime = time.Now()
				return fmt.Errorf("failed to create new tmux window after killing existing: %w", err)
			}
		} else {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to create new tmux window: %w", err)
		}
	}

	// Build and send command
	fullCfg, err := config.LoadFrom(".")
	if err != nil {
		// Proceed with minimal config
		fullCfg = &config.Config{}
	}
	
	agentCommand, err := e.buildAgentCommand(job, plan, workDir, fullCfg.Agent.Args)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to build agent command: %w", err)
	}

	// Small delay to ensure window is ready
	time.Sleep(300 * time.Millisecond)

	// Send agent command to the window using executor pattern
	targetPane := fmt.Sprintf("%s:%s", sessionName, windowName)
	fmt.Printf("Sending command to tmux pane: %s\n", targetPane)
	if err := executor.Execute("tmux", "send-keys", "-t", targetPane, agentCommand, "C-m"); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to send agent command to pane '%s': %w", targetPane, err)
	}

	// Print user instructions
	fmt.Printf("🚀 Interactive host session launched in window '%s'.\n", windowName)
	fmt.Printf("   Attach with: tmux attach -t %s\n", sessionName)
	fmt.Printf("\n👉 When your task is complete, run the following in any terminal:\n")
	fmt.Printf("   flow plan complete %s\n", job.FilePath)
	fmt.Printf("\n   The session can remain open - the plan will continue automatically.\n")

	// Return immediately - the orchestrator will poll for completion
	// Set execErr to nil to indicate successful launch
	execErr = nil
	return nil
}

// waitForWindowClose waits for a specific tmux window to close
func (e *InteractiveAgentExecutor) waitForWindowClose(ctx context.Context, client *tmux.Client, sessionName, windowName string, pollInterval time.Duration) error {
	// For now, we'll use a simple approach: wait for the user to close the window
	// In the future, we could enhance this to check specific window status
	// But for now, we'll instruct the user to close the entire session when done
	return client.WaitForSessionClose(ctx, sessionName, pollInterval)
}

// sanitizeForTmuxSession creates a valid tmux session name from a string
func sanitizeForTmuxSession(name string) string {
	// Replace spaces and special characters with hyphens
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || 
		   (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, name)
	
	// Convert to lowercase for consistency
	sanitized = strings.ToLower(sanitized)
	
	// Remove consecutive hyphens
	for strings.Contains(sanitized, "--") {
		sanitized = strings.ReplaceAll(sanitized, "--", "-")
	}
	
	// Trim hyphens from start and end
	sanitized = strings.Trim(sanitized, "-")
	
	// Ensure it's not empty
	if sanitized == "" {
		sanitized = "session"
	}
	
	// Tmux session names should not be too long
	if len(sanitized) > 50 {
		sanitized = sanitized[:50]
	}
	
	return sanitized
}

// promptForJobStatus prompts the user to select the job status after tmux session ends
func (e *InteractiveAgentExecutor) promptForJobStatus(sessionOrWindowName string) string {
	fmt.Printf("\n💭 Session '%s' has ended. What's the job status?\n", sessionOrWindowName)
	fmt.Println("   c - Mark as completed")
	fmt.Println("   f - Mark as failed")
	fmt.Println("   q - No status change (keep as running)")
	fmt.Print("\nChoice [c/f/q]: ")
	
	var response string
	fmt.Scanln(&response)
	response = strings.ToLower(strings.TrimSpace(response))
	
	// Default to "c" if user just presses enter
	if response == "" {
		response = "c"
	}
	
	// Validate response
	if response != "c" && response != "f" && response != "q" {
		fmt.Printf("Invalid choice '%s'. Defaulting to completed.\n", response)
		response = "c"
	}
	
	return response
}


