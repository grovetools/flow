package orchestration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// artifactJob builds a plan + interactive-agent job on disk with an empty
// session registry, i.e. exactly the state left behind when a liveness sweep
// reaped the job's registry record.
func artifactJob(t *testing.T, startTime time.Time) (*Job, *Plan) {
	t.Helper()
	t.Setenv("GROVE_HOME", t.TempDir())

	planDir := t.TempDir()
	jobPath := filepath.Join(planDir, "34-git-status-mitigations.md")
	content := "---\nid: git-status-mitigations-4cf449ea\ntitle: git-status-mitigations\ntype: interactive_agent\nstatus: running\n---\n\nbody\n"
	if err := os.WriteFile(jobPath, []byte(content), 0o600); err != nil {
		t.Fatalf("writing job file: %v", err)
	}

	job := &Job{
		ID:        "git-status-mitigations-4cf449ea",
		Title:     "git-status-mitigations",
		Type:      JobTypeInteractiveAgent,
		Status:    JobStatusRunning,
		Filename:  "34-git-status-mitigations.md",
		FilePath:  jobPath,
		StartTime: startTime,
		Provider:  "pi",
	}
	return job, &Plan{Directory: planDir, Name: "perf-audit"}
}

func writeArtifactTranscript(t *testing.T, plan *Plan, jobID, name string, modTime time.Time) string {
	t.Helper()
	dir := filepath.Join(plan.Directory, ".artifacts", jobID, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("{\"type\":\"session\"}\n"), 0o600); err != nil {
		t.Fatalf("writing transcript: %v", err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("chtimes transcript: %v", err)
	}
	return path
}

// The completion gate used to accept only a registry record, so a job whose
// record had been reaped had no path to completed at all — even with its own
// 1.2 MB transcript sitting in its artifact directory. That transcript is the
// agent's own output under a path keyed by the job ID, and unlike the registry
// record it cannot be deleted by a liveness misjudgement.
func TestSuccessfulExecutionEvidenceAcceptsArtifactTranscript(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	job, plan := artifactJob(t, start)
	writeArtifactTranscript(t, plan, job.ID, "2026-07-27T18-17-24-234Z_019fa4cb.jsonl", start.Add(time.Minute))

	if err := successfulExecutionEvidence(job, plan); err != nil {
		t.Fatalf("artifact transcript must satisfy the completion gate: %v", err)
	}
	if !JobHasExecutionEvidence(job, plan) {
		t.Fatal("JobHasExecutionEvidence must agree with the completion gate")
	}
}

func TestSuccessfulExecutionEvidenceRejectsWithoutAnyTranscript(t *testing.T) {
	job, plan := artifactJob(t, time.Now().Add(-time.Hour))

	err := successfulExecutionEvidence(job, plan)
	if err == nil {
		t.Fatal("a job with no registry record and no transcript has no evidence")
	}
	if !strings.Contains(err.Error(), job.ID) {
		t.Fatalf("the rejection must name the job, got %q", err)
	}
}

// A retry reuses the job ID and therefore the artifact directory. The previous
// attempt's transcript must not certify this one.
func TestArtifactTranscriptIgnoresPreviousAttempt(t *testing.T) {
	start := time.Now()
	job, plan := artifactJob(t, start)
	writeArtifactTranscript(t, plan, job.ID, "2026-07-27T10-00-00-000Z_old.jsonl", start.Add(-2*time.Hour))

	if got := ArtifactTranscriptForAttempt(job, plan); got != "" {
		t.Fatalf("a transcript predating the attempt is not evidence for it, got %q", got)
	}
	if err := successfulExecutionEvidence(job, plan); err == nil {
		t.Fatal("stale transcript must not satisfy the completion gate")
	}
}

// Rejection means "cannot verify", not "the work failed". Marking the job
// failed here made merely attempting to complete destructive: the attempt
// itself stamped status: failed and completed_at on a job whose agent had run
// to the end.
func TestRejectedCompletionLeavesJobStatusUnchanged(t *testing.T) {
	job, plan := artifactJob(t, time.Now().Add(-time.Hour))

	before, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatalf("reading job file: %v", err)
	}

	completeErr := CompleteJob(job, plan, true)
	if completeErr == nil {
		t.Fatal("completion without evidence must be rejected")
	}

	after, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatalf("re-reading job file: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("a rejected completion must not write the job file:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if job.Status != JobStatusRunning {
		t.Fatalf("in-memory job status must be untouched, got %q", job.Status)
	}

	// The error has to be actionable: which index was consulted, whether the
	// transcript exists, and how to look at it.
	msg := completeErr.Error()
	for _, want := range []string{"status left unchanged", "session registry", "aglogs read perf-audit/34-git-status-mitigations.md"} {
		if !strings.Contains(msg, want) {
			t.Errorf("rejection message missing %q:\n%s", want, msg)
		}
	}
}

func TestResolveJobTranscriptFallsBackToArtifact(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	job, plan := artifactJob(t, start)
	path := writeArtifactTranscript(t, plan, job.ID, "2026-07-27T18-17-24-234Z_019fa4cb-c00a-7b83-9fd8-38a7fc866bfc.jsonl", start.Add(time.Minute))

	source, err := resolveJobTranscript(job, plan)
	if err != nil {
		t.Fatalf("resolveJobTranscript: %v", err)
	}
	if source.MetadataPath != "" {
		t.Fatal("there is no registry record; the binding must be the reconstructed one")
	}
	if source.Spec != path || source.TranscriptPath != path {
		t.Fatalf("the transcript path is the aglogs spec, got spec=%q path=%q", source.Spec, source.TranscriptPath)
	}
	if source.Metadata.ClaudeSessionID != "019fa4cb-c00a-7b83-9fd8-38a7fc866bfc" {
		t.Fatalf("native session id must be recovered from the transcript filename, got %q", source.Metadata.ClaudeSessionID)
	}
	if source.Metadata.JobFilePath != job.FilePath || source.Metadata.PlanName != plan.Name {
		t.Fatalf("reconstructed metadata must carry the job's own identity: %+v", source.Metadata)
	}
}

func TestTranscriptStartTimeFallsBackToFilename(t *testing.T) {
	job := &Job{}
	got := transcriptStartTime(job, "/p/.artifacts/j/sessions/2026-07-27T18-17-24-234Z_abc.jsonl")
	want := time.Date(2026, 7, 27, 18, 17, 24, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("start time from filename = %v, want %v", got, want)
	}
}
