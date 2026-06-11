package orchestration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTranscriptTestJob creates a job .md file and returns a Job pointing
// at it.
func writeTranscriptTestJob(t *testing.T, content string) *Job {
	t.Helper()
	path := filepath.Join(t.TempDir(), "01-test-job.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Job{ID: "test-job", FilePath: path, Filename: filepath.Base(path)}
}

const transcriptTestJobContent = `---
id: test-job
status: running
title: test job
---

The job prompt body.
`

func TestUpdateJobTranscript_AppendsSection(t *testing.T) {
	job := writeTranscriptTestJob(t, transcriptTestJobContent)

	changed, err := NewStatePersister().UpdateJobTranscript(job, "hello transcript\n", false)
	if err != nil {
		t.Fatalf("UpdateJobTranscript() error = %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true on first write")
	}

	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(content)
	for _, want := range []string{
		"id: test-job",
		"status: running",
		"The job prompt body.",
		"# Agent Chat Transcript\n\nhello transcript",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("job file missing %q\n---\n%s", want, got)
		}
	}
}

func TestUpdateJobTranscript_ReplacesExistingAndSkipsUnchanged(t *testing.T) {
	job := writeTranscriptTestJob(t, transcriptTestJobContent+"\n# Agent Chat Transcript\n\nold transcript\n")

	sp := NewStatePersister()
	changed, err := sp.UpdateJobTranscript(job, "new transcript\n", false)
	if err != nil {
		t.Fatalf("UpdateJobTranscript() error = %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true when transcript differs")
	}

	content, _ := os.ReadFile(job.FilePath)
	got := string(content)
	if strings.Contains(got, "old transcript") {
		t.Errorf("old transcript not replaced:\n%s", got)
	}
	if !strings.Contains(got, "# Agent Chat Transcript\n\nnew transcript") {
		t.Errorf("new transcript missing:\n%s", got)
	}
	if !strings.Contains(got, "The job prompt body.") {
		t.Errorf("body before the section was lost:\n%s", got)
	}
	if n := strings.Count(got, "# Agent Chat Transcript"); n != 1 {
		t.Errorf("expected exactly one transcript section, got %d", n)
	}

	// Re-writing the identical transcript must be a no-op.
	before, _ := os.ReadFile(job.FilePath)
	changed, err = NewStatePersister().UpdateJobTranscript(job, "new transcript\n", false)
	if err != nil {
		t.Fatalf("UpdateJobTranscript() second call error = %v", err)
	}
	if changed {
		t.Error("expected changed=false for identical transcript")
	}
	after, _ := os.ReadFile(job.FilePath)
	if string(before) != string(after) {
		t.Error("file modified despite unchanged transcript")
	}
}

func TestUpdateJobTranscript_RewritesLegacyHeader(t *testing.T) {
	job := writeTranscriptTestJob(t, transcriptTestJobContent+"\n## Transcript\n\nold legacy transcript\n")

	changed, err := NewStatePersister().UpdateJobTranscript(job, "fresh transcript\n", false)
	if err != nil {
		t.Fatalf("UpdateJobTranscript() error = %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}

	content, _ := os.ReadFile(job.FilePath)
	got := string(content)
	if strings.Contains(got, "## Transcript") {
		t.Errorf("legacy header not rewritten:\n%s", got)
	}
	if !strings.Contains(got, "# Agent Chat Transcript\n\nfresh transcript") {
		t.Errorf("canonical section missing:\n%s", got)
	}

	// Identical content under the legacy header is also a skip.
	job2 := writeTranscriptTestJob(t, transcriptTestJobContent+"\n## Transcript\n\nsame content\n")
	changed, err = NewStatePersister().UpdateJobTranscript(job2, "same content\n", false)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("expected changed=false when legacy section content matches")
	}
}

func TestUpdateJobTranscript_OnlyIfMissing(t *testing.T) {
	// Existing section: untouched.
	job := writeTranscriptTestJob(t, transcriptTestJobContent+"\n# Agent Chat Transcript\n\nreal transcript\n")
	changed, err := NewStatePersister().UpdateJobTranscript(job, "*never run*", true)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("expected changed=false when section exists and onlyIfMissing=true")
	}
	content, _ := os.ReadFile(job.FilePath)
	if !strings.Contains(string(content), "real transcript") {
		t.Errorf("existing transcript clobbered:\n%s", content)
	}

	// Missing section: appended.
	job2 := writeTranscriptTestJob(t, transcriptTestJobContent)
	changed, err = NewStatePersister().UpdateJobTranscript(job2, "*never run*", true)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("expected changed=true when section missing and onlyIfMissing=true")
	}
	content, _ = os.ReadFile(job2.FilePath)
	if !strings.Contains(string(content), "# Agent Chat Transcript\n\n*never run*") {
		t.Errorf("note not appended:\n%s", content)
	}
}

