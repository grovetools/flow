package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/grovetools/core/fs"
	"github.com/grovetools/core/pkg/paths"
	coresessions "github.com/grovetools/core/pkg/sessions"
)

// ArchiveContextRules copies the active rules file to the job's artifact directory
// and updates the job's frontmatter to track it.
// For oneshot/shell jobs this creates a single context.rules artifact.
func ArchiveContextRules(job *Job, plan *Plan, usedRulesPath string) error {
	if usedRulesPath == "" {
		return nil
	}

	relPath, err := archiveRulesFile(plan, job.ID, "context.rules", usedRulesPath)
	if err != nil {
		return err
	}

	// Update job struct
	job.UsedRulesFile = relPath

	// Update job file frontmatter directly
	jobContent, err := os.ReadFile(job.FilePath)
	if err == nil {
		updates := map[string]interface{}{
			"used_rules_file": relPath,
		}
		newContent, err := UpdateFrontmatter(jobContent, updates)
		if err == nil {
			_ = os.WriteFile(job.FilePath, newContent, 0o600)
		}
	}

	return nil
}

// ArchiveContextRulesForTurn copies the active rules file to a per-turn artifact
// within the job's artifact directory. Returns the relative artifact path for
// inclusion in the turn's <!-- grove: {} --> metadata tag.
// Returns "" if usedRulesPath is empty (no rules to archive).
func ArchiveContextRulesForTurn(plan *Plan, jobID, turnID, usedRulesPath string) (string, error) {
	if usedRulesPath == "" {
		return "", nil
	}

	filename := turnID + "-context.rules"
	return archiveRulesFile(plan, jobID, filename, usedRulesPath)
}

// archiveRulesFile copies usedRulesPath into .artifacts/{jobID}/{filename}
// and returns the relative artifact path.
func archiveRulesFile(plan *Plan, jobID, filename, usedRulesPath string) (string, error) {
	content, err := os.ReadFile(usedRulesPath)
	if err != nil {
		return "", fmt.Errorf("failed to read used rules file: %w", err)
	}

	destArtifactDir := filepath.Join(plan.Directory, ".artifacts", jobID)
	if err := os.MkdirAll(destArtifactDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create artifact directory: %w", err)
	}

	destRulesPath := filepath.Join(destArtifactDir, filename)
	if err := os.WriteFile(destRulesPath, content, 0o600); err != nil {
		return "", fmt.Errorf("failed to write archived rules file: %w", err)
	}

	return filepath.Join(".artifacts", jobID, filename), nil
}

