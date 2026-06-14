package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/grovetools/agentlogs/pkg/agentstream"
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/models"
	"github.com/grovetools/core/pkg/paths"
	"github.com/grovetools/core/pkg/workspace"
	grovecontext "github.com/grovetools/cx/pkg/context"
)

// HeadlessAgentExecutor executes headless agent jobs in isolated git worktrees.
type HeadlessAgentExecutor struct {
	llmClient LLMClient
	config    *ExecutorConfig
}

// NewHeadlessAgentExecutor creates a new headless agent executor.
func NewHeadlessAgentExecutor(llmClient LLMClient, config *ExecutorConfig) *HeadlessAgentExecutor {
	if config == nil {
		config = &ExecutorConfig{
			MaxPromptLength: 1000000,
			Timeout:         30 * time.Minute,
			RetryCount:      1,
			Model:           "default",
		}
	}

	return &HeadlessAgentExecutor{
		llmClient: llmClient,
		config:    config,
	}
}

// Name returns the executor name.
func (e *HeadlessAgentExecutor) Name() string {
	return "headless_agent"
}

// Execute runs an agent job in a worktree.
func (e *HeadlessAgentExecutor) Execute(ctx context.Context, job *Job, plan *Plan) error {
	ulog.Debug("[HEADLESS] Starting execution").
		Field("job_id", job.ID).
		Field("job_title", job.Title).
		Field("plan_name", plan.Name).
		Log(ctx)

	persister := NewStatePersister()

	// Create lock file with the current process's PID.
	if err := CreateLockFile(job.FilePath, os.Getpid()); err != nil {
		return fmt.Errorf("failed to create lock file: %w", err)
	}
	// Ensure lock file is removed when execution finishes.
	defer func() { _ = RemoveLockFile(job.FilePath) }()

	// Update job status to running
	job.StartTime = time.Now()
	if err := job.UpdateStatus(persister, JobStatusRunning); err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}

	ulog.Debug("[HEADLESS] Job status updated to running").
		Field("job_id", job.ID).
		Log(ctx)

	var execErr error

	// Defer terminal-status handling for the SETUP phase only.
	//
	// Headless agents detach: runAgentInWorktree starts the process and
	// returns immediately, leaving a waitAndWriteStatus goroutine to observe
	// the real exit. So this defer must NOT mark the job completed at detach —
	// that was the bug (premature "completed" with a sub-second duration, a
	// completed→pending flap, and duplicate ad-hoc session records). Real
	// completion (status, duration, EndSession, archive, transcript) is done
	// by the goroutine via CompleteJob once the process actually exits.
	//
	// This defer therefore only finalizes FAILURES that occur before the
	// agent process is successfully launched (worktree prep, prompt build,
	// startup error). On the success path execErr is nil and the agent is
	// still running, so we leave the job in `running` for the goroutine.
	defer func() {
		if execErr == nil {
			return
		}
		job.EndTime = time.Now()
		_ = job.UpdateStatus(persister, JobStatusFailed)

		// Clean up the daemon session record for the failed-to-launch agent.
		if client := daemon.New(); client != nil {
			endCtx, endCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer endCancel()
			_ = client.EndSession(endCtx, job.ID, string(JobStatusFailed))
		}
	}()

	// Determine the working directory for the job
	var workDir string
	if job.Worktree != "" {
		var err error
		workDir, err = e.prepareWorktree(ctx, job, plan)
		if err != nil {
			execErr = fmt.Errorf("prepare worktree: %w", err)
			return execErr
		}
	} else {
		var err error
		workDir, err = GetProjectGitRoot(plan.Directory)
		if err != nil {
			workDir = plan.Directory
			ulog.Warn("Not a git repository, using plan directory as working directory").
				Field("work_dir", workDir).
				Log(ctx)
		}
	}

	// Scope to sub-project if job.Repository is set
	workDir = ScopeToSubProject(workDir, job)

	// Gather context files (.grove/context, CLAUDE.md, etc.)
	contextFiles, err := e.gatherContextFiles(job, plan, workDir)
	if err != nil {
		execErr = err
		return execErr
	}

	// Query memory database for related memories
	memories := FetchRelatedMemories(ctx, job)

	// Build the XML prompt
	promptXML, _, err := BuildXMLPrompt(job, plan, workDir, contextFiles, memories)
	if err != nil {
		ulog.Error("Failed to build prompt for job").
			Field("job_id", job.ID).
			Field("job_file", job.FilePath).
			Err(err).
			Pretty(" " + err.Error()).
			Log(ctx)
		execErr = fmt.Errorf("building XML prompt: %w", err)
		return execErr
	}

	// Write the briefing file for auditing
	briefingFilePath, err := WriteBriefingFile(plan, job, promptXML, "")
	if err != nil {
		ulog.Warn("Failed to write briefing file").
			Err(err).
			Log(ctx)
		execErr = fmt.Errorf("writing briefing file: %w", err)
		return execErr
	}

	// Create instruction to read the briefing file (like interactive_agent does)
	instructionPrompt := fmt.Sprintf("Read the briefing file at '%s' and execute the task.", briefingFilePath)

	// Execute agent with the instruction to read the briefing file
	ulog.Debug("[HEADLESS] Starting agent execution").
		Field("job_id", job.ID).
		Log(ctx)
	err = e.runAgentInWorktree(ctx, workDir, instructionPrompt, job, plan)
	if err != nil {
		execErr = fmt.Errorf("run agent: %w", err)
		ulog.Error("[HEADLESS] Agent execution failed").
			Field("job_id", job.ID).
			Err(err).
			Log(ctx)
		return execErr
	}

	// The agent is now detached and running. Archiving, transcript capture,
	// and terminal-status finalization happen in the waitAndWriteStatus
	// goroutine (via CompleteJob) once the process actually exits — NOT here,
	// where the agent has barely started. See the SETUP-phase defer above.
	ulog.Debug("[HEADLESS] Agent launched; completion deferred to exit watcher").
		Field("job_id", job.ID).
		Log(ctx)

	return nil
}

