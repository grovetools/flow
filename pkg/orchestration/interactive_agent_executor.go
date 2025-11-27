package orchestration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattsolo1/grove-core/config"
	grovelogging "github.com/mattsolo1/grove-core/logging"
	"github.com/mattsolo1/grove-core/pkg/tmux"
	"github.com/mattsolo1/grove-core/pkg/workspace"
	"github.com/mattsolo1/grove-core/util/sanitize"
	flowexec "github.com/mattsolo1/grove-flow/pkg/exec"
	"github.com/sirupsen/logrus"
)

// InteractiveAgentProvider defines the interface for launching an interactive agent.
type InteractiveAgentProvider interface {
	Launch(ctx context.Context, job *Job, plan *Plan, workDir string, agentArgs []string) error
}

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

	// Load config to get agent settings
	coreCfg, err := config.LoadFrom(".")
	if err != nil {
		coreCfg = &config.Config{}
	}

	// Unmarshal agent configuration
	type agentConfig struct {
		Args                string   `yaml:"args"`
		InteractiveProvider string   `yaml:"interactive_provider,omitempty"`
	}
	var agentCfg agentConfig
	coreCfg.UnmarshalExtension("agent", &agentCfg)

	// Determine which provider to use
	providerName := "claude" // Default for backward compatibility
	if agentCfg.InteractiveProvider != "" {
		providerName = agentCfg.InteractiveProvider
	}

	var provider InteractiveAgentProvider
	switch providerName {
	case "codex":
		provider = NewCodexAgentProvider()
	case "claude":
		provider = NewClaudeAgentProvider()
	default:
		return fmt.Errorf("unknown interactive_agent provider: '%s'", providerName)
	}

	// Determine workDir once
	workDir, err := e.determineWorkDir(ctx, job, plan)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to determine working directory: %w", err)
	}

	// Get agent args
	var agentArgs []string
	if coreCfg != nil {
		type argsConfig struct {
			Args []string `yaml:"args"`
		}
		var argsCfg argsConfig
		coreCfg.UnmarshalExtension("agent", &argsCfg)
		agentArgs = argsCfg.Args
	}

	// Handle source_block reference if present
	// Resolve it before launching the agent so the agent has the content to work with
	if job.SourceBlock != "" {
		extractedContent, err := resolveSourceBlockForAgent(job.SourceBlock, plan)
		if err != nil {
			return fmt.Errorf("resolving source_block: %w", err)
		}
		// Update the job's PromptBody with the resolved content
		if job.PromptBody != "" {
			job.PromptBody = extractedContent + "\n\n" + job.PromptBody
		} else {
			job.PromptBody = extractedContent
		}
		// Clear the source_block field as it's now resolved
		job.SourceBlock = ""
		// Update the job file with the resolved content
		if err := updateJobFile(job); err != nil {
			return fmt.Errorf("updating job file with resolved source_block: %w", err)
		}
	}

	// Delegate to the selected provider
	return provider.Launch(ctx, job, plan, workDir, agentArgs)
}

