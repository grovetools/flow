package orchestration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Job recording sidecar: links session recordings (asciicast .cast files, e.g.
// from `grove record`) to the job that produced them, in the job's per-job
// artifacts dir (.artifacts/<job-id>/recording.json — the same dir naming as
// commits.json and the .status file). Unlike commits.json, flow never writes
// this sidecar on its own: a QA/pilot run opts in by recording its session and
// linking the cast via `flow plan recordings add` (or AddJobRecording). The
// cast is a movie for a human reviewer — additive evidence, never a substitute
// for a job's machine-readable artifacts. Bump jobRecordingsSchemaVersion on
// any breaking change.

const (
	jobRecordingsSchemaVersion = 1
	jobRecordingsFileName      = "recording.json"
)

// JobRecordingsRecord is the recording.json sidecar: an append-mostly list so
// a run can attach one per-run cast (the common case) or several per-finding
// casts.
type JobRecordingsRecord struct {
	Schema     int            `json:"schema"`
	JobID      string         `json:"job_id"`
	JobFile    string         `json:"job_file"`
	Recordings []JobRecording `json:"recordings"`
}

// JobRecording is one linked cast. Path is relative to the job's artifacts dir
// when the file lives under it (the recommended layout: recordings/<name>.cast),
// absolute otherwise.
type JobRecording struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Format    string `json:"format"` // e.g. "asciicast/v3", sniffed from the header
	Title     string `json:"title,omitempty"`
	Bytes     int64  `json:"bytes"`
	CreatedAt string `json:"created_at"` // cast file mtime at link time
	LinkedAt  string `json:"linked_at"`
	Note      string `json:"note,omitempty"` // freeform, e.g. a finding id
}

// JobRecordingsPath returns the sidecar path for a job, in the same
// .artifacts/<job-id>/ dir as commits.json.
func JobRecordingsPath(plan *Plan, job *Job) string {
	return filepath.Join(plan.Directory, ".artifacts", job.ID, jobRecordingsFileName)
}

// ReadJobRecordings reads and parses a job's recording.json sidecar.
func ReadJobRecordings(plan *Plan, job *Job) (*JobRecordingsRecord, error) {
	data, err := os.ReadFile(JobRecordingsPath(plan, job))
	if err != nil {
		return nil, err
	}
	var rec JobRecordingsRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", JobRecordingsPath(plan, job), err)
	}
	return &rec, nil
}

func writeJobRecordings(plan *Plan, job *Job, rec *JobRecordingsRecord) error {
	path := JobRecordingsPath(plan, job)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating artifacts dir: %w", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// ResolveJobRecordingPath returns the absolute path of a linked cast,
// resolving artifacts-dir-relative entries against the job's artifacts dir.
func ResolveJobRecordingPath(plan *Plan, job *Job, r JobRecording) string {
	if filepath.IsAbs(r.Path) {
		return r.Path
	}
	return filepath.Join(plan.Directory, ".artifacts", job.ID, r.Path)
}

// AddJobRecording links an existing cast file to the job: it validates and
// sniffs the file, then appends an entry to the sidecar (creating it if
// needed). Re-linking a path that is already present updates that entry in
// place instead of duplicating it. Returns the stored entry.
func AddJobRecording(plan *Plan, job *Job, castPath, name, title, note string) (*JobRecording, error) {
	abs, err := filepath.Abs(castPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("cast file: %w", err)
	}
	format, headerTitle, err := sniffAsciicast(abs)
	if err != nil {
		return nil, err
	}
	if title == "" {
		title = headerTitle
	}

	stored := abs
	artifactsDir := filepath.Join(plan.Directory, ".artifacts", job.ID)
	if absArtifacts, aerr := filepath.Abs(artifactsDir); aerr == nil {
		if rel, rerr := filepath.Rel(absArtifacts, abs); rerr == nil && !strings.HasPrefix(rel, "..") {
			stored = rel
		}
	}
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(abs), ".cast")
	}

	rec, readErr := ReadJobRecordings(plan, job)
	if readErr != nil {
		if !os.IsNotExist(readErr) {
			return nil, readErr
		}
		rec = &JobRecordingsRecord{
			Schema:  jobRecordingsSchemaVersion,
			JobID:   job.ID,
			JobFile: filepath.Base(job.FilePath),
		}
	}

	entry := JobRecording{
		Name:      name,
		Path:      stored,
		Format:    format,
		Title:     title,
		Bytes:     info.Size(),
		CreatedAt: info.ModTime().Format(time.RFC3339),
		LinkedAt:  time.Now().Format(time.RFC3339),
		Note:      note,
	}
	replaced := false
	for i := range rec.Recordings {
		if rec.Recordings[i].Path == stored {
			rec.Recordings[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		rec.Recordings = append(rec.Recordings, entry)
	}
	if err := writeJobRecordings(plan, job, rec); err != nil {
		return nil, err
	}
	return &entry, nil
}

// sniffAsciicast validates that path starts with an asciicast v2/v3 header
// line and returns the format string plus the header's title, if any.
func sniffAsciicast(path string) (format, title string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !scanner.Scan() {
		return "", "", fmt.Errorf("%s: empty file, not an asciicast", path)
	}
	var header struct {
		Version int    `json:"version"`
		Title   string `json:"title"`
	}
	if jerr := json.Unmarshal(scanner.Bytes(), &header); jerr != nil {
		return "", "", fmt.Errorf("%s: first line is not an asciicast header: %w", path, jerr)
	}
	if header.Version != 2 && header.Version != 3 {
		return "", "", fmt.Errorf("%s: unsupported asciicast version %d (want 2 or 3)", path, header.Version)
	}
	return fmt.Sprintf("asciicast/v%d", header.Version), header.Title, nil
}
