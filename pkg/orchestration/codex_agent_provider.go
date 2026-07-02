package orchestration

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/grovetools/agentlogs/pkg/agentstream"
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
	"github.com/sirupsen/logrus"
)

type CodexAgentProvider struct {
	log      *logrus.Entry
	ulog     *grovelogging.UnifiedLogger
	agentEnv map[string]string // flow.agent_env injected into the agent process
}

func NewCodexAgentProvider() *CodexAgentProvider {
	return &CodexAgentProvider{
		log:  grovelogging.NewLogger("grove-flow"),
		ulog: grovelogging.NewUnifiedLogger("grove-flow"),
	}
}

func (p *CodexAgentProvider) Launch(ctx context.Context, job *Job, plan *Plan, workDir string, agentArgs []string, briefingFilePath string) error {
	// Update job status to running
	job.Status = JobStatusRunning
	job.StartTime = time.Now()
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}

	agentTarget := "tmux"
	if plan.Orchestration != nil && plan.Orchestration.AgentTarget != "" {
		agentTarget = plan.Orchestration.AgentTarget
	}
	muxType := models.MuxTmux
	if agentTarget == "tuimux" {
		muxType = models.MuxTuimux
	}

	// Register session intent with the daemon BEFORE launching the agent,
	// mirroring the claude provider: the daemon tracks the session and waits
	// for confirmation with the discovered PID/transcript. Failure degrades
	// gracefully — the async confirm falls back to the filesystem registry.
	daemonClient := daemon.NewWithAutoStart()
	defer daemonClient.Close()

	p.log.WithFields(logrus.Fields{
		"job_id":         job.ID,
		"daemon_running": daemonClient.IsRunning(),
	}).Info("Registering codex session intent with daemon")

	if err := daemonClient.RegisterSessionIntent(ctx, daemon.SessionIntent{
		JobID:        job.ID,
		Provider:     "codex",
		JobFilePath:  job.FilePath,
		PlanName:     plan.Name,
		Title:        job.Title,
		WorkDir:      workDir,
		Channels:     job.Channels,
		SignalTarget: job.SignalTarget,
		Autonomous:   job.Autonomous,
		Mux:          muxType,
	}); err != nil {
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

	// Generate session name
	sessionName, err := mux.GenerateSessionName(workDir)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return err
	}

	// Check if session already exists
	sessionExists, _ := engine.SessionExists(ctx, sessionName)

	if !sessionExists {
		p.log.WithField("session", sessionName).Info("Creating new session for interactive job")
		if err := engine.CreateSession(ctx, sessionName, mux.WithWorkDir(workDir)); err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to create session: %w", err)
		}

		sessionPID, err := engine.GetSessionPID(ctx, sessionName)
		if err != nil {
			return fmt.Errorf("could not get session PID to create lock file: %w", err)
		}
		if err := CreateLockFile(job.FilePath, sessionPID); err != nil {
			return fmt.Errorf("failed to create lock file with session PID: %w", err)
		}
	} else {
		p.log.WithField("session", sessionName).Info("Using existing session for interactive job")
	}

	// Build agent command (reuse Claude provider's logic but replace "claude" with "codex")
	agentCommand, err := p.buildAgentCommand(job, plan, briefingFilePath, agentArgs)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to build agent command: %w", err)
	}

	// Create a new window for this specific agent job in the session
	agentWindowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)

	p.ulog.Info("Launching Codex agent in worktree session").
		Field("window", agentWindowName).
		Field("session", sessionName).
		Pretty(theme.IconWorktree + " Launching Codex agent in worktree session").
		Log(ctx)

	isTUIMode := os.Getenv("GROVE_FLOW_TUI_MODE") == "true"
	if err := engine.NewWindow(ctx, sessionName, agentWindowName, workDir, true); err != nil {
		p.log.WithError(err).Warn("Failed to create agent window, may already exist. Will attempt to use it.")
	}

	// Set environment variables in the window's shell so they're available to the codex process
	targetPane := fmt.Sprintf("%s:%s", sessionName, agentWindowName)

	// Update the daemon with the tmux target so channels/pinger can route to this session
	if err := daemonClient.UpdateSessionTmuxTarget(ctx, job.ID, targetPane); err != nil {
		p.log.WithError(err).Warn("Failed to update tmux target on daemon")
	}

	// Inline env vars on the agent command itself so they're scoped to the
	// codex process and don't leak into the user's interactive shell after
	// the agent exits. GROVE_SCOPE is inherited from the executor's env
	// (treemux or daemon), not forced from workDir.
	scopePrefix := ""
	if scope := os.Getenv("GROVE_SCOPE"); scope != "" {
		scopePrefix = fmt.Sprintf("GROVE_SCOPE='%s' ", scope)
	}
	escapedTitle := "'" + strings.ReplaceAll(job.Title, "'", "'\\''") + "'"
	envPrefix := agentEnvInline(p.agentEnv) + "GROVE_AGENT_PROVIDER='codex' " + scopePrefix + fmt.Sprintf("GROVE_FLOW_JOB_ID='%s' GROVE_FLOW_JOB_PATH='%s' GROVE_FLOW_PLAN_NAME='%s' GROVE_FLOW_JOB_TITLE=%s ",
		job.ID, job.FilePath, plan.Name, escapedTitle)
	if node, err := workspace.GetProjectByPath(workDir); err == nil && node != nil {
		logDir := filepath.Join(paths.StateDir(), "logs", "workspaces", node.Identifier("/"))
		envPrefix += fmt.Sprintf("GROVE_LOG_DIR='%s' ", logDir)
	}
	envPrefix += playbookEnvInline(job, plan)

	// Wrap the agent command with deterministic PID capture (pidfile), then
	// prefix inline env so everything runs as one shell invocation — the same
	// shape the claude provider uses.
	wrappedCommand := envPrefix + agentstream.BuildAgentCommand(job.ID, agentCommand)
	if err := engine.SendKeys(ctx, targetPane, wrappedCommand, "C-m"); err != nil {
		p.log.WithError(err).Error("Failed to send agent command")
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to send agent command: %w", err)
	}

	// Asynchronously discover PID + transcript and confirm the session with
	// the daemon (intent was registered above). This replaces the old
	// synchronous newest-file scrape, which raced concurrent codex sessions
	// and blocked the launch path.
	go func() {
		if err := p.discoverAndRegisterSessionAsync(job, plan, workDir, targetPane); err != nil {
			p.log.WithError(err).Error("Failed to confirm codex session")
			// Continue anyway - the agent is running, just tracking may be impaired
		}
	}()

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

	// Return immediately. The lock file indicates the running state.
	return nil
}

