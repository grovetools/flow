package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattsolo1/grove-core/config"
	grovelogging "github.com/mattsolo1/grove-core/logging"
	"github.com/mattsolo1/grove-core/pkg/tmux"
	"github.com/mattsolo1/grove-core/pkg/workspace"
	"github.com/mattsolo1/grove-core/util/sanitize"
	"github.com/mattsolo1/grove-flow/pkg/exec"
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
	var instructionBuilder strings.Builder

	// Add dependency files if the job has dependencies
	if len(job.Dependencies) > 0 {
		var depFiles []string
		for _, dep := range job.Dependencies {
			if dep != nil && dep.FilePath != "" {
				depFiles = append(depFiles, dep.FilePath)
			}
		}

		if job.PrependDependencies {
			// New logic: Emphasize dependencies
			instructionBuilder.WriteString(fmt.Sprintf("CRITICAL CONTEXT: Before you do anything else, you MUST read and fully understand the content of the following files in order. They provide the primary context and requirements for your task: %s. ", strings.Join(depFiles, ", ")))
			instructionBuilder.WriteString(fmt.Sprintf("After you have processed that context, execute the agent job defined in %s. ", job.FilePath))
		} else {
			// Original logic
			instructionBuilder.WriteString(fmt.Sprintf("Read the file %s and execute the agent job defined there. ", job.FilePath))
			instructionBuilder.WriteString("For additional context from previous jobs, also read: ")
			instructionBuilder.WriteString(strings.Join(depFiles, ", "))
			instructionBuilder.WriteString(". ")
		}
	} else {
		// No dependencies, original logic
		instructionBuilder.WriteString(fmt.Sprintf("Read the file %s and execute the agent job defined there. ", job.FilePath))
	}

	// Add context files if specified
	if len(job.PromptSource) > 0 {
		instructionBuilder.WriteString("Also read these context files: ")
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
		instructionBuilder.WriteString(strings.Join(contextFiles, ", "))
	}

	instruction := instructionBuilder.String()

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
func (e *InteractiveAgentExecutor) generateSessionName(workDir string) (string, error) {
	projInfo, err := workspace.GetProjectByPath(workDir)
	if err != nil {
		return "", fmt.Errorf("failed to get project info for session naming: %w", err)
	}
	return projInfo.Identifier(), nil
}

