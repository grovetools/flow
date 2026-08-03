package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

// PiAgentProvider launches the pi coding agent
// (github.com/earendil-works/pi, binary `pi`) in a tmux/tuimux pane. It
// follows the codex provider's lifecycle exactly: daemon
// RegisterSessionIntent before launch, pidfile-based PID discovery, transcript
// discovery via agentstream (AfterTime-scoped), then ConfirmSession with a
// filesystem-registry fallback.
type PiAgentProvider struct {
	log          *logrus.Entry
	ulog         *grovelogging.UnifiedLogger
	agentEnv     map[string]string // flow.agent_env injected into the agent process
	providerName string
	runtime      PiRuntimeDescriptor
}

func NewPiAgentProvider() *PiAgentProvider {
	return newPiAgentProvider(PiRuntimeDescriptor{Name: "pi", Binary: "pi", ConfigDirName: ".pi", ManagedCodexAuth: true})
}

func NewPiAgentProviderFor(providerName string) *PiAgentProvider {
	spec, ok := LookupAgentProvider(providerName)
	if !ok || spec.PiRuntime == nil {
		panic("NewPiAgentProviderFor called with non-Pi provider " + providerName)
	}
	return newPiAgentProvider(*spec.PiRuntime)
}

func newPiAgentProvider(runtime PiRuntimeDescriptor) *PiAgentProvider {
	return &PiAgentProvider{
		log:          grovelogging.NewLogger("grove-flow"),
		ulog:         grovelogging.NewUnifiedLogger("grove-flow"),
		providerName: runtime.Name,
		runtime:      runtime,
	}
}

