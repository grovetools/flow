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
//
// It accepts exactly what JobHasExecutionEvidence accepts — a verified registry
// session, a final report, an archived session, or a live transcript in the
// job's own artifact directory. Two functions with "evidence" in the name must
// not disagree about what evidence is. Narrowing this one to the registry
// record alone left a job whose registry entry had been reaped with no path to
// completed at all, even while its own 1.2 MB transcript sat next to it on
// disk: the archived copy the gate would have accepted is written by
// ArchiveInteractiveSession, which runs after the gate.
//
// The live artifact transcript is the strongest of these. It is the agent's own
// output, written by the agent, under a path keyed by the job's own ID — and
// unlike the registry record it cannot be deleted by a liveness misjudgement.
func successfulExecutionEvidence(job *Job, plan *Plan) error {
	if !isAgentJobType(job.Type) {
		return nil
	}
	if job.Type == JobTypeHeadlessAgent {
		// A recorded non-zero exit is positive evidence of failure and always
		// rejects. A missing or unreadable status file is merely absent
		// evidence, so it falls through to the sources below rather than
		// deciding the outcome on its own.
		if status, err := readHeadlessStatus(headlessStatusPath(plan, job)); err == nil &&
			status.JobID == job.ID && status.ExitCode != 0 {
			if stamp, terr := time.Parse(time.RFC3339, status.Timestamp); terr == nil {
				if job.StartTime.IsZero() || !stamp.Before(job.StartTime.Add(-2*time.Second)) {
					return fmt.Errorf("headless provider exited with code %d", status.ExitCode)
				}
			}
		}
	}

	// A zero exit from a provider launcher is not a completed turn: every agent
	// family needs a current-attempt binding to real output.
	_, registryErr := findVerifiedJobSession(job)
	if registryErr == nil {
		return nil
	}
	if plan != nil {
		artifactDir := filepath.Join(plan.Directory, ".artifacts", job.ID)
		if info, err := os.Stat(filepath.Join(artifactDir, "final-report.json")); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return nil
		}
		if archivedSessionMatchesAttempt(job, artifactDir) {
			return nil
		}
		if ArtifactTranscriptForAttempt(job, plan) != "" {
			return nil
		}
	}
	return registryErr
}

// jobTranscriptSource is the transcript flow will read for a job, together
// with the session metadata describing it. Metadata is either the registry
// record (MetadataPath set) or reconstructed from the job and its artifact
// transcript (MetadataPath empty) — every reconstructed field is copied from
// something that exists on disk, never invented.
type jobTranscriptSource struct {
	// Spec is what `aglogs read` is given: the agent's native session id when
	// the registry knows it, otherwise the absolute transcript path.
	Spec           string
	TranscriptPath string
	Metadata       *coresessions.SessionMetadata
	MetadataPath   string
}

