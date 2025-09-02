package orchestration

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// AppendInteractiveTranscript finds the transcript for an interactive agent job
// and appends it to the job's markdown file.
func AppendInteractiveTranscript(job *Job, plan *Plan) error {
	if job.Type != JobTypeInteractiveAgent {
		return nil // Not an interactive agent job
	}

	jobSpec := fmt.Sprintf("%s/%s", plan.Name, job.Filename)
	cmd := exec.Command("clogs", "read", jobSpec)

	output, err := cmd.CombinedOutput()
	if err != nil {
		// It's not a fatal error if the transcript can't be found.
		// This can happen if the agent session was never started.
		// Log a warning and continue.
		fmt.Printf("Warning: could not get transcript for %s: %v\n", jobSpec, err)
		fmt.Printf("         'clogs' output: %s\n", string(output))
		return nil
	}

	if len(strings.TrimSpace(string(output))) == 0 || strings.Contains(string(output), "no sessions found with job") {
		fmt.Printf("Info: No transcript found for job %s.\n", jobSpec)
		return nil
	}

	// Read current job file content
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("reading job file %s: %w", job.FilePath, err)
	}

	// Check if transcript already exists to avoid duplication
	if strings.Contains(string(content), "## Transcript") {
		fmt.Printf("Info: Transcript already exists in %s. Skipping.\n", job.Filename)
		return nil
	}

	// Append transcript
	transcriptHeader := "\n\n---\n\n## Transcript\n\n"
	newContent := string(content) + transcriptHeader + string(output)

	// Write back to file
	if err := os.WriteFile(job.FilePath, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("writing transcript to job file %s: %w", job.FilePath, err)
	}

	return nil
}