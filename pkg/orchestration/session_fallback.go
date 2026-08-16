package orchestration

import (
	"os"
	"time"

	"github.com/grovetools/core/pkg/sessions"
)

// newFallbackSessionMetadata is the single contract for daemon-unavailable
// provider writers. Every fallback record is attempt-keyed and carries the
// same status and scope provenance as the intent sent to the daemon.
func newFallbackSessionMetadata(job *Job, plan *Plan, workDir, provider, nativeID, sessionType, transcriptPath string, pid int) sessions.SessionMetadata {
	user := os.Getenv("USER")
	if user == "" {
		user = "unknown"
	}
	repo, branch := getGitInfo(workDir)
	startedAt := job.StartTime
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return sessions.SessionMetadata{
		SessionID:        job.ID,
		AttemptID:        job.AttemptID,
		JobID:            job.ID,
		ParentJobID:      job.ParentJobID,
		ClaudeSessionID:  nativeID,
		Provider:         provider,
		PID:              pid,
		WorkingDirectory: workDir,
		User:             user,
		Repo:             repo,
		Branch:           branch,
		StartedAt:        startedAt,
		JobTitle:         job.Title,
		PlanName:         plan.Name,
		JobFilePath:      job.FilePath,
		Status:           string(job.Status),
		Scope:            resolveJobScope(workDir),
		Type:             sessionType,
		TranscriptPath:   transcriptPath,
	}
}
