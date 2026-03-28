package orchestration

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/grovetools/core/config"
	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/sessions"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/pkg/tmux"
	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/flow/pkg/exec"
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
	contextFiles := e.gatherContextFiles(job, plan, workDir)

	// Build the XML prompt and get the list of files to upload.
	promptXML, _, err := BuildXMLPrompt(job, plan, workDir, contextFiles)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
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

	// Log briefing file creation
	requestID, _ := ctx.Value("request_id").(string)
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

	// Unmarshal flow configuration (agent settings moved to flow extension)
	type flowProviderConfig struct {
		Args []string `yaml:"args"`
	}
	type flowConfig struct {
		InteractiveProvider string                        `yaml:"interactive_provider,omitempty"`
		Providers           map[string]flowProviderConfig `yaml:"providers"`
	}
	var flowCfg flowConfig
	coreCfg.UnmarshalExtension("flow", &flowCfg)

	// Determine which provider to use (default to claude)
	providerName := "claude"
	if flowCfg.InteractiveProvider != "" {
		providerName = flowCfg.InteractiveProvider
	}

	// Get agent args for the selected provider
	var agentArgs []string
	if flowCfg.Providers != nil {
		if providerCfg, ok := flowCfg.Providers[providerName]; ok {
			agentArgs = providerCfg.Args
		}
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

	// Launch the agent in an isolated tmux server
	return e.launchIsolatedAgent(ctx, job, plan, workDir, providerName, agentArgs, briefingFilePath)
}

