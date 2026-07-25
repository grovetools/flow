package orchestration

import (
	"context"
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
	"github.com/grovetools/core/pkg/mux"
	"github.com/grovetools/core/pkg/process"
	"github.com/grovetools/core/pkg/sessions"
	"github.com/grovetools/core/tui/theme"
	grovecontext "github.com/grovetools/cx/pkg/context"
	"github.com/grovetools/grove-gemini/pkg/gemini"
	"github.com/sirupsen/logrus"
)

// IsolatedAgentExecutor executes isolated agent jobs in dedicated tmux servers.
// Unlike interactive agents which share a project's tmux session, isolated agents
// run in their own isolated tmux server (using a custom socket like `flow-job-<job-id>`).
// This allows the TUI to send input to them via tmux send-keys while keeping them
// separate from the main tmux session.
type IsolatedAgentExecutor struct {
	skipInteractive bool
	log             *logrus.Entry
	ulog            *grovelogging.UnifiedLogger
	llmClient       LLMClient
	geminiRunner    *gemini.RequestRunner
}

// NewIsolatedAgentExecutor creates a new isolated agent executor.
func NewIsolatedAgentExecutor(llmClient LLMClient, geminiRunner *gemini.RequestRunner, skipInteractive bool) *IsolatedAgentExecutor {
	return &IsolatedAgentExecutor{
		skipInteractive: skipInteractive,
		log:             grovelogging.NewLogger("grove-flow"),
		ulog:            grovelogging.NewUnifiedLogger("grove-flow"),
		llmClient:       llmClient,
		geminiRunner:    geminiRunner,
	}
}

// Name returns the executor name.
func (e *IsolatedAgentExecutor) Name() string {
	return "isolated_agent"
}

// TmuxSocketName returns the socket name for an isolated agent job.
func TmuxSocketName(jobID string) string {
	return fmt.Sprintf("flow-job-%s", jobID)
}

// TmuxTargetPane returns the target pane for sending keys to an isolated agent.
func TmuxTargetPane(jobID string) string {
	return "main:0" // session:window format
}