// resolveJobTranscript finds the transcript for a job, preferring the verified
// registry binding and falling back to the transcript in the job's own artifact
// directory.
//
// The fallback matters because the registry record is deletable — a liveness
// sweep that misjudged a PID could remove it while the agent was still writing
// — whereas .artifacts/<job-id>/sessions/ is written by the agent under a path
// keyed by the job's own ID. Without it a job whose record was reaped could
// neither archive nor append the 1.2 MB transcript sitting beside it.
//
// A plan/job-name aglogs spec is still never used as a fallback: it resolves to
// any session that ever matched that name and can splice another agent's
// transcript into this job (see issue: wrong-session-logs). Path identity is
// safe in a way name identity is not.
func resolveJobTranscript(job *Job, plan *Plan) (*jobTranscriptSource, error) {
	binding, bindErr := findVerifiedJobSession(job)
	if bindErr == nil && binding.Metadata.ClaudeSessionID != "" {
		return &jobTranscriptSource{
			Spec:           binding.Metadata.ClaudeSessionID,
			TranscriptPath: binding.Metadata.TranscriptPath,
			Metadata:       binding.Metadata,
			MetadataPath:   binding.MetadataPath,
		}, nil
	}

	transcript := ArtifactTranscriptForAttempt(job, plan)
	if transcript == "" {
		return nil, bindErr
	}

	jobFilePath := job.FilePath
	if abs, err := filepath.Abs(jobFilePath); err == nil {
		jobFilePath = abs
	}
	provider := job.Provider
	if provider == "" {
		// Artifact-owned session files are written by the Pi runtime; that is
		// the only writer of .artifacts/<job-id>/sessions/*.jsonl.
		provider = "pi"
	}
	return &jobTranscriptSource{
		Spec:           transcript,
		TranscriptPath: transcript,
		Metadata: &coresessions.SessionMetadata{
			SessionID:       job.ID,
			JobID:           job.ID,
			ClaudeSessionID: nativeSessionIDFromTranscript(transcript),
			Provider:        provider,
			Status:          string(job.Status),
			StartedAt:       transcriptStartTime(job, transcript),
			TranscriptPath:  transcript,
			Type:            string(job.Type),
			JobTitle:        job.Title,
			PlanName:        plan.Name,
			JobFilePath:     jobFilePath,
		},
	}, nil
}

// transcriptStartTime dates a reconstructed session record. The job's own start
// time wins; failing that the transcript filename carries the moment the agent
// opened it ("2026-07-27T18-17-24-234Z_<id>.jsonl"), which is closer to the
// truth than a modification time that only says when the agent last wrote.
func transcriptStartTime(job *Job, transcript string) time.Time {
	if job != nil && !job.StartTime.IsZero() {
		return job.StartTime
	}
	// The filename is "<date>T<hh-mm-ss>-<millis>Z_<session id>.jsonl"; Go's
	// layouts cannot express a dash-separated fractional second, so the
	// seconds-precision prefix is parsed and the millis dropped.
	name := filepath.Base(transcript)
	const stampLayout = "2006-01-02T15-04-05"
	if len(name) >= len(stampLayout) {
		if stamp, err := time.Parse(stampLayout, name[:len(stampLayout)]); err == nil {
			return stamp.UTC()
		}
	}
	if info, err := os.Stat(transcript); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

// nativeSessionIDFromTranscript recovers the agent's native session id from an
// artifact transcript filename, which the Pi runtime names
// "<timestamp>_<native-session-id>.jsonl". Returns "" for any other shape
// rather than guessing.
func nativeSessionIDFromTranscript(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	idx := strings.LastIndex(name, "_")
	if idx < 0 || idx == len(name)-1 {
		return ""
	}
	return name[idx+1:]
}

// ArtifactTranscriptForAttempt returns the path of a non-empty transcript the
// agent wrote into this job's own artifact directory for the current attempt,
// or "" when there is none.
//
// Identity comes from the path: .artifacts/<job-id>/sessions/ is owned by that
// job and nothing else writes there. A retry reuses the job ID and so reuses
// the directory, which is why a transcript last written before this attempt
// started does not count — otherwise a previous run's output would certify a
// zero-turn failure as a success.
func ArtifactTranscriptForAttempt(job *Job, plan *Plan) string {
	if job == nil || plan == nil || job.ID == "" || plan.Directory == "" {
		return ""
	}
	sessionsDir := filepath.Join(plan.Directory, ".artifacts", job.ID, "sessions")
	matches, err := filepath.Glob(filepath.Join(sessionsDir, "*.jsonl"))
	if err != nil {
		return ""
	}
	newest := ""
	var newestMod time.Time
	for _, path := range matches {
		info, statErr := os.Stat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			continue
		}
		if !job.StartTime.IsZero() && info.ModTime().Before(job.StartTime.Add(-2*time.Second)) {
			continue
		}
		if newest == "" || info.ModTime().After(newestMod) {
			newest, newestMod = path, info.ModTime()
		}
	}
	return newest
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