// prepareWorktree ensures the worktree exists and is ready.
func (e *HeadlessAgentExecutor) prepareWorktree(ctx context.Context, job *Job, plan *Plan) (string, error) {
	if job.Worktree == "" {
		return "", fmt.Errorf("job %s has no worktree specified", job.ID)
	}

	gitRoot, err := GetProjectGitRoot(plan.Directory)
	if err != nil {
		gitRoot = plan.Directory
	}

	// Check if the worktree already exists. If so, skip preparation.
	// This prevents errors when multiple jobs in a plan share the same worktree.
	if existing, ok := workspace.FindWorktreePath(gitRoot, job.Worktree); ok {
		return existing, nil
	}

	// The new logic:
	opts := workspace.PrepareOptions{
		GitRoot:      gitRoot,
		WorktreeName: job.Worktree,
		BranchName:   job.Worktree, // Convention: branch name matches worktree name
		PlanName:     plan.Name,
	}

	// XDG gate: a plan with sibling workspaces (persisted repos: key) gets
	// its worktree under the XDG data dir.
	if plan.Config != nil && len(plan.Config.Repos) > 0 {
		opts.SiblingWorkspaces = plan.Config.Repos
		opts.UseXDGWorktrees = true
	}

	return workspace.Prepare(ctx, opts, CopyProjectFilesToWorktree)
}

// runAgentInWorktree executes the agent in the worktree context.
func (e *HeadlessAgentExecutor) runAgentInWorktree(ctx context.Context, worktreePath, prompt string, job *Job, plan *Plan) error {
	coreCfg, err := config.LoadFrom(".")
	if err != nil {
		coreCfg = &config.Config{}
	}

	// Unmarshal flow configuration to resolve the provider and its args,
	// mirroring the resolution done by the interactive agent executor.
	var flowCfg FlowConfig
	_ = coreCfg.UnmarshalExtension("flow", &flowCfg)

	// Determine which provider to use (default to claude for backward compatibility)
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

	// Append per-job flags (--model, --effort) for claude only; other
	// providers do not accept these flags.
	if providerName == "claude" {
		agentArgs, err = appendClaudeJobArgs(agentArgs, job, plan)
		if err != nil {
			return err
		}
		// Record the model the agent will actually run with (or claude's
		// default when none was passed) into the job frontmatter.
		backfillClaudeAgentModel(job)
	}

	// Pre-register the session intent with the daemon BEFORE launching the
	// agent, now that the work dir and resolved provider are known.
	e.registerSessionIntent(ctx, job, plan, worktreePath, providerName)

	return e.runOnHost(ctx, worktreePath, prompt, job, plan, providerName, agentArgs)
}

