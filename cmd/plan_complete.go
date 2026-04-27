package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/grovetools/flow/pkg/orchestration"
)

var planCompleteCmd = &cobra.Command{
	Use:   "complete [slug] <job-file>",
	Short: "Mark a job as completed (use: flow complete)",
	Long: `Mark a job as completed. This is especially useful for chat jobs
that would otherwise remain in pending_user status indefinitely.

Examples:
  # Complete a chat job in the active plan or current directory
  flow plan complete my-job.md

  # Complete a job in a specific plan (by slug)
  flow plan complete my-project my-job.md

  # Complete a job using the --plan flag
  flow plan complete --plan my-project my-job.md

  # Complete a job using a path
  flow plan complete my-project/my-job.md`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runPlanComplete,
}

func init() {
	planCompleteCmd.Flags().StringP("plan", "p", "", "Specify the plan slug or directory")
}

// NewCompleteCmd creates the top-level `complete` command.
func NewCompleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "complete [slug] <job-file>",
		Short: "Mark a job as completed",
		Long: `Mark a job as completed. This is especially useful for chat jobs
that would otherwise remain in pending_user status indefinitely.

Examples:
  # Complete a chat job in the active plan or current directory
  flow complete my-job.md

  # Complete a job in a specific plan (by slug)
  flow complete my-project my-job.md

  # Complete a job using the --plan flag
  flow complete --plan my-project my-job.md

  # Complete a job using a path
  flow complete my-project/my-job.md`,
		Args: cobra.RangeArgs(1, 2),
		RunE: runPlanComplete,
	}

	cmd.Flags().StringP("plan", "p", "", "Specify the plan slug or directory")
	return cmd
}

func runPlanComplete(cmd *cobra.Command, args []string) error {
	var planName string
	var jobFile string
	var planDir string

	planFlag, _ := cmd.Flags().GetString("plan")

	if len(args) == 2 {
		// flow complete <slug> <job-file>
		planName = args[0]
		jobFile = args[1]
		if planFlag != "" {
			fmt.Fprintf(os.Stderr, "Warning: --plan flag ignored when two positional arguments are provided\n")
		}
	} else {
		// len(args) == 1
		if planFlag != "" {
			// flow complete --plan <slug> <job-file>
			planName = planFlag
			jobFile = args[0]
		} else {
			jobPath := args[0]
			if filepath.IsAbs(jobPath) || filepath.Dir(jobPath) != "." {
				// flow complete my-slug/my-job.md (path contains directory)
				planName = filepath.Dir(jobPath)
				jobFile = filepath.Base(jobPath)
			} else {
				// flow complete my-job.md (bare filename, use active plan)
				jobFile = jobPath
				activePlan, err := getActivePlanWithMigration()
				if err == nil && activePlan != "" {
					planName = activePlan
				}
			}
		}
	}

	if planName != "" {
		resolvedPath, err := resolvePlanPath(planName, ".")
		if err != nil {
			// If resolution fails, try using planName directly as a path
			planDir = planName
		} else {
			planDir = resolvedPath
		}
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

	// Use the shared completion function (not silent for CLI)
	return orchestration.CompleteJob(job, plan, false)
}
