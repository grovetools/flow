package orchestration

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/grovetools/core/pkg/process"
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
	heldLocks         map[string]bool
}

// NewStatePersister creates a new state persister.
func NewStatePersister() *StatePersister {
	return &StatePersister{
		frontmatterParser: &FrontmatterParser{},
		heldLocks:         make(map[string]bool),
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
	defer func() { _ = lock.Unlock() }()

	// Read current file
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("read job file: %w", err)
	}

	// For abandoned status, we need to parse frontmatter and add a note
	if newStatus == JobStatusAbandoned {
		frontmatter, body, err := sp.frontmatterParser.ParseFrontmatter(content)
		if err != nil {
			return fmt.Errorf("parsing frontmatter: %w", err)
		}

		// Apply updates to frontmatter map
		frontmatter["status"] = string(newStatus)
		frontmatter["updated_at"] = time.Now().Format(time.RFC3339)

		// Add the abandoned note if not already present
		noteMarker := []byte("This job was abandoned by the user.")
		if !bytes.Contains(body, noteMarker) {
			note := "\n\n---\n\n## Note\n\nThis job was abandoned by the user."
			body = append(body, []byte(note)...)
		}

		newContent, err := RebuildMarkdownWithFrontmatter(frontmatter, body)
		if err != nil {
			return fmt.Errorf("rebuilding job content: %w", err)
		}

		// Write atomically
		if err := sp.writeAtomic(job.FilePath, newContent); err != nil {
			return fmt.Errorf("write file: %w", err)
		}
	} else {
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

		// Successful completion invalidates any last_error left over from an
		// earlier failed run (the loader carries it into job.Metadata, and
		// updateFrontmatter deletes keys with empty values). Without this a
		// failed-then-rerun job reports status: completed alongside the stale
		// error, and the success job.finished log event echoes it.
		if newStatus == JobStatusCompleted {
			updates["last_error"] = ""
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
	}

	// Update in-memory state
	job.Status = newStatus
	if newStatus == JobStatusCompleted {
		// Keep in-memory metadata consistent with the cleared frontmatter so
		// downstream logging (e.g. the orchestrator's job.finished event) and
		// any later UpdateJobMetadata call don't resurrect the stale error.
		job.Metadata.LastError = ""
	}
	if newStatus == JobStatusRunning && job.StartTime.IsZero() {
		job.StartTime = time.Now()
	}
	if newStatus == JobStatusCompleted || newStatus == JobStatusFailed || newStatus == JobStatusAbandoned {
		job.EndTime = time.Now()
		// Fire notification on terminal status transition
		FireNotificationOnComplete(job, newStatus)
	}

	return nil
}

// UpdateJobModel sets the job's `model:` frontmatter to the actual model the
// agent launched with, used by the executors to backfill the resolved model so
// the field reflects reality rather than a creation-time default.
//
// Unlike the other StatePersister writers, this uses the package-level
// UpdateFrontmatter (the yaml.Node-based writer) so existing key order and
// formatting are preserved — the job file is human-curated and we patch a
// single key. It also bumps updated_at. An empty value removes model from
// frontmatter, allowing status-TUI users to restore a CLI default.
func (sp *StatePersister) UpdateJobModel(job *Job, newModel string) error {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	lock, err := sp.lockFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer func() { _ = lock.Unlock() }()

	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("read job file: %w", err)
	}

	updates := map[string]interface{}{
		"model":      newModel,
		"updated_at": time.Now().Format(time.RFC3339),
	}

	newContent, err := UpdateFrontmatter(content, updates)
	if err != nil {
		return fmt.Errorf("update frontmatter: %w", err)
	}

	if err := sp.writeAtomic(job.FilePath, newContent); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	job.Model = newModel
	return nil
}