// registerSessionIntent best-effort pre-registers the headless session with
// the daemon before the agent process is launched, mirroring the interactive
// providers (ClaudeAgentProvider.Launch / GrovetermAgentProvider.Launch).
// Without this, headless agents that never fire a tool hook leave the hooks
// session registry empty, making the job's transcript binding unverifiable
// (see issue: wrong-session-logs-bound-to-headless-tuimux-jobs).
// Failure to register is logged as a warning and is never fatal.
func (e *HeadlessAgentExecutor) registerSessionIntent(ctx context.Context, job *Job, plan *Plan, workDir, providerName string) {
	daemonClient := daemon.NewWithAutoStart()
	defer daemonClient.Close()

	ulog.Debug("[HEADLESS] Registering session intent with daemon").
		Field("job_id", job.ID).
		Field("provider", providerName).
		Field("daemon_running", daemonClient.IsRunning()).
		Log(ctx)

	if err := daemonClient.RegisterSessionIntent(ctx, daemon.SessionIntent{
		JobID:        job.ID,
		Provider:     providerName,
		JobFilePath:  job.FilePath,
		PlanName:     plan.Name,
		Title:        job.Title,
		WorkDir:      workDir,
		Channels:     job.Channels,
		SignalTarget: job.SignalTarget,
		Autonomous:   job.Autonomous,
		// Headless agents run as a direct child process, not inside a
		// multiplexer pane.
		Mux: models.MuxNone,
	}); err != nil {
		ulog.Warn("[HEADLESS] Failed to register session intent with daemon").
			Field("job_id", job.ID).
			Field("provider", providerName).
			Err(err).
			Log(ctx)
	} else {
		ulog.Debug("[HEADLESS] Session intent registered").
			Field("job_id", job.ID).
			Log(ctx)
	}
}

