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
	log          *logrus.Entry
	ulog         *grovelogging.UnifiedLogger
	providerName string            // "claude", "codex", "opencode"
	autoSplit    bool              // whether to auto-split the pane in groveterm's UI
	agentTarget  string            // "native" or "tuimux" — selects Mux type
	extraEnv     map[string]string // additional env vars (e.g., GROVE_FLOW_ISOLATED for isolated agents)
}

// NewGrovetermAgentProvider creates a new GrovetermAgentProvider.
// providerName selects the agent CLI ("claude", "codex", "opencode").
// autoSplit controls whether the new pane is split into the active view.
// agentTarget determines the Mux type ("native" → MuxTreemux, "tuimux" → MuxTuimux).
func NewGrovetermAgentProvider(providerName string, autoSplit bool, agentTarget string) *GrovetermAgentProvider {
	return &GrovetermAgentProvider{
		log:          grovelogging.NewLogger("grove-flow"),
		ulog:         grovelogging.NewUnifiedLogger("grove-flow"),
		providerName: providerName,
		autoSplit:    autoSplit,
		agentTarget:  agentTarget,
	}
}

// Launch implements InteractiveAgentProvider. It registers a session intent
// with the daemon and then requests groveterm to spawn a native agent pane.
func (p *GrovetermAgentProvider) Launch(ctx context.Context, job *Job, plan *Plan, workDir string, agentArgs []string, briefingFilePath string) error {
	// Update job status to running
	job.Status = JobStatusRunning
	job.StartTime = time.Now()
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}

	// Register session intent with the daemon BEFORE spawning the pane.
	daemonClient := daemon.NewWithAutoStart()
	defer daemonClient.Close()

	p.log.WithFields(logrus.Fields{
		"job_id":         job.ID,
		"provider":       p.providerName,
		"daemon_running": daemonClient.IsRunning(),
	}).Info("Registering session intent for groveterm native pane")

	if err := daemonClient.RegisterSessionIntent(ctx, daemon.SessionIntent{
		JobID:       job.ID,
		Provider:    p.providerName,
		JobFilePath: job.FilePath,
		PlanName:    plan.Name,
		Title:       job.Title,
		WorkDir:     workDir,
		Channels:    job.Channels,
		Autonomous:  job.Autonomous,
		Mux:         p.effectiveMux(),
	}); err != nil {
		p.log.WithError(err).Warn("Failed to register session intent with daemon")
	} else {
		p.log.Info("Session intent registered successfully")
	}

	// Build the full agent command string (already shell-quoted).
	rawCommand := p.buildCommand(agentArgs, briefingFilePath)

	// Wrap with agentstream to capture PID via deterministic pidfile.
	wrappedCommand := agentstream.BuildAgentCommand(job.ID, rawCommand)

	// Build environment variables for the agent pane. GROVE_SCOPE is inherited
	// from the executor's env (treemux exports it at startup; the daemon
	// process exports its own scope on boot) — we don't force it from workDir.
	// Agents launched from a context without GROVE_SCOPE go to the global daemon.
	envVars := map[string]string{
		"GROVE_FLOW_JOB_ID":    job.ID,
		"GROVE_FLOW_JOB_PATH":  job.FilePath,
		"GROVE_FLOW_PLAN_NAME": plan.Name,
		"GROVE_FLOW_JOB_TITLE": job.Title,
	}
	if scope := os.Getenv("GROVE_SCOPE"); scope != "" {
		envVars["GROVE_SCOPE"] = scope
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
		Field("provider", p.providerName).
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
		if err := p.discoverAndRegisterSessionAsync(job, plan, workDir); err != nil {
			p.log.WithError(err).Error("Failed to discover and register groveterm session")
		}
	}()

	p.ulog.Success("Native agent pane spawned").
		Field("job_id", job.ID).
		Field("provider", p.providerName).
		Pretty(theme.IconSuccess + " Native agent pane spawned in groveterm").
		Log(ctx)

	return nil
}

// discoverAndRegisterSessionAsync discovers the agent PID and transcript path,
// then confirms the session with the daemon so log streaming works.
// Adapted from ClaudeAgentProvider.discoverAndRegisterSessionAsync.
func (p *GrovetermAgentProvider) discoverAndRegisterSessionAsync(job *Job, plan *Plan, workDir string) error {
	logger := grovelogging.NewLogger("flow-groveterm-session-discovery")

	logger.WithFields(map[string]interface{}{
		"job_id":   job.ID,
		"plan":     plan.Name,
		"provider": p.providerName,
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

	// Discover transcript path using agentstream
	var transcriptPath string
	transcriptPath, err = agentstream.DiscoverTranscript(agentstream.DiscoverOptions{
		Provider:  p.providerName,
		WorkDir:   workDir,
		AfterTime: jobStartTime,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to discover transcript via agentstream, retrying...")
		for i := 0; i < 10; i++ {
			time.Sleep(1 * time.Second)
			transcriptPath, err = agentstream.DiscoverTranscript(agentstream.DiscoverOptions{
				Provider:  p.providerName,
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

	// Extract native session ID from transcript path
	var nativeID string
	if transcriptPath != "" {
		nativeID = strings.TrimSuffix(filepath.Base(transcriptPath), ".jsonl")
	}

	// Confirm the session with the daemon
	daemonClient := daemon.NewWithAutoStart()
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
			ClaudeSessionID:  nativeID,
			Provider:         p.providerName,
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

// buildCommand constructs the full shell command string for the agent CLI.
// The instruction is double-quoted so the shell preserves it as a single argument,
// matching the quoting strategy used by ClaudeAgentProvider.buildAgentCommand
// for tmux-based interactive agents.
// agentArgs comes from grove.toml (may include --dangerously-skip-permissions, etc.).
func (p *GrovetermAgentProvider) buildCommand(agentArgs []string, briefingFilePath string) string {
	escapedPath := "'" + strings.ReplaceAll(briefingFilePath, "'", "'\\''") + "'"
	instruction := fmt.Sprintf("Read the briefing file at %s and execute the task.", escapedPath)

	// Build command parts: binary + agentArgs
	cmdParts := []string{p.providerName}
	cmdParts = append(cmdParts, agentArgs...)

	switch p.providerName {
	case "opencode":
		return fmt.Sprintf("%s --prompt \"%s\"", strings.Join(cmdParts, " "), instruction)
	default:
		// claude, codex, and any other provider: instruction as last positional arg
		return fmt.Sprintf("%s \"%s\"", strings.Join(cmdParts, " "), instruction)
	}
}