// UpdateJobFields writes an arbitrary set of frontmatter fields to a job in one
// atomic update, bumps updated_at, and syncs the in-memory Job so observers
// holding the pointer stay consistent. It is the generic persistence backend
// behind the status TUI's schema-driven field editor (the c… Change namespace):
// the typed UpdateJobStatus/UpdateJobType/… wrappers remain for API
// compatibility, but the TUI now commits every scalar config field through here.
//
// Like UpdateJobModel it uses the package-level UpdateFrontmatter (the yaml.Node
// writer) so existing key order and formatting in the human-curated job file are
// preserved — only the touched keys change. Callers pass frontmatter keys
// (e.g. "provider", "cache_ttl", "memory") mapped to their new values: a string
// for enum/text fields, a bool for the memory/auto_complete toggles.
func (sp *StatePersister) UpdateJobFields(job *Job, updates map[string]interface{}) error {
	if len(updates) == 0 {
		return nil
	}

	sp.mu.Lock()
	defer sp.mu.Unlock()

	lock, err := sp.lockFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer func() { _ = lock.Unlock() }()

	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("read job file: %w", err)
	}

	// Copy so updated_at can be added without mutating the caller's map.
	fmUpdates := make(map[string]interface{}, len(updates)+1)
	for k, v := range updates {
		fmUpdates[k] = v
	}
	fmUpdates["updated_at"] = time.Now().Format(time.RFC3339)

	newContent, err := UpdateFrontmatter(content, fmUpdates)
	if err != nil {
		return fmt.Errorf("update frontmatter: %w", err)
	}

	if err := sp.writeAtomic(job.FilePath, newContent); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	// Keep the in-memory Job consistent with the on-disk change (the typed
	// setters each do `job.X = value`; do it generically by yaml tag).
	for k, v := range updates {
		applyJobFieldInMemory(job, k, v)
	}

	return nil
}

// applyJobFieldInMemory sets the Job struct field whose yaml tag matches key to
// value, covering the scalar kinds the field editor edits (string / bool /
// *bool). Unknown keys or type mismatches are ignored — the on-disk write is the
// source of truth and a RefreshMsg reload reconciles anything missed.
func applyJobFieldInMemory(job *Job, key string, value interface{}) {
	rv := reflect.ValueOf(job).Elem()
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		name := strings.Split(rt.Field(i).Tag.Get("yaml"), ",")[0]
		if name != key {
			continue
		}
		fv := rv.Field(i)
		if !fv.CanSet() {
			return
		}
		switch fv.Kind() {
		case reflect.String:
			if s, ok := value.(string); ok {
				fv.SetString(s)
			}
		case reflect.Bool:
			if b, ok := value.(bool); ok {
				fv.SetBool(b)
			}
		case reflect.Ptr:
			if fv.Type().Elem().Kind() == reflect.Bool {
				if b, ok := value.(bool); ok {
					nb := b
					fv.Set(reflect.ValueOf(&nb))
				}
			}
		}
		return
	}
}

// UpdateJobType updates the type of a job in its markdown file.
func (sp *StatePersister) UpdateJobType(job *Job, newType JobType) error {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	// Create file lock
	lock, err := sp.lockFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer func() { _ = lock.Unlock() }()

	// Read current file
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("read job file: %w", err)
	}

	// Update type in frontmatter
	updates := map[string]interface{}{
		"type":       string(newType),
		"updated_at": time.Now().Format(time.RFC3339),
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
	job.Type = newType

	return nil
}

// UpdateJobTemplate updates the template of a job in its markdown file.
// UpdateJobProvider updates an agent CLI provider. Callers must keep this
// axis separate from the direct-API provider inferred from a chat/oneshot model.
func (sp *StatePersister) UpdateJobProvider(job *Job, newProvider string) error {
	return sp.updateJobString(job, "provider", newProvider, func() { job.Provider = newProvider })
}

// UpdateJobSkill updates a job's selected skill and clears any eagerly-derived
// sequence, preventing a stale sequence from surviving a skill replacement.
func (sp *StatePersister) UpdateJobSkill(job *Job, newSkill string) error {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	lock, err := sp.lockFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer func() { _ = lock.Unlock() }()
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("read job file: %w", err)
	}
	newContent, err := sp.updateFrontmatter(content, map[string]interface{}{"skill": newSkill, "skill_sequence": nil, "updated_at": time.Now().Format(time.RFC3339)})
	if err != nil {
		return fmt.Errorf("update frontmatter: %w", err)
	}
	if err := sp.writeAtomic(job.FilePath, newContent); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	job.Skill, job.SkillSequence = newSkill, nil
	return nil
}

func (sp *StatePersister) updateJobString(job *Job, field, value string, apply func()) error {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	lock, err := sp.lockFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer func() { _ = lock.Unlock() }()
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("read job file: %w", err)
	}
	newContent, err := sp.updateFrontmatter(content, map[string]interface{}{field: value, "updated_at": time.Now().Format(time.RFC3339)})
	if err != nil {
		return fmt.Errorf("update frontmatter: %w", err)
	}
	if err := sp.writeAtomic(job.FilePath, newContent); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	apply()
	return nil
}

