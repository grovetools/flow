package orchestration

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/grovetools/agentlogs/pkg/agentstream"
	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/models"
	"github.com/grovetools/core/pkg/sessions"
	"github.com/grovetools/core/tui/theme"
	"github.com/sirupsen/logrus"
)

// GrovetermAgentProvider implements InteractiveAgentProvider by delegating
// agent pane creation to groveterm via the daemon's relay API. Instead of
// launching a tmux session, it sends a SpawnAgentPane request through the
// daemon, which broadcasts an SSE event to groveterm. Groveterm then spawns
// a native PTY pane running the agent CLI directly.
type GrovetermAgentProvider struct {
	log         *logrus.Entry
	ulog        *grovelogging.UnifiedLogger
	spec        *AgentProviderSpec // registry spec for the agent CLI (claude/codex/opencode)
	autoSplit   bool               // whether to auto-split the pane in groveterm's UI
	agentTarget string             // "native" or "tuimux" — selects Mux type
	agentEnv    map[string]string  // flow.agent_env injected into the agent process (before GROVE_* keys)
	extraEnv    map[string]string  // additional env vars (e.g., GROVE_FLOW_ISOLATED for isolated agents)

	// confirmation is the handle for the post-launch discovery goroutine the
	// most recent LaunchPrepared started. See awaitSessionConfirmation.
	confirmation *sessionConfirmation
}

// NewGrovetermAgentProvider creates a new GrovetermAgentProvider.
// spec is the registry entry for the agent CLI to launch — callers resolve it
// via resolveJobProviderSpec, so an unknown provider name errors before this
// constructor is ever reached (no silent claude-shaped fallback).
// autoSplit controls whether the new pane is split into the active view.
// agentTarget determines the Mux type ("native" → MuxTreemux, "tuimux" → MuxTuimux).
func NewGrovetermAgentProvider(spec *AgentProviderSpec, autoSplit bool, agentTarget string) *GrovetermAgentProvider {
	return &GrovetermAgentProvider{
		log:         grovelogging.NewLogger("grove-flow"),
		ulog:        grovelogging.NewUnifiedLogger("grove-flow"),
		spec:        spec,
		autoSplit:   autoSplit,
		agentTarget: agentTarget,
	}
}

// Launch implements InteractiveAgentProvider. It registers a session intent
// with the daemon and then requests groveterm to spawn a native agent pane.
func (p *GrovetermAgentProvider) Launch(ctx context.Context, job *Job, plan *Plan, workDir string, agentArgs []string, briefingFilePath string) error {
	if p.spec.PiRuntime != nil && p.spec.PiRuntime.ManagedCodexAuth {
		if err := requireManagedPiCodexAuth(); err != nil {
			return err
		}
	}
	var err error
	agentArgs, err = appendPiJobSessionArgs(p.spec, plan.Directory, job.ID, agentArgs)
	if err != nil {
		return err
	}

	rawCommand := p.buildCommand(agentArgs, briefingFilePath)
	return p.LaunchPrepared(ctx, job, plan, workDir, rawCommand, "")
}

