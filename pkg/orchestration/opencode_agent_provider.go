package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/mux"
	"github.com/grovetools/core/pkg/paths"
	"github.com/grovetools/core/pkg/process"
	"github.com/grovetools/core/pkg/sessions"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/core/util/sanitize"
	"github.com/sirupsen/logrus"
)

// OpencodeAgentProvider implements InteractiveAgentProvider for the opencode agent.
type OpencodeAgentProvider struct {
	log      *logrus.Entry
	ulog     *grovelogging.UnifiedLogger
	agentEnv map[string]string // flow.agent_env injected into the agent process
}

func NewOpencodeAgentProvider() *OpencodeAgentProvider {
	return &OpencodeAgentProvider{
		log:  grovelogging.NewLogger("grove-flow"),
		ulog: grovelogging.NewUnifiedLogger("grove-flow"),
	}
}

// Launch implements the InteractiveAgentProvider interface for opencode.
func (p *OpencodeAgentProvider) Launch(ctx context.Context, job *Job, plan *Plan, workDir string, agentArgs []string, briefingFilePath string) error {
	// Update job status to running
	job.Status = JobStatusRunning
	job.StartTime = time.Now()
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}

	// --- Synchronous Session Registration ---
	// Register the session BEFORE launching the agent to avoid race conditions.
	// The opencode plugin will enrich this session later with the native session ID.
	registry, err := sessions.NewFileSystemRegistry()
	if err != nil {
		p.log.WithError(err).Error("Failed to create session registry")
	} else {
		user := os.Getenv("USER")
		if user == "" {
			user = "unknown"
		}
		repo, branch := getGitInfo(workDir)

		metadata := sessions.SessionMetadata{
			SessionID:        job.ID,
			ClaudeSessionID:  "", // Empty - plugin will fill this with native opencode session ID
			Provider:         "opencode",
			PID:              0, // Will be updated by plugin
			WorkingDirectory: workDir,
			User:             user,
			Repo:             repo,
			Branch:           branch,
			StartedAt:        time.Now(),
			JobTitle:         job.Title,
			PlanName:         plan.Name,
			JobFilePath:      job.FilePath,
			Type:             "interactive_agent",
		}

		p.log.WithFields(logrus.Fields{
			"session_id": job.ID,
			"provider":   "opencode",
			"work_dir":   workDir,
		}).Info("Registering opencode session synchronously")

		if err := registry.Register(metadata); err != nil {
			p.log.WithError(err).Error("Failed to register session")
		} else {
			p.log.Info("Successfully registered opencode session")
		}
	}
	// --- End Synchronous Session Registration ---

	agentTarget := "tmux"
	if plan.Orchestration != nil && plan.Orchestration.AgentTarget != "" {
		agentTarget = plan.Orchestration.AgentTarget
	}
	engine, err := mux.GetEngine(agentTarget)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("mux engine not available: %w", err)
	}

	sessionName, err := mux.GenerateSessionName(workDir)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return err
	}

	sessionExists, _ := engine.SessionExists(ctx, sessionName)

	if !sessionExists {
		p.log.WithField("session", sessionName).Info("Creating new session for opencode job")
		if err := engine.CreateSession(ctx, sessionName, mux.WithWorkDir(workDir)); err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to create session: %w", err)
		}

		sessionPID, err := engine.GetSessionPID(ctx, sessionName)
		if err != nil {
			return fmt.Errorf("could not get session PID: %w", err)
		}
		if err := CreateLockFile(job.FilePath, sessionPID); err != nil {
			return fmt.Errorf("failed to create lock file: %w", err)
		}
	} else {
		p.log.WithField("session", sessionName).Info("Using existing session for opencode job")
	}

	agentCommand, err := p.buildAgentCommand(job, briefingFilePath, agentArgs)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to build agent command: %w", err)
	}

	agentWindowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)
	p.ulog.Info("Launching OpenCode agent in worktree session").
		Field("window", agentWindowName).
		Field("session", sessionName).
		Pretty(theme.IconWorktree + " Launching OpenCode agent in worktree session").
		Log(ctx)

	isTUIMode := os.Getenv("GROVE_FLOW_TUI_MODE") == "true"
	if err := engine.NewWindow(ctx, sessionName, agentWindowName, workDir, true); err != nil {
		p.log.WithError(err).Warn("Failed to create agent window, may already exist.")
	}

	targetPane := fmt.Sprintf("%s:%s", sessionName, agentWindowName)
	// Inline env vars on the agent command so they scope only to the agent
	// process; typing `export` into the pane would leak into the user's
	// interactive shell after the agent exits. GROVE_SCOPE is inherited
	// from the executor's env (treemux or daemon), not forced from workDir.
	scopePrefix := ""
	if scope := os.Getenv("GROVE_SCOPE"); scope != "" {
		scopePrefix = fmt.Sprintf("GROVE_SCOPE='%s' ", scope)
	}
	escapedTitle := "'" + strings.ReplaceAll(job.Title, "'", "'\\''") + "'"
	envPrefix := agentEnvInline(p.agentEnv) + "GROVE_AGENT_PROVIDER='opencode' " + scopePrefix + fmt.Sprintf("GROVE_FLOW_JOB_ID='%s' GROVE_FLOW_JOB_PATH='%s' GROVE_FLOW_PLAN_NAME='%s' GROVE_FLOW_JOB_TITLE=%s ",
		job.ID, job.FilePath, plan.Name, escapedTitle)
	if node, err := workspace.GetProjectByPath(workDir); err == nil && node != nil {
		logDir := filepath.Join(paths.StateDir(), "logs", "workspaces", node.Identifier("/"))
		envPrefix += fmt.Sprintf("GROVE_LOG_DIR='%s' ", logDir)
	}
	envPrefix += playbookEnvInline(job, plan)

	if err := engine.SendKeys(ctx, targetPane, envPrefix+agentCommand, "C-m"); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to send agent command: %w", err)
	}

	// NOTE: Session was registered synchronously above. The opencode plugin will
	// enrich the session with the native session ID when it starts.
	// We no longer need the async discoverAndRegisterSession call.

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

	if !isTUIMode {
		p.ulog.Info("").Pretty("").Log(ctx) // blank line
		p.ulog.Info("Task completion instructions").
			Pretty(theme.IconArrow + " When your task is complete, run the following:").
			Log(ctx)
		p.ulog.Info("").
			Pretty(fmt.Sprintf("   flow plan complete %s", job.FilePath)).
			Log(ctx)
	}

	return nil
}