// buildHeadlessCommand constructs the exec.Cmd for a provider's headless mode.
// Returns an error for providers that have no headless execution mode.
// PHASE 2 CHANGE: Uses exec.Command (not CommandContext) for process detachment.
// Agents are detached from the daemon and will be adopted on daemon restart.
func buildHeadlessCommand(ctx context.Context, providerName, prompt string, agentArgs []string) (*exec.Cmd, error) {
	switch providerName {
	case "claude":
		// Claude Code headless: prompt is piped via stdin.
		args := []string{"--dangerously-skip-permissions"}
		args = append(args, agentArgs...)
		cmd := exec.Command("claude", args...)
		cmd.Stdin = strings.NewReader(prompt)
		// Detach the process: place it in its own process group so signals
		// sent to the daemon don't propagate to the agent.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return cmd, nil
	case "opencode":
		// Opencode headless: 'opencode run' executes the prompt and exits
		// (same invocation OpencodeAgentProvider.buildAgentCommand uses for
		// non-interactive jobs).
		args := []string{"run"}
		args = append(args, agentArgs...)
		args = append(args, prompt)
		cmd := exec.Command("opencode", args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return cmd, nil
	default:
		return nil, fmt.Errorf("provider %q does not support headless execution (no headless mode implemented); supported headless providers: claude, opencode — set flow.interactive_provider in grove.toml to a supported provider or run this job as interactive_agent", providerName)
	}
}

// runOnHost executes the agent directly on the host machine.
func (e *HeadlessAgentExecutor) runOnHost(ctx context.Context, worktreePath, prompt string, job *Job, plan *Plan, providerName string, agentArgs []string) error {
	ulog.Debug("[HEADLESS] Running agent on host").
		Field("job_id", job.ID).
		Field("worktree", worktreePath).
		Field("provider", providerName).
		Field("agent_args", agentArgs).
		Log(ctx)

	originalDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}
	defer func() { _ = os.Chdir(originalDir) }()

	if err := os.Chdir(worktreePath); err != nil {
		return fmt.Errorf("failed to change to worktree directory: %w", err)
	}

	cmd, err := buildHeadlessCommand(ctx, providerName, prompt, agentArgs)
	if err != nil {
		ulog.Error("[HEADLESS] Provider cannot run headless").
			Field("job_id", job.ID).
			Field("provider", providerName).
			Err(err).
			Log(ctx)
		return err
	}
	cmd.Dir = worktreePath

	// Set environment variables to enable grove-hooks integration for session registration.
	// GROVE_SCOPE is inherited via os.Environ() — treemux exports it at startup
	// and the daemon process exports its own scope on boot. If the executor has
	// no GROVE_SCOPE set, the agent's daemon calls go to the unscoped global daemon.
	escapedTitle := "'" + strings.ReplaceAll(job.Title, "'", "'\\''") + "'"
	cmd.Env = append(os.Environ(),
		"GROVE_FLOW_JOB_ID="+job.ID,
		"GROVE_FLOW_JOB_PATH="+job.FilePath,
		"GROVE_FLOW_PLAN_NAME="+plan.Name,
		"GROVE_FLOW_JOB_TITLE="+escapedTitle,
	)
	if providerName != "claude" {
		// Non-claude providers (e.g. opencode plugins) use this to identify
		// themselves during session registration, matching the interactive path.
		cmd.Env = append(cmd.Env, "GROVE_AGENT_PROVIDER="+providerName)
	}
	if node, err := workspace.GetProjectByPath(worktreePath); err == nil && node != nil {
		cmd.Env = append(cmd.Env, "GROVE_LOG_DIR="+filepath.Join(paths.StateDir(), "logs", "workspaces", node.Identifier("/")))
	}
	if pbName, pbRoot := resolvePlaybookRootForJob(job, plan); pbRoot != "" {
		cmd.Env = append(cmd.Env,
			"PLAYBOOK_ROOT="+pbRoot,
			"PLAYBOOK_NAME="+pbName,
		)
	}

	ulog.Debug("[HEADLESS] Starting agent CLI with environment variables").
		Field("job_id", job.ID).
		Field("provider", providerName).
		Field("GROVE_FLOW_JOB_ID", job.ID).
		Field("GROVE_FLOW_JOB_PATH", job.FilePath).
		Field("GROVE_FLOW_PLAN_NAME", plan.Name).
		Field("GROVE_FLOW_JOB_TITLE", escapedTitle).
		Log(ctx)

	// PHASE 2: Redirect stdout/stderr to /dev/null to prevent cluttering the main process output.
	// The real logs are accessed via `aglogs`. Agent process handles its own logging.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	// PHASE 2: Use Start() instead of Run() to detach the process and return immediately.
	// The agent is now in its own process group (Setpgid=true) and will survive daemon restart.
	if err := cmd.Start(); err != nil {
		ulog.Error("[HEADLESS] Agent CLI startup failed").
			Field("job_id", job.ID).
			Field("provider", providerName).
			Err(err).
			Log(ctx)
		return fmt.Errorf("agent startup failed: %w", err)
	}

	// PHASE 2: Spawn a goroutine to wait for the agent's completion and write .status file.
	// This allows the executor to return immediately while the agent runs detached.
	go e.waitAndWriteStatus(ctx, job, plan, cmd)

	// Confirm the daemon session with the REAL agent PID (and, for claude, the
	// native session id + transcript) in the background. Unlike interactive
	// agents — which run inside a mux pane and must discover their PID — a
	// headless agent is a direct child, so cmd.Process.Pid IS the agent PID.
	// Confirming upgrades the pre-registered intent into a confirmed session;
	// without it the daemon keeps an unconfirmed intent and, when the agent
	// fires workflow/tool hooks, spawns duplicate ad-hoc agent records for the
	// single run.
	go e.confirmSessionAsync(job, plan, worktreePath, providerName, cmd.Process.Pid, job.StartTime)

	// Daemon-less reconcile bridge: when this executor runs standalone (the
	// `flow plan run` LocalRuntime fallback, no daemon present), persist a
	// minimal daemon JobInfo carrying the real agent PID into the same jobs dir
	// the daemon's persistence layer loads from. A later daemon boot will then
	// adopt and reconcile this detached agent via AdoptRunningAgents (PID
	// liveness + the .status file written on exit). The flag is false when the
	// daemon hosts the executor in-process, so we never clobber the daemon's
	// own lifecycle-managed jobs/<id>.json. ID must equal the .status writer's
	// key (job.ID) so adoption's status path resolves.
	if e.config != nil && e.config.DaemonJobPersist {
		e.persistDaemonJobInfo(ctx, job, plan, cmd.Process.Pid)
	}

	ulog.Debug("[HEADLESS] Agent detached").
		Field("job_id", job.ID).
		Field("provider", providerName).
		Field("pid", cmd.Process.Pid).
		Log(ctx)
	return nil
}

