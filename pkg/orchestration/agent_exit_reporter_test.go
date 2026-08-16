package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recordingSessionEnder struct {
	calls     int
	jobID     string
	attemptID string
	outcome   string
}

func (e *recordingSessionEnder) EndSession(_ context.Context, jobID, attemptID, outcome string) error {
	e.calls++
	e.jobID, e.attemptID, e.outcome = jobID, attemptID, outcome
	return nil
}

func TestReportInteractiveAgentExitStartupDeathAndDuplicate(t *testing.T) {
	t.Setenv("GROVE_HOME", t.TempDir())
	job, plan := newPiStartupJob(t)
	ender := &recordingSessionEnder{}
	if err := reportInteractiveAgentExit(context.Background(), job, plan, "", 7, ender); err != nil {
		t.Fatal(err)
	}
	if ender.calls != 1 || ender.outcome != "interrupted" {
		t.Fatalf("EndSession calls=%d outcome=%q", ender.calls, ender.outcome)
	}
	reloaded, err := LoadJob(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != JobStatusFailed || !strings.Contains(reloaded.Metadata.LastError, "code 7") {
		t.Fatalf("job status=%q last_error=%q", reloaded.Status, reloaded.Metadata.LastError)
	}
	if _, err := os.Stat(job.FilePath + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("lock remains: %v", err)
	}
	if err := reportInteractiveAgentExit(context.Background(), job, plan, "", 7, ender); err != nil {
		t.Fatal(err)
	}
	if ender.calls != 1 {
		t.Fatalf("duplicate emitted EndSession: %d", ender.calls)
	}
	logData, _ := os.ReadFile(filepath.Join(plan.Directory, ".artifacts", job.ID, "job.log"))
	if got := strings.Count(string(logData), "provider exited, code 7, supervised"); got != 1 {
		t.Fatalf("terminal log count=%d log=%s", got, logData)
	}
}

func TestReportInteractiveAgentExitStaleAttemptCannotEndRetry(t *testing.T) {
	t.Setenv("GROVE_HOME", t.TempDir())
	job, plan := newPiStartupJob(t)
	job.AttemptID = "01890f5d-e4b8-7cc4-98c4-dc0c0c07398f"
	ender := &recordingSessionEnder{}

	if err := reportInteractiveAgentExit(context.Background(), job, plan,
		"01890f5d-e4b8-7cc3-98c4-dc0c0c07398f", 9, ender); err != nil {
		t.Fatal(err)
	}
	if ender.calls != 0 {
		t.Fatalf("stale reporter ended current attempt: %d calls", ender.calls)
	}
	if _, err := os.Stat(filepath.Join(plan.Directory, ".artifacts", job.ID, supervisedExitReceiptName)); !os.IsNotExist(err) {
		t.Fatalf("stale reporter claimed current attempt receipt: %v", err)
	}
	reloaded, err := LoadJob(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != JobStatusRunning {
		t.Fatalf("stale reporter changed frontmatter to %q", reloaded.Status)
	}
}

func TestReportInteractiveAgentExitZeroOnlyEndsSession(t *testing.T) {
	t.Setenv("GROVE_HOME", t.TempDir())
	job, plan := newPiStartupJob(t)
	ender := &recordingSessionEnder{}
	if err := reportInteractiveAgentExit(context.Background(), job, plan, "", 0, ender); err != nil {
		t.Fatal(err)
	}
	if ender.outcome != "exited" {
		t.Fatalf("outcome=%q", ender.outcome)
	}
	reloaded, _ := LoadJob(job.FilePath)
	if reloaded.Status != JobStatusRunning {
		t.Fatalf("rc=0 changed frontmatter to %q", reloaded.Status)
	}
}

func TestReportInteractiveAgentExitEnrichedDoesNotFailFrontmatter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROVE_HOME", home)
	job, plan := newPiStartupJob(t)
	job.StartTime = time.Now().Add(-time.Second)
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "state", "grove", "hooks", "sessions", "native")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	metadata := `{"session_id":"pi-job","job_id":"pi-job","status":"running","claude_session_id":"native","job_file_path":` + quoteJSON(job.FilePath) + `,"transcript_path":` + quoteJSON(transcript) + `,"started_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}`
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
	ender := &recordingSessionEnder{}
	if err := reportInteractiveAgentExit(context.Background(), job, plan, "", 9, ender); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := LoadJob(job.FilePath)
	if reloaded.Status != JobStatusRunning {
		t.Fatalf("enriched exit changed status=%q", reloaded.Status)
	}
}

func quoteJSON(s string) string {
	b := strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(s)
	return "\"" + b + "\""
}
