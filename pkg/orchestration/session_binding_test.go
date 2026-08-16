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
	metadata := fmt.Sprintf(`{"session_id":%q,"attempt_id":%q,"job_id":%q,"job_file_path":%q,"claude_session_id":%q,"provider":"pi","status":"running","started_at":%q,"transcript_path":%q}`,
		job.ID, job.AttemptID, job.ID, job.FilePath, dir, started.Format(time.RFC3339Nano), transcriptPath)
	if err := os.WriteFile(filepath.Join(sessionDir, "metadata.json"), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
	return transcriptPath
}

func TestFindVerifiedJobSessionAcceptsSameFileWithDifferentPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROVE_HOME", root)
	jobPath := filepath.Join(root, "job.md")
	aliasPath := filepath.Join(root, "job-alias.md")
	if err := os.WriteFile(jobPath, []byte("job\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(jobPath, aliasPath); err != nil {
		t.Fatal(err)
	}

	job := &Job{ID: "job-1", FilePath: jobPath, StartTime: time.Now()}
	registryJob := *job
	registryJob.FilePath = aliasPath
	writeSessionBindingFixture(t, root, "native", &registryJob, job.StartTime, "transcript\n")

	if _, err := findVerifiedJobSession(job); err != nil {
		t.Fatalf("findVerifiedJobSession rejected paths for the same file: %v", err)
	}
}

func TestFindVerifiedJobSessionAcceptsDifferentCaseOnCaseInsensitiveFilesystem(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROVE_HOME", root)
	jobPath := filepath.Join(root, "MixedCaseJob.md")
	if err := os.WriteFile(jobPath, []byte("job\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	alternatePath := filepath.Join(root, "mixedcasejob.md")
	if _, err := os.Stat(alternatePath); err != nil {
		t.Skip("filesystem is case-sensitive")
	}

	job := &Job{ID: "job-1", FilePath: jobPath, StartTime: time.Now()}
	registryJob := *job
	registryJob.FilePath = alternatePath
	writeSessionBindingFixture(t, root, "native", &registryJob, job.StartTime, "transcript\n")

	if _, err := findVerifiedJobSession(job); err != nil {
		t.Fatalf("findVerifiedJobSession rejected differently-cased path for the same file: %v", err)
	}
}

func TestFindVerifiedJobSessionRejectsDifferentJobFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROVE_HOME", root)
	jobPath := filepath.Join(root, "job.md")
	otherPath := filepath.Join(root, "other-job.md")
	for _, path := range []string{jobPath, otherPath} {
		if err := os.WriteFile(path, []byte(path+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	job := &Job{ID: "job-1", FilePath: jobPath, StartTime: time.Now()}
	registryJob := *job
	registryJob.FilePath = otherPath
	writeSessionBindingFixture(t, root, "native", &registryJob, job.StartTime, "transcript\n")

	_, err := findVerifiedJobSession(job)
	if err == nil || !strings.Contains(err.Error(), "job path mismatch") {
		t.Fatalf("expected job path mismatch, got %v", err)
	}
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

func TestFindVerifiedJobSessionRejectsNonUUIDAttemptBeforePathLookup(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROVE_HOME", root)
	jobPath := filepath.Join(root, "job.md")
	if err := os.WriteFile(jobPath, []byte("job\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	job := &Job{ID: "job-1", AttemptID: "../escape", FilePath: jobPath}
	_, err := findVerifiedJobSession(job)
	if err == nil || !strings.Contains(err.Error(), "invalid UUIDv7 attempt id") {
		t.Fatalf("expected confined UUIDv7 rejection, got %v", err)
	}
}

func TestFindVerifiedJobSessionPointLookupDoesNotFallBackToPriorAttempt(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROVE_HOME", root)
	jobPath := filepath.Join(root, "job.md")
	if err := os.WriteFile(jobPath, []byte("job\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	job := &Job{ID: "job-1", AttemptID: "01890f5d-e4b8-7cc3-98c4-dc0c0c07398f", FilePath: jobPath, StartTime: time.Now()}
	prior := *job
	prior.AttemptID = "01890f5d-e4b8-7cc4-98c4-dc0c0c07398f"
	writeSessionBindingFixture(t, root, prior.AttemptID, &prior, job.StartTime, "old transcript\n")

	_, err := findVerifiedJobSession(job)
	if err == nil || !strings.Contains(err.Error(), "point lookup") {
		t.Fatalf("point miss broad-fell back or returned wrong error: %v", err)
	}
}

func TestFindVerifiedJobSessionRejectsMismatchedAttemptAtPointPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROVE_HOME", root)
	jobPath := filepath.Join(root, "job.md")
	if err := os.WriteFile(jobPath, []byte("job\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	job := &Job{ID: "job-1", AttemptID: "01890f5d-e4b8-7cc3-98c4-dc0c0c07398f", FilePath: jobPath, StartTime: time.Now()}
	wrong := *job
	wrong.AttemptID = "01890f5d-e4b8-7cc4-98c4-dc0c0c07398f"
	writeSessionBindingFixture(t, root, job.AttemptID, &wrong, job.StartTime, "wrong transcript\n")

	_, err := findVerifiedJobSession(job)
	if err == nil || !strings.Contains(err.Error(), "attempt id mismatch") {
		t.Fatalf("expected exact attempt rejection, got %v", err)
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
