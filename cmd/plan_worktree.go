package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

var planWorktreeCmd = &cobra.Command{
	Use:   "worktree",
	Short: "Manage worktrees for all jobs in a plan",
	Long:  `Commands to set or unset worktrees for all jobs in a plan.`,
}

var planWorktreeSetCmd = &cobra.Command{
	Use:   "set <plan-directory> <worktree-name>",
	Short: "Set worktree for all jobs in a plan",
	Long:  `Sets the worktree field in the frontmatter of all job files in the specified plan directory.`,
	Args:  cobra.ExactArgs(2),
	RunE:  runPlanWorktreeSet,
}

var planWorktreeUnsetCmd = &cobra.Command{
	Use:   "unset <plan-directory>",
	Short: "Remove worktree from all jobs in a plan",
	Long:  `Removes the worktree field from the frontmatter of all job files in the specified plan directory.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runPlanWorktreeUnset,
}

func init() {
	planWorktreeCmd.AddCommand(planWorktreeSetCmd)
	planWorktreeCmd.AddCommand(planWorktreeUnsetCmd)
}

func runPlanWorktreeSet(cmd *cobra.Command, args []string) error {
	planDir := args[0]
	worktreeName := args[1]

	// Load the plan
	plan, err := orchestration.LoadPlan(planDir)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	updatedCount := 0
	for _, job := range plan.Jobs {
		jobPath := filepath.Join(plan.Directory, job.Filename)

		// Read the job file
		content, err := os.ReadFile(jobPath)
		if err != nil {
			return fmt.Errorf("failed to read job %s: %w", job.Filename, err)
		}

		// Update the worktree in the frontmatter
		updates := map[string]interface{}{
			"worktree": worktreeName,
		}

		updatedContent, err := orchestration.UpdateFrontmatter(content, updates)
		if err != nil {
			return fmt.Errorf("failed to update frontmatter for job %s: %w", job.Filename, err)
		}

		// Write the updated content back
		if err := os.WriteFile(jobPath, updatedContent, 0644); err != nil {
			return fmt.Errorf("failed to write job %s: %w", job.Filename, err)
		}

		updatedCount++
	}

	fmt.Printf("✓ Updated worktree to '%s' for %d jobs in %s\n", worktreeName, updatedCount, planDir)
	return nil
}

func runPlanWorktreeUnset(cmd *cobra.Command, args []string) error {
	planDir := args[0]

	// Load the plan
	plan, err := orchestration.LoadPlan(planDir)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	updatedCount := 0
	for _, job := range plan.Jobs {
		if job.Worktree != "" {
			jobPath := filepath.Join(plan.Directory, job.Filename)

			// Read the job file
			content, err := os.ReadFile(jobPath)
			if err != nil {
				return fmt.Errorf("failed to read job %s: %w", job.Filename, err)
			}

			// Parse frontmatter to get all fields
			frontmatter, body, err := orchestration.ParseFrontmatter(content)
			if err != nil {
				return fmt.Errorf("failed to parse frontmatter for job %s: %w", job.Filename, err)
			}

			// Remove the worktree field
			delete(frontmatter, "worktree")

			// Rebuild the file
			updatedContent, err := orchestration.RebuildMarkdownWithFrontmatter(frontmatter, body)
			if err != nil {
				return fmt.Errorf("failed to rebuild job %s: %w", job.Filename, err)
			}

			// Write the updated content back
			if err := os.WriteFile(jobPath, updatedContent, 0644); err != nil {
				return fmt.Errorf("failed to write job %s: %w", job.Filename, err)
			}

			updatedCount++
		}
	}

	fmt.Printf("✓ Removed worktree from %d jobs in %s\n", updatedCount, planDir)
	return nil
}
