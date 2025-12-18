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

// AppendAgentTranscript finds the transcript for an agent job
// and appends it to the job's markdown file.
func AppendAgentTranscript(job *Job, plan *Plan) error {
	if job.Type != JobTypeInteractiveAgent && job.Type != JobTypeHeadlessAgent {
		return nil // Not an agent job
	}

	log.WithFields(map[string]interface{}{
		"job_id":    job.ID,
		"job_type":  job.Type,
		"plan_name": plan.Name,
		"filename":  job.Filename,
	}).Debug("[TRANSCRIPT] Starting transcript append")

	jobSpec := fmt.Sprintf("%s/%s", plan.Name, job.Filename)

	// Get formatted transcript for job.log (with ANSI colors)
	formattedCmd := exec.Command("grove", "aglogs", "read", jobSpec)
	formattedCmd.Env = append(os.Environ(), "CLICOLOR_FORCE=1")
	formattedOutput, formattedErr := formattedCmd.CombinedOutput()
	formattedStr := string(formattedOutput)

	// Get plain text transcript for .md file (without ANSI colors)
	plainCmd := exec.Command("grove", "aglogs", "read", jobSpec)
	plainOutput, plainErr := plainCmd.CombinedOutput()
	plainStr := string(plainOutput)

	log.WithFields(map[string]interface{}{
		"job_id":   job.ID,
		"job_spec": jobSpec,
	}).Debug("[TRANSCRIPT] Running aglogs read")

	// Use plain text for error checking and .md file writing
	err := plainErr
	outputStr := plainStr

	// Check if a transcript was found. `aglogs read` returns an error if not found.
	if err != nil || len(strings.TrimSpace(outputStr)) == 0 || strings.Contains(outputStr, "no sessions found with job") {
		log.WithFields(map[string]interface{}{
			"job_id":      job.ID,
			"error":       err,
			"output_len":  len(outputStr),
			"has_no_sess": strings.Contains(outputStr, "no sessions found with job"),
		}).Warn("[TRANSCRIPT] No transcript found via aglogs")

		// This is the expected case for a job that was never run.
		// Append a note to the job file instead of treating it as a failure.
		content, readErr := os.ReadFile(job.FilePath)
		if readErr != nil {
			return fmt.Errorf("reading job file %s: %w", job.FilePath, readErr)
		}

		// Only append if transcript section doesn't already exist.
		if !strings.Contains(string(content), "# Agent Chat Transcript") && !strings.Contains(string(content), "## Transcript") {
			note := "\n# Agent Chat Transcript\n\n*This interactive agent job was never run.*"
			newContent := string(content) + note
			if writeErr := os.WriteFile(job.FilePath, []byte(newContent), 0644); writeErr != nil {
				return fmt.Errorf("writing note to job file %s: %w", job.FilePath, writeErr)
			}
		}

		return nil // Not an error.
	}

	log.WithFields(map[string]interface{}{
		"job_id":     job.ID,
		"output_len": len(outputStr),
	}).Debug("[TRANSCRIPT] Successfully retrieved transcript from aglogs")

	// Transcript was found, so proceed with appending it.
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("reading job file %s: %w", job.FilePath, err)
	}

	var newContent string
	// Use plain text for .md file
	transcriptOutput := plainStr

	// Check for both old and new header formats
	existingContent := string(content)
	transcriptIdx := strings.Index(existingContent, "# Agent Chat Transcript")
	if transcriptIdx == -1 {
		transcriptIdx = strings.Index(existingContent, "## Transcript")
	}

	if transcriptIdx != -1 {
		// Transcript section exists, update it
		existingTranscript := existingContent[transcriptIdx:]

		// Try to extract existing content (handle both header formats)
		var existingTranscriptContent string
		if strings.HasPrefix(existingTranscript, "# Agent Chat Transcript\n\n") {
			existingTranscriptContent = strings.TrimSpace(strings.TrimPrefix(existingTranscript, "# Agent Chat Transcript\n\n"))
		} else {
			existingTranscriptContent = strings.TrimSpace(strings.TrimPrefix(existingTranscript, "## Transcript\n\n"))
		}
		newTranscriptContent := strings.TrimSpace(transcriptOutput)

		if existingTranscriptContent == newTranscriptContent {
			fmt.Printf("Info: Transcript unchanged in %s. Skipping.\n", job.Filename)
			return nil
		}

		fmt.Printf("Info: Updating transcript in %s (resumed session detected).\n", job.Filename)
		beforeTranscript := existingContent[:transcriptIdx]
		transcriptHeader := "\n# Agent Chat Transcript\n\n"
		newContent = beforeTranscript + transcriptHeader + transcriptOutput
	} else {
		// No transcript section exists, append it
		transcriptHeader := "\n# Agent Chat Transcript\n\n"
		newContent = string(content) + transcriptHeader + transcriptOutput
	}

	if err := os.WriteFile(job.FilePath, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("writing transcript to job file %s: %w", job.FilePath, err)
	}

	log.WithFields(map[string]interface{}{
		"job_id":   job.ID,
		"filepath": job.FilePath,
	}).Debug("[TRANSCRIPT] Successfully wrote transcript to job file")

	// Also write the formatted transcript to job.log for TUI fast-path loading
	jobLogPath, err := GetJobLogPath(plan, job)
	if err == nil {
		// Use formatted version (with ANSI colors) for job.log
		if formattedErr == nil && len(formattedStr) > 0 {
			if err := os.WriteFile(jobLogPath, []byte(formattedStr), 0644); err != nil {
				// Log a warning but don't fail - this is just for TUI optimization
				log.WithFields(map[string]interface{}{
					"job_id":  job.ID,
					"logpath": jobLogPath,
					"error":   err,
				}).Warn("[TRANSCRIPT] Failed to write formatted transcript to job.log")
			} else {
				log.WithFields(map[string]interface{}{
					"job_id":  job.ID,
					"logpath": jobLogPath,
				}).Debug("[TRANSCRIPT] Successfully wrote formatted transcript to job.log")
			}
		}
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