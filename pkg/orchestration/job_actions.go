package orchestration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// AppendInteractiveTranscript finds the transcript for an interactive agent job
// and appends it to the job's markdown file.
func AppendInteractiveTranscript(job *Job, plan *Plan) error {
	if job.Type != JobTypeInteractiveAgent {
		return nil // Not an interactive agent job
	}

	jobSpec := fmt.Sprintf("%s/%s", plan.Name, job.Filename)
	cmd := exec.Command("grove", "aglogs", "read", jobSpec)

	output, err := cmd.CombinedOutput()
	if err != nil {
		// It's not a fatal error if the transcript can't be found.
		// This can happen if the agent session was never started.
		// Log a warning and continue.
		fmt.Printf("Warning: could not get transcript for %s: %v\n", jobSpec, err)
		fmt.Printf("         'aglogs' output: %s\n", string(output))
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

	// Check if transcript section already exists
	var newContent string
	transcriptOutput := string(output)

	if strings.Contains(string(content), "## Transcript") {
		// Transcript section exists - check if content has changed
		existingContent := string(content)

		// Find the existing transcript section
		transcriptIdx := strings.Index(existingContent, "## Transcript")
		if transcriptIdx == -1 {
			// Shouldn't happen but handle gracefully
			transcriptHeader := "\n\n---\n\n## Transcript\n\n"
			newContent = existingContent + transcriptHeader + transcriptOutput
		} else {
			existingTranscript := existingContent[transcriptIdx:]

			// Check if the new transcript is identical to what's already there
			// (after the "## Transcript\n\n" header)
			existingTranscriptContent := strings.TrimPrefix(existingTranscript, "## Transcript\n\n")
			existingTranscriptContent = strings.TrimSpace(existingTranscriptContent)
			newTranscriptContent := strings.TrimSpace(transcriptOutput)

			if existingTranscriptContent == newTranscriptContent {
				fmt.Printf("Info: Transcript unchanged in %s. Skipping.\n", job.Filename)
				return nil
			}

			// Transcript has new content - replace the entire transcript section
			fmt.Printf("Info: Updating transcript in %s (resumed session detected).\n", job.Filename)
			beforeTranscript := existingContent[:transcriptIdx]
			transcriptHeader := "## Transcript\n\n"
			newContent = beforeTranscript + transcriptHeader + transcriptOutput
		}
	} else {
		// No transcript section exists - create a new one
		transcriptHeader := "\n\n---\n\n## Transcript\n\n"
		newContent = string(content) + transcriptHeader + transcriptOutput
	}

	// Write back to file
	if err := os.WriteFile(job.FilePath, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("writing transcript to job file %s: %w", job.FilePath, err)
	}

	return nil
}

// RenameJob renames a job file and updates its title and dependencies.
func RenameJob(plan *Plan, jobToRename *Job, newTitle string) error {
	// 1. Parse numeric prefix from old filename
	re := regexp.MustCompile(`^(\d{2})-`)
	matches := re.FindStringSubmatch(jobToRename.Filename)
	if len(matches) < 2 {
		return fmt.Errorf("could not parse numeric prefix from filename: %s", jobToRename.Filename)
	}
	prefixNum, _ := strconv.Atoi(matches[1])

	// 2. Generate new filename
	newFilename := GenerateJobFilename(prefixNum, newTitle)
	newFilePath := filepath.Join(plan.Directory, newFilename)

	// 3. Check for collisions
	if _, err := os.Stat(newFilePath); err == nil {
		return fmt.Errorf("a job with the filename '%s' already exists", newFilename)
	}

	// 4. Update the job file content and write to new path
	currentContent, err := os.ReadFile(jobToRename.FilePath)
	if err != nil {
		return fmt.Errorf("reading job file %s: %w", jobToRename.Filename, err)
	}
	updatedContent, err := UpdateFrontmatter(currentContent, map[string]interface{}{"title": newTitle})
	if err != nil {
		return fmt.Errorf("updating frontmatter for %s: %w", jobToRename.Filename, err)
	}
	if err := os.WriteFile(newFilePath, updatedContent, 0644); err != nil {
		return fmt.Errorf("writing new job file %s: %w", newFilename, err)
	}

	// 5. Update dependent jobs and prompt_source references
	for _, job := range plan.Jobs {
		if job.ID == jobToRename.ID {
			continue // Skip the job being renamed
		}

		updates := make(map[string]interface{})

		// Check and update depends_on
		var newDeps []string
		var depsUpdated bool
		for _, dep := range job.DependsOn {
			if dep == jobToRename.Filename {
				newDeps = append(newDeps, newFilename)
				depsUpdated = true
			} else {
				newDeps = append(newDeps, dep)
			}
		}
		if depsUpdated {
			updates["depends_on"] = newDeps
		}

		// Check and update prompt_source
		var newPromptSource []string
		var promptSourceUpdated bool
		for _, source := range job.PromptSource {
			if source == jobToRename.Filename {
				newPromptSource = append(newPromptSource, newFilename)
				promptSourceUpdated = true
			} else {
				newPromptSource = append(newPromptSource, source)
			}
		}
		if promptSourceUpdated {
			updates["prompt_source"] = newPromptSource
		}

		// Only write if there are updates to make
		if len(updates) > 0 {
			depContent, err := os.ReadFile(job.FilePath)
			if err != nil {
				return fmt.Errorf("reading job file %s: %w", job.Filename, err)
			}
			updatedDepContent, err := UpdateFrontmatter(depContent, updates)
			if err != nil {
				return fmt.Errorf("updating references in %s: %w", job.Filename, err)
			}
			if err := os.WriteFile(job.FilePath, updatedDepContent, 0644); err != nil {
				return fmt.Errorf("writing updated job file %s: %w", job.Filename, err)
			}
		}
	}

	// 6. Delete the old job file
	if err := os.Remove(jobToRename.FilePath); err != nil {
		// Log a warning but don't fail the whole operation if we can't delete the old file
		fmt.Printf("Warning: failed to remove old job file %s: %v\n", jobToRename.FilePath, err)
	}

	// 7. Update in-memory plan object for immediate TUI feedback before full refresh
	jobToRename.Title = newTitle
	jobToRename.Filename = newFilename
	jobToRename.FilePath = newFilePath

	return nil
}

// UpdateJobDependencies updates a job's depends_on field in its frontmatter.
func UpdateJobDependencies(job *Job, newDeps []string) error {
	// Read current job file content
	currentContent, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("reading job file %s: %w", job.Filename, err)
	}

	// Update the depends_on field
	updatedContent, err := UpdateFrontmatter(currentContent, map[string]interface{}{"depends_on": newDeps})
	if err != nil {
		return fmt.Errorf("updating frontmatter for %s: %w", job.Filename, err)
	}

	// Write back to file
	if err := os.WriteFile(job.FilePath, updatedContent, 0644); err != nil {
		return fmt.Errorf("writing job file %s: %w", job.Filename, err)
	}

	// Update in-memory job object
	job.DependsOn = newDeps

	return nil
}