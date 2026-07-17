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
	anthropicmodels "github.com/grovetools/grove-anthropic/pkg/models"
	"github.com/sirupsen/logrus"

	modelpkg "github.com/grovetools/flow/pkg/model"
)

// ClaudeAgentProvider implements InteractiveAgentProvider for Claude Code.
type ClaudeAgentProvider struct {
	log      *logrus.Entry
	ulog     *grovelogging.UnifiedLogger
	agentEnv map[string]string // flow.agent_env injected into the agent process
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
	//
	// Resolve the client against the job's owning scope (not the executor's
	// env-implied default) so the intent, the agent-env GROVE_SCOPE the hook
	// reads, and the later ConfirmSession all target ONE daemon. Otherwise a
	// scoped agent's intent lands on the global daemon while its hook record
	// lands on the scoped daemon, leaving a stale pending on one and an orphan
	// running on the other.
	daemonClient := daemon.NewWithAutoStart(resolveJobScope(workDir))
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
		// GROVE_SCOPE must match the daemon the session intent was registered
		// against, or the hook (agent env) and confirm (executor) land on
		// different daemons — leaving a stale pending record on one and an orphan
		// running record on the other. Prefer the executor's own GROVE_SCOPE, but
		// fall back to resolving it from the job's working directory (the same
		// resolver core's daemon factory uses) so a scope is injected whenever the
		// workDir belongs to an ecosystem — not only when the executor happened to
		// have GROVE_SCOPE exported.
		scopePrefix := ""
		if scope := resolveJobScope(workDir); scope != "" {
			scopePrefix = fmt.Sprintf("GROVE_SCOPE='%s' ", scope)
		}
		escapedTitle := "'" + strings.ReplaceAll(job.Title, "'", "'\\''") + "'"
		envPrefix := agentEnvInline(p.agentEnv) + scopePrefix + fmt.Sprintf("GROVE_FLOW_JOB_ID='%s' GROVE_FLOW_JOB_PATH='%s' GROVE_FLOW_PLAN_NAME='%s' GROVE_FLOW_JOB_TITLE=%s ",
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
			if err := engine.KillWindow(ctx, windowTarget); err != nil {
				p.ulog.Warn("Failed to kill existing window").
					Field("window", windowTarget).
					Err(err).
					Log(ctx)
			}
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
	// GROVE_SCOPE is derived from the executor's env, falling back to the
	// job's working directory, so intent/agent-env/confirm land on one daemon.
	scopePrefix := ""
	if scope := resolveJobScope(workDir); scope != "" {
		scopePrefix = fmt.Sprintf("GROVE_SCOPE='%s' ", scope)
	}
	escapedTitle := "'" + strings.ReplaceAll(job.Title, "'", "'\\''") + "'"
	envPrefix := agentEnvInline(p.agentEnv) + scopePrefix + fmt.Sprintf("GROVE_FLOW_JOB_ID='%s' GROVE_FLOW_JOB_PATH='%s' GROVE_FLOW_PLAN_NAME='%s' GROVE_FLOW_JOB_TITLE=%s ",
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

// resolveJobScope resolves the owning daemon scope for a job launched into
// workDir. It prefers the executor's explicit GROVE_SCOPE (set by treemux/the
// daemon on boot), and otherwise resolves the scope from the job's working
// directory exactly as core's daemon factory does. The result is injected into
// the agent env (so the hook's registry record is stamped with it) and used to
// resolve the intent/confirm daemon clients, keeping all three on one daemon.
// An empty return means the unscoped/global daemon.
func resolveJobScope(workDir string) string {
	if scope := os.Getenv("GROVE_SCOPE"); scope != "" {
		return scope
	}
	return workspace.ResolveScope(workDir)
}

// validateClaudeAgentModel is the claude provider's per-job model predicate:
// the model must be usable with the claude CLI's --model flag. It enforces
// that the model is known and Claude-family, distinguishing:
//   - unknown model (not in any registry and not a claude family alias)
//   - known model but wrong provider family
//
// Provider auth is NOT checked here — agent jobs delegate to the claude CLI
// which handles its own OAuth, so checking ANTHROPIC_API_KEY would be a false
// negative.
func validateClaudeAgentModel(model string) error {
	provider, known := modelpkg.LookupModelProvider(model)
	if !known {
		if isClaudeFamilyModel(model) {
			return nil
		}
		return fmt.Errorf("unknown model %q: not found in any provider registry.\n"+
			"Run 'flow models' to see available models", model)
	}
	if provider == modelpkg.ProviderGoogle {
		return fmt.Errorf("model %q is a %s model, but this job runs on the Claude CLI.\n"+
			"Use a Claude-family model (e.g. claude-opus-4-8, claude-sonnet-4-6), set\n"+
			"provider: on the job, or set flow.interactive_provider in grove.toml to route to a different provider",
			model, provider)
	}
	if !isClaudeFamilyModel(model) {
		return fmt.Errorf("model %q is not recognized as a Claude-family model.\n"+
			"Jobs on the claude provider require a Claude model. Use a Claude-family model, set\n"+
			"provider: on the job, or set flow.interactive_provider in grove.toml to route to a different provider", model)
	}
	return nil
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
// is already launching. Only called for the claude provider (via the registry
// spec's BackfillJobModel hook).
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
	// Pass a simple instruction to read the briefing file — cleaner than
	// reading the entire file content into the command. The instruction and
	// command shape come from the provider registry so this path stays
	// byte-identical with the groveterm/isolated launch paths.
	spec, _ := LookupAgentProvider("claude")
	return spec.BuildShellCommand(agentArgs, buildBriefingInstruction(briefingFilePath)), nil
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
		if err := agentstream.CleanupPIDFile(job.ID); err != nil {
			logger.WithError(err).WithField("job_id", job.ID).Warn("Failed to clean up PID file")
		}
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

	// Confirm the session with the daemon using the discovered PID. Resolve the
	// client against the job's working directory (same scope as the intent and
	// the agent-env GROVE_SCOPE) so confirm updates the pending intent record on
	// the SAME daemon instead of orphaning a running record on a different one.
	daemonClient := daemon.NewWithAutoStart(resolveJobScope(workDir))
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
			// Stamp the owning scope so a scoped daemon can recover this live
			// session after a restart (RecoverSessionsForScope filters on Scope).
			Scope: resolveJobScope(workDir),
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
