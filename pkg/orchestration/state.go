package orchestration

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
	
	"gopkg.in/yaml.v3"
)

// FrontmatterParser provides methods for parsing and updating frontmatter.
type FrontmatterParser struct{}

// ParseFrontmatter parses YAML frontmatter from content.
func (fp *FrontmatterParser) ParseFrontmatter(content []byte) (map[string]interface{}, []byte, error) {
	return ParseFrontmatter(content)
}

// WriteFrontmatter writes frontmatter and body to a writer.
func (fp *FrontmatterParser) WriteFrontmatter(w io.Writer, frontmatter map[string]interface{}) error {
	yamlBytes, err := yaml.Marshal(frontmatter)
	if err != nil {
		return fmt.Errorf("marshaling frontmatter: %w", err)
	}
	
	if _, err := w.Write([]byte("---\n")); err != nil {
		return err
	}
	if _, err := w.Write(yamlBytes); err != nil {
		return err
	}
	if _, err := w.Write([]byte("---\n")); err != nil {
		return err
	}
	
	return nil
}

// StatePersister handles persistent state updates for jobs.
type StatePersister struct {
	mu                sync.RWMutex
	frontmatterParser *FrontmatterParser
}

// NewStatePersister creates a new state persister.
func NewStatePersister() *StatePersister {
	return &StatePersister{
		frontmatterParser: &FrontmatterParser{},
	}
}

// UpdateJobStatus updates the status of a job in its markdown file.
func (sp *StatePersister) UpdateJobStatus(job *Job, newStatus JobStatus) error {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	// Create file lock
	lock, err := sp.lockFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer lock.Unlock()

	// Read current file
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("read job file: %w", err)
	}

	// Update status in frontmatter
	updates := map[string]interface{}{
		"status":     string(newStatus),
		"updated_at": time.Now().Format(time.RFC3339),
	}

	// Add started_at for running status
	if newStatus == JobStatusRunning && job.StartTime.IsZero() {
		updates["started_at"] = time.Now().Format(time.RFC3339)
	}

	// Add completed_at for terminal states
	if newStatus == JobStatusCompleted || newStatus == JobStatusFailed {
		updates["completed_at"] = time.Now().Format(time.RFC3339)
		if !job.StartTime.IsZero() {
			duration := time.Since(job.StartTime)
			updates["duration"] = duration.String()
		}
	}

	// Apply update
	newContent, err := sp.updateFrontmatter(content, updates)
	if err != nil {
		return fmt.Errorf("update frontmatter: %w", err)
	}

	// Write atomically
	if err := sp.writeAtomic(job.FilePath, newContent); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	// Update in-memory state
	job.Status = newStatus
	if newStatus == JobStatusRunning && job.StartTime.IsZero() {
		job.StartTime = time.Now()
	}
	if newStatus == JobStatusCompleted || newStatus == JobStatusFailed {
		job.EndTime = time.Now()
	}

	return nil
}

// UpdateJobMetadata updates metadata fields for a job.
func (sp *StatePersister) UpdateJobMetadata(job *Job, meta JobMetadata) error {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	// Create file lock
	lock, err := sp.lockFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer lock.Unlock()

	// Read current file
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("read job file: %w", err)
	}

	// Build updates map
	updates := make(map[string]interface{})
	
	if meta.RetryCount > 0 {
		updates["retry_count"] = meta.RetryCount
	}
	if meta.LastError != "" {
		updates["last_error"] = meta.LastError
	}
	if meta.ExecutionTime > 0 {
		updates["execution_time"] = meta.ExecutionTime.String()
	}
	
	updates["updated_at"] = time.Now().Format(time.RFC3339)

	// Apply update
	newContent, err := sp.updateFrontmatter(content, updates)
	if err != nil {
		return fmt.Errorf("update frontmatter: %w", err)
	}

	// Write atomically
	if err := sp.writeAtomic(job.FilePath, newContent); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	// Update in-memory state
	job.Metadata = meta

	return nil
}

// AppendJobOutput appends output to a job's markdown file.
func (sp *StatePersister) AppendJobOutput(job *Job, output string) error {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	// Create file lock
	lock, err := sp.lockFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer lock.Unlock()

	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return err
	}

	// Parse frontmatter
	frontmatter, body, err := sp.frontmatterParser.ParseFrontmatter(content)
	if err != nil {
		return err
	}

	// Check if output section exists
	outputMarker := []byte("\n\n## Output\n\n")
	if !bytes.Contains(body, outputMarker) {
		body = append(body, outputMarker...)
	}

	// Append timestamped output
	timestamp := time.Now().Format("15:04:05")
	outputLine := fmt.Sprintf("[%s] %s\n", timestamp, output)
	body = append(body, []byte(outputLine)...)

	// Reconstruct file
	var buf bytes.Buffer
	if err := sp.frontmatterParser.WriteFrontmatter(&buf, frontmatter); err != nil {
		return err
	}
	buf.Write(body)

	// Write atomically
	return sp.writeAtomic(job.FilePath, buf.Bytes())
}