// LaunchPrepared owns the complete native/tuimux lifecycle for prepared
// provider command bytes.
func (p *GrovetermAgentProvider) LaunchPrepared(ctx context.Context, job *Job, plan *Plan, workDir, rawCommand, expectedNativeID string) error {
	if p.spec.Name == "claude" {
		if err := enforceClaudeFolderTrust(ctx, job, plan, workDir); err != nil {
			return err
		}
	}

	// Normal launches enter running here. Resume supplies its known native ID
	// after an atomic completed-to-running transition and must retain that
	// attempt time rather than rewriting it through the legacy status path.
	if expectedNativeID == "" {
		job.Status = JobStatusRunning
		job.StartTime = time.Now()
		if err := updateJobFile(job); err != nil {
			return fmt.Errorf("updating job status: %w", err)
		}
	}

	// Register session intent with the daemon BEFORE spawning the pane.
	// sessionHostClient targets the daemon that owns the interactive host UI
	// when one published itself, so intent, the spawn relay below, and the
	// confirmation in discoverAndRegisterSessionAsync all land on the single
	// daemon whose stream feeds the rail, the Agents drawer, and PTY attach.
	daemonClient := sessionHostClient(workDir)
	defer daemonClient.Close()

	p.log.WithFields(logrus.Fields{
		"job_id":         job.ID,
		"provider":       p.spec.Name,
		"daemon_running": daemonClient.IsRunning(),
	}).Info("Registering session intent for groveterm native pane")

	if err := daemonClient.RegisterSessionIntent(ctx, newAgentSessionIntent(job, plan, p.spec.Name, workDir, p.effectiveMux())); err != nil {
		p.log.WithError(err).Warn("Failed to register session intent with daemon")
	} else {
		p.log.Info("Session intent registered successfully")
		p.ulog.Info("session.lifecycle.intent").
			Field("job_id", job.ID).
			Field("provider", p.spec.Name).
			Field("reason", "launch_registered").
			StructuredOnly().Log(ctx)
	}

	// Wrap with agentstream to capture PID via deterministic pidfile.
	wrappedCommand, err := buildSupervisedInteractiveCommand(job, plan, rawCommand)
	if err != nil {
		return err
	}

	// Build environment variables for the agent pane. GROVE_SCOPE is the
	// job's workspace identity (inherited from the executor when it has one,
	// otherwise resolved from workDir) and is what stamps the session record
	// with the right ecosystem. GROVE_HOST_DAEMON_SOCKET is the orthogonal
	// transport answer, carried through so the agent's own hooks talk to the
	// same daemon this launch registered against instead of following
	// GROVE_SCOPE off to the worktree's daemon.
	// Seed flow.agent_env first so the GROVE_* keys below always win on
	// collision (precedence: os env < agent_env < GROVE_*).
	envVars := map[string]string{}
	for k, v := range p.agentEnv {
		envVars[k] = v
	}
	envVars["GROVE_FLOW_JOB_ID"] = job.ID
	envVars["GROVE_FLOW_ATTEMPT_ID"] = job.AttemptID
	envVars["GROVE_FLOW_JOB_PATH"] = job.FilePath
	envVars["GROVE_FLOW_PLAN_NAME"] = plan.Name
	envVars["GROVE_FLOW_JOB_TITLE"] = job.Title
	if scope := resolveJobScope(workDir); scope != "" {
		envVars["GROVE_SCOPE"] = scope
	}
	applyHostSocketEnv(envVars, workDir)
	if p.spec.ProviderEnv != "" {
		// Let grove hooks/plugins identify the provider (defaults to claude
		// when unset).
		envVars["GROVE_AGENT_PROVIDER"] = p.spec.ProviderEnv
	}

	// Add playbook env vars if applicable
	pbName, pbRoot := resolvePlaybookRootForJob(job, plan)
	if pbRoot != "" {
		envVars["PLAYBOOK_ROOT"] = pbRoot
		envVars["PLAYBOOK_NAME"] = pbName
	}

	// Merge extra env vars (e.g., GROVE_FLOW_ISOLATED for isolated agents)
	for k, v := range p.extraEnv {
		envVars[k] = v
	}

	var transcriptLaunch piTranscriptLaunch
	if p.spec.PiRuntime != nil {
		transcriptLaunch, err = capturePiTranscriptLaunch(job, plan.Directory, expectedNativeID)
		if err != nil {
			return err
		}
	}

	p.ulog.Info("Spawning native agent pane in groveterm").
		Field("job_id", job.ID).
		Field("provider", p.spec.Name).
		Field("auto_split", p.autoSplit).
		Pretty(theme.IconInteractiveAgent + " Spawning native agent pane via groveterm").
		Log(ctx)

	// Send the spawn request to the daemon, which relays to groveterm via SSE.
	// Pass the wrapped command with nil Args — the wrapping includes PID capture.
	if err := daemonClient.SpawnAgentPane(ctx, daemon.SpawnAgentRequest{
		JobID:     job.ID,
		PlanName:  plan.Name,
		JobTitle:  job.Title,
		Command:   wrappedCommand,
		Args:      nil,
		WorkDir:   workDir,
		Env:       envVars,
		AutoSplit: p.autoSplit,
	}); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		_ = updateJobFile(job)
		return fmt.Errorf("spawn native agent pane: %w", err)
	}

	// Create lock file with PID 0 — groveterm owns the PTY, so we don't have
	// a stable OS PID. The lock file lets flow plan status know the job is
	// running. The stop hook cleans it up on agent exit.
	if err := CreateLockFile(job.FilePath, 0); err != nil {
		p.log.WithError(err).Warn("Failed to create lock file")
	}

	// Discover the PID and confirm the session in the background so a TUI or
	// groved's jobrunner is not held for the tens of seconds discovery can take.
	// The handle is what makes that safe for a caller that is about to exit —
	// see awaitSessionConfirmation.
	p.confirmation = startSessionConfirmation(func() error {
		if err := p.discoverAndRegisterSessionAsync(job, plan, workDir, expectedNativeID, transcriptLaunch); err != nil {
			p.log.WithError(err).Error("Failed to discover and register groveterm session")
			return err
		}
		return nil
	})

	p.ulog.Success("Native agent pane spawned").
		Field("job_id", job.ID).
		Field("provider", p.spec.Name).
		Pretty(theme.IconSuccess + " Native agent pane spawned in groveterm").
		Log(ctx)

	return nil
}

