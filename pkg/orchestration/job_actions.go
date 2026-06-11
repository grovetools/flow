package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/grovetools/core/pkg/sessions"
	"github.com/grovetools/core/util/delegation"
)

// verifiedTranscriptSpec decides whether a transcript may be streamed for a
// job, based on the hooks session registry lookup result. It returns the
// aglogs spec (the agent's native session id) and true only when the binding
// is verified — i.e. the registry has an entry for the job with a recorded
// native session id.
//
// A plan/job-name aglogs spec is NOT a safe fallback: aglogs resolves it to
// ANY session that ever matched that plan/job name, which can silently stream
// another agent's transcript into this job's log (see issue:
// wrong-session-logs-bound-to-headless-tuimux-jobs). Comparing the
// transcript's session_id against job.ID is not a substitute either — they
// live in different namespaces (Claude session UUID vs flow job id) and never
// match.
func verifiedTranscriptSpec(metadata *sessions.SessionMetadata, findErr error) (string, bool) {
	if findErr == nil && metadata != nil && metadata.ClaudeSessionID != "" {
		return metadata.ClaudeSessionID, true
	}
	return "", false
}

// unverifiedBindingMarker is the single marker line appended to job.log when
// the session binding for a job cannot be verified and the transcript is
// therefore not captured.
func unverifiedBindingMarker(jobID string) string {
	return fmt.Sprintf("[flow] session binding unverified for job %s — transcript not captured (hooks session registry had no entry); see issue: wrong-session-logs\n", jobID)
}