// executeHostMode runs the job directly on the host machine with tmux
func (e *InteractiveAgentExecutor) executeHostMode(ctx context.Context, job *Job, plan *Plan) error {
	// Update job status to running
	job.Status = JobStatusRunning
	job.StartTime = time.Now()
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}

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

	// Regenerate context before launching the agent
	// We'll use the helper from the oneshot executor
	oneShotExec := NewOneShotExecutor(nil)
	if err := oneShotExec.regenerateContextInWorktree(workDir, "interactive-agent", job, plan); err != nil {
		// A context failure shouldn't block an interactive session, but we should warn the user.
		e.log.WithError(err).Warn("Failed to generate job-specific context for interactive session")
		e.prettyLog.WarnPretty(fmt.Sprintf("Warning: Failed to generate job-specific context: %v", err))
	}

	// Create tmux client
	tmuxClient, err := tmux.NewClient()
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("tmux not available: %w", err)
	}

	// Check if job has a worktree - if so, create/reuse a session; otherwise use existing behavior
	if job.Worktree != "" {
		// For jobs with worktrees, create/reuse a session based on the project identifier
		sessionName, err := e.generateSessionName(workDir)
		if err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return err
		}

		// Check if session already exists
		sessionExists, _ := tmuxClient.SessionExists(ctx, sessionName)
		executor := &exec.RealCommandExecutor{}

		if !sessionExists {
			// Create new session with a blank "workspace" window
			opts := tmux.LaunchOptions{
				SessionName:      sessionName,
				WorkingDirectory: workDir,
				WindowName:       "workspace",
				Panes: []tmux.PaneOptions{
					{
						Command: "", // Empty command = default shell
					},
				},
			}
			e.log.WithField("session", sessionName).Info("Creating new tmux session for interactive job")
			if err := tmuxClient.Launch(ctx, opts); err != nil {
				job.Status = JobStatusFailed
				job.EndTime = time.Now()
				return fmt.Errorf("failed to create tmux session: %w", err)
			}

			// Get the tmux session PID and create the lock file.
			tmuxPID, err := tmuxClient.GetSessionPID(ctx, sessionName)
			if err != nil {
				return fmt.Errorf("could not get tmux session PID to create lock file: %w", err)
			}
			if err := CreateLockFile(job.FilePath, tmuxPID); err != nil {
				return fmt.Errorf("failed to create lock file with tmux PID: %w", err)
			}
		} else {
			e.log.WithField("session", sessionName).Info("Using existing session for interactive job")
		}

		// Build agent command
		coreCfg, _ := config.LoadFrom(".")
		var agentArgs []string
		if coreCfg != nil {
			type agentConfig struct {
				Args []string `yaml:"args"`
			}
			var agentCfg agentConfig
			coreCfg.UnmarshalExtension("agent", &agentCfg)
			agentArgs = agentCfg.Args
		}
		agentCommand, err := e.buildAgentCommand(job, plan, workDir, agentArgs)
		if err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to build agent command: %w", err)
		}

		// Create a new window for this specific agent job in the session
		agentWindowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)

		e.log.WithField("window", agentWindowName).Info("Creating window for agent")
		e.prettyLog.InfoPretty(fmt.Sprintf("Creating window '%s' for agent...", agentWindowName))
		if err := executor.Execute("tmux", "new-window", "-t", sessionName, "-n", agentWindowName, "-c", workDir); err != nil {
			e.log.WithError(err).Warn("Failed to create agent window, may already exist. Will attempt to use it.")
			// Don't return error, just log and proceed.
		}

		// Send the agent command to the new window
		targetPane := fmt.Sprintf("%s:%s", sessionName, agentWindowName)
		if err := executor.Execute("tmux", "send-keys", "-t", targetPane, agentCommand, "C-m"); err != nil {
			e.log.WithError(err).Error("Failed to send agent command")
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to send agent command: %w", err)
		}

		// Switch to the agent window if inside tmux
		if os.Getenv("TMUX") != "" {
			if err := executor.Execute("tmux", "switch-client", "-t", sessionName); err != nil {
				e.log.WithError(err).Warn("Failed to switch to session")
			}
			if err := executor.Execute("tmux", "select-window", "-t", targetPane); err != nil {
				e.log.WithError(err).Warn("Failed to switch to agent window")
			}
		} else {
			e.prettyLog.InfoPretty(fmt.Sprintf("   Attach with: tmux attach -t %s", sessionName))
		}

		e.prettyLog.Blank()
		e.prettyLog.InfoPretty("👉 When your task is complete, run the following in any terminal:")
		e.prettyLog.InfoPretty(fmt.Sprintf("   flow plan complete %s", job.FilePath))
		e.prettyLog.Blank()
		e.prettyLog.InfoPretty("   The session can remain open - the plan will continue automatically.")

		// Return immediately. The lock file indicates the running state.
		return nil
	}

	// Original behavior for jobs without worktrees - use repository-based session with new window
	sessionName, err := e.generateSessionName(gitRoot)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return err
	}
	windowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)

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

		// Get the tmux session PID and create the lock file.
		tmuxPID, err := tmuxClient.GetSessionPID(ctx, sessionName)
		if err != nil {
			return fmt.Errorf("could not get tmux session PID to create lock file: %w", err)
		}
		if err := CreateLockFile(job.FilePath, tmuxPID); err != nil {
			return fmt.Errorf("failed to create lock file with tmux PID: %w", err)
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
	coreCfg, err := config.LoadFrom(".")
	if err != nil {
		coreCfg = &config.Config{}
	}

	var agentArgs []string
	if coreCfg != nil {
		type agentConfig struct {
			Args []string `yaml:"args"`
		}
		var agentCfg agentConfig
		coreCfg.UnmarshalExtension("agent", &agentCfg)
		agentArgs = agentCfg.Args
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

	// Return immediately. The lock file indicates the running state.
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