// Execute runs an isolated agent job in a dedicated tmux server.
// Unlike interactive agents, isolated agents run in their own tmux server
// (using a custom socket) and are not attached to any existing session.
func (e *IsolatedAgentExecutor) Execute(ctx context.Context, job *Job, plan *Plan) error {
	// Determine workDir first, as it's needed for briefing file generation
	workDir, err := e.determineWorkDir(ctx, job, plan)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to determine working directory: %w", err)
	}

	var briefingFilePath string

	// Gather context files (.grove/context, CLAUDE.md, etc.)
	contextFiles, err := e.gatherContextFiles(job, plan, workDir)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		_ = updateJobFile(job)
		return fmt.Errorf("failed to gather context files: %w", err)
	}

	// Query memory database for related memories (bounded; see memoryPrefetchTimeout)
	memories := FetchRelatedMemoriesBounded(ctx, job)

	// Build the XML prompt and get the list of files to upload.
	promptXML, _, err := BuildXMLPrompt(job, plan, workDir, contextFiles, memories)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		ulog.Error("Failed to build prompt for job").
			Field("job_id", job.ID).
			Field("job_file", job.FilePath).
			Err(err).
			Pretty(" " + err.Error()).
			Log(ctx)
		return fmt.Errorf("failed to build agent XML prompt: %w", err)
	}

	// Write the briefing file for auditing
	briefingFilePath, err = WriteBriefingFile(plan, job, promptXML, "")
	if err != nil {
		e.ulog.Warn("Failed to write briefing file").Err(err).Log(ctx)
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to write briefing file: %w", err)
	}

	stampJobConfigVector(ctx, job, plan, "", workDir, nil, contextFiles, briefingFilePath)

	// Log briefing file creation
	requestID, _ := ctx.Value(contextKey("request_id")).(string)
	e.ulog.Success("Isolated agent briefing file created").
		Field("job_id", job.ID).
		Field("request_id", requestID).
		Field("plan_name", plan.Name).
		Field("job_file", job.FilePath).
		Field("briefing_file_path", briefingFilePath).
		Field("prompt_chars", len(promptXML)).
		Pretty(theme.IconCode + "  Briefing file created at: " + theme.DefaultTheme.Accent.Render(briefingFilePath)).
		Log(ctx)

	// --- Concept Gathering Logic ---
	if job.GatherConceptNotes || job.GatherConceptPlans {
		conceptContextFile, err := gatherConcepts(ctx, job, plan, workDir)
		if err != nil {
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

	// Unmarshal flow configuration (agent settings moved to flow extension).
	// A malformed config shouldn't hard-fail an agent launch; log and fall back
	// to defaults.
	var flowCfg FlowConfig
	if parsed, cfgErr := FlowConfigFrom(coreCfg); cfgErr != nil {
		e.ulog.Warn("Failed to parse flow configuration; using defaults").
			Field("job_id", job.ID).
			Err(cfgErr).
			Log(ctx)
	} else {
		flowCfg = *parsed
	}

	// Resolve the effective provider (job frontmatter > flow.interactive_provider
	// > claude); unknown names are a hard error.
	spec, err := resolveJobProviderSpec(job, flowCfg)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		_ = updateJobFile(job)
		return fmt.Errorf("agent provider: %w", err)
	}

	// Get agent args for the selected provider (with the claude bypass default).
	agentArgs := resolveProviderArgs(flowCfg, spec.Name)

	// Append per-job flags (model, effort) per the provider's spec; providers
	// without the corresponding flag reject a non-empty value.
	agentArgs, err = appendProviderJobArgs(spec, agentArgs, job)
	if err != nil {
		return err
	}
	// Record the model the agent will actually run with (for claude: the model
	// its own config selects when none was passed) into the job frontmatter.
	if spec.BackfillJobModel != nil {
		spec.BackfillJobModel(job, workDir, flowCfg.AgentEnv)
	}

	// Handle source_block reference if present
	if job.SourceBlock != "" {
		extractedContent, err := resolveSourceBlock(job.SourceBlock, plan)
		if err != nil {
			return fmt.Errorf("resolving source_block: %w", err)
		}
		if job.PromptBody != "" {
			job.PromptBody = extractedContent + "\n\n" + job.PromptBody
		} else {
			job.PromptBody = extractedContent
		}
		job.SourceBlock = ""
		if err := updateJobFile(job); err != nil {
			return fmt.Errorf("updating job file with resolved source_block: %w", err)
		}
	}

	// Determine agent target — must be resolved by the submission path
	// (CLI or TUI). The executor never checks env vars or daemon state.
	target := ""
	if plan.Orchestration != nil && plan.Orchestration.AgentTarget != "" {
		target = plan.Orchestration.AgentTarget
	}

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
		// Isolated agents launch silently into the groveterm icon rail (autoSplit=false)
		provider := NewGrovetermAgentProvider(spec, false, target)
		provider.agentEnv = flowCfg.AgentEnv
		provider.extraEnv = map[string]string{"GROVE_FLOW_ISOLATED": "true"}

		return provider.Launch(ctx, job, plan, workDir, agentArgs, briefingFilePath)
	}

	// Fallback: launch the agent in an isolated tmux server
	return e.launchIsolatedAgent(ctx, job, plan, workDir, spec, agentArgs, flowCfg.AgentEnv, briefingFilePath)
}