func (p *OpencodeAgentProvider) buildAgentCommand(job *Job, briefingFilePath string, agentArgs []string) (string, error) {
	escapedPath := "'" + strings.ReplaceAll(briefingFilePath, "'", "'\\''") + "'"
	prompt := fmt.Sprintf("Read the briefing file at %s and execute the task.", escapedPath)

	// For interactive_agent jobs, use --prompt to keep opencode running for continued interaction
	// For headless/agent jobs, use run subcommand which exits after completing the task
	if job.Type == JobTypeInteractiveAgent {
		cmdParts := []string{"opencode"}
		cmdParts = append(cmdParts, agentArgs...)
		cmdParts = append(cmdParts, "--prompt", fmt.Sprintf("\"%s\"", prompt))
		return strings.Join(cmdParts, " "), nil
	}

	// Headless mode - use 'run' subcommand
	cmdParts := []string{"opencode", "run"}
	cmdParts = append(cmdParts, agentArgs...)
	return fmt.Sprintf("%s \"%s\"", strings.Join(cmdParts, " "), prompt), nil
}

// FindOpencodePIDForPane finds the PID of the 'opencode' process running within a specific tmux pane
func FindOpencodePIDForPane(targetPane string) (int, error) {
	engine, err := mux.DetectMuxEngine(context.Background())
	if err != nil {
		return 0, fmt.Errorf("mux engine not available: %w", err)
	}

	shellPID, err := engine.GetPanePID(context.Background(), targetPane)
	if err != nil {
		return 0, fmt.Errorf("failed to get pane PID: %w", err)
	}

	return process.FindDescendantPID(shellPID, "opencode")
}