// ArchiveInteractiveSession copies session metadata and the transcript to the plan's artifacts.
func ArchiveInteractiveSession(job *Job, plan *Plan) error {
	ctx := context.Background()

	ulog.Debug("[ARCHIVE] Starting session archival").
		Field("job_id", job.ID).
		Field("job_type", string(job.Type)).
		Field("plan_name", plan.Name).
		Log(ctx)

	// This function should only operate on jobs that have a native agent session.
	if job.Type != JobTypeInteractiveAgent && job.Type != JobTypeHeadlessAgent && job.Type != JobTypeIsolatedAgent {
		ulog.Debug("[ARCHIVE] Skipping non-agent job type").
			Field("job_id", job.ID).
			Field("job_type", string(job.Type)).
			Log(ctx)
		return nil
	}

	// 1. Find the session metadata.
	sessionsBaseDir := filepath.Join(paths.StateDir(), "hooks", "sessions")
	ulog.Debug("[ARCHIVE] Looking for session registry").
		Field("job_id", job.ID).
		Field("sessions_base_dir", sessionsBaseDir).
		Log(ctx)

	// List all sessions in the registry for debugging
	entries, listErr := os.ReadDir(sessionsBaseDir)
	if listErr != nil {
		ulog.Debug("[ARCHIVE] Failed to list sessions directory").
			Field("sessions_base_dir", sessionsBaseDir).
			Err(listErr).
			Log(ctx)
	} else {
		sessionIDs := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() {
				sessionIDs = append(sessionIDs, entry.Name())
			}
		}
		ulog.Debug("[ARCHIVE] Available sessions in registry").
			Field("session_count", len(sessionIDs)).
			Field("session_ids", sessionIDs).
			Log(ctx)
	}

	registry, err := coresessions.NewFileSystemRegistry()
	if err != nil {
		ulog.Error("[ARCHIVE] Failed to create session registry").
			Field("job_id", job.ID).
			Err(err).
			Log(ctx)
		return fmt.Errorf("failed to create session registry: %w", err)
	}

	ulog.Debug("[ARCHIVE] Searching for session by job ID").
		Field("job_id", job.ID).
		Log(ctx)

	metadata, err := registry.Find(job.ID)
	if err != nil {
		ulog.Error("[ARCHIVE] Failed to find session metadata").
			Field("job_id", job.ID).
			Field("sessions_base_dir", sessionsBaseDir).
			Err(err).
			Log(ctx)
		return fmt.Errorf("failed to find session metadata for job %s: %w", job.ID, err)
	}

	ulog.Debug("[ARCHIVE] Found session metadata").
		Field("job_id", job.ID).
		Field("claude_session_id", metadata.ClaudeSessionID).
		Field("transcript_path", metadata.TranscriptPath).
		Field("provider", metadata.Provider).
		Log(ctx)

	// 2. Construct the source session directory path.
	// Sessions are stored at $XDG_STATE_HOME/grove/hooks/sessions/{claude-session-id}/
	sourceSessionDir := filepath.Join(sessionsBaseDir, metadata.ClaudeSessionID)
	sourceMetadataPath := filepath.Join(sourceSessionDir, "metadata.json")

	ulog.Debug("[ARCHIVE] Checking source session directory").
		Field("source_session_dir", sourceSessionDir).
		Field("source_metadata_path", sourceMetadataPath).
		Log(ctx)

	if _, statErr := os.Stat(sourceMetadataPath); statErr != nil {
		ulog.Error("[ARCHIVE] Source metadata.json not found").
			Field("source_metadata_path", sourceMetadataPath).
			Err(statErr).
			Log(ctx)
	}

	// 3. Define the destination artifact path.
	destArtifactDir := filepath.Join(plan.Directory, ".artifacts", job.ID)
	ulog.Debug("[ARCHIVE] Creating artifact directory").
		Field("dest_artifact_dir", destArtifactDir).
		Log(ctx)

	if err := os.MkdirAll(destArtifactDir, 0o755); err != nil {
		ulog.Error("[ARCHIVE] Failed to create artifact directory").
			Field("dest_artifact_dir", destArtifactDir).
			Err(err).
			Log(ctx)
		return fmt.Errorf("failed to create artifact directory %s: %w", destArtifactDir, err)
	}

	// 4. Copy metadata.json.
	destMetadataPath := filepath.Join(destArtifactDir, "metadata.json")
	ulog.Debug("[ARCHIVE] Copying metadata.json").
		Field("source", sourceMetadataPath).
		Field("dest", destMetadataPath).
		Log(ctx)

	if err := fs.CopyFile(sourceMetadataPath, destMetadataPath); err != nil {
		ulog.Error("[ARCHIVE] Failed to copy metadata.json").
			Field("source", sourceMetadataPath).
			Field("dest", destMetadataPath).
			Err(err).
			Log(ctx)
		return fmt.Errorf("failed to copy metadata.json: %w", err)
	}

	// 5. Copy the transcript file.
	if metadata.TranscriptPath != "" {
		destTranscriptPath := filepath.Join(destArtifactDir, "transcript.jsonl")
		ulog.Debug("[ARCHIVE] Copying transcript file").
			Field("source", metadata.TranscriptPath).
			Field("dest", destTranscriptPath).
			Log(ctx)

		if err := fs.CopyFile(metadata.TranscriptPath, destTranscriptPath); err != nil {
			ulog.Error("[ARCHIVE] Failed to copy transcript file").
				Field("source", metadata.TranscriptPath).
				Field("dest", destTranscriptPath).
				Err(err).
				Log(ctx)
			return fmt.Errorf("failed to copy transcript file from %s: %w", metadata.TranscriptPath, err)
		}
	} else {
		ulog.Warn("[ARCHIVE] No transcript path in metadata").
			Field("job_id", job.ID).
			Field("claude_session_id", metadata.ClaudeSessionID).
			Log(ctx)
	}

	ulog.Debug("[ARCHIVE] Session archival completed successfully").
		Field("job_id", job.ID).
		Field("dest_artifact_dir", destArtifactDir).
		Log(ctx)

	return nil
}
