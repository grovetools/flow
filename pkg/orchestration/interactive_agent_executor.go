package orchestration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	geminiconfig "github.com/mattsolo1/grove-gemini/pkg/config"
	"github.com/mattsolo1/grove-gemini/pkg/gemini"
	"github.com/sirupsen/logrus"
)

// InteractiveAgentProvider defines the interface for launching an interactive agent.
type InteractiveAgentProvider interface {
	Launch(ctx context.Context, job *Job, plan *Plan, workDir string, agentArgs []string, briefingFilePath string) error
}

// InteractiveAgentExecutor executes interactive agent jobs in tmux sessions.
type InteractiveAgentExecutor struct {
	skipInteractive bool
	log             *logrus.Entry
	prettyLog       *grovelogging.PrettyLogger
	llmClient       LLMClient
	geminiRunner    *gemini.RequestRunner
}

// NewInteractiveAgentExecutor creates a new interactive agent executor.
func NewInteractiveAgentExecutor(llmClient LLMClient, geminiRunner *gemini.RequestRunner, skipInteractive bool) *InteractiveAgentExecutor {
	return &InteractiveAgentExecutor{
		skipInteractive: skipInteractive,
		log:             grovelogging.NewLogger("grove-flow"),
		prettyLog:       grovelogging.NewPrettyLogger(),
		llmClient:       llmClient,
		geminiRunner:    geminiRunner,
	}
}

// Name returns the executor name.
func (e *InteractiveAgentExecutor) Name() string {
	return "interactive_agent"
}

// Execute runs an interactive agent job in a tmux session and blocks until completion.
func (e *InteractiveAgentExecutor) Execute(ctx context.Context, job *Job, plan *Plan) error {
	// Determine workDir first, as it's needed for briefing file generation
	workDir, err := e.determineWorkDir(ctx, job, plan)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to determine working directory: %w", err)
	}

	var briefingFilePath string

	// If generate_plan_from is true, we first call an LLM to generate a plan from the chat.
	if job.GeneratePlanFrom {
		e.prettyLog.InfoPretty("Generating implementation plan from chat dependency...")
		generatedPlanContent, err := e.generatePlanFromDependencies(ctx, job, plan)
		if err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			updateJobFile(job)
			return fmt.Errorf("failed to generate plan from dependencies: %w", err)
		}

		// Write the generated plan to a new briefing file for the agent to execute.
		// The turnID is a unique identifier for this specific generation step.
		bytes := make([]byte, 3)
		rand.Read(bytes)
		turnID := "generated-plan-" + hex.EncodeToString(bytes)

		briefingFilePath, err = WriteBriefingFile(plan, job, generatedPlanContent, turnID)
		if err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			updateJobFile(job)
			return fmt.Errorf("failed to write generated plan briefing file: %w", err)
		}
		e.prettyLog.InfoPretty(fmt.Sprintf("Generated plan briefing file created at: %s", briefingFilePath))
	} else {
		// Build the XML prompt and get the list of files to upload.
		// NOTE: interactive agents currently don't support separate file uploads, so filesToUpload is ignored.
		promptXML, _, err := BuildXMLPrompt(job, plan, workDir)
		if err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to build agent XML prompt: %w", err)
		}

		// Write the briefing file for auditing (no turnID for interactive_agent jobs).
		briefingFilePath, err = WriteBriefingFile(plan, job, promptXML, "")
		if err != nil {
			e.log.WithError(err).Warn("Failed to write briefing file")
			e.prettyLog.WarnPretty(fmt.Sprintf("Warning: Failed to write briefing file: %v", err))
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to write briefing file: %w", err)
		}
		e.prettyLog.InfoPretty(fmt.Sprintf("Briefing file created at: %s", briefingFilePath))
	}

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
		extractedContent, err := resolveSourceBlock(job.SourceBlock, plan)
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

	// Delegate to the selected provider with the briefing file path
	return provider.Launch(ctx, job, plan, workDir, agentArgs, briefingFilePath)
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

