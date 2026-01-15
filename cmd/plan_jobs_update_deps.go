package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

var planJobsUpdateDepsCmd = &cobra.Command{
	Use:   "update-deps <job-file> [dependency-files...]",
	Short: "Update a job's dependencies",
	Long: `Update a job's depends_on field with the specified dependency files.
This command will replace the job's current dependencies with the new list.

Examples:
  # Set dependencies for a job
  flow plan jobs update-deps 03-test-api.md 01-design-api.md 02-implement-api.md

  # Clear all dependencies (no dependencies specified)
  flow plan jobs update-deps 03-test-api.md

  # Update dependencies in a specific plan
  flow plan jobs update-deps my-project/03-test-api.md 01-design-api.md`,
	Args: cobra.MinimumNArgs(1),
	RunE: runPlanJobsUpdateDeps,
}

func runPlanJobsUpdateDeps(cmd *cobra.Command, args []string) error {
	jobPath := args[0]
	newDeps := args[1:] // Remaining args are the dependency files

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

	// Store old dependencies for reporting
	oldDeps := job.DependsOn

	// Update the job's dependencies
	if err := orchestration.UpdateJobDependencies(job, newDeps); err != nil {
		return fmt.Errorf("update dependencies: %w", err)
	}

	// Success message
	fmt.Printf("%s Dependencies updated for: %s\n", color.GreenString("*"), job.Title)

	if len(oldDeps) > 0 {
		fmt.Printf("\nOld dependencies (%d):\n", len(oldDeps))
		for _, dep := range oldDeps {
			fmt.Printf("  - %s\n", dep)
		}
	} else {
		fmt.Printf("\nOld dependencies: (none)\n")
	}

	if len(newDeps) > 0 {
		fmt.Printf("\nNew dependencies (%d):\n", len(newDeps))
		for _, dep := range newDeps {
			fmt.Printf("  - %s\n", dep)
		}
	} else {
		fmt.Printf("\nNew dependencies: (none)\n")
	}

	return nil
}