// buildAgentCommand constructs the codex command for the interactive session.
func (p *CodexAgentProvider) buildAgentCommand(job *Job, plan *Plan, briefingFilePath string, agentArgs []string) (string, error) {
	// Pass a simple instruction to read the briefing file — cleaner than
	// reading the entire file content into the command. The instruction and
	// command shape come from the provider registry so this path stays
	// byte-identical with the groveterm/isolated launch paths.
	spec, _ := LookupAgentProvider("codex")
	return spec.BuildShellCommand(agentArgs, buildBriefingInstruction(briefingFilePath)), nil
}

// discoverAndRegisterSessionAsync discovers the codex PID and transcript path,
// then confirms the session with the daemon (the intent was registered in
// Launch). Designed to run in a goroutine. If the daemon is unreachable it
// falls back to the filesystem session registry, matching the claude provider's
// degradation path.
func (p *CodexAgentProvider) discoverAndRegisterSessionAsync(job *Job, plan *Plan, workDir, targetPane string) error {
	logger := grovelogging.NewLogger("flow-codex-session-discovery")

	logger.WithFields(map[string]interface{}{
		"job_id":      job.ID,
		"plan":        plan.Name,
		"target_pane": targetPane,
	}).Debug("Starting async codex PID discovery and confirmation")

	jobStartTime := job.StartTime
	if jobStartTime.IsZero() {
		jobStartTime = time.Now()
	}

	ctx := context.Background()

	// Wait for PID via deterministic pidfile (written by BuildAgentCommand)
	codexPID, err := agentstream.WaitForPID(ctx, job.ID)
	if err != nil {
		// Fallback to legacy process tree traversal
		logger.WithError(err).Warn("Pidfile-based PID discovery failed, falling back to process tree traversal")
		var pidErr error
		time.Sleep(500 * time.Millisecond)
		maxPIDRetries := 30
		for i := 0; i < maxPIDRetries; i++ {
			codexPID, pidErr = FindCodexPIDForPane(targetPane)
			if pidErr == nil && codexPID > 0 {
				break
			}
			time.Sleep(1 * time.Second)
		}
		if codexPID == 0 {
			logger.WithError(pidErr).Warn("Failed to find codex PID, continuing without it")
		}
	} else {
		logger.WithField("pid", codexPID).Debug("Discovered codex PID via pidfile")
		if err := agentstream.CleanupPIDFile(job.ID); err != nil {
			logger.WithError(err).WithField("job_id", job.ID).Warn("Failed to clean up PID file")
		}
	}

	// Discover the transcript via agentstream. The AfterTime filter scopes the
	// match to sessions started after this launch, so concurrent codex
	// sessions no longer race each other for "newest file".
	transcriptPath, err := agentstream.DiscoverTranscript(agentstream.DiscoverOptions{
		Provider:  "codex",
		WorkDir:   workDir,
		AfterTime: jobStartTime,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to discover codex transcript, retrying...")
		for i := 0; i < 10; i++ {
			time.Sleep(1 * time.Second)
			transcriptPath, err = agentstream.DiscoverTranscript(agentstream.DiscoverOptions{
				Provider:  "codex",
				WorkDir:   workDir,
				AfterTime: jobStartTime,
			})
			if err == nil {
				break
			}
		}
		if err != nil {
			logger.WithError(err).Warn("Codex transcript discovery failed, continuing without it")
		}
	}

	var nativeID string
	if transcriptPath != "" {
		nativeID = codexNativeSessionID(transcriptPath)
	}

	// Confirm the session with the daemon using the discovered PID
	daemonClient := daemon.NewWithAutoStart()
	defer daemonClient.Close()

	if err := daemonClient.ConfirmSession(ctx, daemon.SessionConfirmation{
		JobID:          job.ID,
		NativeID:       nativeID,
		PID:            codexPID,
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
			ClaudeSessionID:  nativeID,
			Provider:         "codex",
			PID:              codexPID,
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
		"session_id":      job.ID,
		"native_id":       nativeID,
		"pid":             codexPID,
		"transcript_path": transcriptPath,
	}).Info("Confirmed codex session")

	return nil
}

// codexRolloutUUIDRe matches the conversation UUID codex appends to rollout
// filenames (rollout-<timestamp>-<uuid>.jsonl).
var codexRolloutUUIDRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// codexNativeSessionID extracts the codex conversation UUID from a rollout
// transcript path. This is the id codex reports as thread-id in notify
// payloads, so storing it as the session's native id lets hooks correlate
// turn-complete events back to the session. Falls back to the basename (sans
// extension) when the filename doesn't carry a UUID.
func codexNativeSessionID(transcriptPath string) string {
	base := strings.TrimSuffix(filepath.Base(transcriptPath), ".jsonl")
	if id := codexRolloutUUIDRe.FindString(base); id != "" {
		return id
	}
	return base
}

// FindCodexPIDForPane finds the PID of the 'codex' process running within a specific tmux pane
// by traversing the process tree from the pane's shell.
func FindCodexPIDForPane(targetPane string) (int, error) {
	engine, err := mux.DetectMuxEngine(context.Background())
	if err != nil {
		return 0, fmt.Errorf("mux engine not available: %w", err)
	}

	shellPID, err := engine.GetPanePID(context.Background(), targetPane)
	if err != nil {
		return 0, fmt.Errorf("failed to get pane PID: %w", err)
	}

	// Find the 'codex' process that is a descendant of that shell.
	return process.FindDescendantPID(shellPID, "codex")
}

// getGitInfo returns the repo name and current branch for the given directory
func getGitInfo(workDir string) (repo, branch string) {
	// Get repo name from git config
	cmd := osexec.Command("git", "-C", workDir, "remote", "get-url", "origin")
	output, err := cmd.Output()
	if err == nil {
		repoURL := strings.TrimSpace(string(output))
		// Extract repo name from URL (e.g., "github.com/user/repo.git" -> "repo")
		parts := strings.Split(repoURL, "/")
		if len(parts) > 0 {
			repo = strings.TrimSuffix(parts[len(parts)-1], ".git")
		}
	}

	// Get current branch
	cmd = osexec.Command("git", "-C", workDir, "branch", "--show-current")
	output, err = cmd.Output()
	if err == nil {
		branch = strings.TrimSpace(string(output))
	}

	return
}
