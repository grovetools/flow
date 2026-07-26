package orchestration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/grovetools/core/pkg/paths"
	coresessions "github.com/grovetools/core/pkg/sessions"
)

// verifiedJobSession is a registry record whose job identity, job path, native
// session id, and transcript all agree. Flow scans every registry record rather
// than using Registry.Find: Find intentionally supports broad legacy aliases
// and returns the first match, which is unsafe when a retried job has stale
// records from an earlier dispatch.
type verifiedJobSession struct {
	Metadata     *coresessions.SessionMetadata
	MetadataPath string
}

// sameFilesystemPath prefers file identity over path spelling. This matters on
// case-insensitive filesystems, where two differently-cased paths can name the
// same job file. If either path cannot be statted, retain the historical clean
// string comparison so not-yet-created paths continue to work.
func sameFilesystemPath(a, b string) bool {
	aInfo, aErr := os.Stat(a)
	bInfo, bErr := os.Stat(b)
	if aErr == nil && bErr == nil {
		return os.SameFile(aInfo, bInfo)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func findVerifiedJobSession(job *Job) (*verifiedJobSession, error) {
	if job == nil || job.ID == "" || job.FilePath == "" {
		return nil, fmt.Errorf("session binding unverified: incomplete Flow job identity")
	}
	wantPath, err := filepath.Abs(job.FilePath)
	if err != nil {
		return nil, fmt.Errorf("session binding unverified for job %s: resolve job path: %w", job.ID, err)
	}
	wantPath = filepath.Clean(wantPath)

	base := filepath.Join(paths.StateDir(), "hooks", "sessions")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, fmt.Errorf("session binding unverified for job %s: read registry: %w", job.ID, err)
	}

	var candidates []*verifiedJobSession
	var rejected []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		metadataPath := filepath.Join(base, entry.Name(), "metadata.json")
		data, readErr := os.ReadFile(metadataPath)
		if readErr != nil {
			continue
		}
		var metadata coresessions.SessionMetadata
		if json.Unmarshal(data, &metadata) != nil || (metadata.JobID != job.ID && metadata.SessionID != job.ID) {
			continue
		}
		gotPath, absErr := filepath.Abs(metadata.JobFilePath)
		if absErr != nil || metadata.JobFilePath == "" || !sameFilesystemPath(gotPath, wantPath) {
			rejected = append(rejected, entry.Name()+": job path mismatch")
			continue
		}
		if metadata.ClaudeSessionID == "" {
			rejected = append(rejected, entry.Name()+": native session id missing")
			continue
		}
		if metadata.TranscriptPath == "" {
			rejected = append(rejected, entry.Name()+": transcript path missing")
			continue
		}
		info, statErr := os.Stat(metadata.TranscriptPath)
		if statErr != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			rejected = append(rejected, entry.Name()+": transcript missing or empty")
			continue
		}
		// A retry reuses the Flow job ID. Refuse records that predate the
		// current attempt; otherwise a successful old transcript can certify a
		// zero-turn provider failure as successful.
		if !job.StartTime.IsZero() && !metadata.StartedAt.IsZero() && metadata.StartedAt.Before(job.StartTime.Add(-2*time.Second)) {
			rejected = append(rejected, entry.Name()+": session predates current attempt")
			continue
		}
		candidates = append(candidates, &verifiedJobSession{Metadata: &metadata, MetadataPath: metadataPath})
	}

	if len(candidates) == 0 {
		detail := "no exact job/path binding with a non-empty transcript"
		if len(rejected) > 0 {
			detail += " (rejected " + strings.Join(rejected, ", ") + ")"
		}
		return nil, fmt.Errorf("session binding unverified for job %s: %s", job.ID, detail)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Metadata.StartedAt.After(candidates[j].Metadata.StartedAt)
	})
	return candidates[0], nil
}

func isAgentJobType(t JobType) bool {
	return t == JobTypeInteractiveAgent || t == JobTypeHeadlessAgent || t == JobTypeIsolatedAgent
}

// successfulExecutionEvidence validates the evidence used to transition an
// agent job to completed. Frontmatter status is authoritative; sidecars and
// transcripts are evidence feeding that transition, never competing status
// authorities.
func successfulExecutionEvidence(job *Job, plan *Plan) error {
	if !isAgentJobType(job.Type) {
		return nil
	}
	if job.Type == JobTypeHeadlessAgent {
		status, err := readHeadlessStatus(headlessStatusPath(plan, job))
		if err != nil {
			return fmt.Errorf("no headless execution status: %w", err)
		}
		if status.JobID != job.ID {
			return fmt.Errorf("headless execution status belongs to job %q", status.JobID)
		}
		stamp, err := time.Parse(time.RFC3339, status.Timestamp)
		if err != nil {
			return fmt.Errorf("invalid headless execution timestamp: %w", err)
		}
		if !job.StartTime.IsZero() && stamp.Before(job.StartTime.Add(-2*time.Second)) {
			return fmt.Errorf("headless execution status predates current attempt")
		}
		if status.ExitCode != 0 {
			return fmt.Errorf("headless provider exited with code %d", status.ExitCode)
		}
	}
	// A zero exit from a provider launcher is not a completed turn. All agent
	// families must also have an exact current-attempt binding to a non-empty
	// transcript before frontmatter may transition to completed.
	_, err := findVerifiedJobSession(job)
	return err
}

// JobHasExecutionEvidence reports whether a completed job has durable evidence
// of its current attempt. It is deliberately conservative: retry is enabled
// only when no evidence exists, never merely because one optional artifact is
// absent.
func JobHasExecutionEvidence(job *Job, plan *Plan) bool {
	if job == nil || plan == nil {
		return false
	}
	if isAgentJobType(job.Type) {
		artifactDir := filepath.Join(plan.Directory, ".artifacts", job.ID)
		if info, err := os.Stat(filepath.Join(artifactDir, "final-report.json")); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return true
		}
		if archivedSessionMatchesAttempt(job, artifactDir) {
			return true
		}
		return successfulExecutionEvidence(job, plan) == nil
	}
	artifactDir := filepath.Join(plan.Directory, ".artifacts", job.ID)
	patterns := []string{"request-manifest-*.json", "metrics.json", "commands.jsonl", "job.log"}
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(filepath.Join(artifactDir, pattern))
		for _, match := range matches {
			if info, err := os.Stat(match); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
				return true
			}
		}
	}
	return false
}

func archivedSessionMatchesAttempt(job *Job, artifactDir string) bool {
	transcriptInfo, err := os.Stat(filepath.Join(artifactDir, "transcript.jsonl"))
	if err != nil || !transcriptInfo.Mode().IsRegular() || transcriptInfo.Size() == 0 {
		return false
	}
	data, err := os.ReadFile(filepath.Join(artifactDir, "metadata.json"))
	if err != nil {
		return false
	}
	var metadata coresessions.SessionMetadata
	if json.Unmarshal(data, &metadata) != nil || (metadata.JobID != job.ID && metadata.SessionID != job.ID) {
		return false
	}
	if !job.StartTime.IsZero() && !metadata.StartedAt.IsZero() && metadata.StartedAt.Before(job.StartTime.Add(-2*time.Second)) {
		return false
	}
	return true
}