// launchIsolatedAgent starts the agent in an isolated tmux server using a custom socket.
func (e *IsolatedAgentExecutor) launchIsolatedAgent(ctx context.Context, job *Job, plan *Plan, workDir string, spec *AgentProviderSpec, agentArgs []string, agentEnv map[string]string, briefingFilePath string) error {
	// Update job status to running
	job.Status = JobStatusRunning
	job.StartTime = time.Now()
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}

	// Create the isolated tmux socket name
	socketName := TmuxSocketName(job.ID)
	sessionName := "main" // Simple session name since the socket is unique
	targetPane := TmuxTargetPane(job.ID)

	e.ulog.Info("Creating isolated tmux server for agent").
		Field("socket", socketName).
		Field("job_id", job.ID).
		Field("provider", spec.Name).
		Pretty(theme.IconInteractiveAgent + " Creating isolated tmux server: " + socketName).
		Log(ctx)

	engine, err := mux.NewTmuxEngineWithSocket(socketName)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to create isolated mux engine: %w", err)
	}

	if err := engine.CreateSession(ctx, sessionName, mux.WithWorkDir(workDir)); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to create isolated session: %w", err)
	}

	// Build the agent command from the provider's spec (same instruction and
	// command shape as the interactive/groveterm launch paths).
	agentCommand := spec.BuildShellCommand(agentArgs, buildBriefingInstruction(briefingFilePath))

	// Inline env vars on the agent command itself — scoped to the agent
	// process, not exported into the pane's shell, so nothing leaks after
	// the agent exits. GROVE_SCOPE is inherited from the executor's env
	// (treemux or daemon), not forced from workDir.
	scopePrefix := ""
	if scope := os.Getenv("GROVE_SCOPE"); scope != "" {
		scopePrefix = fmt.Sprintf("GROVE_SCOPE='%s' ", scope)
	}
	escapedTitle := "'" + strings.ReplaceAll(job.Title, "'", "'\\''") + "'"
	configPath := strings.ReplaceAll(AgentConfigArtifactPath(plan.Directory, job.ID), "'", "'\\''")
	envPrefix := agentEnvInline(agentEnv) + scopePrefix + fmt.Sprintf("GROVE_FLOW_JOB_ID='%s' GROVE_FLOW_JOB_PATH='%s' GROVE_FLOW_PLAN_NAME='%s' GROVE_FLOW_JOB_TITLE=%s GROVE_FLOW_ISOLATED='true' GROVE_CONFIG_FILE='%s' ",
		job.ID, job.FilePath, plan.Name, escapedTitle, configPath)
	envPrefix += playbookEnvInline(job, plan)

	// Wrap agent command with deterministic PID capture, then prefix
	// inline env so everything runs as one shell invocation.
	wrappedCommand := envPrefix + agentstream.BuildAgentCommand(job.ID, agentCommand)
	// Send the agent command to the isolated pane
	if err := engine.SendKeys(ctx, targetPane, wrappedCommand, "C-m"); err != nil {
		e.log.WithError(err).Error("Failed to send agent command")
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to send agent command: %w", err)
	}

	e.ulog.Success("Isolated agent launched").
		Field("socket", socketName).
		Field("job_id", job.ID).
		Field("provider", spec.Name).
		Pretty(theme.IconSuccess + " Isolated agent launched in socket: " + socketName).
		Log(ctx)

	// Register the session asynchronously
	go func() {
		if err := e.discoverAndRegisterSession(job, plan, workDir, socketName, targetPane, spec.Name); err != nil {
			e.log.WithError(err).Error("Failed to register isolated session")
		}
	}()

	// Create lock file with a placeholder PID (we'll discover the real one async)
	// For isolated agents, we use the socket name as an identifier
	if err := CreateLockFile(job.FilePath, 0); err != nil {
		e.log.WithError(err).Warn("Failed to create lock file")
	}

	return nil
}

// discoverAndRegisterSession discovers the agent PID and registers the session.
func (e *IsolatedAgentExecutor) discoverAndRegisterSession(job *Job, plan *Plan, workDir, socketName, targetPane, providerName string) error {
	logger := grovelogging.NewLogger("flow-isolated-session-discovery")

	logger.WithFields(map[string]interface{}{
		"job_id":   job.ID,
		"plan":     plan.Name,
		"socket":   socketName,
		"provider": providerName,
	}).Debug("Starting isolated agent PID discovery and registration")

	// Wait for PID via deterministic pidfile (written by BuildAgentCommand)
	ctx := context.Background()
	agentPID, err := agentstream.WaitForPID(ctx, job.ID)
	if err != nil {
		// Fallback to legacy process tree traversal
		logger.WithError(err).Warn("Pidfile-based PID discovery failed, falling back to process tree traversal")
		time.Sleep(500 * time.Millisecond)
		var pidErr error
		maxPIDRetries := 30
		for i := 0; i < maxPIDRetries; i++ {
			agentPID, pidErr = e.findAgentPIDForIsolatedPane(socketName, targetPane, providerName, logger)
			if pidErr == nil && agentPID > 0 {
				break
			}
			time.Sleep(1 * time.Second)
		}
		if agentPID == 0 {
			return fmt.Errorf("failed to find agent PID: %w", pidErr)
		}
	} else {
		logger.WithField("pid", agentPID).Debug("Discovered agent PID via pidfile")
		_ = agentstream.CleanupPIDFile(job.ID)
	}

	// Update lock file with actual PID
	if err := CreateLockFile(job.FilePath, agentPID); err != nil {
		logger.WithError(err).Warn("Failed to update lock file with agent PID")
	}

	// Get user info
	user := os.Getenv("USER")
	if user == "" {
		user = "unknown"
	}

	// Get git info
	repo, branch := getGitInfo(workDir)

	// Create metadata
	metadata := sessions.SessionMetadata{
		SessionID:        job.ID,
		ParentJobID:      job.ParentJobID,
		ClaudeSessionID:  "", // Will be discovered later if needed
		Provider:         providerName,
		PID:              agentPID,
		WorkingDirectory: workDir,
		User:             user,
		Repo:             repo,
		Branch:           branch,
		StartedAt:        time.Now(),
		JobTitle:         job.Title,
		PlanName:         plan.Name,
		JobFilePath:      job.FilePath,
		Type:             "isolated_agent",
	}

	// Register using the FileSystemRegistry
	registry, err := sessions.NewFileSystemRegistry()
	if err != nil {
		return fmt.Errorf("failed to create session registry: %w", err)
	}

	if err := registry.Register(metadata); err != nil {
		return fmt.Errorf("failed to register session: %w", err)
	}

	logger.WithFields(logrus.Fields{
		"session_id": job.ID,
		"pid":        agentPID,
		"socket":     socketName,
	}).Info("Registered isolated agent session")

	return nil
}

