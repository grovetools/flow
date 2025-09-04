package cmd

import (
	"context"
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

	// Append transcript if it's an interactive agent job
	if job.Type == orchestration.JobTypeInteractiveAgent {
		fmt.Println("Appending interactive session transcript...")
		if err := orchestration.AppendInteractiveTranscript(job, plan); err != nil {
			// Log warning but don't fail the command
			fmt.Printf("Warning: failed to append transcript: %v\n", err)
		} else {
			fmt.Println(color.GreenString("✓") + " Appended session transcript.")
		}
	}

	// Summarize the job content if enabled
	flowCfg, err := loadFlowConfig()
	if err != nil {
		// Don't fail the command, just log a warning
		fmt.Printf("Warning: could not load flow config for summarization: %v\n", err)
	} else if flowCfg.SummarizeOnComplete {
		summaryCfg := orchestration.SummaryConfig{
			Enabled:  flowCfg.SummarizeOnComplete,
			Model:    flowCfg.SummaryModel,
			Prompt:   flowCfg.SummaryPrompt,
			MaxChars: flowCfg.SummaryMaxChars,
		}

		fmt.Println("Generating job summary...")
		summary, err := orchestration.SummarizeJobContent(context.Background(), job, plan, summaryCfg)
		if err != nil {
			fmt.Printf("Warning: failed to generate job summary: %v\n", err)
		} else if summary != "" {
			// Add summary to frontmatter
			if err := orchestration.AddSummaryToJobFile(job, summary); err != nil {
				fmt.Printf("Warning: failed to add summary to job file: %v\n", err)
			} else {
				fmt.Println(color.GreenString("✓") + " Added summary to job frontmatter.")
			}
		}
	}

	// Notify grove-hooks if this is an interactive agent job
	if job.Type == orchestration.JobTypeInteractiveAgent {
		// Use the completion hook to mark the job as completed in grove-hooks
		orchestration.NotifyJobCompleteExternal(job, nil)
	}

	// Success message
	fmt.Printf("%s Job completed: %s\n", color.GreenString("✓"), job.Title)
	fmt.Printf("Status: %s → %s\n", oldStatus, orchestration.JobStatusCompleted)

	// Special message for chat jobs
	if job.Type == orchestration.JobTypeChat {
		fmt.Printf("\nChat conversation ended. You can transform this chat into executable jobs using:\n")
		fmt.Printf("  flow plan add %s --template generate-plan --prompt-file %s\n",
			planDir, jobFile)
	}

	return nil
}