// markTranscriptUnverified records (best-effort) that the job's transcript was
// intentionally not captured because the session binding could not be
// verified: it logs a warning and appends a single marker line to job.log
// (deduplicated across repeated calls).
func markTranscriptUnverified(ctx context.Context, job *Job, plan *Plan) {
	ulog.Warn("[TRANSCRIPT] Session binding unverified — refusing to stream transcript").
		Field("job_id", job.ID).
		Field("plan_name", plan.Name).
		Field("filename", job.Filename).
		Log(ctx)

	jobLogPath, err := GetJobLogPath(plan, job)
	if err != nil {
		return
	}
	marker := unverifiedBindingMarker(job.ID)
	if existing, readErr := os.ReadFile(jobLogPath); readErr == nil && strings.Contains(string(existing), strings.TrimSpace(marker)) {
		return // marker already present
	}
	f, err := os.OpenFile(jobLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		ulog.Warn("[TRANSCRIPT] Failed to write unverified-binding marker to job.log").
			Field("job_id", job.ID).
			Field("logpath", jobLogPath).
			Err(err).
			Log(ctx)
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.WriteString(marker)
}

// hasLegacyStyleTranscript reports whether the job file already holds a
// non-empty transcript section that predates the markdown render style
// (captured terminal output from the old plain aglogs shell-out). Used only
// to log the one-time rewrite when the section flips to markdown style.
func hasLegacyStyleTranscript(jobFilePath string) bool {
	content, err := os.ReadFile(jobFilePath)
	if err != nil {
		return false
	}
	body := string(content)
	idx := strings.Index(body, transcriptSectionHeader)
	header := transcriptSectionHeader
	if idx == -1 {
		idx = strings.Index(body, legacyTranscriptSectionHeader)
		header = legacyTranscriptSectionHeader
	}
	if idx == -1 {
		return false
	}
	section := strings.TrimSpace(strings.TrimPrefix(body[idx:], header))
	if section == "" {
		return false
	}
	// Markdown-style transcripts carry stable bold role/tool labels.
	for _, marker := range []string{"**User:**", "**Assistant:**", "**Tool:", "**Thinking:**"} {
		if strings.Contains(section, marker) {
			return false
		}
	}
	return true
}

// AppendAgentTranscript finds the transcript for an agent job
// and appends it to the job's markdown file.
//
// The transcript is streamed ONLY when the session binding is verified via
// the hooks session registry (see verifiedTranscriptSpec). On an unverified
// binding the transcript is not captured; a marker line is appended to
// job.log instead.
func AppendAgentTranscript(job *Job, plan *Plan) error {
	ctx := context.Background()

	if job.Type != JobTypeInteractiveAgent && job.Type != JobTypeHeadlessAgent {
		return nil // Not an agent job
	}

	ulog.Debug("[TRANSCRIPT] Starting transcript append").
		Field("job_id", job.ID).
		Field("job_type", job.Type).
		Field("plan_name", plan.Name).
		Field("filename", job.Filename).
		Log(ctx)

	// Resolve the aglogs spec via the hooks session registry. There is NO
	// unverified fallback: streaming by plan/job name can bind another
	// agent's transcript to this job (see verifiedTranscriptSpec).
	var aglogsSpec string
	var verified bool
	if registry, regErr := sessions.NewFileSystemRegistry(); regErr == nil {
		metadata, findErr := registry.Find(job.ID)
		aglogsSpec, verified = verifiedTranscriptSpec(metadata, findErr)
		if verified {
			ulog.Debug("[TRANSCRIPT] Using session ID from registry").
				Field("job_id", job.ID).
				Field("claude_session_id", metadata.ClaudeSessionID).
				Field("provider", metadata.Provider).
				Log(ctx)
		} else {
			ulog.Debug("[TRANSCRIPT] Session not found in registry").
				Field("job_id", job.ID).
				Field("find_error", findErr).
				Log(ctx)
		}
	}

	if !verified {
		markTranscriptUnverified(ctx, job, plan)
		return nil // Not an error; transcript intentionally not captured.
	}

	// Get formatted transcript for job.log (with ANSI colors)
	formattedCmd := delegation.Command("aglogs", "read", aglogsSpec)
	formattedCmd.Env = append(os.Environ(), "CLICOLOR_FORCE=1")
	formattedOutput, formattedErr := formattedCmd.CombinedOutput()
	formattedStr := string(formattedOutput)

	// Get markdown transcript for the .md file: environment-independent
	// (no TTY/theme/icon dependence), injection-safe indented tool blocks.
	plainCmd := delegation.Command("aglogs", "read", aglogsSpec, "--style=markdown")
	plainOutput, plainErr := plainCmd.CombinedOutput()
	plainStr := string(plainOutput)

	ulog.Debug("[TRANSCRIPT] Running aglogs read").
		Field("job_id", job.ID).
		Field("aglogs_spec", aglogsSpec).
		Log(ctx)

	// Use plain text for error checking and .md file writing
	err := plainErr
	outputStr := plainStr

	// Check if a transcript was found. `aglogs read` returns an error if not found.
	if err != nil || len(strings.TrimSpace(outputStr)) == 0 || strings.Contains(outputStr, "no sessions found with job") {
		ulog.Warn("[TRANSCRIPT] No transcript found via aglogs").
			Field("job_id", job.ID).
			Err(err).
			Field("output_len", len(outputStr)).
			Field("has_no_sess", strings.Contains(outputStr, "no sessions found with job")).
			Log(ctx)

		// This is the expected case for a job that was never run.
		// Append a note to the job file instead of treating it as a failure.
		// onlyIfMissing keeps an existing transcript section untouched.
		note := "*This interactive agent job was never run.*"
		if _, writeErr := NewStatePersister().UpdateJobTranscript(job, note, true); writeErr != nil {
			return fmt.Errorf("writing note to job file %s: %w", job.FilePath, writeErr)
		}

		return nil // Not an error.
	}

	ulog.Debug("[TRANSCRIPT] Successfully retrieved transcript from aglogs").
		Field("job_id", job.ID).
		Field("output_len", len(outputStr)).
		Log(ctx)

	// Transcript was found; splice it into the job file under the job-file
	// lock via StatePersister.UpdateJobTranscript. groved's jobrunner calls
	// AppendAgentTranscript from the daemon process concurrently with
	// flow-side writers, so the unlocked full-file rewrite this used to do
	// raced StatePersister's frontmatter updates.
	hadOldFormat := hasLegacyStyleTranscript(job.FilePath)
	transcriptOutput := plainStr // markdown-style text for the .md file
	changed, err := NewStatePersister().UpdateJobTranscript(job, transcriptOutput, false)
	if err != nil {
		return fmt.Errorf("writing transcript to job file %s: %w", job.FilePath, err)
	}
	if changed && hadOldFormat {
		// One-time per file: once rewritten, the section is markdown-style
		// and this no longer fires.
		ulog.Info("Transcript migrated to markdown style").
			Field("job_id", job.ID).
			Field("filepath", job.FilePath).
			Log(ctx)
	}
	if !changed {
		ulog.Info("Transcript unchanged, skipping").
			Field("filename", job.Filename).
			Log(ctx)
		return nil
	}

	ulog.Debug("[TRANSCRIPT] Successfully wrote transcript to job file").
		Field("job_id", job.ID).
		Field("filepath", job.FilePath).
		Log(ctx)

	// Also write the formatted transcript to job.log for TUI fast-path loading
	jobLogPath, err := GetJobLogPath(plan, job)
	if err == nil {
		// Use formatted version (with ANSI colors) for job.log
		if formattedErr == nil && len(formattedStr) > 0 {
			if err := os.WriteFile(jobLogPath, []byte(formattedStr), 0o600); err != nil {
				// Log a warning but don't fail - this is just for TUI optimization
				ulog.Warn("[TRANSCRIPT] Failed to write formatted transcript to job.log").
					Field("job_id", job.ID).
					Field("logpath", jobLogPath).
					Err(err).
					Log(ctx)
			} else {
				ulog.Debug("[TRANSCRIPT] Successfully wrote formatted transcript to job.log").
					Field("job_id", job.ID).
					Field("logpath", jobLogPath).
					Log(ctx)
			}
		}
	}

	return nil
}

// RenameJob renames a job file and updates its title and dependencies.
func RenameJob(plan *Plan, jobToRename *Job, newTitle string) error {
	// 1. Parse numeric prefix from old filename
	re := regexp.MustCompile(`^(\d+)-`)
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
	if err := os.WriteFile(newFilePath, updatedContent, 0o600); err != nil {
		return fmt.Errorf("writing new job file %s: %w", newFilename, err)
	}

	// 5. Update dependent jobs and include file references
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

		// Check and update include files
		var newInclude []string
		var includeUpdated bool
		for _, source := range job.Include {
			if source == jobToRename.Filename {
				newInclude = append(newInclude, newFilename)
				includeUpdated = true
			} else {
				newInclude = append(newInclude, source)
			}
		}
		if includeUpdated {
			updates["include"] = newInclude
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
			if err := os.WriteFile(job.FilePath, updatedDepContent, 0o600); err != nil {
				return fmt.Errorf("writing updated job file %s: %w", job.Filename, err)
			}
		}
	}

	// 6. Delete the old job file
	if err := os.Remove(jobToRename.FilePath); err != nil {
		// Log a warning but don't fail the whole operation if we can't delete the old file
		ulog.Warn("Failed to remove old job file").
			Field("filepath", jobToRename.FilePath).
			Err(err).
			Log(context.Background())
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
	if err := os.WriteFile(job.FilePath, updatedContent, 0o600); err != nil {
		return fmt.Errorf("writing job file %s: %w", job.Filename, err)
	}

	// Update in-memory job object
	job.DependsOn = newDeps

	return nil
}