// findAgentPIDForIsolatedPane finds the PID of the agent process running in an isolated tmux pane.
func (e *IsolatedAgentExecutor) findAgentPIDForIsolatedPane(socketName, targetPane, providerName string, logger *logrus.Entry) (int, error) {
	engine, err := mux.NewTmuxEngineWithSocket(socketName)
	if err != nil {
		return 0, fmt.Errorf("failed to create isolated mux engine: %w", err)
	}

	shellPID, err := engine.GetPanePID(context.Background(), targetPane)
	if err != nil {
		return 0, fmt.Errorf("failed to get pane PID: %w", err)
	}

	// Find the agent process that is a descendant of that shell
	targetComm := providerName
	if providerName == "claude" {
		// Try claude first, then node as fallback
		pid, err := process.FindDescendantPID(shellPID, "claude")
		if err == nil {
			return pid, nil
		}
		targetComm = "node"
	}

	return process.FindDescendantPID(shellPID, targetComm)
}

// determineWorkDir determines the working directory for a job.
func (e *IsolatedAgentExecutor) determineWorkDir(ctx context.Context, job *Job, plan *Plan) (string, error) {
	// For jobs with worktrees, we need to prepare the worktree if it doesn't exist yet
	if job.Worktree != "" {
		gitRoot, err := GetProjectGitRoot(plan.Directory)
		if err != nil {
			return "", fmt.Errorf("could not find git root: %w", err)
		}

		if _, _, exists := resolveWorktreeForJob(gitRoot, job.Worktree); !exists {
			// Isolated agents don't auto-create worktrees: they must already
			// exist, or the job runs in the main directory.
			e.log.WithField("worktree", job.Worktree).Warn("Worktree does not exist for isolated agent")
		}
	}

	return DetermineWorkingDirectory(plan, job)
}

// gatherContextFiles collects context files for the job.
func (e *IsolatedAgentExecutor) gatherContextFiles(job *Job, plan *Plan, workDir string) ([]string, error) {
	var contextFiles []string

	contextDir := ScopeToSubProject(workDir, job)

	if contextDir != "" {
		// Resolve context path via centralized manager
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
		for _, contextPath := range FindContextFiles(plan) {
			if _, err := os.Stat(contextPath); err == nil {
				contextFiles = append(contextFiles, contextPath)
			}
		}
	}

	return contextFiles, nil
}

// SendInputToIsolatedAgent sends input text to an isolated agent.
// Tries the daemon's native agent pane input API first (groveterm), falls back to tmux.
func SendInputToIsolatedAgent(jobID, input string) error {
	ctx := context.Background()

	// The session registry records the provider (and workdir) the isolated
	// job actually launched with; resolve the input mode from it. An
	// unresolvable session keeps the historical vim default (Claude).
	inputMode := "vim"
	if registry, err := sessions.NewFileSystemRegistry(); err == nil {
		if metadata, err := registry.Find(jobID); err == nil && metadata != nil {
			inputMode = resolveInputMode(metadata.WorkingDirectory, metadata.Provider)
		}
	}

	// Try daemon API first.
	daemonClient := daemon.NewWithAutoStart()
	if connected, _ := daemonClient.IsTerminalConnected(ctx); connected {
		payload := input + "\r"
		if inputMode == "vim" {
			payload = "\x1bi" + input + "\r"
		}
		err := daemonClient.SendAgentInput(ctx, jobID, payload)
		daemonClient.Close()
		if err == nil {
			return nil
		}
	} else {
		daemonClient.Close()
	}

	// Fallback: send-keys on the isolated tmux server.
	socketName := TmuxSocketName(jobID)
	targetPane := TmuxTargetPane(jobID)

	engine, err := mux.NewTmuxEngineWithSocket(socketName)
	if err != nil {
		return fmt.Errorf("failed to create isolated mux engine: %w", err)
	}

	if inputMode == "vim" {
		if err := engine.SendKeys(context.Background(), targetPane, "Escape", "i", input); err != nil {
			return err
		}
	} else {
		if err := engine.SendKeys(context.Background(), targetPane, input); err != nil {
			return err
		}
	}

	return engine.SendKeys(context.Background(), targetPane, "C-Enter")
}

