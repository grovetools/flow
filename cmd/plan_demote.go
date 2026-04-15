package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

var planDemoteCmd = &cobra.Command{
	Use:   "demote <job-file-path>",
	Short: "Demote a plan job back to an nb inbox note",
	Long: `Move a flow plan job back to the nb inbox as a note.

The job's title and prompt body are used to create the new note.
If the job has a note_ref pointing to a different workspace, the note
is routed back to that workspace's inbox. Otherwise, it goes to the
inbox of the workspace containing the plan.

The job's status is set to "abandoned" after demotion.

Examples:
  flow plan demote plans/my-plan/03-stale-task.md
  flow plan demote /abs/path/to/plans/my-plan/03-stale-task.md`,
	Args: cobra.ExactArgs(1),
	RunE: runPlanDemote,
}

func runPlanDemote(cmd *cobra.Command, args []string) error {
	jobFilePath := args[0]

	// Make path absolute
	if !filepath.IsAbs(jobFilePath) {
		absPath, err := filepath.Abs(jobFilePath)
		if err != nil {
			return fmt.Errorf("resolving job path: %w", err)
		}
		jobFilePath = absPath
	}

	// Load the job file
	job, err := orchestration.LoadJob(jobFilePath)
	if err != nil {
		return fmt.Errorf("loading job file: %w", err)
	}
	job.FilePath = jobFilePath
	job.Filename = filepath.Base(jobFilePath)

	// Determine the target workspace directory for the new note.
	// If the job has a note_ref, route to that note's workspace.
	// Otherwise, use the workspace containing the plan.
	targetWorkspaceDir := resolveTargetWorkspace(jobFilePath, job.NoteRef)

	// Build the nb new command
	nbArgs := []string{"new", job.Title, "--type", "inbox", "--no-edit"}
	nbCmd := exec.Command("nb", nbArgs...)
	nbCmd.Dir = targetWorkspaceDir

	// Pipe the job's prompt body to stdin
	if job.PromptBody != "" {
		nbCmd.Stdin = strings.NewReader(job.PromptBody)
	}

	// Capture stdout for the new note path
	nbCmd.Stderr = os.Stderr
	output, err := nbCmd.Output()
	if err != nil {
		return fmt.Errorf("creating note via nb new: %w", err)
	}

	// Parse the note path from nb output (nb logs "Created: <path>")
	notePath := parseNotePathFromOutput(string(output))

	// Update the job status to abandoned
	sp := orchestration.NewStatePersister()
	if err := sp.UpdateJobStatus(job, orchestration.JobStatusAbandoned); err != nil {
		return fmt.Errorf("updating job status to abandoned: %w", err)
	}

	// Print the new note path to stdout
	if notePath != "" {
		fmt.Println(notePath)
	} else {
		fmt.Println("Job demoted to note (note path not captured)")
	}

	return nil
}

// resolveTargetWorkspace determines the workspace directory for note creation.
// If noteRef is set and points to a path containing /workspaces/<name>/,
// use that workspace. Otherwise, derive from the plan's location.
func resolveTargetWorkspace(jobFilePath, noteRef string) string {
	if noteRef != "" {
		wsDir := extractWorkspaceDir(noteRef)
		if wsDir != "" {
			return wsDir
		}
	}

	// Fall back: the plan directory is jobFilePath's parent's parent
	// e.g., /notebooks/ws/plans/my-plan/01-job.md → /notebooks/ws
	planDir := filepath.Dir(jobFilePath)
	return extractWorkspaceDir(planDir)
}

// extractWorkspaceDir finds the workspace root from an absolute path.
// It looks for /workspaces/<name>/ in the path and returns everything
// up to and including the workspace name directory.
func extractWorkspaceDir(absPath string) string {
	// Look for /workspaces/ in the path
	parts := strings.Split(absPath, string(filepath.Separator))
	for i, part := range parts {
		if part == "workspaces" && i+1 < len(parts) {
			// Return the path up to and including the workspace name
			wsPath := string(filepath.Separator) + filepath.Join(parts[1:i+2]...)
			return wsPath
		}
	}
	// Fallback: try parent directories looking for plans/ or inbox/
	// If path contains /plans/, go up two levels
	dir := absPath
	for dir != "/" && dir != "." {
		base := filepath.Base(dir)
		if base == "plans" || base == "inbox" {
			return filepath.Dir(dir)
		}
		dir = filepath.Dir(dir)
	}
	// Last resort: use the plan directory's parent
	return filepath.Dir(filepath.Dir(absPath))
}

// parseNotePathFromOutput extracts the note file path from nb new output.
// nb new outputs lines like "Created: /path/to/note.md"
func parseNotePathFromOutput(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Created: ") {
			return strings.TrimPrefix(line, "Created: ")
		}
		// Also handle if the path is just printed directly
		if strings.HasSuffix(line, ".md") && filepath.IsAbs(line) {
			return line
		}
	}
	return ""
}
