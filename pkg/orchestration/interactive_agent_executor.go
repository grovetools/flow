package orchestration

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/grovetools/agentlogs/pkg/agentstream"
	flowexec "github.com/grovetools/flow/pkg/exec"
	"github.com/grovetools/core/config"
	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/sessions"
	"github.com/grovetools/core/pkg/tmux"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/core/util/sanitize"
	grovecontext "github.com/grovetools/cx/pkg/context"
	geminiconfig "github.com/grovetools/grove-gemini/pkg/config"
	"github.com/grovetools/grove-gemini/pkg/gemini"
	"github.com/sirupsen/logrus"
)

// InteractiveAgentProvider defines the interface for launching an interactive agent.
type InteractiveAgentProvider interface {
	Launch(ctx context.Context, job *Job, plan *Plan, workDir string, agentArgs []string, briefingFilePath string) error
}

// FlowProviderConfig holds per-provider configuration from grove.toml.
type FlowProviderConfig struct {
	Args      []string `yaml:"args"`
	InputMode string   `yaml:"input_mode"` // "vim" or "standard"
}

// FlowConfig holds the flow extension configuration from grove.toml.
type FlowConfig struct {
	InteractiveProvider string                          `yaml:"interactive_provider,omitempty"`
	AgentTarget         string                          `yaml:"agent_target,omitempty"` // "auto", "native", or "tmux"
	Providers           map[string]FlowProviderConfig   `yaml:"providers"`
}

// InteractiveAgentExecutor executes interactive agent jobs in tmux sessions.
type InteractiveAgentExecutor struct {
	skipInteractive bool
	log             *logrus.Entry
	ulog            *grovelogging.UnifiedLogger
	llmClient       LLMClient
	geminiRunner    *gemini.RequestRunner
}

// NewInteractiveAgentExecutor creates a new interactive agent executor.
func NewInteractiveAgentExecutor(llmClient LLMClient, geminiRunner *gemini.RequestRunner, skipInteractive bool) *InteractiveAgentExecutor {
	return &InteractiveAgentExecutor{
		skipInteractive: skipInteractive,
		log:             grovelogging.NewLogger("grove-flow"),
		ulog:            grovelogging.NewUnifiedLogger("grove-flow"),
		llmClient:       llmClient,
		geminiRunner:    geminiRunner,
	}
}

// Name returns the executor name.
func (e *InteractiveAgentExecutor) Name() string {
	return "interactive_agent"
}

