package orchestration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeSessionBindingFixture(t *testing.T, root, dir string, job *Job, started time.Time, transcript string) string {
	t.Helper()
	sessionDir := filepath.Join(root, "state", "grove", "hooks", "sessions", dir)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(sessionDir, "transcript.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	metadata := fmt.Sprintf(`{"session_id":%q,"job_id":%q,"job_file_path":%q,"claude_session_id":%q,"provider":"pi","started_at":%q,"transcript_path":%q}`,
		job.ID, job.ID, job.FilePath, dir, started.Format(time.RFC3339Nano), transcriptPath)
	if err := os.WriteFile(filepath.Join(sessionDir, "metadata.json"), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
	return transcriptPath
}

func TestFindVerifiedJobSessionRejectsStaleRetryBinding(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROVE_HOME", root)
	jobPath := filepath.Join(root, "job.md")
	job := &Job{ID: "job-1", FilePath: jobPath, StartTime: time.Now()}
	writeSessionBindingFixture(t, root, "old-native", job, job.StartTime.Add(-time.Hour), "old transcript\n")
	newPath := writeSessionBindingFixture(t, root, "new-native", job, job.StartTime.Add(time.Second), "new transcript\n")

	binding, err := findVerifiedJobSession(job)
	if err != nil {
		t.Fatalf("findVerifiedJobSession: %v", err)
	}
	if binding.Metadata.TranscriptPath != newPath {
		t.Fatalf("selected %q, want current attempt %q", binding.Metadata.TranscriptPath, newPath)
	}
}

func TestFindVerifiedJobSessionFailsLoudlyForOnlyStaleBinding(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROVE_HOME", root)
	job := &Job{ID: "job-1", FilePath: filepath.Join(root, "job.md"), StartTime: time.Now()}
	writeSessionBindingFixture(t, root, "old-native", job, job.StartTime.Add(-time.Hour), "old transcript\n")

	_, err := findVerifiedJobSession(job)
	if err == nil || !strings.Contains(err.Error(), "predates current attempt") {
		t.Fatalf("expected explicit stale-binding error, got %v", err)
	}
}
