package orchestration

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testRecordingsFixture(t *testing.T) (*Plan, *Job) {
	t.Helper()
	tmp := t.TempDir()
	planDir := filepath.Join(tmp, "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plan := &Plan{Directory: planDir}
	job := &Job{
		ID:        "test-job-abc12345",
		FilePath:  filepath.Join(planDir, "01-test-job.md"),
		StartTime: time.Now(),
	}
	return plan, job
}

func writeTestCast(t *testing.T, path, header string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := header + "\n[0.1, \"o\", \"hello\"]\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAddJobRecordingRelativeInsideArtifacts(t *testing.T) {
	plan, job := testRecordingsFixture(t)
	cast := filepath.Join(plan.Directory, ".artifacts", job.ID, "recordings", "run.cast")
	writeTestCast(t, cast, `{"version": 3, "term": {"cols": 200, "rows": 50}, "title": "pilot run"}`)

	entry, err := AddJobRecording(plan, job, cast, "", "", "")
	if err != nil {
		t.Fatalf("AddJobRecording: %v", err)
	}
	if entry.Path != filepath.Join("recordings", "run.cast") {
		t.Errorf("Path = %q, want artifacts-relative recordings/run.cast", entry.Path)
	}
	if entry.Format != "asciicast/v3" {
		t.Errorf("Format = %q, want asciicast/v3", entry.Format)
	}
	if entry.Name != "run" {
		t.Errorf("Name = %q, want run (filename default)", entry.Name)
	}
	if entry.Title != "pilot run" {
		t.Errorf("Title = %q, want header title", entry.Title)
	}
	if entry.Bytes <= 0 {
		t.Errorf("Bytes = %d, want > 0", entry.Bytes)
	}

	rec, err := ReadJobRecordings(plan, job)
	if err != nil {
		t.Fatalf("ReadJobRecordings: %v", err)
	}
	if rec.Schema != jobRecordingsSchemaVersion || rec.JobID != job.ID || rec.JobFile != "01-test-job.md" {
		t.Errorf("record header = %+v", rec)
	}
	if len(rec.Recordings) != 1 {
		t.Fatalf("len(Recordings) = %d, want 1", len(rec.Recordings))
	}
	if got := ResolveJobRecordingPath(plan, job, rec.Recordings[0]); got != cast {
		t.Errorf("ResolveJobRecordingPath = %q, want %q", got, cast)
	}
}

func TestAddJobRecordingOutsideArtifactsIsAbsoluteAndUpdatesInPlace(t *testing.T) {
	plan, job := testRecordingsFixture(t)
	cast := filepath.Join(t.TempDir(), "elsewhere.cast")
	writeTestCast(t, cast, `{"version": 2, "width": 80, "height": 24}`)

	first, err := AddJobRecording(plan, job, cast, "n1", "t1", "")
	if err != nil {
		t.Fatalf("AddJobRecording: %v", err)
	}
	if !filepath.IsAbs(first.Path) {
		t.Errorf("Path = %q, want absolute for a cast outside the artifacts dir", first.Path)
	}
	if first.Format != "asciicast/v2" {
		t.Errorf("Format = %q, want asciicast/v2", first.Format)
	}

	// Re-linking the same path replaces the entry rather than appending.
	if _, err := AddJobRecording(plan, job, cast, "n2", "t2", "per-finding F1"); err != nil {
		t.Fatalf("AddJobRecording (relink): %v", err)
	}
	rec, err := ReadJobRecordings(plan, job)
	if err != nil {
		t.Fatalf("ReadJobRecordings: %v", err)
	}
	if len(rec.Recordings) != 1 {
		t.Fatalf("len(Recordings) = %d after relink, want 1", len(rec.Recordings))
	}
	if r := rec.Recordings[0]; r.Name != "n2" || r.Title != "t2" || r.Note != "per-finding F1" {
		t.Errorf("relinked entry = %+v, want updated name/title/note", r)
	}
}

func TestAddJobRecordingRejectsNonAsciicast(t *testing.T) {
	plan, job := testRecordingsFixture(t)

	missing := filepath.Join(plan.Directory, "nope.cast")
	if _, err := AddJobRecording(plan, job, missing, "", "", ""); err == nil {
		t.Error("expected error for missing file")
	}

	notCast := filepath.Join(t.TempDir(), "not.cast")
	if err := os.WriteFile(notCast, []byte("plain text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := AddJobRecording(plan, job, notCast, "", "", ""); err == nil {
		t.Error("expected error for a non-asciicast file")
	}

	badVersion := filepath.Join(t.TempDir(), "bad.cast")
	writeTestCast(t, badVersion, `{"version": 9}`)
	if _, err := AddJobRecording(plan, job, badVersion, "", "", ""); err == nil {
		t.Error("expected error for unsupported asciicast version")
	}
}