func TestUpdateJobTranscript_LockContention(t *testing.T) {
	job := writeTranscriptTestJob(t, transcriptTestJobContent)
	lockPath := job.FilePath + ".lock"

	// A live foreign process (the test runner's parent) holds the lock:
	// the write must be refused, and the file left untouched.
	foreignPID := os.Getppid()
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%d\n", foreignPID)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStatePersister().UpdateJobTranscript(job, "blocked write\n", false); err == nil {
		t.Fatal("expected lock-contention error while a live process holds the lock")
	}
	content, _ := os.ReadFile(job.FilePath)
	if strings.Contains(string(content), "blocked write") {
		t.Error("file written despite held lock")
	}

	// A dead holder's lock is stale: it must be broken and the write proceed.
	if err := os.WriteFile(lockPath, []byte("99999999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := NewStatePersister().UpdateJobTranscript(job, "recovered write\n", false)
	if err != nil {
		t.Fatalf("expected stale lock to be broken, got error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true after breaking stale lock")
	}
	content, _ = os.ReadFile(job.FilePath)
	if !strings.Contains(string(content), "recovered write") {
		t.Errorf("transcript missing after stale-lock recovery:\n%s", content)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Error("lock file not released after write")
	}
}

func TestUpdateJobSection_InsertBeforeHeader(t *testing.T) {
	job := writeTranscriptTestJob(t, transcriptTestJobContent+"\n# Agent Chat Transcript\n\nthe transcript\n")

	changed, err := NewStatePersister().UpdateJobSection(job, "# Workflow Runs", "run details\n", "# Agent Chat Transcript", false)
	if err != nil {
		t.Fatalf("UpdateJobSection() error = %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true on first write")
	}

	content, _ := os.ReadFile(job.FilePath)
	got := string(content)
	secIdx := strings.Index(got, "# Workflow Runs")
	trIdx := strings.Index(got, "# Agent Chat Transcript")
	if secIdx == -1 || trIdx == -1 {
		t.Fatalf("missing section or transcript header:\n%s", got)
	}
	if secIdx > trIdx {
		t.Errorf("section not inserted before transcript header:\n%s", got)
	}
	for _, want := range []string{
		"The job prompt body.",
		"# Workflow Runs\n\nrun details",
		"the transcript",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("job file missing %q\n---\n%s", want, got)
		}
	}
}