// KillIsolatedAgentServer kills the isolated tmux server for a job.
func KillIsolatedAgentServer(jobID string) error {
	socketName := TmuxSocketName(jobID)
	engine, err := mux.NewTmuxEngineWithSocket(socketName)
	if err != nil {
		return fmt.Errorf("failed to create isolated mux engine: %w", err)
	}
	return engine.KillServer(context.Background(), "")
}

// SendInterruptToIsolatedAgent sends Ctrl+C to interrupt an isolated agent.
// Tries the daemon's native agent pane input API first (groveterm), falls back to tmux.
func SendInterruptToIsolatedAgent(jobID string) error {
	// Try daemon API first.
	ctx := context.Background()
	daemonClient := daemon.NewWithAutoStart()
	if connected, _ := daemonClient.IsTerminalConnected(ctx); connected {
		err := daemonClient.SendAgentInput(ctx, jobID, "\x03")
		daemonClient.Close()
		if err == nil {
			return nil
		}
	} else {
		daemonClient.Close()
	}

	// Fallback: send-keys C-c on the isolated tmux server.
	socketName := TmuxSocketName(jobID)
	targetPane := TmuxTargetPane(jobID)

	engine, err := mux.NewTmuxEngineWithSocket(socketName)
	if err != nil {
		return fmt.Errorf("failed to create isolated mux engine: %w", err)
	}
	return engine.SendKeys(context.Background(), targetPane, "C-c")
}

// CaptureIsolatedAgentOutput captures the visible output from an isolated agent's pane.
// Tries the daemon's native agent pane capture API first (groveterm), falls back to tmux.
func CaptureIsolatedAgentOutput(jobID string) (string, error) {
	// Try daemon API first.
	ctx := context.Background()
	daemonClient := daemon.NewWithAutoStart()
	if connected, _ := daemonClient.IsTerminalConnected(ctx); connected {
		result, err := daemonClient.CaptureAgentPane(ctx, jobID)
		daemonClient.Close()
		if err == nil {
			return result, nil
		}
	} else {
		daemonClient.Close()
	}

	// Fallback: capture-pane on the isolated tmux server.
	socketName := TmuxSocketName(jobID)
	targetPane := TmuxTargetPane(jobID)

	engine, err := mux.NewTmuxEngineWithSocket(socketName)
	if err != nil {
		return "", fmt.Errorf("failed to create isolated mux engine: %w", err)
	}
	return engine.CapturePane(context.Background(), targetPane)
}

// IsIsolatedAgentRunning checks if an isolated agent is still running.
// Tries the daemon's session store first (groveterm), falls back to engine list-sessions.
func IsIsolatedAgentRunning(jobID string) bool {
	// Try daemon API first — check if a running session exists for this job.
	ctx := context.Background()
	daemonClient := daemon.NewWithAutoStart()
	if connected, _ := daemonClient.IsTerminalConnected(ctx); connected {
		sessions, err := daemonClient.GetSessions(ctx)
		daemonClient.Close()
		if err == nil {
			for _, s := range sessions {
				if s.ID == jobID && (s.Status == "running" || s.Status == "idle") {
					return true
				}
			}
		}
	} else {
		daemonClient.Close()
	}

	// Fallback: list-sessions on the isolated tmux server.
	socketName := TmuxSocketName(jobID)
	engine, err := mux.NewTmuxEngineWithSocket(socketName)
	if err != nil {
		return false
	}
	sessions, err := engine.ListSessions(context.Background())
	return err == nil && len(sessions) > 0
}

// SetOutput sets the output writer for the executor.
// For isolated agents, output is captured via tmux rather than written to a stream.
func (e *IsolatedAgentExecutor) SetOutput(w io.Writer) {
	// No-op for isolated agents - output is captured via tmux capture-pane
}