// generatePlanFromDependencies constructs a prompt from chat dependencies and calls an LLM to generate a plan.
func (e *InteractiveAgentExecutor) generatePlanFromDependencies(ctx context.Context, job *Job, plan *Plan) (string, error) {
	if len(job.Dependencies) == 0 {
		return "", fmt.Errorf("job with generate_plan_from has no dependencies")
	}

	// Assume the first dependency is the chat log to be summarized.
	chatDep := job.Dependencies[0]
	chatContentBytes, err := os.ReadFile(chatDep.FilePath)
	if err != nil {
		return "", fmt.Errorf("reading chat dependency file %s: %w", chatDep.FilePath, err)
	}
	_, chatBody, err := ParseFrontmatter(chatContentBytes)
	if err != nil {
		return "", fmt.Errorf("parsing frontmatter from chat dependency: %w", err)
	}

	// Load the agent-xml template for plan generation instructions.
	templateManager := NewTemplateManager()
	template, err := templateManager.FindTemplate("agent-xml")
	if err != nil {
		return "", fmt.Errorf("resolving agent-xml template: %w", err)
	}

	// Combine template prompt with the chat content.
	fullPrompt := fmt.Sprintf("%s\n\n## Chat Transcript\n\n%s", template.Prompt, string(chatBody))

	// Determine the model to use.
	effectiveModel := job.Model
	if effectiveModel == "" && plan.Config != nil {
		effectiveModel = plan.Config.Model
	}
	if effectiveModel == "" {
		effectiveModel = "gemini-2.0-flash-exp" // Fallback
	}

	// Determine working directory for context discovery
	workDir, err := DetermineWorkingDirectory(plan, job)
	if err != nil {
		// Fallback to plan directory if working directory cannot be determined
		workDir = plan.Directory
	}

	// Make the LLM call.
	// Check if mocking is enabled - if so, always use llmClient regardless of model
	if os.Getenv("GROVE_MOCK_LLM_RESPONSE_FILE") != "" {
		opts := LLMOptions{Model: effectiveModel, WorkingDir: workDir}
		return e.llmClient.Complete(ctx, job, plan, fullPrompt, opts)
	}

	if strings.HasPrefix(effectiveModel, "gemini") {
		apiKey, _ := geminiconfig.ResolveAPIKey()
		opts := gemini.RequestOptions{
			Model:            effectiveModel,
			Prompt:           fullPrompt,
			WorkDir:          workDir, // Enable context file discovery
			SkipConfirmation: true,
			APIKey:           apiKey,
			Caller:           "grove-flow-generate-plan",
			JobID:            job.ID,
			PlanName:         plan.Name,
		}
		return e.geminiRunner.Run(ctx, opts)
	}

	// Fallback for other models.
	opts := LLMOptions{Model: effectiveModel, WorkingDir: workDir}
	return e.llmClient.Complete(ctx, job, plan, fullPrompt, opts)
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
func (p *ClaudeAgentProvider) Launch(ctx context.Context, job *Job, plan *Plan, workDir string, agentArgs []string, briefingFilePath string) error {
	// Update job status to running
	job.Status = JobStatusRunning
	job.StartTime = time.Now()
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}

	// Regenerate context before launching the agent
	oneShotExec := NewOneShotExecutor(NewCommandLLMClient(), nil)
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
		agentCommand, err := p.buildAgentCommand(job, plan, briefingFilePath, agentArgs)
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
	agentCommand, err := p.buildAgentCommand(job, plan, briefingFilePath, agentArgs)
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
func (p *ClaudeAgentProvider) buildAgentCommand(job *Job, plan *Plan, briefingFilePath string, agentArgs []string) (string, error) {
	// Pass a simple instruction to read the briefing file.
	// This is cleaner than reading the entire file content into the command.
	// Shell escape the entire briefing file path.
	escapedPath := "'" + strings.ReplaceAll(briefingFilePath, "'", "'\\''") + "'"

	// Build command with agent args
	cmdParts := []string{"claude"}
	if job.AgentContinue {
		cmdParts = append(cmdParts, "--continue")
	}
	cmdParts = append(cmdParts, agentArgs...)

	// Pass instruction to read the briefing file
	return fmt.Sprintf("%s \"Read the briefing file at %s and execute the task.\"", strings.Join(cmdParts, " "), escapedPath), nil
}

// generateSessionName creates a unique session name for the interactive job.
func (p *ClaudeAgentProvider) generateSessionName(workDir string) (string, error) {
	projInfo, err := workspace.GetProjectByPath(workDir)
	if err != nil {
		return "", fmt.Errorf("failed to get project info for session naming: %w", err)
	}
	return projInfo.Identifier(), nil
}
