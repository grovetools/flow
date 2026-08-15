package orchestration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendAgentStartupBreadcrumbLeavesJobStateUntouched(t *testing.T) {
	job, plan := newPiStartupJob(t)

	handled, err := appendAgentStartupBreadcrumb(job, plan, "claude", agentStartupEvidence{
		pid:        deadPID,
		paneOutput: "Claude Code\nTrust this folder?\nprovider terminated",
	})
	if err != nil {
		t.Fatalf("appendAgentStartupBreadcrumb() error = %v", err)
	}
	if !handled {
		t.Fatal("expected dead provider to produce a breadcrumb")
	}

	reloaded, err := LoadJob(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != JobStatusRunning {
		t.Fatalf("status = %q, want running (Phase 0 is observability-only)", reloaded.Status)
	}
	data, err := os.ReadFile(filepath.Join(plan.Directory, ".artifacts", job.ID, "job.log"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, claudeStartupFailureReason) || !strings.Contains(got, "Trust this folder?") {
		t.Fatalf("job.log missing reason or pane tail:\n%s", got)
	}
}

func TestAppendAgentStartupBreadcrumbRequiresExitEvidence(t *testing.T) {
	job, plan := newPiStartupJob(t)
	handled, err := appendAgentStartupBreadcrumb(job, plan, "claude", agentStartupEvidence{pid: os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("live provider must not be diagnosed as exited")
	}
}

func TestClaudeFolderTrustPreflight(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	job, plan := newPiStartupJob(t)

	if err := warnIfClaudeFolderUntrusted(job, plan, workDir); err != nil {
		t.Fatal(err)
	}
	logPath, _ := GetJobLogPath(plan, job)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "folder trust") || !strings.Contains(string(data), workDir) {
		t.Fatalf("missing trust warning does not name cause and directory:\n%s", data)
	}

	canonical, err := filepath.Abs(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(canonical); resolveErr == nil {
		canonical = resolved
	}
	trust := map[string]any{"projects": map[string]any{
		canonical: map[string]any{"hasTrustDialogAccepted": true},
	}}
	encoded, _ := json.Marshal(trust)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(logPath)
	if err := warnIfClaudeFolderUntrusted(job, plan, workDir); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(logPath)
	if string(after) != string(before) {
		t.Fatalf("trusted directory unexpectedly emitted warning:\nbefore=%s\nafter=%s", before, after)
	}
}
