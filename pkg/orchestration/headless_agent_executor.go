package orchestration

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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

	// Defer final status update
	defer func() {
		finalStatus := JobStatusCompleted
		if execErr != nil {
			finalStatus = JobStatusFailed
		}
		job.EndTime = time.Now()
		_ = job.UpdateStatus(persister, finalStatus)

		// Clean up daemon session record for this headless agent.
		if client := daemon.New(); client != nil {
			endCtx, endCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer endCancel()
			_ = client.EndSession(endCtx, job.ID, string(finalStatus))
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
	} else {
		ulog.Debug("[HEADLESS] Agent execution completed successfully").
			Field("job_id", job.ID).
			Log(ctx)
	}

	// After agent completes, archive its session artifacts
	ulog.Debug("[HEADLESS] Archiving session artifacts").
		Field("job_id", job.ID).
		Log(ctx)
	if err := ArchiveInteractiveSession(job, plan); err != nil {
		ulog.Warn("[HEADLESS] Failed to archive session artifacts for headless agent job").
			Err(err).
			Log(ctx)
	} else {
		ulog.Debug("[HEADLESS] Session artifacts archived successfully").
			Field("job_id", job.ID).
			Log(ctx)
	}

	// Archive any Claude Code workflow runs the agent spawned. Without this,
	// headless jobs — whose deferred status update marks them completed
	// before CompleteJob ever runs — lose their workflow artifacts entirely.
	if err := ArchiveWorkflowRuns(job, plan); err != nil {
		ulog.Warn("[HEADLESS] Failed to archive workflow runs for headless agent job").
			Err(err).
			Log(ctx)
	}

	// Append the formatted transcript using the generalized function
	ulog.Debug("[HEADLESS] Appending formatted transcript").
		Field("job_id", job.ID).
		Log(ctx)
	if err := AppendAgentTranscript(job, plan); err != nil {
		ulog.Warn("[HEADLESS] Failed to append transcript to job file").
			Err(err).
			Log(ctx)
	} else {
		ulog.Debug("[HEADLESS] Formatted transcript appended successfully").
			Field("job_id", job.ID).
			Log(ctx)
	}

	return execErr
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

	// Check if the worktree directory already exists. If so, skip preparation.
	// This prevents errors when multiple jobs in a plan share the same worktree.
	worktreePath := filepath.Join(gitRoot, ".grove-worktrees", job.Worktree)
	if _, err := os.Stat(worktreePath); err == nil {
		return worktreePath, nil
	}

	// The new logic:
	opts := workspace.PrepareOptions{
		GitRoot:      gitRoot,
		WorktreeName: job.Worktree,
		BranchName:   job.Worktree, // Convention: branch name matches worktree name
		PlanName:     plan.Name,
	}

	if plan.Config != nil && len(plan.Config.Repos) > 0 {
		opts.Repos = plan.Config.Repos
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
func buildHeadlessCommand(ctx context.Context, providerName, prompt string, agentArgs []string) (*exec.Cmd, error) {
	switch providerName {
	case "claude":
		// Claude Code headless: prompt is piped via stdin.
		args := []string{"--dangerously-skip-permissions"}
		args = append(args, agentArgs...)
		cmd := exec.CommandContext(ctx, "claude", args...)
		cmd.Stdin = strings.NewReader(prompt)
		return cmd, nil
	case "opencode":
		// Opencode headless: 'opencode run' executes the prompt and exits
		// (same invocation OpencodeAgentProvider.buildAgentCommand uses for
		// non-interactive jobs).
		args := []string{"run"}
		args = append(args, agentArgs...)
		args = append(args, prompt)
		return exec.CommandContext(ctx, "opencode", args...), nil
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

	// We use cmd.Run() and don't capture output. The agent process itself handles logging.
	// We also redirect stdout/stderr to /dev/null to prevent cluttering the main process output.
	// The real logs are accessed via `aglogs`.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Run(); err != nil {
		ulog.Error("[HEADLESS] Agent CLI execution failed").
			Field("job_id", job.ID).
			Field("provider", providerName).
			Err(err).
			Log(ctx)
		return fmt.Errorf("agent execution failed: %w", err)
	}

	ulog.Debug("[HEADLESS] Agent CLI execution completed").
		Field("job_id", job.ID).
		Field("provider", providerName).
		Log(ctx)
	return nil
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
