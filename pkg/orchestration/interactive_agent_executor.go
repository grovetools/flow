package orchestration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/grovetools/agentlogs/pkg/agentstream"
	"github.com/grovetools/core/config"
	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/models"
	"github.com/grovetools/core/pkg/mux"
	"github.com/grovetools/core/pkg/paths"
	"github.com/grovetools/core/pkg/process"
	"github.com/grovetools/core/pkg/sessions"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/core/util/sanitize"
	grovecontext "github.com/grovetools/cx/pkg/context"
	anthropicmodels "github.com/grovetools/grove-anthropic/pkg/models"
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
	InteractiveProvider string                        `yaml:"interactive_provider,omitempty"`
	AgentTarget         string                        `yaml:"agent_target,omitempty"` // "auto", "native", or "tmux"
	Providers           map[string]FlowProviderConfig `yaml:"providers"`
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
	// Validate model before any setup work so the user gets an actionable
	// error instead of a generic failure after the agent tries to launch.
	if err := ValidateModelForJob(job.Model, job.Type); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		e.ulog.Error("Model validation failed").
			Field("job_id", job.ID).
			Field("model", job.Model).
			Err(err).
			Pretty(" " + err.Error()).
			Log(ctx)
		return fmt.Errorf("model validation: %w", err)
	}

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
			_ = updateJobFile(job)
			return fmt.Errorf("failed to generate plan from dependencies: %w", err)
		}

		// Write the generated plan to a new briefing file for the agent to execute.
		// The turnID is a unique identifier for this specific generation step.
		bytes := make([]byte, 3)
		_, _ = rand.Read(bytes)
		turnID := "generated-plan-" + hex.EncodeToString(bytes)

		briefingFilePath, err = WriteBriefingFile(plan, job, generatedPlanContent, turnID)
		if err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			_ = updateJobFile(job)
			return fmt.Errorf("failed to write generated plan briefing file: %w", err)
		}

		// Log briefing file creation with structured logging
		requestID, _ := ctx.Value(contextKey("request_id")).(string)
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
			_ = updateJobFile(job)
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
			_ = updateJobFile(job)
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
		requestID, _ := ctx.Value(contextKey("request_id")).(string)
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
			requestID, _ := ctx.Value(contextKey("request_id")).(string)
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
	_ = coreCfg.UnmarshalExtension("flow", &flowCfg)

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

	grovelogging.NewUnifiedLogger("flow.executor").Info("Executor routing agent job").
		Field("job_id", job.ID).
		Field("agent_target", target).
		Field("provider", providerName).
		StructuredOnly().Log(ctx)

	useNative := false
	switch target {
	case "native", "tuimux":
		useNative = true
	case "tmux":
		useNative = false
	default:
		return fmt.Errorf("agent_target not set: job submitted without routing context — this is a bug in the submission path (CLI or TUI should always tag jobs)")
	}

	if useNative {
		provider = NewGrovetermAgentProvider(providerName, false, target)
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

	// Append per-job flags (--model, --effort) for claude only; other
	// providers do not accept these flags.
	if providerName == "claude" {
		var err error
		agentArgs, err = appendClaudeJobArgs(agentArgs, job, plan)
		if err != nil {
			return err
		}
		// Record the model the agent will actually run with (or claude's
		// default when none was passed) into the job frontmatter.
		backfillClaudeAgentModel(job)
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

		// Shared detection resolves the owning repo and whether the worktree
		// already exists; we only prepare a missing one.
		ownerRoot, _, exists := resolveWorktreeForJob(gitRoot, job.Worktree)
		if !exists {
			opts := workspace.PrepareOptions{
				GitRoot:      ownerRoot,
				WorktreeName: job.Worktree,
				BranchName:   job.Worktree,
				PlanName:     plan.Name,
			}

			if plan.Config != nil && len(plan.Config.Repos) > 0 {
				opts.SiblingWorkspaces = plan.Config.Repos
			}
			// Layout is decided by ecosystem-ness, NOT the sibling list: an
			// anchored full-ecosystem worktree persists an empty repos: yet
			// lives in the XDG layout.
			opts.UseXDGWorktrees = workspaceIsEcosystem(ownerRoot)

			if _, err := workspace.Prepare(ctx, opts, CopyProjectFilesToWorktree); err != nil {
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
		effectiveModel = "gemini-2.5-flash" // Fallback
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
		"job_id":         job.ID,
		"daemon_running": daemonClient.IsRunning(),
	}).Info("Registering session intent with daemon")

	agentTarget := models.MuxTmux
	if plan.Orchestration != nil && plan.Orchestration.AgentTarget != "" {
		agentTarget = plan.Orchestration.AgentTarget
	}
	muxType := models.MuxTmux
	if agentTarget == "tuimux" {
		muxType = models.MuxTuimux
	}

	if err := daemonClient.RegisterSessionIntent(ctx, daemon.SessionIntent{
		JobID:        job.ID,
		Provider:     "claude",
		JobFilePath:  job.FilePath,
		PlanName:     plan.Name,
		Title:        job.Title,
		WorkDir:      workDir,
		Channels:     job.Channels,
		SignalTarget: job.SignalTarget,
		Autonomous:   job.Autonomous,
		Mux:          muxType,
	}); err != nil {
		// Log warning but continue - agent can still run, just tracking may be impaired
		p.log.WithError(err).Warn("Failed to register session intent with daemon")
	} else {
		p.log.Info("Session intent registered successfully")
	}

	engine, err := mux.GetEngine(agentTarget)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("mux engine not available: %w", err)
	}

	grovelogging.NewUnifiedLogger("flow.agent").Info("Launching agent session").
		Field("job_id", job.ID).
		Field("agent_target", agentTarget).
		Field("mux_type", muxType).
		Field("engine", fmt.Sprintf("%T", engine)).
		StructuredOnly().Log(ctx)

	// Check if job has a worktree - if so, create/reuse a session
	alog := grovelogging.NewUnifiedLogger("flow.agent")
	if job.Worktree != "" {
		// For jobs with worktrees, create/reuse a session based on the project identifier
		sessionName, err := mux.GenerateSessionName(workDir)
		if err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return err
		}

		alog.Info("Checking session existence").
			Field("job_id", job.ID).
			Field("session", sessionName).
			Field("engine", fmt.Sprintf("%T", engine)).
			StructuredOnly().Log(ctx)

		// Check if session already exists
		sessionExists, _ := engine.SessionExists(ctx, sessionName)

		if !sessionExists {
			alog.Info("Creating new session").
				Field("session", sessionName).
				Field("work_dir", workDir).
				StructuredOnly().Log(ctx)
			if err := engine.CreateSession(ctx, sessionName, mux.WithWorkDir(workDir)); err != nil {
				alog.Info("CreateSession FAILED").
					Field("session", sessionName).
					Field("error", err.Error()).
					StructuredOnly().Log(ctx)
				job.Status = JobStatusFailed
				job.EndTime = time.Now()
				return fmt.Errorf("failed to create session: %w", err)
			}
			alog.Info("CreateSession succeeded").Field("session", sessionName).StructuredOnly().Log(ctx)

			// Get the session PID and create the lock file.
			tmuxPID, err := engine.GetSessionPID(ctx, sessionName)
			if err != nil {
				alog.Info("GetSessionPID FAILED").Field("error", err.Error()).StructuredOnly().Log(ctx)
				return fmt.Errorf("could not get tmux session PID to create lock file: %w", err)
			}
			if err := CreateLockFile(job.FilePath, tmuxPID); err != nil {
				return fmt.Errorf("failed to create lock file with tmux PID: %w", err)
			}
		} else {
			alog.Info("Using existing session").Field("session", sessionName).StructuredOnly().Log(ctx)
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

		alog.Info("Creating agent window").
			Field("window", agentWindowName).
			Field("session", sessionName).
			Field("agent_command_len", len(agentCommand)).
			StructuredOnly().Log(ctx)

		isTUIMode := os.Getenv("GROVE_FLOW_TUI_MODE") == "true"

		// Create new window - always use Detached to avoid stealing focus.
		if err := engine.NewWindow(ctx, sessionName, agentWindowName, workDir, true); err != nil {
			alog.Info("NewWindow FAILED").Field("error", err.Error()).StructuredOnly().Log(ctx)
			p.log.WithError(err).Warn("Failed to create agent window, may already exist. Will attempt to use it.")
		}

		// Set environment variables in the window's shell
		targetPane := fmt.Sprintf("%s:%s", sessionName, agentWindowName)

		// Update the daemon with the tmux target so channels/pinger can route to this session
		if err := daemonClient.UpdateSessionTmuxTarget(ctx, job.ID, targetPane); err != nil {
			p.log.WithError(err).Warn("Failed to update tmux target on daemon")
		}

		// Inline env vars on the agent command itself (not via a separate
		// `export` line) so they scope only to the agent process and its
		// descendants. Typing `export` into the pane would leak these vars
		// into the user's interactive shell after the agent exits.
		//
		// GROVE_SCOPE is inherited from the executor's env (treemux exports
		// it at startup; the daemon process exports its own scope on boot).
		// If the executor has no GROVE_SCOPE set, we don't inject one —
		// agents launched from plain tmux hit the unscoped global daemon.
		scopePrefix := ""
		if scope := os.Getenv("GROVE_SCOPE"); scope != "" {
			scopePrefix = fmt.Sprintf("GROVE_SCOPE='%s' ", scope)
		}
		escapedTitle := "'" + strings.ReplaceAll(job.Title, "'", "'\\''") + "'"
		envPrefix := scopePrefix + fmt.Sprintf("GROVE_FLOW_JOB_ID='%s' GROVE_FLOW_JOB_PATH='%s' GROVE_FLOW_PLAN_NAME='%s' GROVE_FLOW_JOB_TITLE=%s ",
			job.ID, job.FilePath, plan.Name, escapedTitle)
		if node, err := workspace.GetProjectByPath(workDir); err == nil && node != nil {
			logDir := filepath.Join(paths.StateDir(), "logs", "workspaces", node.Identifier("/"))
			envPrefix += fmt.Sprintf("GROVE_LOG_DIR='%s' ", logDir)
		}
		envPrefix += playbookEnvInline(job, plan)

		// Wrap agent command with deterministic PID capture, then prefix
		// inline env so everything runs as one shell invocation.
		wrappedCommand := envPrefix + agentstream.BuildAgentCommand(job.ID, agentCommand)
		// Send the agent command to the new window
		if err := engine.SendKeys(ctx, targetPane, wrappedCommand, "C-m"); err != nil {
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
			if mux.ActiveMux() != mux.MuxNone {
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
	alog.Info("No worktree — using project git root path").
		Field("job_id", job.ID).
		Field("plan_dir", plan.Directory).
		StructuredOnly().Log(ctx)

	gitRoot, err := GetProjectGitRoot(plan.Directory)
	if err != nil {
		return fmt.Errorf("could not find project git root: %w", err)
	}

	sessionName, err := mux.GenerateSessionName(gitRoot)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return err
	}
	windowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)

	alog.Info("Non-worktree session setup").
		Field("session", sessionName).
		Field("window", windowName).
		Field("git_root", gitRoot).
		StructuredOnly().Log(ctx)

	// Ensure session exists
	sessionExists, _ := engine.SessionExists(ctx, sessionName)
	if !sessionExists {
		p.ulog.Info("Session not found, creating it").
			Field("session", sessionName).
			Pretty(fmt.Sprintf("Session '%s' not found, creating it...", sessionName)).
			Log(ctx)

		if err := engine.CreateSession(ctx, sessionName, mux.WithWorkDir(gitRoot)); err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to create session: %w", err)
		}

		tmuxPID, err := engine.GetSessionPID(ctx, sessionName)
		if err != nil {
			return fmt.Errorf("could not get session PID to create lock file: %w", err)
		}
		if err := CreateLockFile(job.FilePath, tmuxPID); err != nil {
			return fmt.Errorf("failed to create lock file with session PID: %w", err)
		}
	}

	// Create new window
	p.ulog.Info("Launching agent in project session").
		Field("session", sessionName).
		Field("window", windowName).
		Field("workdir", workDir).
		Pretty(theme.IconRepo + " Launching agent in project session").
		Log(ctx)

	windowTarget := fmt.Sprintf("%s:%s", sessionName, windowName)
	if err := engine.NewWindow(ctx, sessionName, windowName, workDir, true); err != nil {
		if strings.Contains(err.Error(), "duplicate window") {
			p.ulog.Info("Window already exists, attempting to kill it first").
				Field("window", windowName).
				Log(ctx)
			_ = engine.KillWindow(ctx, windowTarget)
			time.Sleep(100 * time.Millisecond)

			if err := engine.NewWindow(ctx, sessionName, windowName, workDir, true); err != nil {
				job.Status = JobStatusFailed
				job.EndTime = time.Now()
				return fmt.Errorf("failed to create new window after killing existing: %w", err)
			}
		} else {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to create new window: %w", err)
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

	// Inline env vars on the agent command itself (see the matching site
	// above for rationale). Scoping env to the agent process avoids
	// polluting the user's interactive shell after the agent exits.
	// GROVE_SCOPE is inherited from the executor's env, not forced.
	scopePrefix := ""
	if scope := os.Getenv("GROVE_SCOPE"); scope != "" {
		scopePrefix = fmt.Sprintf("GROVE_SCOPE='%s' ", scope)
	}
	escapedTitle := "'" + strings.ReplaceAll(job.Title, "'", "'\\''") + "'"
	envPrefix := scopePrefix + fmt.Sprintf("GROVE_FLOW_JOB_ID='%s' GROVE_FLOW_JOB_PATH='%s' GROVE_FLOW_PLAN_NAME='%s' GROVE_FLOW_JOB_TITLE=%s ",
		job.ID, job.FilePath, plan.Name, escapedTitle)
	if node, nodeErr := workspace.GetProjectByPath(workDir); nodeErr == nil && node != nil {
		logDir := filepath.Join(paths.StateDir(), "logs", "workspaces", node.Identifier("/"))
		envPrefix += fmt.Sprintf("GROVE_LOG_DIR='%s' ", logDir)
	}
	envPrefix += playbookEnvInline(job, plan)

	// Wrap agent command with deterministic PID capture, then prefix
	// inline env so everything runs as one shell invocation.
	wrappedCommand := envPrefix + agentstream.BuildAgentCommand(job.ID, agentCommand)
	p.ulog.Debug("Sending command to tmux pane").
		Field("pane", targetPane).
		Log(ctx)
	if err := engine.SendKeys(ctx, targetPane, wrappedCommand, "C-m"); err != nil {
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

// appendClaudeJobArgs appends per-job claude CLI flags to the static provider
// args from grove.toml: --model and --effort, both read from job frontmatter
// only. Values are passed through without validating against claude's accepted
// set, but are rejected if they contain characters that would need shell
// quoting, since several launch paths join args into a raw shell command
// string. Only call this when the effective provider is claude.
//
// The agent model is resolved from job frontmatter — NOT from the plan-level
// default. Plan/global model defaults (e.g. gemini-* for chat/oneshot jobs)
// only apply to oneshot/chat jobs; no job-creation path stamps them onto
// agent jobs (see JobType.InheritsPlanModel). An empty model means "let the claude
// CLI use its own configured default", which the executor then records into
// the job frontmatter via backfillClaudeAgentModel.
//
// A non-claude model set on a claude agent job is a hard error rather than a
// silent fallback: previously such a model (e.g. gemini-3.5-flash) was dropped
// and the job quietly ran a DEFAULT CLAUDE agent while its frontmatter still
// advertised the gemini model. Surfacing the error tells the user to route to
// the right provider via flow.interactive_provider instead of masquerading.
func appendClaudeJobArgs(agentArgs []string, job *Job, plan *Plan) ([]string, error) {
	model := job.Model
	if model != "" && !isClaudeFamilyModel(model) {
		return nil, fmt.Errorf("model %q is not a Claude model but this job runs the claude agent CLI; "+
			"plan/global defaults apply only to chat/oneshot jobs. To run a different provider, set "+
			"flow.interactive_provider in grove.toml, or set a claude-family model on the job", model)
	}
	// When no model is pinned on the job, pass NO --model so the claude CLI
	// honors the user's own configured default (e.g. opusplan). We do NOT force
	// the agent default here; that alias is only used as the best-effort
	// frontmatter LABEL in backfillClaudeAgentModel.
	if model == "" && job.Effort == "" {
		return agentArgs, nil
	}

	// Copy so we never mutate the shared provider config slice
	// (jobs may build args concurrently from the same FlowConfig).
	args := make([]string, len(agentArgs), len(agentArgs)+4)
	copy(args, agentArgs)

	if model != "" {
		if !isShellSafeArgValue(model) {
			return nil, fmt.Errorf("model %q contains characters that are unsafe in a shell command; use a plain model name/alias", model)
		}
		args = append(args, "--model", model)
	}
	if job.Effort != "" {
		if !isShellSafeArgValue(job.Effort) {
			return nil, fmt.Errorf("effort %q contains characters that are unsafe in a shell command; use a plain effort level", job.Effort)
		}
		args = append(args, "--effort", job.Effort)
	}
	return args, nil
}

// isClaudeFamilyModel reports whether model is usable with the claude CLI's
// --model flag: a claude-* ID (including gateway forms like
// us.anthropic.claude-*), a registered alias, or a bare family alias such as
// "opus"/"sonnet"/"haiku"/"fable" (prefix match so variants like "opusplan"
// pass). Anything else (gemini-*, gpt-*, ...) belongs to another provider.
func isClaudeFamilyModel(model string) bool {
	m := strings.ToLower(resolveModelAlias(model))
	if strings.Contains(m, "claude") {
		return true
	}
	for _, family := range []string{"opus", "sonnet", "haiku", "fable"} {
		if strings.HasPrefix(m, family) {
			return true
		}
	}
	return false
}

// canonicalClaudeModel normalizes a claude model name to its canonical short
// alias via the grove-anthropic model registry: a full API id
// ("claude-opus-4-6-20260115") collapses to its alias ("claude-opus-4-6"), an
// alias is returned unchanged, and anything the registry doesn't know (e.g. a
// bare family alias like "opus") is returned as-is. Used to record a stable,
// human-meaningful model in job frontmatter rather than a churny dated id.
func canonicalClaudeModel(model string) string {
	if model == "" {
		return ""
	}
	fullID := anthropicmodels.ResolveAlias(model) // alias -> id, or unchanged
	for alias, id := range anthropicmodels.Aliases() {
		if id == fullID {
			return alias
		}
	}
	return model
}

// backfillClaudeAgentModel records the model a claude agent job will ACTUALLY
// run with into the job's frontmatter, so the `model:` field stops lying.
//
//   - Empty job.Model: no --model is passed, so the claude CLI self-selects the
//     user's configured default. We record the canonical agent default alias
//     (anthropicmodels.DefaultAgentAlias) as a best-effort label instead of
//     leaving the field blank. The runtime model still honors the user's CLI
//     config; this is only the displayed label.
//   - Non-empty job.Model: normalize it to its canonical alias.
//
// The write is skipped when the value is already canonical (no churn). This is
// best-effort: a frontmatter write failure is logged, never fatal — the agent
// is already launching. Only call this for the claude provider.
func backfillClaudeAgentModel(job *Job) {
	resolved := canonicalClaudeModel(job.Model)
	if resolved == "" {
		resolved = anthropicmodels.DefaultAgentAlias
	}
	if resolved == job.Model {
		return // already canonical; nothing to write
	}
	if err := NewStatePersister().UpdateJobModel(job, resolved); err != nil {
		logrus.WithError(err).WithFields(logrus.Fields{
			"job_id": job.ID,
			"model":  resolved,
		}).Warn("Failed to backfill resolved agent model into job frontmatter")
	}
}

// isShellSafeArgValue reports whether s can be embedded unquoted in the
// shell command strings built by the agent providers.
func isShellSafeArgValue(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.' || r == ':' || r == '/' || r == '@' || r == ',' || r == '+':
		default:
			return false
		}
	}
	return true
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
		_ = agentstream.CleanupPIDFile(job.ID)
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

// findClaudePIDForPane finds the PID of the Claude Code process running in a specific tmux pane
func (p *ClaudeAgentProvider) findClaudePIDForPane(targetPane string, logger *logrus.Entry) (int, error) {
	engine, err := mux.DetectMuxEngine(context.Background())
	if err != nil {
		return 0, fmt.Errorf("mux engine not available: %w", err)
	}

	shellPID, err := engine.GetPanePID(context.Background(), targetPane)
	if err != nil {
		return 0, fmt.Errorf("failed to get pane PID: %w", err)
	}

	// Find the 'claude' process that is a descendant of that shell
	// Try 'claude' first (for the binary), then 'node' (for Node.js-based versions)
	pid, err := process.FindDescendantPID(shellPID, "claude")
	if err != nil {
		pid, err = process.FindDescendantPID(shellPID, "node")
		if err != nil {
			return 0, fmt.Errorf("failed to find claude or node process: %w", err)
		}
	}
	return pid, nil
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
	logger := grovelogging.NewLogger("flow.orchestration.agent")
	logger.WithField("job_id", job.ID).Debug("CaptureOutput called")

	// Determine workDir upfront so we can scope the daemon connection correctly.
	workDir, err := DetermineWorkingDirectory(plan, job)
	if err != nil {
		return "", fmt.Errorf("could not determine working directory: %w", err)
	}

	// Try daemon API first. This works when a native groveterm pane is connected
	// OR when the agent runs as an out-of-process tuimux PTY — the daemon now has
	// a native PtyID capture tier, so a connected terminal is no longer required.
	ctx := context.Background()
	daemonClient := daemon.NewWithAutoStart()
	connected, _ := daemonClient.IsTerminalConnected(ctx)
	session, _ := daemonClient.GetSession(ctx, job.ID)
	hasPty := session != nil && session.PtyID != ""
	if connected || hasPty {
		result, err := daemonClient.CaptureAgentPane(ctx, job.ID)
		daemonClient.Close()
		if err == nil {
			logger.WithFields(map[string]interface{}{
				"tier":   "native",
				"job_id": job.ID,
				"bytes":  len(result),
			}).Debug("Native capture succeeded")
			return result, nil
		}
		logger.WithError(err).WithField("job_id", job.ID).Warn("Native capture failed, falling back to tmux")
	} else {
		daemonClient.Close()
	}

	projInfo, err := ResolveProjectForSessionNaming(workDir)
	if err != nil {
		return "", fmt.Errorf("failed to resolve project for session naming: %w", err)
	}

	sessionName := projInfo.Identifier("_")
	windowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)
	targetPane := fmt.Sprintf("%s:%s", sessionName, windowName)

	engine, err := mux.DetectMuxEngine(ctx)
	if err != nil {
		return "", fmt.Errorf("mux engine not available: %w", err)
	}

	result, err := engine.CapturePane(ctx, targetPane)
	if err != nil {
		return "", err
	}
	logger.WithFields(map[string]interface{}{
		"tier":        "tmux",
		"job_id":      job.ID,
		"tmux_target": targetPane,
		"bytes":       len(result),
	}).Debug("Tmux capture succeeded")
	return result, nil
}

// SendInputToInteractiveAgent sends input text to an interactive agent.
// Tries the daemon's native agent pane input API first (groveterm), falls back to tmux.
func SendInputToInteractiveAgent(plan *Plan, job *Job, input string) error {
	logger := grovelogging.NewLogger("flow.orchestration.agent")
	logger.WithFields(map[string]interface{}{
		"job_id":    job.ID,
		"input_len": len(input),
	}).Debug("SendInput called")

	ctx := context.Background()

	// Determine workDir upfront so we can scope the daemon connection correctly.
	workDir, err := DetermineWorkingDirectory(plan, job)
	if err != nil {
		return fmt.Errorf("could not determine working directory: %w", err)
	}

	// Try daemon API first — works when agent runs in a native groveterm pane.
	daemonClient := daemon.NewWithAutoStart()
	connected, _ := daemonClient.IsTerminalConnected(ctx)
	if connected {
		logger.WithFields(map[string]interface{}{
			"tier":   "native",
			"job_id": job.ID,
		}).Info("Dispatching input")
		// Build the payload with vim-mode handling and submit key.
		payload := buildAgentInputPayload(workDir, input)
		err := daemonClient.SendAgentInput(ctx, job.ID, payload)
		daemonClient.Close()
		if err == nil {
			logger.Debug("Native send succeeded")
			return nil
		}
		logger.WithError(err).WithField("job_id", job.ID).Warn("Native send failed, falling back to tmux")
	} else {
		daemonClient.Close()
	}

	// Fallback: tmux send-keys on the project's tmux session.

	projInfo, err := ResolveProjectForSessionNaming(workDir)
	if err != nil {
		return fmt.Errorf("failed to resolve project for session naming: %w", err)
	}

	sessionName := projInfo.Identifier("_")
	windowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)
	targetPane := fmt.Sprintf("%s:%s", sessionName, windowName)

	inputMode := resolveInputMode(workDir)

	if !connected {
		logger.WithFields(map[string]interface{}{
			"tier":        "tmux",
			"job_id":      job.ID,
			"tmux_target": targetPane,
		}).Info("Dispatching input")
	}

	engine, err := mux.DetectMuxEngine(ctx)
	if err != nil {
		return fmt.Errorf("mux engine not available: %w", err)
	}

	if inputMode == "vim" {
		if err := engine.SendKeys(ctx, targetPane, "Escape", "i", input); err != nil {
			return fmt.Errorf("failed to send input text: %w", err)
		}
	} else {
		if err := engine.SendKeys(ctx, targetPane, input); err != nil {
			return fmt.Errorf("failed to send input text: %w", err)
		}
	}

	if err := engine.SendKeys(ctx, targetPane, "C-m"); err != nil {
		return fmt.Errorf("failed to send submit key: %w", err)
	}

	logger.WithField("tmux_target", targetPane).Debug("Send succeeded")
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
	_ = coreCfg.UnmarshalExtension("flow", &flowCfg)

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
	logger := grovelogging.NewLogger("flow.orchestration.agent")
	logger.WithField("job_id", job.ID).Debug("SendInterrupt called")

	ctx := context.Background()

	// Determine workDir upfront so we can scope the daemon connection correctly.
	workDir, err := DetermineWorkingDirectory(plan, job)
	if err != nil {
		return fmt.Errorf("could not determine working directory: %w", err)
	}

	// Try daemon API first — send Ctrl+C via native pane.
	daemonClient := daemon.NewWithAutoStart()
	connected, _ := daemonClient.IsTerminalConnected(ctx)
	if connected {
		logger.WithFields(map[string]interface{}{
			"tier":   "native",
			"job_id": job.ID,
		}).Info("Dispatching interrupt")
		err := daemonClient.SendAgentInput(ctx, job.ID, "\x03")
		daemonClient.Close()
		if err == nil {
			logger.Debug("Native interrupt succeeded")
			return nil
		}
		logger.WithError(err).WithField("job_id", job.ID).Warn("Native interrupt failed, falling back to tmux")
	} else {
		daemonClient.Close()
	}

	projInfo, err := ResolveProjectForSessionNaming(workDir)
	if err != nil {
		return fmt.Errorf("failed to resolve project for session naming: %w", err)
	}

	sessionName := projInfo.Identifier("_")
	windowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)
	targetPane := fmt.Sprintf("%s:%s", sessionName, windowName)

	if !connected {
		logger.WithFields(map[string]interface{}{
			"tier":        "tmux",
			"job_id":      job.ID,
			"tmux_target": targetPane,
		}).Info("Dispatching interrupt")
	}

	engine, err := mux.DetectMuxEngine(ctx)
	if err != nil {
		return fmt.Errorf("mux engine not available: %w", err)
	}

	if err := engine.SendKeys(ctx, targetPane, "C-c"); err != nil {
		return err
	}
	logger.WithField("tmux_target", targetPane).Debug("Interrupt succeeded")
	return nil
}
