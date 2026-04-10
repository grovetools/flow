package orchestration

import (
	"context"
	"fmt"
	"strings"
	"time"

	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/daemon"
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
	extraEnv     map[string]string // additional env vars (e.g., GROVE_FLOW_ISOLATED for isolated agents)
}

// NewGrovetermAgentProvider creates a new GrovetermAgentProvider.
// providerName selects the agent CLI ("claude", "codex", "opencode").
// autoSplit controls whether the new pane is split into the active view.
func NewGrovetermAgentProvider(providerName string, autoSplit bool) *GrovetermAgentProvider {
	return &GrovetermAgentProvider{
		log:          grovelogging.NewLogger("grove-flow"),
		ulog:         grovelogging.NewUnifiedLogger("grove-flow"),
		providerName: providerName,
		autoSplit:    autoSplit,
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
	}); err != nil {
		p.log.WithError(err).Warn("Failed to register session intent with daemon")
	} else {
		p.log.Info("Session intent registered successfully")
	}

	// Build the command and args for the agent CLI
	command, args := p.buildCommand(agentArgs, briefingFilePath)

	// Build environment variables for the agent pane
	envVars := map[string]string{
		"GROVE_FLOW_JOB_ID":    job.ID,
		"GROVE_FLOW_JOB_PATH":  job.FilePath,
		"GROVE_FLOW_PLAN_NAME": plan.Name,
		"GROVE_FLOW_JOB_TITLE": job.Title,
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

	// Send the spawn request to the daemon, which relays to groveterm via SSE
	if err := daemonClient.SpawnAgentPane(ctx, daemon.SpawnAgentRequest{
		JobID:     job.ID,
		PlanName:  plan.Name,
		JobTitle:  job.Title,
		Command:   command,
		Args:      args,
		WorkDir:   workDir,
		Env:       envVars,
		AutoSplit: p.autoSplit,
	}); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		updateJobFile(job)
		return fmt.Errorf("spawn native agent pane: %w", err)
	}

	// Create lock file with PID 0 — groveterm owns the PTY, so we don't have
	// a stable OS PID. The lock file lets flow plan status know the job is
	// running. The stop hook cleans it up on agent exit.
	if err := CreateLockFile(job.FilePath, 0); err != nil {
		p.log.WithError(err).Warn("Failed to create lock file")
	}

	p.ulog.Success("Native agent pane spawned").
		Field("job_id", job.ID).
		Field("provider", p.providerName).
		Pretty(theme.IconSuccess + " Native agent pane spawned in groveterm").
		Log(ctx)

	return nil
}

// buildCommand constructs the command and args for the agent CLI based on the provider.
// agentArgs comes from grove.toml (may include --dangerously-skip-permissions, etc.).
func (p *GrovetermAgentProvider) buildCommand(agentArgs []string, briefingFilePath string) (string, []string) {
	escapedPath := "'" + strings.ReplaceAll(briefingFilePath, "'", "'\\''") + "'"
	instruction := fmt.Sprintf("Read the briefing file at %s and execute the task.", escapedPath)

	switch p.providerName {
	case "claude":
		args := append([]string{}, agentArgs...)
		args = append(args, "-p", instruction)
		return "claude", args
	case "codex":
		args := append([]string{}, agentArgs...)
		args = append(args, instruction)
		return "codex", args
	case "opencode":
		args := append([]string{}, agentArgs...)
		args = append(args, "--prompt", instruction)
		return "opencode", args
	default:
		// Fallback: treat providerName as the command itself
		args := append([]string{}, agentArgs...)
		args = append(args, instruction)
		return p.providerName, args
	}
}