// determineWorkDir determines the working directory for a job based on its worktree configuration.
func (e *InteractiveAgentExecutor) determineWorkDir(ctx context.Context, job *Job, plan *Plan) (string, error) {
	// For jobs with worktrees, we need to prepare the worktree if it doesn't exist yet
	if job.Worktree != "" {
		gitRoot, err := GetGitRootSafe(plan.Directory)
		if err != nil {
			return "", fmt.Errorf("could not find git root: %w", err)
		}

		// Check if we're already in the requested worktree to avoid duplicate paths
		currentPath := gitRoot
		if !strings.HasSuffix(currentPath, filepath.Join(".grove-worktrees", job.Worktree)) {
			// Extract the main repository path if we're in a worktree
			actualGitRoot := gitRoot
			if strings.Contains(gitRoot, ".grove-worktrees") {
				parts := strings.Split(gitRoot, ".grove-worktrees")
				if len(parts) > 0 {
					actualGitRoot = strings.TrimSuffix(parts[0], string(filepath.Separator))
				}
			}

			// Prepare the worktree
			opts := workspace.PrepareOptions{
				GitRoot:      actualGitRoot,
				WorktreeName: job.Worktree,
				BranchName:   job.Worktree,
				PlanName:     plan.Name,
			}

			if plan.Config != nil && len(plan.Config.Repos) > 0 {
				opts.Repos = plan.Config.Repos
			}

			_, err := workspace.Prepare(ctx, opts, CopyProjectFilesToWorktree)
			if err != nil {
				return "", fmt.Errorf("failed to prepare host worktree: %w", err)
			}
		}
	}

	// Use the shared logic to determine the final working directory
	return DetermineWorkingDirectory(plan, job)
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

// ClaudeAgentProvider implements InteractiveAgentProvider for Claude Code.
type ClaudeAgentProvider struct {
	log       *logrus.Entry
	prettyLog *grovelogging.PrettyLogger
}

func NewClaudeAgentProvider() *ClaudeAgentProvider {
	return &ClaudeAgentProvider{
		log:       grovelogging.NewLogger("grove-flow"),
		prettyLog: grovelogging.NewPrettyLogger(),
	}
}

// Launch implements the InteractiveAgentProvider interface for Claude.
// This contains the logic previously in executeHostMode.
func (p *ClaudeAgentProvider) Launch(ctx context.Context, job *Job, plan *Plan, workDir string, agentArgs []string) error {
	// Update job status to running
	job.Status = JobStatusRunning
	job.StartTime = time.Now()
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}

	// Regenerate context before launching the agent
	oneShotExec := NewOneShotExecutor(nil)
	if err := oneShotExec.regenerateContextInWorktree(workDir, "interactive-agent", job, plan); err != nil {
		p.log.WithError(err).Warn("Failed to generate job-specific context for interactive session")
		p.prettyLog.WarnPretty(fmt.Sprintf("Warning: Failed to generate job-specific context: %v", err))
	}

	// Create tmux client
	tmuxClient, err := tmux.NewClient()
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("tmux not available: %w", err)
	}

	// Check if job has a worktree - if so, create/reuse a session
	if job.Worktree != "" {
		// For jobs with worktrees, create/reuse a session based on the project identifier
		sessionName, err := p.generateSessionName(workDir)
		if err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return err
		}

		// Check if session already exists
		sessionExists, _ := tmuxClient.SessionExists(ctx, sessionName)
		executor := &flowexec.RealCommandExecutor{}

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
			p.log.WithField("session", sessionName).Info("Creating new tmux session for interactive job")
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
			p.log.WithField("session", sessionName).Info("Using existing session for interactive job")
		}

		// Build agent command
		agentCommand, err := p.buildAgentCommand(job, plan, workDir, agentArgs)
		if err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to build agent command: %w", err)
		}

		// Create a new window for this specific agent job in the session
		agentWindowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)

		p.log.WithField("window", agentWindowName).Info("Creating window for agent")
		p.prettyLog.InfoPretty(fmt.Sprintf("Creating window '%s' for agent...", agentWindowName))
		if err := executor.Execute("tmux", "new-window", "-t", sessionName, "-n", agentWindowName, "-c", workDir); err != nil {
			p.log.WithError(err).Warn("Failed to create agent window, may already exist. Will attempt to use it.")
		}

		// Set environment variables in the window's shell
		targetPane := fmt.Sprintf("%s:%s", sessionName, agentWindowName)

		// Export environment variables in the window's shell
		// Use separate export commands for shell compatibility (bash/zsh/fish)
		// and properly quote the title to handle spaces and special characters.
		escapedTitle := "'" + strings.ReplaceAll(job.Title, "'", "'\\''") + "'"
		envCommand := fmt.Sprintf("export GROVE_FLOW_JOB_ID='%s'; export GROVE_FLOW_JOB_PATH='%s'; export GROVE_FLOW_PLAN_NAME='%s'; export GROVE_FLOW_JOB_TITLE=%s",
			job.ID, job.FilePath, plan.Name, escapedTitle)
		if err := executor.Execute("tmux", "send-keys", "-t", targetPane, envCommand, "C-m"); err != nil {
			p.log.WithError(err).Error("Failed to set environment variables")
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to set environment variables: %w", err)
		}

		// Small delay to ensure environment variables are set
		time.Sleep(100 * time.Millisecond)

		// Send the agent command to the new window
		if err := executor.Execute("tmux", "send-keys", "-t", targetPane, agentCommand, "C-m"); err != nil {
			p.log.WithError(err).Error("Failed to send agent command")
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to send agent command: %w", err)
		}

		// Conditionally switch to the agent window
		if os.Getenv("TMUX") != "" {
			// Check if we are in the correct session before trying to select window
			currentSessionCmd := exec.Command("tmux", "display-message", "-p", "#S")
			currentSessionOutput, err := currentSessionCmd.Output()
			if err == nil {
				currentSession := strings.TrimSpace(string(currentSessionOutput))
				if currentSession == sessionName {
					// We are in the correct session, just switch to the window
					if err := executor.Execute("tmux", "select-window", "-t", targetPane); err != nil {
						p.log.WithError(err).Warn("Failed to switch to agent window")
					}
				} else {
					// In a different session, print instructions
					p.prettyLog.InfoPretty(fmt.Sprintf("   Agent started in session '%s'. To view, run: tmux switch-client -t %s", sessionName, sessionName))
				}
			} else {
				// Couldn't determine current session, print instructions
				p.log.WithError(err).Warn("Could not get current tmux session")
				p.prettyLog.InfoPretty(fmt.Sprintf("   Agent started in session '%s'. To view, run: tmux switch-client -t %s", sessionName, sessionName))
			}
		} else {
			// Not in tmux, print instructions
			p.prettyLog.InfoPretty(fmt.Sprintf("   Attach with: tmux attach -t %s", sessionName))
		}

		// Only show completion instructions if not running from the TUI
		if os.Getenv("GROVE_FLOW_TUI_MODE") != "true" {
			p.prettyLog.Blank()
			p.prettyLog.InfoPretty("👉 When your task is complete, run the following in any terminal:")
			p.prettyLog.InfoPretty(fmt.Sprintf("   flow plan complete %s", job.FilePath))
			p.prettyLog.Blank()
			p.prettyLog.InfoPretty("   The session can remain open - the plan will continue automatically.")
		}

		return nil
	}

	// Original behavior for jobs without worktrees
	gitRoot, err := GetGitRootSafe(plan.Directory)
	if err != nil {
		return fmt.Errorf("could not find git root: %w", err)
	}

	sessionName, err := p.generateSessionName(gitRoot)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return err
	}
	windowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)

	// Ensure session exists
	sessionExists, _ := tmuxClient.SessionExists(ctx, sessionName)
	if !sessionExists {
		p.log.WithField("session", sessionName).Info("Tmux session not found, creating it")
		p.prettyLog.Success(fmt.Sprintf("Tmux session '%s' not found, creating it...", sessionName))
		opts := tmux.LaunchOptions{
			SessionName:      sessionName,
			WorkingDirectory: gitRoot,
		}
		if err := tmuxClient.Launch(ctx, opts); err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to create tmux session: %w", err)
		}

		tmuxPID, err := tmuxClient.GetSessionPID(ctx, sessionName)
		if err != nil {
			return fmt.Errorf("could not get tmux session PID to create lock file: %w", err)
		}
		if err := CreateLockFile(job.FilePath, tmuxPID); err != nil {
			return fmt.Errorf("failed to create lock file with tmux PID: %w", err)
		}
	}

	// Create new window
	p.log.WithFields(logrus.Fields{
		"session": sessionName,
		"window":  windowName,
		"workdir": workDir,
	}).Info("Creating tmux window")
	p.prettyLog.InfoPretty(fmt.Sprintf("Creating tmux window: session=%s, window=%s, workdir=%s", sessionName, windowName, workDir))

	executor := &flowexec.RealCommandExecutor{}

	if err := executor.Execute("tmux", "new-window", "-t", sessionName, "-n", windowName, "-c", workDir); err != nil {
		if strings.Contains(err.Error(), "duplicate window") {
			p.log.WithField("window", windowName).Info("Window already exists, attempting to kill it first")
			p.prettyLog.InfoPretty(fmt.Sprintf("Window '%s' already exists, attempting to kill it first", windowName))
			executor.Execute("tmux", "kill-window", "-t", fmt.Sprintf("%s:%s", sessionName, windowName))
			time.Sleep(100 * time.Millisecond)

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
	agentCommand, err := p.buildAgentCommand(job, plan, workDir, agentArgs)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to build agent command: %w", err)
	}

	time.Sleep(300 * time.Millisecond)

	targetPane := fmt.Sprintf("%s:%s", sessionName, windowName)

	// Use separate export commands for shell compatibility (bash/zsh/fish)
	// and properly quote the title to handle spaces and special characters.
	escapedTitle := "'" + strings.ReplaceAll(job.Title, "'", "'\\''") + "'"
	envCommand := fmt.Sprintf("export GROVE_FLOW_JOB_ID='%s'; export GROVE_FLOW_JOB_PATH='%s'; export GROVE_FLOW_PLAN_NAME='%s'; export GROVE_FLOW_JOB_TITLE=%s",
		job.ID, job.FilePath, plan.Name, escapedTitle)
	if err := executor.Execute("tmux", "send-keys", "-t", targetPane, envCommand, "C-m"); err != nil {
		p.log.WithError(err).Error("Failed to set environment variables")
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to set environment variables: %w", err)
	}

	time.Sleep(100 * time.Millisecond)

	p.log.WithField("pane", targetPane).Info("Sending command to tmux pane")
	p.prettyLog.InfoPretty(fmt.Sprintf("Sending command to tmux pane: %s", targetPane))
	if err := executor.Execute("tmux", "send-keys", "-t", targetPane, agentCommand, "C-m"); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to send agent command to pane '%s': %w", targetPane, err)
	}

	p.log.WithField("window", windowName).Info("Interactive host session launched")
	p.prettyLog.InfoPretty(fmt.Sprintf("🚀 Interactive host session launched in window '%s'.", windowName))
	p.prettyLog.InfoPretty(fmt.Sprintf("   Attach with: tmux attach -t %s", sessionName))

	// Only show completion instructions if not running from the TUI
	if os.Getenv("GROVE_FLOW_TUI_MODE") != "true" {
		p.prettyLog.Blank()
		p.prettyLog.InfoPretty("👉 When your task is complete, run the following in any terminal:")
		p.prettyLog.InfoPretty(fmt.Sprintf("   flow plan complete %s", job.FilePath))
		p.prettyLog.Blank()
		p.prettyLog.InfoPretty("   The session can remain open - the plan will continue automatically.")
	}

	return nil
}

