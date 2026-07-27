package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/core/pkg/mux"
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
	return cmd
}

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
	// user whose plan runs under treemux/tuimux. Resolve agent_target from the
	// caller's environment exactly like `plan run` does at the CLI perimeter.
	agentTarget := "tmux" // safe default
	if mux.ActiveMux() == mux.MuxTuimux {
		agentTarget = "tuimux"
	} else if os.Getenv("GROVE_TERMINAL") != "" {
		agentTarget = "native"
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
	fmt.Printf("* Job status updated to 'running'.\n")

	fmt.Printf("* Resumed session for job '%s'.\n", job.Title)
	return nil
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
