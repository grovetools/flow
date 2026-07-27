package orchestration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPiExtensionFailureSummary(t *testing.T) {
	output := "Pi startup\nError: Failed to load extension /tmp/broken.ts: boom\nreturned to shell"
	got, ok := piExtensionFailureSummary(output)
	if !ok {
		t.Fatal("expected extension failure to be detected")
	}
	if !strings.Contains(got, "Failed to load extension /tmp/broken.ts: boom") {
		t.Fatalf("summary = %q", got)
	}
}

func TestHandlePiStartupFailurePersistsVisibleFailure(t *testing.T) {
	planDir := t.TempDir()
	jobPath := filepath.Join(planDir, "01-pi-job.md")
	content := `---
id: pi-job
status: running
title: Pi job
type: interactive_agent
---

Do work.
`
	if err := os.WriteFile(jobPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	job := &Job{
		ID:        "pi-job",
		Title:     "Pi job",
		Type:      JobTypeInteractiveAgent,
		Status:    JobStatusRunning,
		FilePath:  jobPath,
		Filename:  filepath.Base(jobPath),
		StartTime: time.Now().Add(-time.Second),
	}
	plan := &Plan{Directory: planDir}
	if err := CreateLockFile(jobPath, 123); err != nil {
		t.Fatal(err)
	}

	handled, err := handlePiStartupFailure(job, plan, 99999999,
		"Error: Failed to load extension broken.ts: missing export", errors.New("no transcript found"))
	if err != nil {
		t.Fatalf("handlePiStartupFailure() error = %v", err)
	}
	if !handled {
		t.Fatal("expected startup failure to be handled")
	}

	reloaded, err := LoadJob(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != JobStatusFailed {
		t.Fatalf("status = %q, want failed", reloaded.Status)
	}
	if !strings.Contains(reloaded.Metadata.LastError, "Failed to load extension") {
		t.Fatalf("last_error = %q", reloaded.Metadata.LastError)
	}

	jobFile, err := os.ReadFile(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(jobFile), "Pi failed during startup before a transcript was created") ||
		!strings.Contains(string(jobFile), "missing export") {
		t.Fatalf("job transcript does not contain startup error:\n%s", jobFile)
	}

	logFile, err := os.ReadFile(filepath.Join(planDir, ".artifacts", job.ID, "job.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logFile), "Failed to load extension") {
		t.Fatalf("job.log does not contain startup error:\n%s", logFile)
	}
	if _, err := os.Stat(lockFileName(jobPath)); !os.IsNotExist(err) {
		t.Fatalf("lock file still exists, stat error = %v", err)
	}
}