// ValidateJobStates validates all job states in a plan.
func (sp *StatePersister) ValidateJobStates(plan *Plan) []error {
	var errors []error

	for _, job := range plan.Jobs {
		// Check file exists
		if _, err := os.Stat(job.FilePath); os.IsNotExist(err) {
			errors = append(errors, fmt.Errorf("job file missing: %s", job.FilePath))
			continue
		}

		// Verify frontmatter is valid
		content, err := os.ReadFile(job.FilePath)
		if err != nil {
			errors = append(errors, fmt.Errorf("read job %s: %w", job.FilePath, err))
			continue
		}

		fm, _, err := sp.frontmatterParser.ParseFrontmatter(content)
		if err != nil {
			errors = append(errors, fmt.Errorf("invalid frontmatter in %s: %w", job.FilePath, err))
			continue
		}

		// Check required fields
		if _, ok := fm["id"]; !ok {
			errors = append(errors, fmt.Errorf("missing 'id' in %s", job.FilePath))
		}
		if _, ok := fm["status"]; !ok {
			errors = append(errors, fmt.Errorf("missing 'status' in %s", job.FilePath))
		}

		// Check status is valid
		if status, ok := fm["status"].(string); ok {
			if !isValidStatus(JobStatus(status)) {
				errors = append(errors, fmt.Errorf("invalid status '%s' in %s", status, job.FilePath))
			}
		}
	}

	return errors
}

// File locking

// FileLock represents a lock on a file.
type FileLock struct {
	path string
	file *os.File
}

func (sp *StatePersister) lockFile(path string) (*FileLock, error) {
	lockPath := path + ".lock"

	// Try to create lock file exclusively
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		if os.IsExist(err) {
			// Check if lock is stale (older than 5 minutes)
			if info, err := os.Stat(lockPath); err == nil {
				if time.Since(info.ModTime()) > 5*time.Minute {
					// Remove stale lock
					os.Remove(lockPath)
					// Retry
					file, err = os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
					if err != nil {
						return nil, fmt.Errorf("file is locked")
					}
				} else {
					return nil, fmt.Errorf("file is locked")
				}
			}
		} else {
			return nil, err
		}
	}

	// Write PID for debugging
	fmt.Fprintf(file, "%d\n", os.Getpid())
	file.Sync()

	return &FileLock{path: lockPath, file: file}, nil
}

// Unlock releases the file lock.
func (fl *FileLock) Unlock() error {
	if fl.file != nil {
		fl.file.Close()
	}
	return os.Remove(fl.path)
}

// Atomic file operations

func (sp *StatePersister) writeAtomic(path string, content []byte) error {
	// Get current file permissions if file exists
	var perm os.FileMode = 0644
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode()
	}

	dir := filepath.Dir(path)
	// Use a pattern that clearly identifies it as a temp file
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}

	// Ensure cleanup on error
	success := false
	defer func() {
		if !success {
			f.Close()
			os.Remove(f.Name())
		}
	}()

	// Set file permissions
	if err = f.Chmod(perm); err != nil {
		return err
	}

	// Write content
	if _, err = f.Write(content); err != nil {
		return err
	}

	// Sync to ensure data is on disk
	if err = f.Sync(); err != nil {
		return err
	}

	// Close before rename
	if err = f.Close(); err != nil {
		return err
	}

	// Atomic rename
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}

	success = true
	return nil
}

// Frontmatter operations

func (sp *StatePersister) updateFrontmatter(content []byte, updates map[string]interface{}) ([]byte, error) {
	// Parse existing frontmatter
	frontmatter, body, err := sp.frontmatterParser.ParseFrontmatter(content)
	if err != nil {
		return nil, err
	}

	// Apply updates
	for key, value := range updates {
		if value == nil || value == "" || value == 0 {
			delete(frontmatter, key)
		} else {
			frontmatter[key] = value
		}
	}

	// Reconstruct file
	var buf bytes.Buffer
	if err := sp.frontmatterParser.WriteFrontmatter(&buf, frontmatter); err != nil {
		return nil, err
	}
	buf.Write(body)

	return buf.Bytes(), nil
}

// Helper functions

func isValidStatus(status JobStatus) bool {
	switch status {
	case JobStatusPending, JobStatusRunning, JobStatusCompleted,
		JobStatusFailed, JobStatusBlocked, JobStatusNeedsReview:
		return true
	}
	return false
}