// persistDaemonJobInfo writes a minimal daemon JobInfo record for a
// standalone (daemon-less) headless agent launch. See the DaemonJobPersist
// guard in runOnHost for why this is gated. All failures are best-effort: a
// missed write simply means the job won't be adopted, which is no worse than
// today's behavior.
func (e *HeadlessAgentExecutor) persistDaemonJobInfo(ctx context.Context, job *Job, plan *Plan, pid int) {
	daemonJobsDir := filepath.Join(paths.StateDir(), "daemon", "jobs")
	if err := os.MkdirAll(daemonJobsDir, 0o755); err != nil {
		ulog.Warn("[HEADLESS] Failed to create daemon jobs dir for adoption record").
			Field("job_id", job.ID).
			Field("dir", daemonJobsDir).
			Err(err).
			Log(ctx)
		return
	}

	info := &models.JobInfo{
		ID:          job.ID,
		PlanDir:     plan.Directory,
		JobFile:     filepath.Base(job.FilePath),
		Status:      "running",
		Type:        models.JobType("headless_agent"),
		PID:         pid,
		SubmittedAt: job.StartTime,
		StartedAt:   &job.StartTime,
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		ulog.Warn("[HEADLESS] Failed to marshal daemon JobInfo for adoption record").
			Field("job_id", job.ID).
			Err(err).
			Log(ctx)
		return
	}

	jobPath := filepath.Join(daemonJobsDir, job.ID+".json")
	if err := os.WriteFile(jobPath, data, 0o644); err != nil {
		ulog.Warn("[HEADLESS] Failed to write daemon JobInfo for adoption record").
			Field("job_id", job.ID).
			Field("path", jobPath).
			Err(err).
			Log(ctx)
		return
	}

	ulog.Debug("[HEADLESS] Wrote daemon JobInfo for adoption record").
		Field("job_id", job.ID).
		Field("pid", pid).
		Field("path", jobPath).
		Log(ctx)
}

// confirmSessionAsync confirms the headless session with the daemon using the
// real agent PID. It mirrors ClaudeAgentProvider.discoverAndRegisterSessionAsync
// but is simpler: the PID is already known (direct child process), so only the
// native session id + transcript need discovering. Designed to run in a
// goroutine; all failures are logged warnings and never fatal.
func (e *HeadlessAgentExecutor) confirmSessionAsync(job *Job, plan *Plan, workDir, providerName string, pid int, startTime time.Time) {
	ctx := context.Background()

	if startTime.IsZero() {
		startTime = time.Now()
	}

	// Transcript/native-session discovery is provider-specific; today only the
	// claude provider writes a discoverable ~/.claude transcript. For other
	// providers we still confirm with the PID so the session is de-duped.
	var transcriptPath string
	if providerName == "claude" {
		var err error
		transcriptPath, err = agentstream.DiscoverTranscript(agentstream.DiscoverOptions{
			Provider:  "claude",
			WorkDir:   workDir,
			AfterTime: startTime,
		})
		if err != nil {
			// Retry with backoff — the transcript file appears a beat after the
			// process starts.
			for i := 0; i < 10; i++ {
				time.Sleep(1 * time.Second)
				transcriptPath, err = agentstream.DiscoverTranscript(agentstream.DiscoverOptions{
					Provider:  "claude",
					WorkDir:   workDir,
					AfterTime: startTime,
				})
				if err == nil {
					break
				}
			}
			if err != nil {
				ulog.Warn("[HEADLESS] Transcript discovery failed; confirming session with PID only").
					Field("job_id", job.ID).
					Err(err).
					Log(ctx)
			}
		}
	}

	var nativeID string
	if transcriptPath != "" {
		nativeID = strings.TrimSuffix(filepath.Base(transcriptPath), ".jsonl")
	}

	daemonClient := daemon.NewWithAutoStart()
	defer daemonClient.Close()

	if err := daemonClient.ConfirmSession(ctx, daemon.SessionConfirmation{
		JobID:          job.ID,
		NativeID:       nativeID,
		PID:            pid,
		TranscriptPath: transcriptPath,
	}); err != nil {
		ulog.Warn("[HEADLESS] Failed to confirm session with daemon").
			Field("job_id", job.ID).
			Field("pid", pid).
			Err(err).
			Log(ctx)
		return
	}

	ulog.Debug("[HEADLESS] Session confirmed with daemon").
		Field("job_id", job.ID).
		Field("pid", pid).
		Field("native_id", nativeID).
		Log(ctx)
}