// buildAgentCommand constructs the agent command for the interactive session.
func (p *ClaudeAgentProvider) buildAgentCommand(job *Job, plan *Plan, worktreePath string, agentArgs []string) (string, error) {
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
			instructionBuilder.WriteString(fmt.Sprintf("CRITICAL CONTEXT: Before you do anything else, you MUST read and fully understand the content of the following files in order. They provide the primary context and requirements for your task: %s. ", strings.Join(depFiles, ", ")))
			instructionBuilder.WriteString(fmt.Sprintf("After you have processed that context, execute the agent job defined in %s. ", job.FilePath))
		} else {
			instructionBuilder.WriteString(fmt.Sprintf("Read the file %s and execute the agent job defined there. ", job.FilePath))
			instructionBuilder.WriteString("For additional context from previous jobs, also read: ")
			instructionBuilder.WriteString(strings.Join(depFiles, ", "))
			instructionBuilder.WriteString(". ")
		}
	} else {
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
func (p *ClaudeAgentProvider) generateSessionName(workDir string) (string, error) {
	projInfo, err := workspace.GetProjectByPath(workDir)
	if err != nil {
		return "", fmt.Errorf("failed to get project info for session naming: %w", err)
	}
	return projInfo.Identifier(), nil
}

// resolveSourceBlockForAgent reads and extracts content from a source_block reference
// Format: path/to/file.md#block-id1,block-id2 or path/to/file.md (for entire file)
func resolveSourceBlockForAgent(sourceBlock string, plan *Plan) (string, error) {
	// Parse the source block reference
	parts := strings.SplitN(sourceBlock, "#", 2)
	filePath := parts[0]

	// Resolve file path relative to plan directory
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(plan.Directory, filePath)
	}

	// Read the source file
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("reading source file %s: %w", filePath, err)
	}

	// If no block IDs specified, return entire file content (without frontmatter)
	if len(parts) == 1 {
		_, bodyContent, err := ParseFrontmatter(content)
		if err != nil {
			return "", fmt.Errorf("parsing frontmatter: %w", err)
		}
		return string(bodyContent), nil
	}

	// Extract specific blocks
	blockIDs := strings.Split(parts[1], ",")

	// Parse the chat file to find blocks
	turns, err := ParseChatFile(content)
	if err != nil {
		return "", fmt.Errorf("parsing chat file: %w", err)
	}

	// Create a map of block IDs to content
	blockMap := make(map[string]*ChatTurn)
	for _, turn := range turns {
		if turn.Directive != nil && turn.Directive.ID != "" {
			blockMap[turn.Directive.ID] = turn
		}
	}

	// Extract requested blocks
	var extractedContent strings.Builder
	foundCount := 0
	for _, blockID := range blockIDs {
		if turn, ok := blockMap[blockID]; ok {
			if foundCount > 0 {
				extractedContent.WriteString("\n\n---\n\n")
			}
			extractedContent.WriteString(turn.Content)
			foundCount++
		} else {
			return "", fmt.Errorf("block ID '%s' not found in source file", blockID)
		}
	}

	if foundCount == 0 {
		return "", fmt.Errorf("no valid blocks found")
	}

	return extractedContent.String(), nil
}


