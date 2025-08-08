package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

var planCompleteCmd = &cobra.Command{
	Use:   "complete <job-file>",
	Short: "Mark a job as completed",
	Long: `Mark a job as completed. This is especially useful for chat jobs 
that would otherwise remain in pending_user status indefinitely.

Examples:
  # Complete a chat job
  flow plan complete my-project/plan.md
  
  # Complete any job by its filename
  flow plan complete my-project/01-design-api.md`,
	Args: cobra.ExactArgs(1),
	RunE: runPlanComplete,
}

func runPlanComplete(cmd *cobra.Command, args []string) error {
	jobPath := args[0]

	// Determine if it's a file or needs to be resolved
	var planDir string
	var jobFile string

	// Check if it's an absolute path or contains a separator
	if filepath.IsAbs(jobPath) || filepath.Dir(jobPath) != "." {
		// Extract directory and filename
		planDir = filepath.Dir(jobPath)
		jobFile = filepath.Base(jobPath)
	} else {
		// Just a filename, use current directory
		planDir = "."
		jobFile = jobPath
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

	// Check current status
	if job.Status == orchestration.JobStatusCompleted {
		fmt.Printf("Job already completed: %s\n", jobFile)
		return nil
	}

	// Update status
	oldStatus := job.Status
	job.Status = orchestration.JobStatusCompleted
	
	// Use the state persister to update the job file
	persister := orchestration.NewStatePersister()
	if err := persister.UpdateJobStatus(job, orchestration.JobStatusCompleted); err != nil {
		return fmt.Errorf("update job status: %w", err)
	}

	// Success message
	fmt.Printf("%s Job completed: %s\n", color.GreenString("✓"), job.Title)
	fmt.Printf("Status: %s → %s\n", oldStatus, orchestration.JobStatusCompleted)

	// Special message for chat jobs
	if job.Type == orchestration.JobTypeChat {
		fmt.Printf("\nChat conversation ended. You can transform this chat into executable jobs using:\n")
		fmt.Printf("  grove jobs add-step %s --template generate-plan --prompt-file %s\n", 
			planDir, jobFile)
	}

	return nil
}