// launchIsolatedAgent starts the agent in an isolated tmux server using a custom socket.
func (e *IsolatedAgentExecutor) launchIsolatedAgent(ctx context.Context, job *Job, plan *Plan, workDir, providerName string, agentArgs []string, briefingFilePath string) error {
	// Update job status to running
	job.Status = JobStatusRunning
	job.StartTime = time.Now()
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}

	// Create the isolated tmux socket name
	socketName := TmuxSocketName(job.ID)
	sessionName := "main"      // Simple session name since the socket is unique
	windowName := "0"          // First window
	targetPane := TmuxTargetPane(job.ID)

	e.ulog.Info("Creating isolated tmux server for agent").
		Field("socket", socketName).
		Field("job_id", job.ID).
		Field("provider", providerName).
		Pretty(theme.IconInteractiveAgent + " Creating isolated tmux server: " + socketName).
		Log(ctx)

	executor := &exec.RealCommandExecutor{}

	// Create a new tmux server with a custom socket
	// The -d flag creates the session in detached mode
	createArgs := []string{
		"-L", socketName,       // Use custom socket
		"new-session",
		"-d",                   // Detached
		"-s", sessionName,      // Session name
		"-n", windowName,       // Window name
		"-c", workDir,          // Working directory
	}

	if err := executor.Execute("tmux", createArgs...); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to create isolated tmux session: %w", err)
	}

	// Build the agent command based on provider
	agentCommand, err := e.buildAgentCommand(providerName, briefingFilePath, agentArgs)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to build agent command: %w", err)
	}

	// Set environment variables in the isolated tmux pane
	escapedTitle := "'" + strings.ReplaceAll(job.Title, "'", "'\\''") + "'"
	envCommand := fmt.Sprintf("export GROVE_FLOW_JOB_ID='%s'; export GROVE_FLOW_JOB_PATH='%s'; export GROVE_FLOW_PLAN_NAME='%s'; export GROVE_FLOW_JOB_TITLE=%s; export GROVE_FLOW_ISOLATED='true'",
		job.ID, job.FilePath, plan.Name, escapedTitle)

	if err := executor.Execute("tmux", "-L", socketName, "send-keys", "-t", targetPane, envCommand, "C-m"); err != nil {
		e.log.WithError(err).Error("Failed to set environment variables")
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to set environment variables: %w", err)
	}

	// Small delay to ensure environment variables are set
	time.Sleep(100 * time.Millisecond)

	// Send the agent command to the isolated pane
	if err := executor.Execute("tmux", "-L", socketName, "send-keys", "-t", targetPane, agentCommand, "C-m"); err != nil {
		e.log.WithError(err).Error("Failed to send agent command")
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to send agent command: %w", err)
	}

	e.ulog.Success("Isolated agent launched").
		Field("socket", socketName).
		Field("job_id", job.ID).
		Field("provider", providerName).
		Pretty(theme.IconSuccess + " Isolated agent launched in socket: " + socketName).
		Log(ctx)

	// Register the session asynchronously
	go func() {
		if err := e.discoverAndRegisterSession(job, plan, workDir, socketName, targetPane, providerName); err != nil {
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

// buildAgentCommand constructs the agent command based on the provider.
func (e *IsolatedAgentExecutor) buildAgentCommand(providerName, briefingFilePath string, agentArgs []string) (string, error) {
	escapedPath := "'" + strings.ReplaceAll(briefingFilePath, "'", "'\\''") + "'"
	prompt := fmt.Sprintf("Read the briefing file at %s and execute the task.", escapedPath)

	var cmdParts []string
	switch providerName {
	case "claude":
		cmdParts = []string{"claude"}
	case "codex":
		cmdParts = []string{"codex"}
	case "opencode":
		// For isolated agents, use interactive mode with --prompt
		cmdParts = []string{"opencode"}
		cmdParts = append(cmdParts, agentArgs...)
		cmdParts = append(cmdParts, "--prompt", fmt.Sprintf("\"%s\"", prompt))
		return strings.Join(cmdParts, " "), nil
	default:
		return "", fmt.Errorf("unknown provider for isolated agent: '%s'", providerName)
	}

	cmdParts = append(cmdParts, agentArgs...)
	return fmt.Sprintf("%s \"%s\"", strings.Join(cmdParts, " "), prompt), nil
}

// discoverAndRegisterSession discovers the agent PID and registers the session.
func (e *IsolatedAgentExecutor) discoverAndRegisterSession(job *Job, plan *Plan, workDir, socketName, targetPane, providerName string) error {
	logger := grovelogging.NewLogger("flow-isolated-session-discovery")

	logger.WithFields(map[string]interface{}{
		"job_id":     job.ID,
		"plan":       plan.Name,
		"socket":     socketName,
		"provider":   providerName,
	}).Debug("Starting isolated agent PID discovery and registration")

	// Wait for the agent process to start
	time.Sleep(500 * time.Millisecond)

	// Try to discover the agent PID
	var agentPID int
	var pidErr error
	maxPIDRetries := 30
	for i := 0; i < maxPIDRetries; i++ {
		agentPID, pidErr = e.findAgentPIDForIsolatedPane(socketName, targetPane, providerName, logger)
		if pidErr == nil && agentPID > 0 {
			logger.WithFields(logrus.Fields{
				"pid":         agentPID,
				"retry_count": i,
			}).Debug("Discovered agent PID")
			break
		}
		logger.WithFields(logrus.Fields{
			"error":       pidErr,
			"retry_count": i,
			"max_retries": maxPIDRetries,
		}).Debug("Agent PID not found yet, retrying...")
		time.Sleep(1 * time.Second)
	}

	if agentPID == 0 {
		return fmt.Errorf("failed to find agent PID after %d seconds: %w", maxPIDRetries, pidErr)
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
	// Use tmux with the custom socket to get the pane PID
	cmd := tmux.Command("-L", socketName, "display-message", "-p", "-t", targetPane, "#{pane_pid}")
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("failed to get pane PID: %w", err)
	}

	shellPID := 0
	fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &shellPID)
	if shellPID == 0 {
		return 0, fmt.Errorf("failed to parse pane PID from: %s", string(output))
	}

	// Find the agent process that is a descendant of that shell
	targetComm := providerName
	if providerName == "claude" {
		// Try claude first, then node as fallback
		pid, err := findDescendantPID(shellPID, "claude")
		if err == nil {
			return pid, nil
		}
		targetComm = "node"
	}

	return findDescendantPID(shellPID, targetComm)
}

// determineWorkDir determines the working directory for a job.
func (e *IsolatedAgentExecutor) determineWorkDir(ctx context.Context, job *Job, plan *Plan) (string, error) {
	// For jobs with worktrees, we need to prepare the worktree if it doesn't exist yet
	if job.Worktree != "" {
		gitRoot, err := GetProjectGitRoot(plan.Directory)
		if err != nil {
			return "", fmt.Errorf("could not find git root: %w", err)
		}

		// Check if we're already in the requested worktree
		currentPath := gitRoot
		if !strings.HasSuffix(currentPath, filepath.Join(".grove-worktrees", job.Worktree)) {
			actualGitRoot := gitRoot
			if strings.Contains(gitRoot, ".grove-worktrees") {
				parts := strings.Split(gitRoot, ".grove-worktrees")
				if len(parts) > 0 {
					actualGitRoot = strings.TrimSuffix(parts[0], string(filepath.Separator))
				}
			}

			worktreePath := filepath.Join(actualGitRoot, ".grove-worktrees", job.Worktree)
			if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
				// Worktree doesn't exist - for isolated agents, we don't auto-create worktrees
				// They should either exist or the job should run in the main directory
				e.log.WithField("worktree", job.Worktree).Warn("Worktree does not exist for isolated agent")
			}
		}
	}

	return DetermineWorkingDirectory(plan, job)
}

