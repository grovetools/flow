package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

var planUpdateNoteRefCmd = &cobra.Command{
	Use:   "update-note-ref [plan] [new-note-path]",
	Short: "Updates the note_ref in all job files for a plan",
	Long:  `Internal command to update the note_ref field in all job frontmatter after a note has been moved.`,
	Args:  cobra.ExactArgs(2),
	RunE:  runPlanUpdateNoteRef,
	Hidden: true,
}

func runPlanUpdateNoteRef(cmd *cobra.Command, args []string) error {
	planName := args[0]
	newNotePath := args[1]

	planPath, err := resolvePlanPathWithActiveJob(planName)
	if err != nil {
		return err
	}

	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	// Update note_ref in all jobs that have one
	updated := 0
	for _, job := range plan.Jobs {
		if job.NoteRef != "" {
			jobPath := filepath.Join(planPath, job.Filename)

			// Read job file
			content, err := os.ReadFile(jobPath)
			if err != nil {
				fmt.Printf("Warning: failed to read %s: %v\n", job.Filename, err)
				continue
			}

			// Parse frontmatter
			frontmatter, body, err := orchestration.ParseFrontmatter(content)
			if err != nil {
				fmt.Printf("Warning: failed to parse frontmatter in %s: %v\n", job.Filename, err)
				continue
			}

			// Update note_ref
			frontmatter["note_ref"] = newNotePath

			// Rebuild and write
			newContent, err := orchestration.RebuildMarkdownWithFrontmatter(frontmatter, body)
			if err != nil {
				fmt.Printf("Warning: failed to rebuild %s: %v\n", job.Filename, err)
				continue
			}

			if err := os.WriteFile(jobPath, newContent, 0644); err != nil {
				fmt.Printf("Warning: failed to write %s: %v\n", job.Filename, err)
				continue
			}

			updated++
		}
	}

	if updated > 0 {
		fmt.Printf("* Updated note_ref in %d job(s)\n", updated)
	}

	return nil
}

func init() {
	planCmd.AddCommand(planUpdateNoteRefCmd)
}