func TestUpdateJobSection_FallsBackToEOF(t *testing.T) {
	job := writeTranscriptTestJob(t, transcriptTestJobContent)

	// insertBeforeHeader absent from the body: append at EOF.
	changed, err := NewStatePersister().UpdateJobSection(job, "# Workflow Runs", "run details\n", "# Agent Chat Transcript", false)
	if err != nil {
		t.Fatalf("UpdateJobSection() error = %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}

	content, _ := os.ReadFile(job.FilePath)
	got := string(content)
	if !strings.Contains(got, "The job prompt body.\n\n# Workflow Runs\n\nrun details") {
		t.Errorf("section not appended at EOF:\n%s", got)
	}
}

func TestUpdateJobSection_ReplacesExistingBoundedByInsertBefore(t *testing.T) {
	job := writeTranscriptTestJob(t, transcriptTestJobContent+
		"\n# Workflow Runs\n\nold run details\n\n# Agent Chat Transcript\n\nthe transcript\n")

	changed, err := NewStatePersister().UpdateJobSection(job, "# Workflow Runs", "new run details\n", "# Agent Chat Transcript", false)
	if err != nil {
		t.Fatalf("UpdateJobSection() error = %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true when section content differs")
	}

	content, _ := os.ReadFile(job.FilePath)
	got := string(content)
	if strings.Contains(got, "old run details") {
		t.Errorf("old section content not replaced:\n%s", got)
	}
	if !strings.Contains(got, "# Workflow Runs\n\nnew run details") {
		t.Errorf("new section content missing:\n%s", got)
	}
	if !strings.Contains(got, "# Agent Chat Transcript\n\nthe transcript") {
		t.Errorf("transcript section after the splice was damaged:\n%s", got)
	}
	if n := strings.Count(got, "# Workflow Runs"); n != 1 {
		t.Errorf("expected exactly one section, got %d", n)
	}
}

func TestUpdateJobSection_Idempotent(t *testing.T) {
	job := writeTranscriptTestJob(t, transcriptTestJobContent+"\n# Agent Chat Transcript\n\nthe transcript\n")

	sp := NewStatePersister()
	if _, err := sp.UpdateJobSection(job, "# Workflow Runs", "run details\n", "# Agent Chat Transcript", false); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(job.FilePath)

	changed, err := sp.UpdateJobSection(job, "# Workflow Runs", "run details\n", "# Agent Chat Transcript", false)
	if err != nil {
		t.Fatalf("UpdateJobSection() second call error = %v", err)
	}
	if changed {
		t.Error("expected changed=false for identical section content")
	}
	after, _ := os.ReadFile(job.FilePath)
	if string(before) != string(after) {
		t.Error("file modified despite unchanged section content")
	}
}

func TestUpdateJobSection_OnlyIfMissing(t *testing.T) {
	job := writeTranscriptTestJob(t, transcriptTestJobContent+"\n# Workflow Runs\n\nexisting details\n")

	changed, err := NewStatePersister().UpdateJobSection(job, "# Workflow Runs", "replacement\n", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("expected changed=false when section exists and onlyIfMissing=true")
	}
	content, _ := os.ReadFile(job.FilePath)
	if !strings.Contains(string(content), "existing details") {
		t.Errorf("existing section clobbered:\n%s", content)
	}
}

func TestUpdateJobSection_LockContention(t *testing.T) {
	job := writeTranscriptTestJob(t, transcriptTestJobContent)
	lockPath := job.FilePath + ".lock"

	// A live foreign process (the test runner's parent) holds the lock:
	// the write must be refused, and the file left untouched.
	foreignPID := os.Getppid()
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%d\n", foreignPID)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStatePersister().UpdateJobSection(job, "# Workflow Runs", "blocked write\n", "", false); err == nil {
		t.Fatal("expected lock-contention error while a live process holds the lock")
	}
	content, _ := os.ReadFile(job.FilePath)
	if strings.Contains(string(content), "blocked write") {
		t.Error("file written despite held lock")
	}
}

func TestUpdateJobTranscript_NoFrontmatterFile(t *testing.T) {
	job := writeTranscriptTestJob(t, "Just a body, no frontmatter.\n")

	changed, err := NewStatePersister().UpdateJobTranscript(job, "transcript\n", false)
	if err != nil {
		t.Fatalf("UpdateJobTranscript() error = %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	content, _ := os.ReadFile(job.FilePath)
	got := string(content)
	if strings.HasPrefix(got, "---") {
		t.Errorf("frontmatter invented for a file that had none:\n%s", got)
	}
	if !strings.Contains(got, "Just a body, no frontmatter.") ||
		!strings.Contains(got, "# Agent Chat Transcript\n\ntranscript") {
		t.Errorf("unexpected content:\n%s", got)
	}
}