// waitAndWriteStatus waits for an agent process to complete and writes its exit status to a .status file.
// PHASE 2: This allows the daemon to adopt orphaned processes on restart by reading the .status file.
// The file is written to the job's artifact directory with format: {"exit_code": <int>, "timestamp": <RFC3339>}
func (e *HeadlessAgentExecutor) waitAndWriteStatus(ctx context.Context, job *Job, plan *Plan, cmd *exec.Cmd) {
	err := cmd.Wait()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			// Non-exit error (shouldn't happen with detached process)
			exitCode = -1
		}
	}

	// Construct the .status file path in the job's artifacts directory
	// The path follows: .artifacts/<job-id>/.status
	statusPath := filepath.Join(plan.Directory, ".artifacts", job.ID, ".status")
	statusDir := filepath.Dir(statusPath)

	// Ensure the directory exists
	if err := os.MkdirAll(statusDir, 0o755); err != nil {
		ulog.Error("[HEADLESS] Failed to create status directory").
			Field("job_id", job.ID).
			Field("path", statusDir).
			Err(err).
			Log(ctx)
		return
	}

	// Write the status file with JSON format
	statusData := map[string]interface{}{
		"exit_code": exitCode,
		"timestamp": time.Now().Format(time.RFC3339),
		"job_id":    job.ID,
	}

	data, err := json.Marshal(statusData)
	if err != nil {
		ulog.Error("[HEADLESS] Failed to marshal status").
			Field("job_id", job.ID).
			Err(err).
			Log(ctx)
		return
	}

	if err := os.WriteFile(statusPath, data, 0o644); err != nil {
		ulog.Error("[HEADLESS] Failed to write status file").
			Field("job_id", job.ID).
			Field("path", statusPath).
			Err(err).
			Log(ctx)
		return
	}

	ulog.Debug("[HEADLESS] Status file written").
		Field("job_id", job.ID).
		Field("exit_code", exitCode).
		Field("path", statusPath).
		Log(ctx)

	// Finalize the job now that the agent has REALLY exited. CompleteJob is the
	// unified completion handler: it flips status to completed (writing an
	// accurate completed_at + duration measured from job.StartTime), notifies
	// the daemon to end the session, archives session artifacts + workflow
	// runs, and appends the transcript. Doing this here — rather than in a
	// detach-time defer — is the core of the lifecycle fix: status, duration,
	// and process lifetime finally agree, and the completed→pending flap and
	// duplicate ad-hoc session records disappear.
	//
	// CAVEAT (CLI-spawned jobs): when a headless job is launched by `flow plan
	// run`, this goroutine lives in the short-lived CLI process and is killed
	// when the CLI exits — so CompleteJob may never run for that path. The
	// .status file written just above is the durable source of truth a daemon
	// collector can reconcile from. For daemon-spawned jobs (JobRunner), the
	// executor runs inside the long-lived groved process, so this goroutine
	// survives until the agent exits and completion happens here as intended.
	//
	// CompleteJob is idempotent for agent jobs (archival/transcript re-runs are
	// overwrites/no-ops), so a later `flow plan complete` recovering a
	// CLI-spawned job does no harm.
	if err := CompleteJob(job, plan, true); err != nil {
		ulog.Warn("[HEADLESS] Failed to finalize job after agent exit").
			Field("job_id", job.ID).
			Field("exit_code", exitCode).
			Err(err).
			Log(ctx)
	}
}

// gatherContextFiles collects context files (.grove/context, CLAUDE.md, etc.) for the job.
func (e *HeadlessAgentExecutor) gatherContextFiles(job *Job, plan *Plan, workDir string) ([]string, error) {
	var contextFiles []string

	// Scope to sub-project if job.Repository is set (for ecosystem worktrees)
	contextDir := ScopeToSubProject(workDir, job)

	if contextDir != "" {
		// When using a worktree/context dir, ONLY use context from that directory
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
		// No worktree, use the default context search
		for _, contextPath := range FindContextFiles(plan) {
			if _, err := os.Stat(contextPath); err == nil {
				contextFiles = append(contextFiles, contextPath)
			}
		}
	}

	return contextFiles, nil
}
