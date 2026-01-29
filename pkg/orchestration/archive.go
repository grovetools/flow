package orchestration

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/grovetools/core/fs"
	"github.com/grovetools/core/pkg/paths"
	coresessions "github.com/grovetools/core/pkg/sessions"
)

// ArchiveInteractiveSession copies session metadata and the transcript to the plan's artifacts.
func ArchiveInteractiveSession(job *Job, plan *Plan) error {
	// This function should only operate on jobs that have a native agent session.
	if job.Type != JobTypeInteractiveAgent && job.Type != JobTypeHeadlessAgent {
		return nil
	}

	// 1. Find the session metadata.
	registry, err := coresessions.NewFileSystemRegistry()
	if err != nil {
		return fmt.Errorf("failed to create session registry: %w", err)
	}
	metadata, err := registry.Find(job.ID)
	if err != nil {
		return fmt.Errorf("failed to find session metadata for job %s: %w", job.ID, err)
	}

	// 2. Construct the source session directory path.
	// Sessions are stored at $XDG_STATE_HOME/grove/hooks/sessions/{claude-session-id}/
	sessionsBaseDir := filepath.Join(paths.StateDir(), "hooks", "sessions")
	sourceSessionDir := filepath.Join(sessionsBaseDir, metadata.ClaudeSessionID)
	sourceMetadataPath := filepath.Join(sourceSessionDir, "metadata.json")

	// 3. Define the destination artifact path.
	destArtifactDir := filepath.Join(plan.Directory, ".artifacts", job.ID)
	if err := os.MkdirAll(destArtifactDir, 0755); err != nil {
		return fmt.Errorf("failed to create artifact directory %s: %w", destArtifactDir, err)
	}

	// 4. Copy metadata.json.
	destMetadataPath := filepath.Join(destArtifactDir, "metadata.json")
	if err := fs.CopyFile(sourceMetadataPath, destMetadataPath); err != nil {
		return fmt.Errorf("failed to copy metadata.json: %w", err)
	}

	// 5. Copy the transcript file.
	if metadata.TranscriptPath != "" {
		destTranscriptPath := filepath.Join(destArtifactDir, "transcript.jsonl")
		if err := fs.CopyFile(metadata.TranscriptPath, destTranscriptPath); err != nil {
			return fmt.Errorf("failed to copy transcript file from %s: %w", metadata.TranscriptPath, err)
		}
	}

	return nil
}