func (sp *StatePersister) UpdateJobTemplate(job *Job, newTemplate string) error {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	// Create file lock
	lock, err := sp.lockFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer func() { _ = lock.Unlock() }()

	// Read current file
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("read job file: %w", err)
	}

	// Update template in frontmatter
	updates := map[string]interface{}{
		"template":   newTemplate,
		"updated_at": time.Now().Format(time.RFC3339),
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
	job.Template = newTemplate

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
	defer func() { _ = lock.Unlock() }()

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
	defer func() { _ = lock.Unlock() }()

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

// Transcript section headers. transcriptSectionHeader is the canonical
// heading written by UpdateJobTranscript; legacyTranscriptSectionHeader is
// the pre-rename heading still present in older job files (matched on read,
// rewritten to the canonical form on update).
const (
	transcriptSectionHeader       = "# Agent Chat Transcript"
	legacyTranscriptSectionHeader = "## Transcript"
)

// UpdateJobTranscript replaces (or appends) the job file's agent transcript
// section with the given transcript, under the job-file lock and via an
// atomic write. When onlyIfMissing is true an existing transcript section is
// left untouched (used for the "never run" note).
//
// This is the ONLY sanctioned way to write the transcript section: it is
// called from both flow processes and groved's jobrunner, which used to race
// each other (and StatePersister's own frontmatter writers) through an
// unlocked full-file rewrite.
func (sp *StatePersister) UpdateJobTranscript(job *Job, transcript string, onlyIfMissing bool) (bool, error) {
	return sp.updateJobSection(job, transcriptSectionHeader,
		[]string{legacyTranscriptSectionHeader}, transcript, "", onlyIfMissing)
}

// UpdateJobSection replaces (or inserts) a named markdown section in the job
// file's body, under the job-file lock and via an atomic write. The
// frontmatter is parsed and preserved; the splice operates on the body only.
//
// header is the exact section heading (e.g. "# Workflow Runs"). When the
// section is missing it is inserted immediately before insertBeforeHeader
// when that heading exists in the body, otherwise appended at EOF. When the
// section already exists it is replaced in place: the replaced region runs
// from header to the next occurrence of insertBeforeHeader after it (EOF
// when insertBeforeHeader is empty or absent). When onlyIfMissing is true an
// existing section is left untouched.
//
// It returns true when the file was modified. A byte-identical section is
// skipped without a write (so repeated completion calls are cheap and don't
// churn mtimes).
func (sp *StatePersister) UpdateJobSection(job *Job, header, content, insertBeforeHeader string, onlyIfMissing bool) (bool, error) {
	return sp.updateJobSection(job, header, nil, content, insertBeforeHeader, onlyIfMissing)
}

// updateJobSection implements UpdateJobSection/UpdateJobTranscript.
// legacyHeaders are alternative headings matched (and rewritten to header)
// when header itself is absent.
func (sp *StatePersister) updateJobSection(job *Job, header string, legacyHeaders []string, content, insertBeforeHeader string, onlyIfMissing bool) (bool, error) {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	lock, err := sp.lockFile(job.FilePath)
	if err != nil {
		return false, fmt.Errorf("acquire lock: %w", err)
	}
	defer func() { _ = lock.Unlock() }()

	fileContent, err := os.ReadFile(job.FilePath)
	if err != nil {
		return false, fmt.Errorf("read job file: %w", err)
	}

	frontmatter, body, err := sp.frontmatterParser.ParseFrontmatter(fileContent)
	if err != nil {
		return false, fmt.Errorf("parsing frontmatter: %w", err)
	}

	bodyStr := string(body)
	matchedHeader := header
	idx := strings.Index(bodyStr, header)
	for _, legacy := range legacyHeaders {
		if idx != -1 {
			break
		}
		idx = strings.Index(bodyStr, legacy)
		matchedHeader = legacy
	}

	var before, after string
	if idx != -1 {
		if onlyIfMissing {
			return false, nil
		}
		// The existing section runs to the next occurrence of
		// insertBeforeHeader after it, or EOF.
		sectionEnd := len(bodyStr)
		if insertBeforeHeader != "" {
			if rel := strings.Index(bodyStr[idx+len(matchedHeader):], insertBeforeHeader); rel != -1 {
				sectionEnd = idx + len(matchedHeader) + rel
			}
		}
		existing := strings.TrimSpace(strings.TrimPrefix(bodyStr[idx:sectionEnd], matchedHeader))
		if existing == strings.TrimSpace(content) {
			return false, nil // unchanged; skip the write
		}
		before = bodyStr[:idx]
		after = bodyStr[sectionEnd:]
	} else if insertBeforeHeader != "" {
		if insIdx := strings.Index(bodyStr, insertBeforeHeader); insIdx != -1 {
			before = bodyStr[:insIdx]
			after = bodyStr[insIdx:]
		} else {
			before = bodyStr
		}
	} else {
		before = bodyStr
	}

	before = strings.TrimRight(before, "\n")
	if before != "" {
		before += "\n\n"
	}
	var newBody string
	if after != "" {
		after = strings.TrimLeft(after, "\n")
		newBody = before + header + "\n\n" + strings.TrimRight(content, "\n") + "\n\n" + after
	} else {
		newBody = before + header + "\n\n" + content
	}

	var newContent []byte
	if bytes.HasPrefix(fileContent, []byte("---")) {
		newContent, err = RebuildMarkdownWithFrontmatter(frontmatter, []byte(newBody))
		if err != nil {
			return false, fmt.Errorf("rebuilding job content: %w", err)
		}
	} else {
		// No frontmatter in the file; don't invent one.
		newContent = []byte(newBody)
	}

	if err := sp.writeAtomic(job.FilePath, newContent); err != nil {
		return false, fmt.Errorf("write file: %w", err)
	}
	return true, nil
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
	path      string
	file      *os.File
	persister *StatePersister
}

func (sp *StatePersister) lockFile(path string) (*FileLock, error) {
	lockPath := path + ".lock"
	currentPID := os.Getpid()

	// Try to create lock file exclusively
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			// Check if lock belongs to current process (for executor-created locks)
			if !sp.heldLocks[lockPath] {
				if content, err := os.ReadFile(lockPath); err == nil {
					var pidInLock int
					if _, err := fmt.Sscanf(string(content), "%d", &pidInLock); err == nil && pidInLock == currentPID {
						// Lock file belongs to us - we can proceed
						// This happens when the executor creates the lock file first
						// Return a "no-op" lock that won't try to unlock the executor's lock
						sp.heldLocks[lockPath] = true
						return &FileLock{path: lockPath, file: nil, persister: sp}, nil
					}
				}
			}

			// Check if the lock holder process is still alive
			isStale := false
			if content, readErr := os.ReadFile(lockPath); readErr == nil {
				var pidInLock int
				if _, scanErr := fmt.Sscanf(string(content), "%d", &pidInLock); scanErr == nil {
					// PID 0 means the lock was created before the real PID was known (e.g., groveterm-native agents).
					// If the process is dead (or PID 0), the lock is stale.
					if !process.IsProcessAlive(pidInLock) {
						isStale = true
					}
				}
			}
			// Fall back to time-based staleness if we couldn't determine PID status
			if !isStale {
				if info, statErr := os.Stat(lockPath); statErr == nil {
					if time.Since(info.ModTime()) > 5*time.Minute {
						isStale = true
					}
				}
			}
			if isStale {
				os.Remove(lockPath)
				// Retry
				file, err = os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
				if err != nil {
					return nil, fmt.Errorf("file is locked")
				}
			} else {
				return nil, fmt.Errorf("file is locked")
			}
		} else {
			return nil, err
		}
	}

	// Write PID for debugging (only if we created the lock)
	if file != nil {
		fmt.Fprintf(file, "%d\n", currentPID)
		_ = file.Sync()
	}

	sp.heldLocks[lockPath] = true
	return &FileLock{path: lockPath, file: file, persister: sp}, nil
}

// Unlock releases the file lock.
func (fl *FileLock) Unlock() error {
	if fl.persister != nil {
		delete(fl.persister.heldLocks, fl.path)
	}
	if fl.file != nil {
		fl.file.Close()
		// Only remove the lock file if we created it
		return os.Remove(fl.path)
	}
	// No-op lock (created by executor) - don't remove it
	return nil
}

// Atomic file operations

func (sp *StatePersister) writeAtomic(path string, content []byte) error {
	// Get current file permissions if file exists
	var perm os.FileMode = 0o644
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
		JobStatusFailed, JobStatusBlocked, JobStatusNeedsReview,
		JobStatusPendingUser, JobStatusPendingLLM, JobStatusAbandoned,
		JobStatusHold, JobStatusTodo, JobStatusIdle:
		return true
	}
	return false
}