// Execute runs an interactive agent job in a tmux session and blocks until completion.
// The output writer is ignored for interactive agents as they run in a separate tmux session.
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
		e.ulog.Info("Generating implementation plan from chat dependency").Log(ctx)
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

		// Log briefing file creation with structured logging
		requestID, _ := ctx.Value("request_id").(string)
		e.ulog.Success("Generated plan briefing file created").
			Field("job_id", job.ID).
			Field("request_id", requestID).
			Field("plan_name", plan.Name).
			Field("job_file", job.FilePath).
			Field("turn_id", turnID).
			Field("briefing_file_path", briefingFilePath).
			Field("prompt_chars", len(generatedPlanContent)).
			Pretty(theme.IconSuccess + " Generated plan briefing: " + theme.DefaultTheme.Accent.Render(briefingFilePath)).
			Log(ctx)
	} else {
		// Gather context files (.grove/context, CLAUDE.md, etc.)
		contextFiles, err := e.gatherContextFiles(job, plan, workDir)
		if err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			updateJobFile(job)
			return fmt.Errorf("failed to gather context files: %w", err)
		}

		// Query memory database for related memories
		memories := FetchRelatedMemories(ctx, job)

		// Build the XML prompt and get the list of files to upload.
		// NOTE: interactive agents currently don't support separate file uploads, so filesToUpload is ignored.
		promptXML, _, err := BuildXMLPrompt(job, plan, workDir, contextFiles, memories)
		if err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			updateJobFile(job)
			e.ulog.Error("Failed to build prompt for job").
				Field("job_id", job.ID).
				Field("job_file", job.FilePath).
				Err(err).
				Pretty(" " + err.Error()).
				Log(ctx)
			return fmt.Errorf("failed to build agent XML prompt: %w", err)
		}

		// Write the briefing file for auditing (no turnID for interactive_agent jobs).
		briefingFilePath, err = WriteBriefingFile(plan, job, promptXML, "")
		if err != nil {
			e.ulog.Warn("Failed to write briefing file").Err(err).Log(ctx)
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to write briefing file: %w", err)
		}

		// Log briefing file creation with structured logging
		requestID, _ := ctx.Value("request_id").(string)
		e.ulog.Success("Interactive agent briefing file created").
			Field("job_id", job.ID).
			Field("request_id", requestID).
			Field("plan_name", plan.Name).
			Field("job_file", job.FilePath).
			Field("briefing_file_path", briefingFilePath).
			Field("prompt_chars", len(promptXML)).
			Pretty(theme.IconCode + "  Briefing file created at: " + theme.DefaultTheme.Accent.Render(briefingFilePath)).
			Log(ctx)
	}

	// --- Concept Gathering Logic ---
	if job.GatherConceptNotes || job.GatherConceptPlans {
		conceptContextFile, err := gatherConcepts(ctx, job, plan, workDir)
		if err != nil {
			requestID, _ := ctx.Value("request_id").(string)
			e.ulog.Warn("Failed to gather concepts").
				Err(err).
				Field("request_id", requestID).
				Field("job_id", job.ID).
				Log(ctx)
		} else if conceptContextFile != "" {
			e.ulog.Success("Aggregated concepts context created").
				Field("concept_context_file", conceptContextFile).
				Pretty(theme.IconSuccess + " Aggregated concepts context: " + theme.DefaultTheme.Accent.Render(conceptContextFile)).
				Log(ctx)
		}
	}

	// Load config to get agent settings
	coreCfg, err := config.LoadFrom(".")
	if err != nil {
		coreCfg = &config.Config{}
	}

	// Unmarshal flow configuration (agent settings moved to flow extension)
	var flowCfg FlowConfig
	coreCfg.UnmarshalExtension("flow", &flowCfg)

	// Determine which provider to use
	providerName := "claude" // Default for backward compatibility
	if flowCfg.InteractiveProvider != "" {
		providerName = flowCfg.InteractiveProvider
	}

	// Determine agent target — must be resolved by the submission path
	// (CLI or TUI). The executor never checks env vars or daemon state.
	var provider InteractiveAgentProvider

	target := ""
	if plan.Orchestration != nil && plan.Orchestration.AgentTarget != "" {
		target = plan.Orchestration.AgentTarget
	}

	useNative := false
	switch target {
	case "native":
		useNative = true
	case "tmux":
		useNative = false
	default:
		return fmt.Errorf("agent_target not set: job submitted without routing context — this is a bug in the submission path (CLI or TUI should always tag jobs)")
	}

	if useNative {
		provider = NewGrovetermAgentProvider(providerName, false)
	} else {
		// Fallback to legacy tmux-based providers
		switch providerName {
		case "codex":
			provider = NewCodexAgentProvider()
		case "claude":
			provider = NewClaudeAgentProvider()
		case "opencode":
			provider = NewOpencodeAgentProvider()
		default:
			return fmt.Errorf("unknown interactive_agent provider: '%s'", providerName)
		}
	}

	// Get agent args for the selected provider
	var agentArgs []string
	if flowCfg.Providers != nil {
		if providerCfg, ok := flowCfg.Providers[providerName]; ok {
			agentArgs = providerCfg.Args
		}
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
		gitRoot, err := GetProjectGitRoot(plan.Directory)
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
			worktreePath := filepath.Join(actualGitRoot, ".grove-worktrees", job.Worktree)
			if _, err := os.Stat(worktreePath); err == nil {
				// Worktree already exists, skip preparation.
			} else {
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
		return e.llmClient.Complete(ctx, job, plan, fullPrompt, opts, io.Discard)
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
	// Use io.Discard since this is for plan generation and the output isn't streamed
	opts := LLMOptions{Model: effectiveModel, WorkingDir: workDir}
	return e.llmClient.Complete(ctx, job, plan, fullPrompt, opts, io.Discard)
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
	ctx := context.Background()
	e.ulog.Info("").Pretty("").Log(ctx) // blank line
	e.ulog.Info("Session has ended. What's the job status?").
		Field("session", sessionOrWindowName).
		Pretty(fmt.Sprintf("💭 Session '%s' has ended. What's the job status?", sessionOrWindowName)).
		Log(ctx)
	e.ulog.Info("").Pretty("   c - Mark as completed").Log(ctx)
	e.ulog.Info("").Pretty("   f - Mark as failed").Log(ctx)
	e.ulog.Info("").Pretty("   q - No status change (keep as running)").Log(ctx)
	e.ulog.Info("").Pretty("").Log(ctx) // blank line
	e.ulog.Info("").Pretty("Choice [c/f/q]: ").Log(ctx)

	var response string
	fmt.Scanln(&response)
	response = strings.ToLower(strings.TrimSpace(response))

	// Default to "c" if user just presses enter
	if response == "" {
		response = "c"
	}

	// Validate response
	if response != "c" && response != "f" && response != "q" {
		e.ulog.Warn("Invalid choice. Defaulting to completed").
			Field("choice", response).
			Log(ctx)
		response = "c"
	}

	return response
}

// ClaudeAgentProvider implements InteractiveAgentProvider for Claude Code.
type ClaudeAgentProvider struct {
	log  *logrus.Entry
	ulog *grovelogging.UnifiedLogger
}

func NewClaudeAgentProvider() *ClaudeAgentProvider {
	return &ClaudeAgentProvider{
		log:  grovelogging.NewLogger("grove-flow"),
		ulog: grovelogging.NewUnifiedLogger("grove-flow"),
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

	// Register session intent with the daemon BEFORE launching the agent.
	// This eliminates the PID race condition by pre-registering the session.
	// The daemon will track this session and wait for confirmation with the actual PID.
	daemonClient := daemon.NewWithAutoStart()
	defer daemonClient.Close()

	p.log.WithFields(logrus.Fields{
		"job_id":        job.ID,
		"daemon_running": daemonClient.IsRunning(),
	}).Info("Registering session intent with daemon")

	if err := daemonClient.RegisterSessionIntent(ctx, daemon.SessionIntent{
		JobID:       job.ID,
		Provider:    "claude",
		JobFilePath: job.FilePath,
		PlanName:    plan.Name,
		Title:       job.Title,
		WorkDir:     workDir,
		Channels:    job.Channels,
		Autonomous:  job.Autonomous,
	}); err != nil {
		// Log warning but continue - agent can still run, just tracking may be impaired
		p.log.WithError(err).Warn("Failed to register session intent with daemon")
	} else {
		p.log.Info("Session intent registered successfully")
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

		if !sessionExists {
			p.log.WithField("session", sessionName).Info("Creating new tmux session for interactive job")
			executor := &flowexec.RealCommandExecutor{}
			if err := executor.Execute("tmux", "new-session", "-d", "-s", sessionName, "-n", "workspace", "-c", workDir); err != nil {
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

		p.ulog.Info("Launching agent in worktree session").
			Field("window", agentWindowName).
			Field("session", sessionName).
			Pretty(theme.IconWorktree + " Launching agent in worktree session").
			Log(ctx)

		isTUIMode := os.Getenv("GROVE_FLOW_TUI_MODE") == "true"

		// Create new window - always use Detached to avoid stealing focus.
		// Use tmuxClient (not RealCommandExecutor) to ensure correct tmux server targeting
		// when the daemon runs on a separate tmux server (e.g., tmux -L groved).
		if err := tmuxClient.NewWindowWithOptions(ctx, tmux.NewWindowOptions{
			Target:     sessionName,
			WindowName: agentWindowName,
			WorkingDir: workDir,
			Detached:   true,
		}); err != nil {
			p.log.WithError(err).Warn("Failed to create agent window, may already exist. Will attempt to use it.")
		}

		// Set environment variables in the window's shell
		targetPane := fmt.Sprintf("%s:%s", sessionName, agentWindowName)

		// Update the daemon with the tmux target so channels/pinger can route to this session
		if err := daemonClient.UpdateSessionTmuxTarget(ctx, job.ID, targetPane); err != nil {
			p.log.WithError(err).Warn("Failed to update tmux target on daemon")
		}

		// Export environment variables in the window's shell
		// Use separate export commands for shell compatibility (bash/zsh/fish)
		// and properly quote the title to handle spaces and special characters.
		escapedTitle := "'" + strings.ReplaceAll(job.Title, "'", "'\\''") + "'"
		envCommand := fmt.Sprintf("export GROVE_FLOW_JOB_ID='%s'; export GROVE_FLOW_JOB_PATH='%s'; export GROVE_FLOW_PLAN_NAME='%s'; export GROVE_FLOW_JOB_TITLE=%s",
			job.ID, job.FilePath, plan.Name, escapedTitle)
		envCommand += playbookEnvExports(job, plan)
		if err := tmuxClient.SendKeys(ctx, targetPane, envCommand, "C-m"); err != nil {
			p.log.WithError(err).Error("Failed to set environment variables")
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to set environment variables: %w", err)
		}

		// Small delay to ensure environment variables are set
		time.Sleep(100 * time.Millisecond)

		// Wrap agent command with deterministic PID capture
		wrappedCommand := agentstream.BuildAgentCommand(job.ID, agentCommand)
		// Send the agent command to the new window
		if err := tmuxClient.SendKeys(ctx, targetPane, wrappedCommand, "C-m"); err != nil {
			p.log.WithError(err).Error("Failed to send agent command")
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to send agent command: %w", err)
		}

		// Asynchronously discover PID and register session in background
		// This prevents blocking the TUI when launching interactive agents
		go func() {
			if err := p.discoverAndRegisterSessionAsync(job, plan, workDir, targetPane); err != nil {
				p.log.WithError(err).Error("Failed to register session with valid PID")
				// Continue anyway - the agent is running, just tracking may be impaired
			}
		}()

		// Print instructions for how to view the agent (don't auto-switch windows)
		if !isTUIMode {
			if os.Getenv("TMUX") != "" {
				p.ulog.Info("Agent started in session").
					Field("session", sessionName).
					Pretty(fmt.Sprintf("   Agent started in session '%s'. To view, run: tmux select-window -t %s", sessionName, targetPane)).
					Log(ctx)
			} else {
				p.ulog.Info("Agent session ready").
					Field("session", sessionName).
					Pretty(fmt.Sprintf("   Attach with: tmux attach -t %s", sessionName)).
					Log(ctx)
			}
		}

		// Only show completion instructions if not running from the TUI
		if os.Getenv("GROVE_FLOW_TUI_MODE") != "true" {
			p.ulog.Info("").Pretty("").Log(ctx) // blank line
			p.ulog.Info("Task completion instructions").
				Pretty(theme.IconArrow + " When your task is complete, run the following in any terminal:").
				Log(ctx)
			p.ulog.Info("").
				Pretty(fmt.Sprintf("   flow plan complete %s", job.FilePath)).
				Log(ctx)
			p.ulog.Info("").Pretty("").Log(ctx) // blank line
			p.ulog.Info("").
				Pretty("   The session can remain open - the plan will continue automatically.").
				Log(ctx)
		}

		return nil
	}

	// Original behavior for jobs without worktrees
	gitRoot, err := GetProjectGitRoot(plan.Directory)
	if err != nil {
		return fmt.Errorf("could not find project git root: %w", err)
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
		p.ulog.Info("Tmux session not found, creating it").
			Field("session", sessionName).
			Pretty(fmt.Sprintf("Tmux session '%s' not found, creating it...", sessionName)).
			Log(ctx)

		executor := &flowexec.RealCommandExecutor{}
		if err := executor.Execute("tmux", "new-session", "-d", "-s", sessionName, "-n", "workspace", "-c", gitRoot); err != nil {
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
	p.ulog.Info("Launching agent in project session").
		Field("session", sessionName).
		Field("window", windowName).
		Field("workdir", workDir).
		Pretty(theme.IconRepo + " Launching agent in project session").
		Log(ctx)

	// Create new window - use tmuxClient (not RealCommandExecutor) to ensure correct
	// tmux server targeting when the daemon runs on a separate tmux server.
	windowTarget := fmt.Sprintf("%s:%s", sessionName, windowName)
	if err := tmuxClient.NewWindowWithOptions(ctx, tmux.NewWindowOptions{
		Target:     sessionName,
		WindowName: windowName,
		WorkingDir: workDir,
		Detached:   true,
	}); err != nil {
		if strings.Contains(err.Error(), "duplicate window") {
			p.ulog.Info("Window already exists, attempting to kill it first").
				Field("window", windowName).
				Log(ctx)
			tmuxClient.KillWindow(ctx, windowTarget)
			time.Sleep(100 * time.Millisecond)

			if err := tmuxClient.NewWindowWithOptions(ctx, tmux.NewWindowOptions{
				Target:     sessionName,
				WindowName: windowName,
				WorkingDir: workDir,
				Detached:   true,
			}); err != nil {
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

	targetPane := windowTarget

	// Use separate export commands for shell compatibility (bash/zsh/fish)
	// and properly quote the title to handle spaces and special characters.
	escapedTitle := "'" + strings.ReplaceAll(job.Title, "'", "'\\''") + "'"
	envCommand := fmt.Sprintf("export GROVE_FLOW_JOB_ID='%s'; export GROVE_FLOW_JOB_PATH='%s'; export GROVE_FLOW_PLAN_NAME='%s'; export GROVE_FLOW_JOB_TITLE=%s",
		job.ID, job.FilePath, plan.Name, escapedTitle)
	envCommand += playbookEnvExports(job, plan)
	if err := tmuxClient.SendKeys(ctx, targetPane, envCommand, "C-m"); err != nil {
		p.log.WithError(err).Error("Failed to set environment variables")
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to set environment variables: %w", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Wrap agent command with deterministic PID capture
	wrappedCommand := agentstream.BuildAgentCommand(job.ID, agentCommand)
	p.ulog.Debug("Sending command to tmux pane").
		Field("pane", targetPane).
		Log(ctx)
	if err := tmuxClient.SendKeys(ctx, targetPane, wrappedCommand, "C-m"); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to send agent command to pane '%s': %w", targetPane, err)
	}

	// Asynchronously discover PID and register session in background
	// This prevents blocking the TUI when launching interactive agents
	go func() {
		if err := p.discoverAndRegisterSessionAsync(job, plan, workDir, targetPane); err != nil {
			p.log.WithError(err).Error("Failed to register session with valid PID")
			// Continue anyway - the agent is running, just tracking may be impaired
		}
	}()

	p.ulog.Success("Interactive session launched").
		Field("window", windowName).
		Pretty(theme.IconSuccess + " Interactive session launched in window '" + windowName + "'").
		Log(ctx)
	p.ulog.Info("").
		Pretty(fmt.Sprintf("   Attach with: tmux attach -t %s", sessionName)).
		Log(ctx)

	// Only show completion instructions if not running from the TUI
	if os.Getenv("GROVE_FLOW_TUI_MODE") != "true" {
		p.ulog.Info("").Pretty("").Log(ctx) // blank line
		p.ulog.Info("Task completion instructions").
			Pretty(theme.IconArrow + " When your task is complete, run the following in any terminal:").
			Log(ctx)
		p.ulog.Info("").
			Pretty(fmt.Sprintf("   flow plan complete %s", job.FilePath)).
			Log(ctx)
		p.ulog.Info("").Pretty("").Log(ctx) // blank line
		p.ulog.Info("").
			Pretty("   The session can remain open - the plan will continue automatically.").
			Log(ctx)
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
	cmdParts = append(cmdParts, agentArgs...)

	// Pass instruction to read the briefing file
	return fmt.Sprintf("%s \"Read the briefing file at %s and execute the task.\"", strings.Join(cmdParts, " "), escapedPath), nil
}

// generateSessionName creates a unique session name for the interactive job (notebook-aware).
func (p *ClaudeAgentProvider) generateSessionName(workDir string) (string, error) {
	projInfo, err := ResolveProjectForSessionNaming(workDir)
	if err != nil {
		return "", fmt.Errorf("failed to get project info for session naming: %w", err)
	}
	return projInfo.Identifier("_"), nil
}

// discoverAndRegisterSessionAsync discovers the Claude Code PID and confirms the session with the daemon.
// This function is designed to be called from a goroutine - it blocks internally but
// does not block the caller. It uses agentstream for deterministic PID capture via pidfile.
// The session intent should already be registered via RegisterSessionIntent() before this is called.
func (p *ClaudeAgentProvider) discoverAndRegisterSessionAsync(job *Job, plan *Plan, workDir, targetPane string) error {
	logger := grovelogging.NewLogger("flow-claude-session-discovery")

	logger.WithFields(map[string]interface{}{
		"job_id":      job.ID,
		"plan":        plan.Name,
		"target_pane": targetPane,
	}).Debug("Starting async Claude Code PID discovery and confirmation")

	// Record the job start time for session file discovery
	jobStartTime := job.StartTime
	if jobStartTime.IsZero() {
		jobStartTime = time.Now()
	}

	ctx := context.Background()

	// Wait for PID via deterministic pidfile (written by BuildAgentCommand)
	claudePID, err := agentstream.WaitForPID(ctx, job.ID)
	if err != nil {
		// Fallback to legacy process tree traversal
		logger.WithError(err).Warn("Pidfile-based PID discovery failed, falling back to process tree traversal")
		var pidErr error
		time.Sleep(500 * time.Millisecond)
		maxPIDRetries := 30
		for i := 0; i < maxPIDRetries; i++ {
			claudePID, pidErr = p.findClaudePIDForPane(targetPane, logger)
			if pidErr == nil && claudePID > 0 {
				break
			}
			time.Sleep(1 * time.Second)
		}
		if claudePID == 0 {
			return fmt.Errorf("failed to find Claude Code PID: %w", pidErr)
		}
	} else {
		logger.WithField("pid", claudePID).Debug("Discovered Claude Code PID via pidfile")
		agentstream.CleanupPIDFile(job.ID)
	}

	// Discover transcript path using agentstream
	transcriptPath, err := agentstream.DiscoverTranscript(agentstream.DiscoverOptions{
		Provider:  "claude",
		WorkDir:   workDir,
		AfterTime: jobStartTime,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to discover transcript via agentstream, retrying...")
		// Retry with backoff
		for i := 0; i < 10; i++ {
			time.Sleep(1 * time.Second)
			transcriptPath, err = agentstream.DiscoverTranscript(agentstream.DiscoverOptions{
				Provider:  "claude",
				WorkDir:   workDir,
				AfterTime: jobStartTime,
			})
			if err == nil {
				break
			}
		}
		if err != nil {
			logger.WithError(err).Warn("Transcript path discovery failed, continuing without it")
		}
	}

	// Extract Claude session ID from transcript path for backwards compatibility
	var claudeSessionID string
	if transcriptPath != "" {
		claudeSessionID = strings.TrimSuffix(filepath.Base(transcriptPath), ".jsonl")
	}

	// Confirm the session with the daemon using the discovered PID
	daemonClient := daemon.NewWithAutoStart()
	defer daemonClient.Close()

	if err := daemonClient.ConfirmSession(ctx, daemon.SessionConfirmation{
		JobID:          job.ID,
		NativeID:       claudeSessionID,
		PID:            claudePID,
		TranscriptPath: transcriptPath,
	}); err != nil {
		logger.WithError(err).Warn("Failed to confirm session with daemon, falling back to filesystem registry")

		user := os.Getenv("USER")
		if user == "" {
			user = "unknown"
		}
		repo, branch := getGitInfo(workDir)

		metadata := sessions.SessionMetadata{
			SessionID:        job.ID,
			ClaudeSessionID:  claudeSessionID,
			Provider:         "claude",
			PID:              claudePID,
			WorkingDirectory: workDir,
			User:             user,
			Repo:             repo,
			Branch:           branch,
			StartedAt:        time.Now(),
			JobTitle:         job.Title,
			PlanName:         plan.Name,
			JobFilePath:      job.FilePath,
			Type:             "interactive_agent",
			TranscriptPath:   transcriptPath,
		}

		registry, regErr := sessions.NewFileSystemRegistry()
		if regErr != nil {
			return fmt.Errorf("failed to create session registry: %w", regErr)
		}

		if regErr := registry.Register(metadata); regErr != nil {
			return fmt.Errorf("failed to register session: %w", regErr)
		}
	}

	logger.WithFields(logrus.Fields{
		"session_id": job.ID,
		"pid":        claudePID,
	}).Info("Confirmed Claude session")

	return nil
}

// sanitizePathForClaude replicates Claude's path sanitization algorithm
// Claude replaces "/" with "-" and then replaces "-." with "--"
func sanitizePathForClaude(path string) string {
	// First replace all "/" with "-"
	result := strings.ReplaceAll(path, "/", "-")

	// Then replace all "-." with "--" to handle hidden directories like .grove-worktrees
	result = strings.ReplaceAll(result, "-.", "--")

	return result
}

// getFirstTimestampFromSessionFile reads the first timestamp from a Claude session jsonl file.
// This is more reliable than using file modification time, which can be stale on some systems
// (e.g., when Claude uses buffered writes or memory-mapped files).
func getFirstTimestampFromSessionFile(filePath string) (time.Time, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return time.Time{}, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	// Use a larger buffer for potentially large JSON lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	// Read up to 10 lines looking for a timestamp
	for i := 0; i < 10 && scanner.Scan(); i++ {
		var entry struct {
			Timestamp time.Time `json:"timestamp"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err == nil && !entry.Timestamp.IsZero() {
			return entry.Timestamp, nil
		}
	}

	return time.Time{}, fmt.Errorf("no timestamp found in first 10 lines of %s", filePath)
}

// findClaudeSessionID finds the Claude Code session ID by looking for the most recent session file
// created after the specified job start time (to avoid reusing old sessions)
func (p *ClaudeAgentProvider) findClaudeSessionID(workDir string, jobStartTime time.Time, logger *logrus.Entry) (string, error) {
	// Claude stores sessions in ~/.claude/projects/<sanitized-path>/*.jsonl
	// The directory name is the working directory with slashes replaced
	homeDir, err := os.UserHomeDir()
	if err != nil {
		logger.WithError(err).Error("Failed to get user home directory")
		return "", err
	}

	// Sanitize the working directory path to match Claude's format
	// Claude replaces "/" with "-" and removes leading "." from path components
	sanitizedPath := sanitizePathForClaude(workDir)
	claudeProjectsDir := filepath.Join(homeDir, ".claude", "projects", sanitizedPath)

	logger.WithFields(map[string]interface{}{
		"work_dir":            workDir,
		"sanitized_path":      sanitizedPath,
		"claude_projects_dir": claudeProjectsDir,
		"job_start_time":      jobStartTime,
	}).Debug("Looking for Claude session files")

	// Check if the directory exists
	if _, err := os.Stat(claudeProjectsDir); os.IsNotExist(err) {
		logger.WithField("claude_projects_dir", claudeProjectsDir).Debug("Claude projects directory not found")
		return "", fmt.Errorf("Claude projects directory not found: %s", claudeProjectsDir)
	}

	// Find the most recent .jsonl file (excluding agent-*.jsonl files which are sub-agents)
	// Only consider files modified AFTER the job started to avoid reusing old sessions
	entries, err := os.ReadDir(claudeProjectsDir)
	if err != nil {
		logger.WithError(err).Error("Failed to read Claude projects directory")
		return "", fmt.Errorf("failed to read Claude projects directory: %w", err)
	}

	logger.WithField("entry_count", len(entries)).Debug("Found entries in Claude projects directory")

	var latestFile string
	var latestTime time.Time
	skippedOld := 0

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		// Skip agent-* files (sub-agents)
		if strings.HasPrefix(entry.Name(), "agent-") {
			continue
		}

		if !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}

		filePath := filepath.Join(claudeProjectsDir, entry.Name())

		// Read the first timestamp from the file content instead of relying on
		// file modification time. File mod times can be stale on some systems
		// (e.g., macOS with APFS when Claude uses buffered writes).
		contentTime, err := getFirstTimestampFromSessionFile(filePath)
		if err != nil {
			// Fall back to file mod time if we can't read content timestamp
			info, infoErr := entry.Info()
			if infoErr != nil {
				continue
			}
			contentTime = info.ModTime()
		}

		// Only consider files with timestamps after the job started
		// This prevents reusing old session files from previous jobs
		if !contentTime.After(jobStartTime) {
			skippedOld++
			continue
		}

		if contentTime.After(latestTime) {
			latestTime = contentTime
			latestFile = entry.Name()
		}
	}

	if skippedOld > 0 {
		logger.WithFields(map[string]interface{}{
			"skipped_count":  skippedOld,
			"job_start_time": jobStartTime,
		}).Debug("Skipped session files predating job start")
	}

	if latestFile == "" {
		logger.WithField("claude_projects_dir", claudeProjectsDir).Warn("No Claude session files found")
		return "", fmt.Errorf("no Claude session files found in %s", claudeProjectsDir)
	}

	// Extract session ID from filename (remove .jsonl extension)
	sessionID := strings.TrimSuffix(latestFile, ".jsonl")
	logger.WithFields(map[string]interface{}{
		"latest_file":   latestFile,
		"session_id":    sessionID,
		"modified_time": latestTime,
	}).Debug("Found latest Claude session file")
	return sessionID, nil
}

// findClaudePIDForPane finds the PID of the Claude Code process running in a specific tmux pane
func (p *ClaudeAgentProvider) findClaudePIDForPane(targetPane string, logger *logrus.Entry) (int, error) {
	// Use tmux display-message to get the pane PID
	cmd := tmux.Command("display-message", "-p", "-t", targetPane, "#{pane_pid}")
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("failed to get pane PID: %w", err)
	}

	shellPID, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		return 0, fmt.Errorf("failed to parse pane PID: %w", err)
	}

	// Find the 'claude' process that is a descendant of that shell
	// Try 'claude' first (for the binary), then 'node' (for Node.js-based versions)
	pid, err := p.findDescendantPID(shellPID, "claude", logger)
	if err != nil {
		// Fallback to searching for node
		pid, err = p.findDescendantPID(shellPID, "node", logger)
		if err != nil {
			return 0, fmt.Errorf("failed to find claude or node process: %w", err)
		}
	}
	return pid, nil
}

// findDescendantPID recursively finds a descendant process with a given name.
func (p *ClaudeAgentProvider) findDescendantPID(parentPID int, targetComm string, logger *logrus.Entry) (int, error) {
	// Get all processes
	cmd := exec.Command("ps", "-o", "pid,ppid,comm")
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	// Build a process tree (map of ppid to children) and a pid-to-command map
	tree := make(map[int][]int)
	pidToComm := make(map[int]string)
	lines := strings.Split(string(output), "\n")
	for _, line := range lines[1:] { // skip header
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			pid, _ := strconv.Atoi(fields[0])
			ppid, _ := strconv.Atoi(fields[1])
			comm := fields[2]
			tree[ppid] = append(tree[ppid], pid)
			pidToComm[pid] = comm
		}
	}

	// Traverse from parentPID using breadth-first search
	queue := []int{parentPID}
	visited := make(map[int]bool)

	for len(queue) > 0 {
		currentPID := queue[0]
		queue = queue[1:]

		if visited[currentPID] {
			continue
		}
		visited[currentPID] = true

		// Check if the current process is the target
		if comm, ok := pidToComm[currentPID]; ok && strings.Contains(comm, targetComm) {
			return currentPID, nil
		}

		// Add children to the queue
		if children, ok := tree[currentPID]; ok {
			queue = append(queue, children...)
		}
	}

	return 0, fmt.Errorf("descendant process '%s' not found for parent PID %d", targetComm, parentPID)
}

// gatherContextFiles collects context files (.grove/context, CLAUDE.md, etc.) for the job.
func (e *InteractiveAgentExecutor) gatherContextFiles(job *Job, plan *Plan, workDir string) ([]string, error) {
	var contextFiles []string

	// Scope to sub-project if job.Repository is set (for ecosystem worktrees)
	contextDir := ScopeToSubProject(workDir, job)

	if contextDir != "" {
		// When using a worktree/context dir, ONLY use context from that directory
		ctxMgr := grovecontext.NewManager(contextDir)
		contextPath := ctxMgr.ResolveContextPath()

		if _, err := os.Stat(contextPath); err == nil {
			contextFiles = append(contextFiles, contextPath)
		}

		claudePath := filepath.Join(contextDir, "CLAUDE.md")
		if _, err := os.Stat(claudePath); err == nil {
			contextFiles = append(contextFiles, claudePath)
		}
	} else {
		// No worktree, use the default context search
		for _, contextPath := range FindContextFiles(plan) {
			if _, err := os.Stat(contextPath); err == nil {
				contextFiles = append(contextFiles, contextPath)
			}
		}
	}

	return contextFiles, nil
}

// ResolveInteractiveAgentPane returns the tmux target pane string (session:window)
// for an interactive agent job, using the same resolution logic as send/capture.
func ResolveInteractiveAgentPane(plan *Plan, job *Job) (string, error) {
	workDir, err := DetermineWorkingDirectory(plan, job)
	if err != nil {
		return "", fmt.Errorf("could not determine working directory: %w", err)
	}

	projInfo, err := ResolveProjectForSessionNaming(workDir)
	if err != nil {
		return "", fmt.Errorf("failed to resolve project for session naming: %w", err)
	}

	sessionName := projInfo.Identifier("_")
	windowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)
	return fmt.Sprintf("%s:%s", sessionName, windowName), nil
}

// CaptureInteractiveAgentOutput captures the visible output from an interactive agent's pane.
// Tries the daemon's native agent pane capture API first (groveterm), falls back to tmux.
func CaptureInteractiveAgentOutput(plan *Plan, job *Job) (string, error) {
	// Try daemon API first — works when agent runs in a native groveterm pane.
	ctx := context.Background()
	daemonClient := daemon.NewWithAutoStart()
	if connected, _ := daemonClient.IsTerminalConnected(ctx); connected {
		result, err := daemonClient.CaptureAgentPane(ctx, job.ID)
		daemonClient.Close()
		if err == nil {
			return result, nil
		}
		// Daemon capture failed — fall through to tmux.
	} else {
		daemonClient.Close()
	}

	// Fallback: tmux capture-pane on the project's tmux session.
	workDir, err := DetermineWorkingDirectory(plan, job)
	if err != nil {
		return "", fmt.Errorf("could not determine working directory: %w", err)
	}

	projInfo, err := ResolveProjectForSessionNaming(workDir)
	if err != nil {
		return "", fmt.Errorf("failed to resolve project for session naming: %w", err)
	}

	sessionName := projInfo.Identifier("_")
	windowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)
	targetPane := fmt.Sprintf("%s:%s", sessionName, windowName)

	tmuxClient, err := tmux.NewClient()
	if err != nil {
		return "", fmt.Errorf("tmux not available: %w", err)
	}

	return tmuxClient.CapturePane(ctx, targetPane)
}

// SendInputToInteractiveAgent sends input text to an interactive agent.
// Tries the daemon's native agent pane input API first (groveterm), falls back to tmux.
func SendInputToInteractiveAgent(plan *Plan, job *Job, input string) error {
	ctx := context.Background()

	// Try daemon API first — works when agent runs in a native groveterm pane.
	daemonClient := daemon.NewWithAutoStart()
	if connected, _ := daemonClient.IsTerminalConnected(ctx); connected {
		// Build the payload with vim-mode handling and submit key.
		workDir, _ := DetermineWorkingDirectory(plan, job)
		payload := buildAgentInputPayload(workDir, input)
		err := daemonClient.SendAgentInput(ctx, job.ID, payload)
		daemonClient.Close()
		if err == nil {
			return nil
		}
		// Daemon send failed — fall through to tmux.
	} else {
		daemonClient.Close()
	}

	// Fallback: tmux send-keys on the project's tmux session.
	workDir, err := DetermineWorkingDirectory(plan, job)
	if err != nil {
		return fmt.Errorf("could not determine working directory: %w", err)
	}

	projInfo, err := ResolveProjectForSessionNaming(workDir)
	if err != nil {
		return fmt.Errorf("failed to resolve project for session naming: %w", err)
	}

	sessionName := projInfo.Identifier("_")
	windowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)
	targetPane := fmt.Sprintf("%s:%s", sessionName, windowName)

	inputMode := resolveInputMode(workDir)

	tmuxClient, err := tmux.NewClient()
	if err != nil {
		return fmt.Errorf("tmux not available: %w", err)
	}

	if inputMode == "vim" {
		if err := tmuxClient.SendKeys(ctx, targetPane, "Escape", "i", input); err != nil {
			return fmt.Errorf("failed to send input text: %w", err)
		}
	} else {
		if err := tmuxClient.SendKeys(ctx, targetPane, input); err != nil {
			return fmt.Errorf("failed to send input text: %w", err)
		}
	}

	if err := tmuxClient.SendKeys(ctx, targetPane, "C-m"); err != nil {
		return fmt.Errorf("failed to send submit key: %w", err)
	}

	return nil
}

// buildAgentInputPayload constructs the input string with vim-mode handling and trailing CR.
func buildAgentInputPayload(workDir, input string) string {
	inputMode := resolveInputMode(workDir)
	if inputMode == "vim" {
		return "\x1bi" + input + "\r"
	}
	return input + "\r"
}

// resolveInputMode reads the flow config to determine the input mode for the provider.
func resolveInputMode(workDir string) string {
	coreCfg, cfgErr := config.LoadFrom(workDir)
	if cfgErr != nil {
		coreCfg = &config.Config{}
	}
	var flowCfg FlowConfig
	coreCfg.UnmarshalExtension("flow", &flowCfg)

	inputMode := "vim" // default
	providerName := "claude"
	if flowCfg.InteractiveProvider != "" {
		providerName = flowCfg.InteractiveProvider
	}
	if providerCfg, ok := flowCfg.Providers[providerName]; ok && providerCfg.InputMode != "" {
		inputMode = providerCfg.InputMode
	}
	return inputMode
}

// SendInterruptToInteractiveAgent sends Ctrl+C to interrupt an interactive agent.
// Tries the daemon's native agent pane input API first (groveterm), falls back to tmux.
func SendInterruptToInteractiveAgent(plan *Plan, job *Job) error {
	ctx := context.Background()

	// Try daemon API first — send Ctrl+C via native pane.
	daemonClient := daemon.NewWithAutoStart()
	if connected, _ := daemonClient.IsTerminalConnected(ctx); connected {
		err := daemonClient.SendAgentInput(ctx, job.ID, "\x03")
		daemonClient.Close()
		if err == nil {
			return nil
		}
	} else {
		daemonClient.Close()
	}

	// Fallback: tmux send-keys C-c.
	workDir, err := DetermineWorkingDirectory(plan, job)
	if err != nil {
		return fmt.Errorf("could not determine working directory: %w", err)
	}

	projInfo, err := ResolveProjectForSessionNaming(workDir)
	if err != nil {
		return fmt.Errorf("failed to resolve project for session naming: %w", err)
	}

	sessionName := projInfo.Identifier("_")
	windowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)
	targetPane := fmt.Sprintf("%s:%s", sessionName, windowName)

	tmuxClient, err := tmux.NewClient()
	if err != nil {
		return fmt.Errorf("tmux not available: %w", err)
	}

	return tmuxClient.SendKeys(ctx, targetPane, "C-c")
}
