package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	grovelogging "github.com/mattsolo1/grove-core/logging"
	"github.com/mattsolo1/grove-core/pkg/tmux"
	"github.com/mattsolo1/grove-core/pkg/workspace"
	"github.com/mattsolo1/grove-flow/pkg/exec"
	proxy_config "github.com/mattsolo1/grove-proxy/pkg/config"
	"github.com/sirupsen/logrus"
)

// InteractiveAgentExecutor executes interactive agent jobs in tmux sessions.
type InteractiveAgentExecutor struct {
	skipInteractive bool
	log             *logrus.Entry
	prettyLog       *grovelogging.PrettyLogger
}

// NewInteractiveAgentExecutor creates a new interactive agent executor.
func NewInteractiveAgentExecutor(skipInteractive bool) *InteractiveAgentExecutor {
	return &InteractiveAgentExecutor{
		skipInteractive: skipInteractive,
		log:             grovelogging.NewLogger("grove-flow"),
		prettyLog:       grovelogging.NewPrettyLogger(),
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
		// Check if we're already in the requested worktree to avoid duplicate paths
		currentPath := gitRoot
		if strings.HasSuffix(currentPath, filepath.Join(".grove-worktrees", job.Worktree)) {
			// We're already in the requested worktree, use the current directory
			workDir = currentPath
		} else {
			// A worktree is specified, so prepare it using the centralized helper with repos filter
			// If gitRoot is already a worktree path, we need to find the actual git root
			// by going up the directory tree to find the main repository
			actualGitRoot := gitRoot
			if strings.Contains(gitRoot, ".grove-worktrees") {
				// Extract the main repository path by removing the worktree portion
				parts := strings.Split(gitRoot, ".grove-worktrees")
				if len(parts) > 0 {
					actualGitRoot = strings.TrimSuffix(parts[0], string(filepath.Separator))
				}
			}
			
			// The new logic:
			opts := workspace.PrepareOptions{
				GitRoot:      actualGitRoot,
				WorktreeName: job.Worktree,
				BranchName:   job.Worktree,
				PlanName:     plan.Name,
			}

			if plan.Config != nil && len(plan.Config.Repos) > 0 {
				opts.Repos = plan.Config.Repos
			}

			worktreePath, err := workspace.Prepare(ctx, opts)
			if err != nil {
				job.Status = JobStatusFailed
				job.EndTime = time.Now()
				return fmt.Errorf("failed to prepare host worktree: %w", err)
			}
			workDir = worktreePath
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
			e.log.WithField("session", sessionName).Info("Session already exists")
			e.prettyLog.Success(fmt.Sprintf("Session '%s' already exists.", sessionName))
			
			// Build agent command
			fullCfg, _ := proxy_config.LoadFrom(".")
			var agentArgs []string
			if fullCfg != nil && fullCfg.Agent != nil {
				agentArgs = fullCfg.Agent.Args
			}
			agentCommand, err := e.buildAgentCommand(job, plan, workDir, agentArgs)
			if err != nil {
				job.Status = JobStatusFailed
				job.EndTime = time.Now()
				return fmt.Errorf("failed to build agent command: %w", err)
			}

			// Create a new window for this agent job in the existing session
			executor := &exec.RealCommandExecutor{}
			agentWindowName := tmux.SanitizeForTmuxSession(job.Title)
			
			// Create new window for the agent
			e.log.WithField("window", agentWindowName).Info("Creating window for agent")
			e.prettyLog.InfoPretty(fmt.Sprintf("Creating window '%s' for agent...", agentWindowName))
			if err := executor.Execute("tmux", "new-window", "-t", sessionName, "-n", agentWindowName, "-c", workDir); err != nil {
				e.log.WithError(err).Warn("Failed to create agent window")
				e.prettyLog.WarnPretty(fmt.Sprintf("Warning: failed to create agent window: %v", err))
			} else {
				// Send the agent command to the new window
				targetPane := fmt.Sprintf("%s:%s", sessionName, agentWindowName)
				if err := executor.Execute("tmux", "send-keys", "-t", targetPane, agentCommand, "C-m"); err != nil {
					e.log.WithError(err).Warn("Failed to send agent command")
					e.prettyLog.WarnPretty(fmt.Sprintf("Warning: failed to send agent command: %v", err))
				}
				
				// Switch to the agent window
				if os.Getenv("TMUX") != "" {
					if err := executor.Execute("tmux", "select-window", "-t", targetPane); err != nil {
						e.log.WithError(err).Warn("Failed to switch to agent window")
						e.prettyLog.WarnPretty(fmt.Sprintf("Warning: failed to switch to agent window: %v", err))
					}
				}
			}
			
			e.prettyLog.Blank()
			e.prettyLog.InfoPretty("👉 Exit the tmux session when done to continue the plan.")
			e.prettyLog.InfoPretty("💡 To mark as complete without closing, run in another terminal:")
			e.prettyLog.InfoPretty(fmt.Sprintf("   flow plan complete %s", job.FilePath))

			// Wait for the existing session to close
			err = tmuxClient.WaitForSessionClose(ctx, sessionName, 2*time.Second)

			// Handle interruption
			if err != nil {
				if ctx.Err() != nil {
					e.log.WithField("session", sessionName).Warn("Interrupted. Session will continue running")
					e.prettyLog.WarnPretty(fmt.Sprintf("Interrupted. Session '%s' will continue running.", sessionName))
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
				e.log.WithField("session", sessionName).Info("Session marked as completed")
				e.prettyLog.Success(fmt.Sprintf("Session '%s' marked as completed. Continuing plan...", sessionName))
				job.Status = JobStatusCompleted
			case "f":
				e.log.WithField("session", sessionName).Info("Session marked as failed")
				e.prettyLog.ErrorPretty(fmt.Sprintf("Session '%s' marked as failed.", sessionName), nil)
				job.Status = JobStatusFailed
			case "q":
				e.log.WithField("session", sessionName).Info("Session closed with no status change")
				e.prettyLog.InfoPretty(fmt.Sprintf("⏸️  Session '%s' closed with no status change.", sessionName))
				// Keep current status (should still be Running)
			}

			job.EndTime = time.Now()
			return nil
		}

		// Build agent command
		fullCfg, _ := proxy_config.LoadFrom(".")
		var agentArgs []string
		if fullCfg != nil && fullCfg.Agent != nil {
			agentArgs = fullCfg.Agent.Args
		}
		agentCommand, err := e.buildAgentCommand(job, plan, workDir, agentArgs)
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

		e.log.WithField("session", sessionName).Info("Launching interactive session")
		e.prettyLog.InfoPretty(fmt.Sprintf("🚀 Launching interactive session '%s'...", sessionName))
		if err := tmuxClient.Launch(ctx, opts); err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to launch tmux session: %w", err)
		}

		// Create second window for the agent
		executor := &exec.RealCommandExecutor{}
		agentWindowName := tmux.SanitizeForTmuxSession(job.Title)
		
		// Create new window for the agent
		if err := executor.Execute("tmux", "new-window", "-t", sessionName, "-n", agentWindowName, "-c", workDir); err != nil {
			e.log.WithError(err).Warn("Failed to create agent window")
			e.prettyLog.WarnPretty(fmt.Sprintf("Warning: failed to create agent window: %v", err))
		} else {
			// Send the agent command to the new window
			targetPane := fmt.Sprintf("%s:%s", sessionName, agentWindowName)
			if err := executor.Execute("tmux", "send-keys", "-t", targetPane, agentCommand, "C-m"); err != nil {
				e.log.WithError(err).Warn("Failed to send agent command")
				e.prettyLog.WarnPretty(fmt.Sprintf("Warning: failed to send agent command: %v", err))
			}
			
			// Switch to the agent window
			if err := executor.Execute("tmux", "select-window", "-t", targetPane); err != nil {
				e.log.WithError(err).Warn("Failed to switch to agent window")
				e.prettyLog.WarnPretty(fmt.Sprintf("Warning: failed to switch to agent window: %v", err))
			}
		}

		// Check if we're already in a tmux session
		if os.Getenv("TMUX") != "" {
			// We're already in tmux, so switch to the new session
			e.log.WithField("session", sessionName).Info("Switching to session")
			e.prettyLog.Success(fmt.Sprintf("Switching to session '%s'...", sessionName))
			executor := &exec.RealCommandExecutor{}
			if err := executor.Execute("tmux", "switch-client", "-t", sessionName); err != nil {
				// If switch fails, just print instructions
				e.log.WithError(err).Warn("Could not switch to session")
				e.prettyLog.InfoPretty(fmt.Sprintf("   Could not switch to session. Attach with: tmux attach -t %s", sessionName))
			}
		} else {
			// Not in tmux, just print instructions
			e.prettyLog.InfoPretty(fmt.Sprintf("   Attach with: tmux attach -t %s", sessionName))
		}

		e.prettyLog.Blank()
		e.prettyLog.InfoPretty("👉 When your task is complete, run the following in any terminal:")
		e.prettyLog.InfoPretty(fmt.Sprintf("   flow plan complete %s", job.FilePath))
		e.prettyLog.Blank()
		e.prettyLog.InfoPretty("   The session can remain open - the plan will continue automatically.")

		// Return immediately - the orchestrator will poll for completion
		// Set execErr to nil to indicate successful launch
		execErr = nil
		return nil
	}

	// Original behavior for jobs without worktrees - use repository-based session with new window
	repoName := filepath.Base(gitRoot)
	sessionName := tmux.SanitizeForTmuxSession(repoName)
	windowName := "job-" + tmux.SanitizeForTmuxSession(job.Title)

	// Ensure session exists
	sessionExists, _ := tmuxClient.SessionExists(ctx, sessionName)
	if !sessionExists {
		e.log.WithField("session", sessionName).Info("Tmux session not found, creating it")
		e.prettyLog.Success(fmt.Sprintf("Tmux session '%s' not found, creating it...", sessionName))
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
	e.log.WithFields(logrus.Fields{
		"session": sessionName,
		"window":  windowName,
		"workdir": workDir,
	}).Info("Creating tmux window")
	e.prettyLog.InfoPretty(fmt.Sprintf("Creating tmux window: session=%s, window=%s, workdir=%s", sessionName, windowName, workDir))
	
	// Create a RealCommandExecutor like chat/plan launch do
	executor := &exec.RealCommandExecutor{}
	
	// Create the window with the same pattern as chat/plan launch
	if err := executor.Execute("tmux", "new-window", "-t", sessionName, "-n", windowName, "-c", workDir); err != nil {
		// Check if it's because window already exists
		if strings.Contains(err.Error(), "duplicate window") {
			e.log.WithField("window", windowName).Info("Window already exists, attempting to kill it first")
			e.prettyLog.InfoPretty(fmt.Sprintf("Window '%s' already exists, attempting to kill it first", windowName))
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
	fullCfg, err := proxy_config.LoadFrom(".")
	if err != nil {
		// Proceed with minimal config
		fullCfg = &proxy_config.AppConfig{Agent: &proxy_config.AgentConfig{}}
	}

	var agentArgs []string
	if fullCfg != nil && fullCfg.Agent != nil {
		agentArgs = fullCfg.Agent.Args
	}
	agentCommand, err := e.buildAgentCommand(job, plan, workDir, agentArgs)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to build agent command: %w", err)
	}

	// Small delay to ensure window is ready
	time.Sleep(300 * time.Millisecond)

	// Send agent command to the window using executor pattern
	targetPane := fmt.Sprintf("%s:%s", sessionName, windowName)
	e.log.WithField("pane", targetPane).Info("Sending command to tmux pane")
	e.prettyLog.InfoPretty(fmt.Sprintf("Sending command to tmux pane: %s", targetPane))
	if err := executor.Execute("tmux", "send-keys", "-t", targetPane, agentCommand, "C-m"); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to send agent command to pane '%s': %w", targetPane, err)
	}

	// Print user instructions
	e.log.WithField("window", windowName).Info("Interactive host session launched")
	e.prettyLog.InfoPretty(fmt.Sprintf("🚀 Interactive host session launched in window '%s'.", windowName))
	e.prettyLog.InfoPretty(fmt.Sprintf("   Attach with: tmux attach -t %s", sessionName))
	e.prettyLog.Blank()
	e.prettyLog.InfoPretty("👉 When your task is complete, run the following in any terminal:")
	e.prettyLog.InfoPretty(fmt.Sprintf("   flow plan complete %s", job.FilePath))
	e.prettyLog.Blank()
	e.prettyLog.InfoPretty("   The session can remain open - the plan will continue automatically.")

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


// promptForJobStatus prompts the user to select the job status after tmux session ends
func (e *InteractiveAgentExecutor) promptForJobStatus(sessionOrWindowName string) string {
	e.prettyLog.Blank()
	e.prettyLog.InfoPretty(fmt.Sprintf("💭 Session '%s' has ended. What's the job status?", sessionOrWindowName))
	e.prettyLog.InfoPretty("   c - Mark as completed")
	e.prettyLog.InfoPretty("   f - Mark as failed")
	e.prettyLog.InfoPretty("   q - No status change (keep as running)")
	e.prettyLog.Blank()
	e.prettyLog.InfoPretty("Choice [c/f/q]: ")
	
	var response string
	fmt.Scanln(&response)
	response = strings.ToLower(strings.TrimSpace(response))
	
	// Default to "c" if user just presses enter
	if response == "" {
		response = "c"
	}
	
	// Validate response
	if response != "c" && response != "f" && response != "q" {
		e.log.WithField("choice", response).Warn("Invalid choice. Defaulting to completed")
		e.prettyLog.WarnPretty(fmt.Sprintf("Invalid choice '%s'. Defaulting to completed.", response))
		response = "c"
	}
	
	return response
}


