package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/grovetools/flow/pkg/orchestration"
)

var planRetryCmd = &cobra.Command{
	Use:   "retry [slug] <job-file>",
	Short: "Reset failed/orphaned jobs without frontmatter edits (use: flow retry)",
	Long: `Reset a failed or orphaned job back to pending status without manual frontmatter edits.

For jobs with status: failed, clears last_error, completed_at, and duration.
For jobs with status: running, requires --force to override (with liveness hints).
For status: completed or pending_user, refuses with helpful error messages.

The --run flag immediately submits the job after resetting (equivalent of retry + flow plan run).

Examples:
  # Reset a failed job in the active plan
  flow plan retry my-job.md

  # Reset a failed job in a specific plan (by slug)
  flow plan retry my-project my-job.md

  # Reset and immediately submit
  flow plan retry --run my-job.md

  # Force reset a running job
  flow plan retry --force --plan my-project my-job.md

  # Reset a job using the unified --at target
  flow plan retry --at my-feature my-job.md

  # Using the flow retry alias
  flow retry my-job.md`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runPlanRetry,
}

func init() {
	planRetryCmd.Flags().StringP("plan", "p", "", "Specify the plan slug or directory")
	planRetryCmd.Flags().BoolP("run", "r", false, "Immediately submit the job after resetting")
	planRetryCmd.Flags().BoolP("force", "f", false, "Force reset of running jobs")
	planRetryCmd.Flags().String("agent-target", "", "With --run, the launch target for agent jobs: tmux, native, or tuimux (default: derived from this process's mux)")
}

// NewRetryCmd creates the top-level `retry` command.
func NewRetryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "retry [slug] <job-file>",
		Short: "Reset failed/orphaned jobs without frontmatter edits",
		Long: `Reset a failed or orphaned job back to pending status without manual frontmatter edits.

For jobs with status: failed, clears last_error, completed_at, and duration.
For jobs with status: running, requires --force to override (with liveness hints).
For status: completed or pending_user, refuses with helpful error messages.

The --run flag immediately submits the job after resetting (equivalent of retry + flow plan run).

Examples:
  # Reset a failed job
  flow retry my-job.md

  # Reset a job in a specific plan (by slug)
  flow retry my-project my-job.md

  # Reset and immediately submit
  flow retry --run my-job.md

  # Force reset a running job
  flow retry --force --plan my-project my-job.md

  # Reset a job using the unified --at target
  flow retry --at my-feature my-job.md`,
		Args: cobra.RangeArgs(1, 2),
		RunE: runPlanRetry,
	}

	cmd.Flags().StringP("plan", "p", "", "Specify the plan slug or directory")
	cmd.Flags().BoolP("run", "r", false, "Immediately submit the job after resetting")
	cmd.Flags().BoolP("force", "f", false, "Force reset of running jobs")
	cmd.Flags().String("agent-target", "", "With --run, the launch target for agent jobs: tmux, native, or tuimux (default: derived from this process's mux)")
	return cmd
}

func runPlanRetry(cmd *cobra.Command, args []string) error {
	var planName string
	var jobFile string
	var planDir string

	planFlag, _ := cmd.Flags().GetString("plan")
	autoRun, _ := cmd.Flags().GetBool("run")
	force, _ := cmd.Flags().GetBool("force")

	if len(args) == 2 {
		// flow retry <slug> <job-file>
		planName = args[0]
		jobFile = args[1]
		if planFlag != "" {
			fmt.Fprintf(os.Stderr, "Warning: --plan flag ignored when two positional arguments are provided\n")
		}
	} else {
		// len(args) == 1
		if planFlag != "" {
			// flow retry --plan <slug> <job-file>
			planName = planFlag
			jobFile = args[0]
		} else {
			jobPath := args[0]
			if filepath.IsAbs(jobPath) || filepath.Dir(jobPath) != "." {
				// flow retry my-slug/my-job.md (path contains directory)
				planName = filepath.Dir(jobPath)
				jobFile = filepath.Base(jobPath)
			} else {
				// flow retry my-job.md (bare filename, use active plan)
				jobFile = jobPath
				activePlan, err := getActivePlanWithMigration()
				if err == nil && activePlan != "" {
					planName = activePlan
				}
			}
		}
	}

	if planName != "" {
		resolvedPath, err := resolvePlanPathCtx(cmd.Context(), planName, ".")
		if err != nil {
			// If resolution fails, try using planName directly as a path
			planDir = planName
		} else {
			planDir = resolvedPath
		}
	} else if unified, ok := TargetFromContext(cmd.Context()); ok && unified.PlanDir != "" {
		planDir = unified.PlanDir
	} else {
		planDir = "."
	}

	// Load the plan
	plan, err := orchestration.LoadPlan(planDir)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}

	// Find the job
	job, found := plan.GetJobByFilename(jobFile)
	if !found {
		return fmt.Errorf("job not found: %s", jobFile)
	}

	// --run submits the job, so it needs the same routing decision `plan run`
	// makes. AgentTargetForSubmission reads plan.Orchestration first, so an
	// explicit target only has to be injected there; leaving it nil keeps the
	// existing environment derivation.
	agentTargetFlag, _ := cmd.Flags().GetString("agent-target")
	if strings.TrimSpace(agentTargetFlag) != "" {
		agentTarget, err := orchestration.ResolveAgentTargetExplicit(agentTargetFlag)
		if err != nil {
			return err
		}
		plan.Orchestration = &orchestration.Config{AgentTarget: agentTarget}
	}

	// Call RetryJob with the flags
	return orchestration.RetryJob(job, plan, force, autoRun)
}
