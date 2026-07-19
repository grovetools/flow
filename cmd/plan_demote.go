package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/grovetools/flow/pkg/orchestration"
)

var demoteWorkspaceFlag string

func init() {
	planDemoteCmd.Flags().StringVar(&demoteWorkspaceFlag, "workspace", "", "Override target workspace for the demoted note")
}

var planDemoteCmd = &cobra.Command{
	Use:   "demote <job-file-path>",
	Short: "Demote a plan job back to an nb inbox note",
	Long: `Move a flow plan job back to the nb inbox as a note.

If the job has a note_ref pointing to an in_progress/ note, the note is
moved back to inbox/ directly. Otherwise, the job's title and prompt body
are used to create a new note via nb new.

Use --workspace to override where the demoted note lands.

The job's status is set to "abandoned" after demotion.

Examples:
  flow plan demote plans/my-plan/03-stale-task.md
  flow plan demote /abs/path/to/plans/my-plan/03-stale-task.md
  flow plan demote job.md --workspace /path/to/workspace`,
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

	// The note is resolved by QUERYING nb for the plan's notes and filtering on
	// plan_job — note frontmatter (plan_ref/plan_job) is the source of truth, not
	// the job's note_ref (which is now a non-load-bearing provenance hint).
	planName := filepath.Base(filepath.Dir(jobFilePath))
	note, qErr := orchestration.JobNote(planName, job.Filename)
	if qErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: nb query for plan notes failed: %v\n", qErr)
	}
	if note != nil {
		return demoteResolvedNote(note.Path, job)
	}

	// Fallback: only when the query finds nothing, honor a path-shaped legacy
	// note_ref that still stats clean (older job files stored absolute note
	// paths). Say so in the output so the resolution path is not silent.
	if job.NoteRef != "" && filepath.IsAbs(job.NoteRef) {
		if _, statErr := os.Stat(job.NoteRef); statErr == nil {
			fmt.Fprintln(os.Stderr, "Note: nb query found no linked note; falling back to legacy note_ref path.")
			return demoteResolvedNote(job.NoteRef, job)
		}
	}

	// Last resort: no resolvable note anywhere — recreate one via nb new.
	return demoteViaNbNew(job, jobFilePath)
}

// demoteResolvedNote moves an already-resolved note back to inbox/ via nb,
// clears its plan_ref/plan_job frontmatter, and marks the job abandoned. It
// prints the note's new path to stdout.
//
// When --workspace is set the note is routed to THAT workspace's inbox/ rather
// than its own, using nb move's explicit destination-path form. Without the
// flag the behavior is unchanged: a plain group move within the note's own
// workspace.
func demoteResolvedNote(notePath string, job *orchestration.Job) error {
	workspaceOverride, err := resolveWorkspaceOverride()
	if err != nil {
		return err
	}

	var newPath string
	if workspaceOverride != "" {
		newPath, err = orchestration.MoveNoteToWorkspace(notePath, workspaceOverride, "inbox")
	} else {
		newPath, err = orchestration.MoveNote(notePath, "inbox")
	}
	if err != nil {
		return fmt.Errorf("moving note back to inbox: %w", err)
	}

	// Clear the note↔plan link now that the note is back in the inbox.
	if err := orchestration.ClearNoteLink(newPath); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not clear note link on %s: %v\n", newPath, err)
	}

	// Mark the job as abandoned
	sp := orchestration.NewStatePersister()
	if err := sp.UpdateJobStatus(job, orchestration.JobStatusAbandoned); err != nil {
		return fmt.Errorf("updating job status to abandoned: %w", err)
	}

	fmt.Println(newPath)
	return nil
}

// demoteViaNbNew creates a new note via nb new (fallback when no in_progress note exists).
func demoteViaNbNew(job *orchestration.Job, jobFilePath string) error {
	// Determine the target workspace directory. --workspace wins here exactly as
	// it does on the resolved path; both routes share resolveWorkspaceOverride
	// so the flag can never be honored by one and ignored by the other.
	targetWorkspaceDir, err := resolveWorkspaceOverride()
	if err != nil {
		return err
	}
	if targetWorkspaceDir == "" {
		targetWorkspaceDir = resolveTargetWorkspace(jobFilePath, job.NoteRef)
	}

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

// resolveWorkspaceOverride returns the absolute workspace directory requested
// via --workspace, or "" when the flag was not set. It is the single reader of
// demoteWorkspaceFlag: BOTH demote routes (the resolved-note move and the
// nb new fallback) call it, so the flag cannot regress into being honored on
// one path and silently dropped on the other.
func resolveWorkspaceOverride() (string, error) {
	if demoteWorkspaceFlag == "" {
		return "", nil
	}
	abs, err := filepath.Abs(demoteWorkspaceFlag)
	if err != nil {
		return "", fmt.Errorf("resolving workspace path: %w", err)
	}
	return abs, nil
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
