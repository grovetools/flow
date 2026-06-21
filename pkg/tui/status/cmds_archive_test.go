package status

import (
	"os"
	"path/filepath"
	"testing"
)

// writeArchivedAgentMD lays out one archived agent transcript at
// <planDir>/.artifacts/<jobID>/workflows/<runID>/agents/agent-<id>.md.
func writeArchivedAgentMD(t *testing.T, planDir, jobID, runID, agentID, content string) {
	t.Helper()
	agentsDir := filepath.Join(planDir, ".artifacts", jobID, "workflows", runID, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "agent-"+agentID+".md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestLoadArchivedAgentMarkdown_ExplicitRunID confirms the cold-load fallback
// reads the canonical archived .md by exact run id and returns it verbatim.
func TestLoadArchivedAgentMarkdown_ExplicitRunID(t *testing.T) {
	planDir := t.TempDir()
	content := "# Agent `a1` — wf_chrono\n\n› Survey the nb module\n"
	writeArchivedAgentMD(t, planDir, "job-1", "wf_chrono", "a1", content)

	got, ok := loadArchivedAgentMarkdown(planDir, "job-1", "wf_chrono", "a1")
	if !ok {
		t.Fatal("expected archived transcript to be found")
	}
	if got != content {
		t.Errorf("content mismatch:\ngot  %q\nwant %q", got, content)
	}
}

// TestLoadArchivedAgentMarkdown_ScanRuns confirms the fallback scans every
// archived wf_* run dir when the run id is unknown (empty).
func TestLoadArchivedAgentMarkdown_ScanRuns(t *testing.T) {
	planDir := t.TempDir()
	content := "# Agent `a2` — wf_other\n\nresult\n"
	writeArchivedAgentMD(t, planDir, "job-1", "wf_other", "a2", content)

	got, ok := loadArchivedAgentMarkdown(planDir, "job-1", "", "a2")
	if !ok {
		t.Fatal("expected archived transcript to be found by scan")
	}
	if got != content {
		t.Errorf("content mismatch:\ngot  %q\nwant %q", got, content)
	}
}

// TestLoadArchivedAgentMarkdown_Miss confirms a clean miss when no archived
// transcript exists or required path components are empty.
func TestLoadArchivedAgentMarkdown_Miss(t *testing.T) {
	planDir := t.TempDir()
	writeArchivedAgentMD(t, planDir, "job-1", "wf_chrono", "a1", "x")

	if _, ok := loadArchivedAgentMarkdown(planDir, "job-1", "wf_chrono", "missing"); ok {
		t.Error("expected miss for an unknown agent id")
	}
	if _, ok := loadArchivedAgentMarkdown("", "job-1", "wf_chrono", "a1"); ok {
		t.Error("expected miss for empty plan dir")
	}
	if _, ok := loadArchivedAgentMarkdown(planDir, "", "wf_chrono", "a1"); ok {
		t.Error("expected miss for empty job id")
	}
}