func (p *PiAgentProvider) Launch(ctx context.Context, job *Job, plan *Plan, workDir string, agentArgs []string, briefingFilePath string) error {
	if p.runtime.ManagedCodexAuth {
		if err := requireManagedPiCodexAuth(); err != nil {
			return err
		}
	}
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
	// mirroring the claude/codex providers: the daemon tracks the session and
	// waits for confirmation with the discovered PID/transcript. Failure
	// degrades gracefully — the async confirm falls back to the filesystem
	// registry.
	//
	// sessionHostClient, like every other interactive provider: the intent, the
	// confirmation below, and the eventual EndSession must all land on the one
	// daemon whose stream feeds the host's rail and Agents drawer.
	daemonClient := sessionHostClient(workDir)
	defer daemonClient.Close()

	p.log.WithFields(logrus.Fields{
		"job_id":         job.ID,
		"daemon_running": daemonClient.IsRunning(),
	}).Info("Registering pi session intent with daemon")

	if err := daemonClient.RegisterSessionIntent(ctx, newAgentSessionIntent(job, plan, p.providerName, workDir, muxType)); err != nil {
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

	// Pi sessions are owned by Flow and scoped to this guest job's artifact
	// directory. --session-dir is appended after user args so callers cannot
	// redirect transcripts into HOME or another job.
	sessionDir, err := preparePiJobSessionDir(plan.Directory, job.ID)
	if err != nil {
		return err
	}
	agentArgs = append(agentArgs, "--session-dir", sessionDir)

	// Build agent command via the provider registry so the pane command bytes
	// stay identical with the groveterm/isolated launch paths.
	agentCommand, err := p.buildAgentCommand(job, plan, briefingFilePath, agentArgs)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to build agent command: %w", err)
	}

	// Create a new window for this specific agent job in the session
	agentWindowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)

	p.ulog.Info("Launching pi agent in worktree session").
		Field("window", agentWindowName).
		Field("session", sessionName).
		Pretty(theme.IconWorktree + " Launching pi agent in worktree session").
		Log(ctx)

	isTUIMode := os.Getenv("GROVE_FLOW_TUI_MODE") == "true"
	if err := engine.NewWindow(ctx, sessionName, agentWindowName, workDir, true); err != nil {
		p.log.WithError(err).Warn("Failed to create agent window, may already exist. Will attempt to use it.")
	}

	targetPane := fmt.Sprintf("%s:%s", sessionName, agentWindowName)

	// Update the daemon with the tmux target so channels/pinger can route to this session
	if err := daemonClient.UpdateSessionTmuxTarget(ctx, job.ID, targetPane); err != nil {
		p.log.WithError(err).Warn("Failed to update tmux target on daemon")
	}

	// Inline env vars on the agent command itself so they're scoped to the
	// pi process and don't leak into the user's interactive shell after the
	// agent exits. GROVE_SCOPE is inherited from the executor's env (treemux
	// or daemon), not forced from workDir. GROVE_HOST_DAEMON_SOCKET is the
	// orthogonal transport answer (claude/codex carry it the same way): pi's
	// own hooks open their own daemon client, and the Stop hook's terminal
	// status must reach the daemon this launch registered against instead of
	// following GROVE_SCOPE off to the worktree's daemon.
	scopePrefix := ""
	if scope := os.Getenv("GROVE_SCOPE"); scope != "" {
		scopePrefix = fmt.Sprintf("GROVE_SCOPE='%s' ", scope)
	}
	escapedTitle := "'" + strings.ReplaceAll(job.Title, "'", "'\\''") + "'"
	configPath := strings.ReplaceAll(AgentConfigArtifactPath(plan.Directory, job.ID), "'", "'\\''")
	envPrefix := agentEnvInline(p.agentEnv) + fmt.Sprintf("GROVE_AGENT_PROVIDER='%s' ", p.providerName) + scopePrefix + hostSocketEnvInline(workDir) + fmt.Sprintf("GROVE_FLOW_JOB_ID='%s' GROVE_FLOW_JOB_PATH='%s' GROVE_FLOW_PLAN_NAME='%s' GROVE_FLOW_JOB_TITLE=%s GROVE_CONFIG_FILE='%s' ",
		job.ID, job.FilePath, plan.Name, escapedTitle, configPath)
	if node, err := workspace.GetProjectByPath(workDir); err == nil && node != nil {
		logDir := filepath.Join(paths.StateDir(), "logs", "workspaces", node.Identifier("/"))
		envPrefix += fmt.Sprintf("GROVE_LOG_DIR='%s' ", logDir)
	}
	envPrefix += playbookEnvInline(job, plan)

	// Wrap the agent command with deterministic PID capture (pidfile), then
	// prefix inline env so everything runs as one shell invocation — the same
	// shape the claude/codex providers use.
	wrappedCommand := envPrefix + agentstream.BuildAgentCommand(job.ID, agentCommand)
	if err := engine.SendKeys(ctx, targetPane, wrappedCommand, "C-m"); err != nil {
		p.log.WithError(err).Error("Failed to send agent command")
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to send agent command: %w", err)
	}

	// Asynchronously discover PID + transcript and confirm the session with
	// the daemon (intent was registered above).
	go func() {
		if err := p.discoverAndRegisterSessionAsync(job, plan, workDir, targetPane); err != nil {
			p.log.WithError(err).Error("Failed to confirm pi session")
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

// buildAgentCommand constructs the pi command for the interactive session.
// pi auto-submits a positional argument as the first prompt (interactive-mode
// startup in the pi source), so the standard positional briefing instruction
// works unchanged.
func (p *PiAgentProvider) buildAgentCommand(job *Job, plan *Plan, briefingFilePath string, agentArgs []string) (string, error) {
	spec, _ := LookupAgentProvider(p.providerName)
	return spec.BuildShellCommand(agentArgs, buildBriefingInstruction(briefingFilePath)), nil
}

// discoverAndRegisterSessionAsync discovers the pi PID and transcript path,
// then confirms the session with the daemon (the intent was registered in
// Launch). Designed to run in a goroutine. If the daemon is unreachable it
// falls back to the filesystem session registry, matching the claude/codex
// providers' degradation path.
func (p *PiAgentProvider) discoverAndRegisterSessionAsync(job *Job, plan *Plan, workDir, targetPane string) error {
	logger := grovelogging.NewLogger("flow-pi-session-discovery")

	logger.WithFields(map[string]interface{}{
		"job_id":      job.ID,
		"plan":        plan.Name,
		"target_pane": targetPane,
	}).Debug("Starting async pi PID discovery and confirmation")

	jobStartTime := job.StartTime
	if jobStartTime.IsZero() {
		jobStartTime = time.Now()
	}

	ctx := context.Background()

	agentTarget := "tmux"
	if plan.Orchestration != nil && plan.Orchestration.AgentTarget != "" {
		agentTarget = plan.Orchestration.AgentTarget
	}

	// Watch the pane from here, before the PID discovery (up to 30s of pidfile
	// wait plus 30s of process-tree fallback) and the transcript retry loop.
	// pi can print a startup error and exit within a second; CapturePane fails
	// as soon as the pane closes, so a single capture at the end of discovery
	// is always too late to see why. The watcher retains the last useful
	// snapshot and stops as soon as discovery settles.
	var (
		paneWatcher    *piPaneWatcher
		paneCaptureErr error
	)
	if engine, engineErr := mux.GetEngine(agentTarget); engineErr != nil {
		paneCaptureErr = fmt.Errorf("resolving %s engine for pane capture: %w", agentTarget, engineErr)
		logger.WithError(engineErr).WithField("agent_target", agentTarget).
			Warn("Pane capture unavailable; pi startup failures will be diagnosed without terminal output")
	} else {
		paneWatcher = startPiPaneWatcher(ctx, func(captureCtx context.Context) (string, error) {
			return engine.CapturePane(captureCtx, targetPane)
		})
		defer func() { _, _ = paneWatcher.stop() }()
	}

	// Wait for PID via deterministic pidfile (written by BuildAgentCommand).
	piPID, err := agentstream.WaitForPID(ctx, job.ID)
	if err != nil {
		// Fallback to process tree traversal. Note: pi is a Node CLI, so the
		// process may be named "node" rather than "pi" on some systems; the
		// pidfile above is the reliable path and this is best-effort only.
		logger.WithError(err).Warn("Pidfile-based PID discovery failed, falling back to process tree traversal")
		var pidErr error
		time.Sleep(500 * time.Millisecond)
		maxPIDRetries := 30
		for i := 0; i < maxPIDRetries; i++ {
			piPID, pidErr = findPiPIDForPane(targetPane, p.runtime.Binary)
			if pidErr == nil && piPID > 0 {
				break
			}
			time.Sleep(1 * time.Second)
		}
		if piPID == 0 {
			logger.WithError(pidErr).Warn("Failed to find pi PID, continuing without it")
		}
	} else {
		logger.WithField("pid", piPID).Debug("Discovered pi PID via pidfile")
		if err := agentstream.CleanupPIDFile(job.ID); err != nil {
			logger.WithError(err).WithField("job_id", job.ID).Warn("Failed to clean up PID file")
		}
	}
	// Once the PID is known the watcher can capture the instant pi dies,
	// instead of waiting for the next backoff tick to find an empty pane.
	paneWatcher.observePID(piPID)

	// Discover the transcript via agentstream. The AfterTime filter scopes the
	// match to sessions started after this launch, so concurrent pi sessions
	// don't race each other for "newest file" — except when this launch OPENS a
	// pre-existing transcript (a resume, or a seeded `responder: pi-session`
	// chat), whose header timestamp necessarily predates the launch. See
	// piDiscoveryAfterTime.
	launchSpec, _ := LookupAgentProvider(p.providerName)
	discovery := agentstream.DiscoverOptions{
		Provider:   "pi",
		WorkDir:    workDir,
		AfterTime:  piDiscoveryAfterTime(launchSpec, plan.Directory, job.ID, false, jobStartTime),
		SessionDir: piJobSessionDir(plan.Directory, job.ID),
	}
	transcriptPath, err := agentstream.DiscoverTranscript(discovery)
	if err != nil {
		logger.WithError(err).Warn("Failed to discover pi transcript, retrying...")
		for i := 0; i < 10; i++ {
			time.Sleep(1 * time.Second)
			transcriptPath, err = agentstream.DiscoverTranscript(discovery)
			if err == nil {
				break
			}
		}
		if err != nil {
			logger.WithError(err).Warn("pi transcript discovery failed, continuing without it")
		}
	}

	// Discovery is settled either way now: release the watcher so a healthy
	// launch stops paying for captures.
	var paneOutput string
	if paneWatcher != nil {
		paneOutput, paneCaptureErr = paneWatcher.stop()
	}

	if transcriptPath == "" {
		if paneOutput == "" {
			logger.WithError(paneCaptureErr).WithField("job_id", job.ID).
				Warn("No pi terminal output captured; startup diagnosis limited to the capture error")
		}
		handled, failureErr := handlePiStartupFailure(job, plan, piStartupEvidence{
			pid:          piPID,
			paneOutput:   paneOutput,
			captureErr:   paneCaptureErr,
			discoveryErr: err,
		})
		if failureErr != nil {
			return failureErr
		}
		if handled {
			logger.WithField("job_id", job.ID).Warn("Pi failed before creating a transcript; job marked failed")
			return nil
		}
		// No proof of death: an unknown PID alone must not fail a pi that is
		// merely slow to write its first transcript.
		logger.WithFields(map[string]interface{}{
			"job_id": job.ID,
			"pid":    piPID,
		}).Warn("pi produced no transcript but shows no evidence of failure; leaving job status unchanged")
	}

	var nativeID string
	if transcriptPath != "" {
		nativeID = piNativeSessionID(transcriptPath)
	}

	// Do not turn the pending intent into a live session until its exact
	// Flow-owned transcript is known. Confirming with an empty path causes
	// groved's token refresher to fall back to a global transcript scan.
	spec, _ := LookupAgentProvider(p.providerName)
	if err := requirePiTranscriptPath(spec, plan.Directory, job.ID, transcriptPath); err != nil {
		return err
	}

	// Confirm the session with the daemon using the discovered PID. Must be the
	// same host-routed daemon the intent was registered against above.
	daemonClient := sessionHostClient(workDir)
	defer daemonClient.Close()

	if err := daemonClient.ConfirmSession(ctx, daemon.SessionConfirmation{
		JobID:          job.ID,
		NativeID:       nativeID,
		PID:            piPID,
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
			ParentJobID:      job.ParentJobID,
			ClaudeSessionID:  nativeID,
			Provider:         p.providerName,
			PID:              piPID,
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
		"pid":             piPID,
		"transcript_path": transcriptPath,
	}).Info("Confirmed pi session")

	return nil
}

// piNativeSessionID extracts the pi session id (a uuidv7) from a session
// transcript path. pi names session files
// <timestamp-with-:-and-.-as-dashes>_<sessionId>.jsonl (SessionManager
// newSession in packages/coding-agent/src/core/session-manager.ts), and
// neither the munged timestamp nor a UUID contains "_", so the id is the part
// after the last underscore. Falls back to the basename (sans extension) when
// no underscore is present.
func piNativeSessionID(transcriptPath string) string {
	base := strings.TrimSuffix(filepath.Base(transcriptPath), ".jsonl")
	if idx := strings.LastIndex(base, "_"); idx >= 0 && idx+1 < len(base) {
		return base[idx+1:]
	}
	return base
}

// FindPiPIDForPane finds the PID of the 'pi' process running within a specific
// tmux pane by traversing the process tree from the pane's shell.
func FindPiPIDForPane(targetPane string) (int, error) {
	return findPiPIDForPane(targetPane, "pi")
}

func findPiPIDForPane(targetPane, binary string) (int, error) {
	engine, err := mux.DetectMuxEngine(context.Background())
	if err != nil {
		return 0, fmt.Errorf("mux engine not available: %w", err)
	}

	shellPID, err := engine.GetPanePID(context.Background(), targetPane)
	if err != nil {
		return 0, fmt.Errorf("failed to get pane PID: %w", err)
	}

	// Find this Pi-family runtime process beneath the pane shell.
	return process.FindDescendantPID(shellPID, binary)
}
