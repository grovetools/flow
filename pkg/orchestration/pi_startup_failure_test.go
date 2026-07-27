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

// deadPID is a PID high enough that no live process owns it, so
// process.IsProcessAlive reports the "discovered PID has exited" case.
const deadPID = 99999999

// newPiStartupJob builds a running interactive_agent job with a held lock file,
// mirroring the state a Pi launch is in while session discovery runs.
func newPiStartupJob(t *testing.T) (*Job, *Plan) {
	t.Helper()
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
	if err := CreateLockFile(jobPath, 123); err != nil {
		t.Fatal(err)
	}
	return job, &Plan{Directory: planDir}
}

func TestHandlePiStartupFailurePersistsVisibleFailure(t *testing.T) {
	job, plan := newPiStartupJob(t)
	planDir := plan.Directory
	jobPath := job.FilePath

	handled, err := handlePiStartupFailure(job, plan, piStartupEvidence{
		pid:          deadPID,
		paneOutput:   "Error: Failed to load extension broken.ts: missing export",
		discoveryErr: errors.New("no transcript found"),
	})
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

func TestPiStartupErrorSummaryClassifiesOutput(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		wantKind piStartupErrorKind
		wantText string
	}{
		{
			name:     "extension failure",
			output:   "pi\nError: Failed to load extension \"/x/oracle.ts\": Cannot find module 'yaml'\nHint: pi -ne",
			wantKind: piStartupErrorExtension,
			wantText: "Cannot find module 'yaml'",
		},
		{
			name:     "generic startup error",
			output:   "pi\nError: Cannot find module 'yaml'\n",
			wantKind: piStartupErrorFatal,
			wantText: "Cannot find module 'yaml'",
		},
		{
			name:     "healthy banner",
			output:   "pi v3.2.1\nReady.\n> ",
			wantKind: piStartupErrorNone,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			summary, kind := piStartupErrorSummary(tc.output)
			if kind != tc.wantKind {
				t.Fatalf("kind = %d, want %d (summary %q)", kind, tc.wantKind, summary)
			}
			if tc.wantText != "" && !strings.Contains(summary, tc.wantText) {
				t.Fatalf("summary = %q, want it to contain %q", summary, tc.wantText)
			}
		})
	}
}

// The real incident produced no pane output at all: the PTY was already gone
// when capture ran. The failure must then say why there is no terminal output.
func TestHandlePiStartupFailureNamesCaptureFailure(t *testing.T) {
	job, plan := newPiStartupJob(t)

	handled, err := handlePiStartupFailure(job, plan, piStartupEvidence{
		pid:          deadPID,
		captureErr:   errors.New("pty session 7e3a6082 not found"),
		discoveryErr: errors.New("no pi session files found"),
	})
	if err != nil {
		t.Fatalf("handlePiStartupFailure() error = %v", err)
	}
	if !handled {
		t.Fatal("expected a dead PID to be handled as a startup failure")
	}

	reloaded, err := LoadJob(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != JobStatusFailed {
		t.Fatalf("status = %q, want failed", reloaded.Status)
	}
	if !strings.Contains(reloaded.Metadata.LastError, "terminal output unavailable") ||
		!strings.Contains(reloaded.Metadata.LastError, "pty session 7e3a6082 not found") {
		t.Fatalf("last_error does not explain the missing terminal output: %q", reloaded.Metadata.LastError)
	}

	jobFile, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(jobFile), "pty session 7e3a6082 not found") {
		t.Fatalf("job transcript does not name the capture failure:\n%s", jobFile)
	}
}

// Extension errors must produce the extension summary even without a PID: an
// instantly dying pi may never write a pidfile, and the captured error text is
// the evidence in that case.
func TestHandlePiStartupFailureExtensionErrorWithoutPID(t *testing.T) {
	job, plan := newPiStartupJob(t)

	handled, err := handlePiStartupFailure(job, plan, piStartupEvidence{
		pid:        0,
		paneOutput: "Error: Failed to load extension \"/x/flow-pipeline.ts\": Cannot find module 'yaml'",
	})
	if err != nil {
		t.Fatalf("handlePiStartupFailure() error = %v", err)
	}
	if !handled {
		t.Fatal("expected captured extension error to be handled as a startup failure")
	}

	reloaded, err := LoadJob(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != JobStatusFailed {
		t.Fatalf("status = %q, want failed", reloaded.Status)
	}
	if !strings.Contains(reloaded.Metadata.LastError, "Failed to load extension") ||
		!strings.Contains(reloaded.Metadata.LastError, "Cannot find module 'yaml'") {
		t.Fatalf("last_error = %q", reloaded.Metadata.LastError)
	}
}

// An unknown PID is not death. A pi that is merely slow to write its first
// transcript must keep running.
func TestHandlePiStartupFailureUnknownPIDIsInconclusive(t *testing.T) {
	job, plan := newPiStartupJob(t)

	handled, err := handlePiStartupFailure(job, plan, piStartupEvidence{
		pid:          0,
		captureErr:   errors.New("capture unavailable"),
		discoveryErr: errors.New("no pi session files found"),
	})
	if err != nil {
		t.Fatalf("handlePiStartupFailure() error = %v", err)
	}
	if handled {
		t.Fatal("unknown PID with no failure evidence must not be treated as a startup failure")
	}

	reloaded, err := LoadJob(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != JobStatusRunning {
		t.Fatalf("status = %q, want running (job must not be failed on an unknown PID)", reloaded.Status)
	}
	if _, err := os.Stat(lockFileName(job.FilePath)); err != nil {
		t.Fatalf("lock file was removed for an inconclusive startup: %v", err)
	}
}

// A live PID with no error output is also inconclusive: pi is simply slow.
func TestHandlePiStartupFailureLivePIDIsInconclusive(t *testing.T) {
	job, plan := newPiStartupJob(t)

	handled, err := handlePiStartupFailure(job, plan, piStartupEvidence{
		pid:          os.Getpid(),
		paneOutput:   "pi v3\nReady.\n> ",
		discoveryErr: errors.New("no pi session files found"),
	})
	if err != nil {
		t.Fatalf("handlePiStartupFailure() error = %v", err)
	}
	if handled {
		t.Fatal("a live pi with no error output must not be marked failed")
	}
	reloaded, err := LoadJob(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != JobStatusRunning {
		t.Fatalf("status = %q, want running", reloaded.Status)
	}
}
