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
	}

	// Wrap with agentstream to capture PID via deterministic pidfile.
	wrappedCommand := agentstream.BuildAgentCommand(job.ID, rawCommand)

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

	// Asynchronously discover PID and register session in background
	go func() {
		if err := p.discoverAndRegisterSessionAsync(job, plan, workDir, expectedNativeID); err != nil {
			p.log.WithError(err).Error("Failed to discover and register groveterm session")
		}
	}()

	p.ulog.Success("Native agent pane spawned").
		Field("job_id", job.ID).
		Field("provider", p.spec.Name).
		Pretty(theme.IconSuccess + " Native agent pane spawned in groveterm").
		Log(ctx)

	return nil
}

// discoverAndRegisterSessionAsync discovers the agent PID and transcript path,
// then confirms the session with the daemon so log streaming works.
// Adapted from ClaudeAgentProvider.discoverAndRegisterSessionAsync.
func (p *GrovetermAgentProvider) discoverAndRegisterSessionAsync(job *Job, plan *Plan, workDir, expectedNativeID string) error {
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

	// Wait for PID via deterministic pidfile (written by BuildAgentCommand wrapper)
	pid, err := agentstream.WaitForPID(ctx, job.ID)
	if err != nil {
		logger.WithError(err).Warn("Pidfile-based PID discovery failed")
		pid = 0
	} else {
		logger.WithField("pid", pid).Debug("Discovered agent PID via pidfile")
		_ = agentstream.CleanupPIDFile(job.ID)
	}

	// Discover transcript path using the product descriptor. Rebranded Pi
	// providers emit Pi v3 files but keep their provider identity in records.
	discovery := agentstream.DiscoverOptions{
		Provider:  p.spec.Name,
		WorkDir:   workDir,
		AfterTime: jobStartTime,
	}
	if p.spec.PiRuntime != nil {
		discovery.Provider = "pi"
		discovery.SessionDir = piJobSessionDir(plan.Directory, job.ID)
	}
	var transcriptPath string
	transcriptPath, err = agentstream.DiscoverTranscript(discovery)
	if err != nil {
		logger.WithError(err).Warn("Failed to discover transcript via agentstream, retrying...")
		for i := 0; i < 10; i++ {
			time.Sleep(1 * time.Second)
			transcriptPath, err = agentstream.DiscoverTranscript(discovery)
			if err == nil {
				break
			}
		}
		if err != nil {
			logger.WithError(err).Warn("Transcript path discovery failed, continuing without it")
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
		NativeID:       nativeID,
		PID:            pid,
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
			Provider:         p.spec.Name,
			PID:              pid,
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
			Scope:            resolveJobScope(workDir),
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
		"pid":             pid,
		"transcript_path": transcriptPath,
	}).Info("Confirmed groveterm agent session")

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
