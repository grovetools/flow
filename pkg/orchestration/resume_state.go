package orchestration

import (
	"fmt"
	"os"
	"time"
)

// BeginResumedAttempt atomically transitions a completed job into a fresh
// running attempt. It returns a rollback function for callers to invoke when
// launching the resumed process fails. Rollback restores the exact file bytes
// and in-memory Job state that preceded this call; unlike UpdateJobStatus, it
// does not synthesize terminal timestamps or fire terminal notifications.
func (sp *StatePersister) BeginResumedAttempt(job *Job) (func() error, error) {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	lock, err := sp.lockFile(job.FilePath)
	if err != nil {
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	defer func() { _ = lock.Unlock() }()

	originalContent, err := os.ReadFile(job.FilePath)
	if err != nil {
		return nil, fmt.Errorf("read job file: %w", err)
	}
	frontmatter, _, err := sp.frontmatterParser.ParseFrontmatter(originalContent)
	if err != nil {
		return nil, fmt.Errorf("parsing frontmatter: %w", err)
	}
	persistedStatus, _ := frontmatter["status"].(string)
	if JobStatus(persistedStatus) != JobStatusCompleted {
		return nil, fmt.Errorf("cannot begin resumed attempt: persisted status is %q, must be %q", persistedStatus, JobStatusCompleted)
	}
	originalJob := *job
	attemptTime := time.Now().UTC().Truncate(time.Second)

	updates := map[string]interface{}{
		"status":       string(JobStatusRunning),
		"started_at":   attemptTime.Format(time.RFC3339),
		"updated_at":   attemptTime.Format(time.RFC3339),
		"completed_at": nil,
		"duration":     nil,
		"last_error":   nil,
	}
	newContent, err := sp.updateFrontmatter(originalContent, updates)
	if err != nil {
		return nil, fmt.Errorf("update frontmatter: %w", err)
	}
	if err := sp.writeAtomic(job.FilePath, newContent); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	job.Status = JobStatusRunning
	job.StartTime = attemptTime
	job.EndTime = time.Time{}
	job.UpdatedAt = attemptTime
	job.CompletedAt = time.Time{}
	job.Duration = 0
	job.Metadata.LastError = ""

	rollback := func() error {
		sp.mu.Lock()
		defer sp.mu.Unlock()

		lock, err := sp.lockFile(job.FilePath)
		if err != nil {
			return fmt.Errorf("acquire lock: %w", err)
		}
		defer func() { _ = lock.Unlock() }()

		if err := sp.writeAtomic(job.FilePath, originalContent); err != nil {
			return fmt.Errorf("restore job file: %w", err)
		}
		*job = originalJob
		return nil
	}

	return rollback, nil
}
