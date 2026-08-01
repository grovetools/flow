package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/grovetools/core/util/delegation"
	"github.com/spf13/cobra"

	"github.com/grovetools/flow/pkg/orchestration"
)

// NewPlanResumeCmd creates the `plan resume` command.
func NewPlanResumeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resume <job-file>",
		Short: "Resume a completed agent job",
		Long: `Resumes a completed agent session by finding its native agent session ID
and re-launching it through the plan's configured tmux, native, or tuimux target.
Claude, Codex, and Pi-family sessions are supported.`,
		Args: cobra.ExactArgs(1),
		RunE: runPlanResume,
	}
	cmd.Flags().StringVar(&planResumeAgentTarget, "agent-target", "", "Launch target for the resumed session: tmux, native, or tuimux (default: derived from this process's mux)")
	return cmd
}

// planResumeAgentTarget overrides the environment derivation. See
// orchestration.ResolveAgentTargetExplicit.
var planResumeAgentTarget string

func runPlanResume(cmd *cobra.Command, args []string) error {
	jobPath := args[0]

	// 1. Load and Validate Job
	planDir := filepath.Dir(jobPath)
	jobFile := filepath.Base(jobPath)

	plan, err := orchestration.LoadPlan(planDir)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	// LoadPlan never populates plan.Orchestration (only the run path does), so
	// without this the resume launch always fell back to tmux — invisible to a
	// user whose plan runs under treemux/tuimux. Resolve agent_target the same
	// way `plan run` does at the CLI perimeter: --agent-target if given, else
	// the caller's environment.
	agentTarget, err := orchestration.ResolveAgentTargetExplicit(planResumeAgentTarget)
	if err != nil {
		return err
	}
	plan.Orchestration = &orchestration.Config{AgentTarget: agentTarget}

	job, found := plan.GetJobByFilename(jobFile)
	if !found {
		return fmt.Errorf("job not found: %s", jobFile)
	}

	if job.Type != orchestration.JobTypeInteractiveAgent && job.Type != orchestration.JobTypeAgent {
		return fmt.Errorf("cannot resume job: only 'interactive_agent' and 'agent' jobs are supported (job type is '%s')", job.Type)
	}
	if job.Status != orchestration.JobStatusCompleted {
		return fmt.Errorf("cannot resume job: status is '%s', must be 'completed'", job.Status)
	}

	// 2. Retrieve Agent Session ID via aglogs
	aglogsCmd := delegation.Command("aglogs", "get-session-info", job.FilePath)
	output, err := aglogsCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get session info from aglogs: %w\nOutput: %s", err, string(output))
	}

	var sessionInfo resumeSessionInfo
	if err := json.Unmarshal(output, &sessionInfo); err != nil {
		return fmt.Errorf("failed to parse session info from aglogs: %w", err)
	}

	// Resolve and validate the complete orchestration launch before changing
	// durable job state. In particular, unsupported providers and missing or
	// invalid agent_target values cannot briefly move the job to running.
	workingDir, err := orchestration.DetermineWorkingDirectory(plan, job)
	if err != nil {
		return fmt.Errorf("failed to determine working directory: %w", err)
	}
	nativeSessionID := strings.TrimSpace(sessionInfo.AgentSessionID)
	prepared, err := orchestration.PrepareInteractiveAgentResume(job, plan, workingDir, sessionInfo.Provider, nativeSessionID)
	if err != nil {
		return fmt.Errorf("cannot prepare agent resume: %w", err)
	}

	// Any launch failure after the atomic transition restores the exact
	// completed state. The prepared launcher reuses the normal provider
	// lifecycle (intent, env, PID wrapper, mux routing, and confirmation).
	persister := orchestration.NewStatePersister()
	err = runResumeWithRollback(job,
		persister.BeginResumedAttempt,
		func() error { return prepared.Launch(cmd.Context()) },
	)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "* Job status updated to 'running'.\n")

	// The provider finishes the launch in a background goroutine: PID capture,
	// transcript discovery, provider startup-failure diagnosis, and the daemon
	// ConfirmSession that promotes the pending intent into an attachable
	// session. This process is about to exit, which kills that goroutine — often
	// before the Go runtime has even scheduled it. That is how a resumed session
	// ends up stuck at status=pending in the daemon store with a dead PTY, and
	// how a pi that dies a second after spawn takes its own error message with
	// it while the job file still claims `running`.
	//
	// A confirmation failure is reported, never fatal: the agent is already live
	// and must not be rolled back to `completed` behind its own back.
	fmt.Fprintf(out, "* Waiting for the session to register with the daemon...\n")
	if err := awaitResumeConfirmation(cmd.Context(), out, resumeConfirmationTimeout, resumeConfirmationProgressEvery, prepared.AwaitSessionConfirmation); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "! Session confirmation did not complete: %v\n", err)
		fmt.Fprintf(cmd.ErrOrStderr(), "  The agent is running, but the daemon may still show it as pending.\n")
	}

	fmt.Fprintf(out, "* Resumed session for job '%s'.\n", job.Title)
	return nil
}

// resumeConfirmationTimeout bounds how long `flow plan resume` holds the
// terminal waiting for session confirmation.
//
// The work being waited on is legitimately slow in the worst case:
// agentstream.WaitForPID polls for the pidfile for up to 30s, and transcript
// discovery then retries ten times at one-second intervals, so a ceiling much
// under a minute would routinely abandon a healthy-but-slow launch and
// reintroduce the orphaning. A live agent settles in well under a second — this
// bounds pathology, not the expected cost. It also stays comfortably inside the
// assistant supervisor's 3-minute per-invocation budget
// (daemon assistant.DefaultFlowTimeout), which shells out to this command.
const resumeConfirmationTimeout = 60 * time.Second

// resumeConfirmationProgressEvery keeps a slow confirmation from reading as a
// hung terminal. Without it the command can print nothing for a full minute,
// which invites the Ctrl-C that recreates the very orphaning this wait prevents.
const resumeConfirmationProgressEvery = 5 * time.Second

// awaitResumeConfirmation blocks on await until it settles, the bound expires,
// or the command's context is cancelled, emitting a progress line every tick.
func awaitResumeConfirmation(ctx context.Context, out io.Writer, timeout, tick time.Duration, await func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// await is run on its own goroutine so the ticker can keep printing even for
	// a provider whose wait ignores context cancellation.
	done := make(chan error, 1)
	go func() { done <- await(ctx) }()

	started := time.Now()
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-ticker.C:
			fmt.Fprintf(out, "  ... still waiting (%ds elapsed, giving up at %ds)\n",
				int(time.Since(started).Seconds()), int(timeout.Seconds()))
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

type resumeSessionInfo struct {
	AgentSessionID string `json:"agent_session_id"`
	Provider       string `json:"provider"`
}

func runResumeWithRollback(
	job *orchestration.Job,
	begin func(*orchestration.Job) (func() error, error),
	launch func() error,
) error {
	rollback, err := begin(job)
	if err != nil {
		return fmt.Errorf("failed to begin resumed job attempt: %w", err)
	}
	if err := launch(); err != nil {
		launchErr := fmt.Errorf("failed to re-launch agent session: %w", err)
		if rollbackErr := rollback(); rollbackErr != nil {
			return errors.Join(launchErr, fmt.Errorf("failed to restore job after resume failure: %w", rollbackErr))
		}
		return launchErr
	}
	return nil
}