// awaitSessionConfirmation implements preparedInteractiveAgentProvider: it
// blocks until the goroutine started by LaunchPrepared has finished discovering
// the session and confirming it with the daemon. Only callers that are about to
// exit need it; the daemon jobrunner outlives the goroutine and skips it.
func (p *GrovetermAgentProvider) awaitSessionConfirmation(ctx context.Context) error {
	return p.confirmation.wait(ctx)
}

// discoverAndRegisterSessionAsync discovers the agent PID and transcript path,
// then confirms the session with the daemon so log streaming works.
// Adapted from ClaudeAgentProvider.discoverAndRegisterSessionAsync.
func (p *GrovetermAgentProvider) discoverAndRegisterSessionAsync(job *Job, plan *Plan, workDir, expectedNativeID string, transcriptLaunch piTranscriptLaunch) error {
	logger := grovelogging.NewLogger("flow-groveterm-session-discovery")

	logger.WithFields(map[string]interface{}{
		"job_id":   job.ID,
		"plan":     plan.Name,
		"provider": p.spec.Name,
	}).Debug("Starting async groveterm session discovery and confirmation")

	jobStartTime := job.StartTime
	if jobStartTime.IsZero() {
		jobStartTime = time.Now()
	}

	ctx := context.Background()

	// Start watching the pane before the PID wait (up to 30s) and the
	// transcript retry loop (another 10s). A Pi that fails to load an
	// extension prints the reason and exits about a second after spawn, taking
	// its PTY with it, so a single capture after those waits sees nothing at
	// all. The watcher keeps the last useful snapshot and is stopped the moment
	// discovery settles, so a healthy launch pays only a couple of captures.
	// Capture every provider, not only Pi: Claude can exit before its first
	// hook and take the native PTY (and its only useful diagnosis) with it.
	captureClient := sessionHostClient(workDir)
	defer captureClient.Close()
	paneWatcher := startPiPaneWatcher(ctx, func(captureCtx context.Context) (string, error) {
		return captureClient.CaptureAgentPane(captureCtx, job.ID)
	})
	defer func() { _, _ = paneWatcher.stop() }()

	// Wait for PID via deterministic pidfile (written by BuildAgentCommand wrapper)
	pid, err := agentstream.WaitForPID(ctx, job.ID)
	if err != nil {
		logger.WithError(err).Warn("Pidfile-based PID discovery failed")
		pid = 0
	} else {
		logger.WithField("pid", pid).Debug("Discovered agent PID via pidfile")
	}
	// Hand the PID to the watcher so it captures the instant Pi dies rather
	// than on the next backoff tick, while the pane still has contents.
	paneWatcher.observePID(pid)

	// Pi-family discovery is launch-bound: ordinary retries may only claim a
	// file absent from their pre-spawn baseline. Other providers retain their
	// timestamp-scoped agentstream discovery.
	discovery := agentstream.DiscoverOptions{
		Provider:  p.spec.Name,
		WorkDir:   workDir,
		AfterTime: jobStartTime,
	}
	var transcriptPath string
	if p.spec.PiRuntime != nil {
		transcriptPath, err = discoverPiTranscriptForLaunch(plan.Directory, job.ID, transcriptLaunch)
	} else {
		transcriptPath, err = agentstream.DiscoverTranscript(discovery)
	}
	if err != nil {
		logger.WithError(err).Warn("Failed to discover transcript via agentstream, retrying...")
		for i := 0; i < 10; i++ {
			time.Sleep(1 * time.Second)
			if p.spec.PiRuntime != nil {
				transcriptPath, err = discoverPiTranscriptForLaunch(plan.Directory, job.ID, transcriptLaunch)
			} else {
				transcriptPath, err = agentstream.DiscoverTranscript(discovery)
			}
			if err == nil {
				break
			}
		}
		if err != nil {
			logger.WithError(err).Warn("Transcript path discovery failed, continuing without it")
		}
	}

	// Discovery is settled either way now: release the watcher so a healthy
	// launch stops paying for captures.
	paneOutput, captureErr := paneWatcher.stop()

	if p.spec.PiRuntime != nil && transcriptPath == "" {
		if paneOutput == "" {
			logger.WithError(captureErr).WithField("job_id", job.ID).
				Warn("No Pi terminal output captured; startup diagnosis limited to the capture error")
		}
		handled, failureErr := handlePiStartupFailure(job, plan, piStartupEvidence{
			pid:          pid,
			paneOutput:   paneOutput,
			captureErr:   captureErr,
			discoveryErr: err,
		})
		if failureErr != nil {
			return failureErr
		}
		if handled {
			logger.WithField("job_id", job.ID).Warn("Pi failed before creating a transcript; job marked failed")
			return nil
		}
		// No proof of death: an unknown PID alone must not fail a Pi that is
		// merely slow to write its first transcript.
		logger.WithFields(map[string]interface{}{
			"job_id": job.ID,
			"pid":    pid,
		}).Warn("Pi produced no transcript but shows no evidence of failure; leaving job status unchanged")
	} else if transcriptPath == "" {
		if _, breadcrumbErr := handleAgentStartupFailure(job, plan, p.spec.Name, agentStartupEvidence{
			pid: pid, paneOutput: paneOutput, captureErr: captureErr, discoveryErr: err,
		}); breadcrumbErr != nil {
			logger.WithError(breadcrumbErr).WithField("job_id", job.ID).Warn("Failed to persist provider startup breadcrumb")
		}
	}

	// Extract native session ID from transcript path. Codex rollout filenames
	// embed the conversation UUID (rollout-<ts>-<uuid>.jsonl) and pi session
	// filenames embed the session uuidv7 (<ts>_<uuid>.jsonl) — store just the
	// UUID so hooks can correlate provider events back to the session.
	var nativeID string
	if transcriptPath != "" {
		switch p.spec.Name {
		case "codex":
			nativeID = codexNativeSessionID(transcriptPath)
		case "pi", "grove-agent":
			nativeID = piNativeSessionID(transcriptPath)
		default:
			nativeID = strings.TrimSuffix(filepath.Base(transcriptPath), ".jsonl")
		}
	}
	if nativeID == "" {
		nativeID = expectedNativeID
	}

	// Never promote a Pi-family intent to a live groved session without the
	// exact Flow-owned transcript. Leaving the intent pending avoids global
	// resolution while a failed/slow launch is diagnosed.
	if err := requirePiTranscriptPath(p.spec, plan.Directory, job.ID, transcriptPath); err != nil {
		return err
	}

	// Confirm the session with the daemon that owns the host UI — the same
	// endpoint LaunchPrepared registered the intent against, so the pending
	// record is promoted in place rather than orphaned on another daemon.
	daemonClient := sessionHostClient(workDir)
	defer daemonClient.Close()

	if err := daemonClient.ConfirmSession(ctx, daemon.SessionConfirmation{
		JobID:          job.ID,
		AttemptID:      job.AttemptID,
		NativeID:       nativeID,
		PID:            pid,
		TranscriptPath: transcriptPath,
	}); err != nil {
		logger.WithError(err).Warn("Failed to confirm session with daemon, falling back to filesystem registry")

		metadata := newFallbackSessionMetadata(job, plan, workDir, p.spec.Name, nativeID, "interactive_agent", transcriptPath, pid)

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
		"pid":             pid,
		"transcript_path": transcriptPath,
	}).Info("Confirmed groveterm agent session")
	p.ulog.Info("session.lifecycle.confirmed").
		Field("job_id", job.ID).
		Field("native_id", nativeID).
		Field("provider", p.spec.Name).
		Field("reason", "pid_discovered").
		StructuredOnly().Log(ctx)

	return nil
}

// effectiveMux returns the Mux type based on agentTarget.
func (p *GrovetermAgentProvider) effectiveMux() string {
	if p.agentTarget == "tuimux" {
		return models.MuxTuimux
	}
	return models.MuxTreemux
}

// buildCommand constructs the full shell command string for the agent CLI via
// the provider registry spec. The instruction is double-quoted so the shell
// preserves it as a single argument, matching the quoting strategy used by the
// tmux-based interactive agent providers (the command bytes are identical
// across the tmux, groveterm, and isolated launch paths).
// agentArgs comes from grove.toml (may include --dangerously-skip-permissions, etc.).
func (p *GrovetermAgentProvider) buildCommand(agentArgs []string, briefingFilePath string) string {
	return p.spec.BuildShellCommand(agentArgs, buildBriefingInstruction(briefingFilePath))
}
