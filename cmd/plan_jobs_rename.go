package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

var planJobsRenameCmd = &cobra.Command{
	Use:   "rename <job-file> <new-title>",
	Short: "Rename a job and update all dependencies",
	Long: `Rename a job file and its title in the frontmatter.
This command will also update any dependent jobs to reference the new filename.

Examples:
  # Rename a job in the current plan
  flow plan jobs rename 01-design-api.md "Design REST API"

  # Rename a job in a specific plan
  flow plan jobs rename my-project/01-design-api.md "Design REST API"`,
	Args: cobra.ExactArgs(2),
	RunE: runPlanJobsRename,
}

func runPlanJobsRename(cmd *cobra.Command, args []string) error {
	jobPath := args[0]
	newTitle := args[1]

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

	// Store old filename for reporting
	oldFilename := job.Filename
	oldTitle := job.Title

	// Rename the job
	if err := orchestration.RenameJob(plan, job, newTitle); err != nil {
		return fmt.Errorf("rename job: %w", err)
	}

	// Success message
	fmt.Printf("%s Job renamed successfully\n", color.GreenString("✓"))
	fmt.Printf("  Title:    %s → %s\n", oldTitle, newTitle)
	fmt.Printf("  Filename: %s → %s\n", oldFilename, job.Filename)

	// Find and report updated jobs
	type updateInfo struct {
		filename      string
		updatedDepends bool
		updatedPrompt  bool
	}
	updatedJobs := make(map[string]*updateInfo)

	for _, otherJob := range plan.Jobs {
		if otherJob.ID == job.ID {
			continue
		}

		info := &updateInfo{filename: otherJob.Filename}

		// Check if depends_on was updated
		for _, dep := range otherJob.DependsOn {
			if dep == job.Filename {
				info.updatedDepends = true
				break
			}
		}

		// Check if include was updated
		for _, source := range otherJob.Include {
			if source == job.Filename {
				info.updatedPrompt = true
				break
			}
		}

		if info.updatedDepends || info.updatedPrompt {
			updatedJobs[otherJob.Filename] = info
		}
	}

	if len(updatedJobs) > 0 {
		fmt.Printf("\n%s Updated %d job(s):\n", color.GreenString("✓"), len(updatedJobs))
		for _, info := range updatedJobs {
			var fields []string
			if info.updatedDepends {
				fields = append(fields, "depends_on")
			}
			if info.updatedPrompt {
				fields = append(fields, "include")
			}
			fmt.Printf("  - %s (%s)\n", info.filename, strings.Join(fields, ", "))
		}
	}

	return nil
}