// gatherContextFiles collects context files for the job.
func (e *IsolatedAgentExecutor) gatherContextFiles(job *Job, plan *Plan, workDir string) []string {
	var contextFiles []string

	contextDir := ScopeToSubProject(workDir, job)

	if contextDir != "" {
		// Resolve context path via notebook locator
		node, _ := workspace.GetProjectByPath(contextDir)
		cfg, _ := config.LoadFrom(contextDir)
		if cfg == nil {
			cfg, _ = config.LoadDefault()
		}
		locator := workspace.NewNotebookLocator(cfg)

		contextPath := filepath.Join(contextDir, ".grove", "context")
		if genDir, err := locator.GetContextGeneratedDir(node); err == nil {
			contextPath = filepath.Join(genDir, "context")
		}

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

	return contextFiles
}

// SendInputToIsolatedAgent sends input text to an isolated agent via tmux send-keys.
// Uses a sequence that works in both vim mode and normal mode in claude code:
// 1. Escape i <text> - ensures we're in insert mode (Escape is no-op in normal mode)
// 2. C-Enter - submits the input (must be separate send-keys call)
func SendInputToIsolatedAgent(jobID, input string) error {
	socketName := TmuxSocketName(jobID)
	targetPane := TmuxTargetPane(jobID)

	executor := &exec.RealCommandExecutor{}

	// First: Escape to ensure clean state, i to enter insert mode, then the text
	if err := executor.Execute("tmux", "-L", socketName, "send-keys", "-t", targetPane, "Escape", "i", input); err != nil {
		return err
	}

	// Second: C-Enter to submit (must be separate call)
	return executor.Execute("tmux", "-L", socketName, "send-keys", "-t", targetPane, "C-Enter")
}

// KillIsolatedAgentServer kills the isolated tmux server for a job.
func KillIsolatedAgentServer(jobID string) error {
	socketName := TmuxSocketName(jobID)
	executor := &exec.RealCommandExecutor{}
	// kill-server will terminate all sessions and the tmux server for this socket
	return executor.Execute("tmux", "-L", socketName, "kill-server")
}

// SendInterruptToIsolatedAgent sends Ctrl+C to interrupt an isolated agent.
func SendInterruptToIsolatedAgent(jobID string) error {
	socketName := TmuxSocketName(jobID)
	targetPane := TmuxTargetPane(jobID)

	executor := &exec.RealCommandExecutor{}
	return executor.Execute("tmux", "-L", socketName, "send-keys", "-t", targetPane, "C-c")
}

// CaptureIsolatedAgentOutput captures the visible output from an isolated agent's pane.
func CaptureIsolatedAgentOutput(jobID string) (string, error) {
	socketName := TmuxSocketName(jobID)
	targetPane := TmuxTargetPane(jobID)

	cmd := tmux.Command("-L", socketName, "capture-pane", "-t", targetPane, "-p")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to capture pane output: %w", err)
	}

	return string(output), nil
}

// IsIsolatedAgentRunning checks if an isolated agent's tmux server is still running.
func IsIsolatedAgentRunning(jobID string) bool {
	socketName := TmuxSocketName(jobID)
	cmd := tmux.Command("-L", socketName, "list-sessions")
	err := cmd.Run()
	return err == nil
}

// SetOutput sets the output writer for the executor.
// For isolated agents, output is captured via tmux rather than written to a stream.
func (e *IsolatedAgentExecutor) SetOutput(w io.Writer) {
	// No-op for isolated agents - output is captured via tmux capture-pane
}
