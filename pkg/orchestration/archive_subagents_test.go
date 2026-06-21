package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildStandaloneSubagentSession assembles a fake Claude session dir holding
// FLAT standalone subagents at subagents/agent-*.jsonl (each with a sibling
// meta.json), PLUS a nested workflow agent under subagents/workflows/wf_*/ that
// the standalone archiver must NOT pick up. Returns the session dir path.
func buildStandaloneSubagentSession(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	subagentsDir := filepath.Join(dir, "subagents")
	if err := os.MkdirAll(subagentsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Two flat standalone agents. One has a meta.json description (human name);
	// the other has none, so it must fall back to the agent ID.
	if err := os.WriteFile(filepath.Join(subagentsDir, "agent-aaa111.jsonl"), []byte(sampleAgentTranscript), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subagentsDir, "agent-aaa111.meta.json"), []byte(`{"description":"Explore the nb module"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subagentsDir, "agent-bbb222.jsonl"), []byte(sampleAgentTranscript), 0o600); err != nil {
		t.Fatal(err)
	}

	// A nested workflow agent that must be SKIPPED by the standalone archiver
	// (it is owned by ArchiveWorkflowRuns).
	wfDir := filepath.Join(subagentsDir, "workflows", "wf_zzz")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wfDir, "agent-wf999.jsonl"), []byte(sampleAgentTranscript), 0o600); err != nil {
		t.Fatal(err)
	}

	return dir
}

func TestArchiveStandaloneSubagentsFromDirs_CapturesFlatAgentsSkipsWorkflows(t *testing.T) {
	sessionDir := buildStandaloneSubagentSession(t)
	job := writeTranscriptTestJob(t, transcriptTestJobContent+"\n# Agent Chat Transcript\n\nthe transcript\n")
	plan := &Plan{Directory: t.TempDir()}

	if err := archiveStandaloneSubagentsFromDirs(context.Background(), job, plan, []string{sessionDir}, false); err != nil {
		t.Fatalf("archiveStandaloneSubagentsFromDirs() error = %v", err)
	}

	destSubagentsDir := filepath.Join(plan.Directory, ".artifacts", job.ID, "subagents")

	// Both flat standalone agents get rendered .md transcripts.
	for _, id := range []string{"aaa111", "bbb222"} {
		md := filepath.Join(destSubagentsDir, "agent-"+id+".md")
		if _, err := os.Stat(md); err != nil {
			t.Errorf("standalone agent markdown not written for %s: %v", id, err)
		}
	}

	// The nested workflow agent must NOT be captured here.
	if _, err := os.Stat(filepath.Join(destSubagentsDir, "agent-wf999.md")); err == nil {
		t.Error("workflow agent was double-counted as a standalone subagent")
	}

	// Raw jsonl copies stay behind the archive_agent_transcripts flag.
	if _, err := os.Stat(filepath.Join(destSubagentsDir, "agent-aaa111.jsonl")); err == nil {
		t.Error("raw jsonl copied despite includeTranscripts=false")
	}

	// The aaa111 render uses the meta.json description as a display name.
	aaaMD, err := os.ReadFile(filepath.Join(destSubagentsDir, "agent-aaa111.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(aaaMD), "Explore the nb module") {
		t.Errorf("meta.json description not used as display name:\n%s", aaaMD)
	}

	// The job .md gets a "# Subagents" section, inserted before the transcript
	// section, listing both flat agents (with the meta description) and NOT the
	// workflow agent.
	jobContent, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(jobContent)
	secIdx := strings.Index(got, subagentsSectionHeader)
	trIdx := strings.Index(got, transcriptSectionHeader)
	if secIdx == -1 {
		t.Fatalf("subagents section missing:\n%s", got)
	}
	if trIdx == -1 || secIdx > trIdx {
		t.Errorf("subagents section not inserted before the transcript section:\n%s", got)
	}
	for _, want := range []string{
		"# Subagents",
		"`aaa111` — Explore the nb module",
		"[agent-aaa111.md](.artifacts/test-job/subagents/agent-aaa111.md)",
		"`bbb222`",
		"[agent-bbb222.md](.artifacts/test-job/subagents/agent-bbb222.md)",
		"the transcript",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("job file missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "wf999") {
		t.Errorf("workflow agent leaked into the # Subagents section:\n%s", got)
	}
}

func TestArchiveStandaloneSubagentsFromDirs_RawJSONLGated(t *testing.T) {
	sessionDir := buildStandaloneSubagentSession(t)
	job := writeTranscriptTestJob(t, transcriptTestJobContent+"\n# Agent Chat Transcript\n\nthe transcript\n")
	plan := &Plan{Directory: t.TempDir()}

	if err := archiveStandaloneSubagentsFromDirs(context.Background(), job, plan, []string{sessionDir}, true); err != nil {
		t.Fatalf("archiveStandaloneSubagentsFromDirs() error = %v", err)
	}

	destSubagentsDir := filepath.Join(plan.Directory, ".artifacts", job.ID, "subagents")
	for _, name := range []string{"agent-aaa111.md", "agent-aaa111.jsonl", "agent-bbb222.md", "agent-bbb222.jsonl"} {
		if _, err := os.Stat(filepath.Join(destSubagentsDir, name)); err != nil {
			t.Errorf("expected %s with includeTranscripts=true: %v", name, err)
		}
	}
}

func TestArchiveStandaloneSubagentsFromDirs_NoAgentsNoOp(t *testing.T) {
	// A session dir with no subagents/ dir at all, and one with only a workflow
	// agent — neither must touch the job file or write a section.
	emptyDir := t.TempDir()

	wfOnly := t.TempDir()
	wfDir := filepath.Join(wfOnly, "subagents", "workflows", "wf_zzz")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wfDir, "agent-wf999.jsonl"), []byte(sampleAgentTranscript), 0o600); err != nil {
		t.Fatal(err)
	}

	job := writeTranscriptTestJob(t, transcriptTestJobContent)
	before, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	plan := &Plan{Directory: t.TempDir()}

	if err := archiveStandaloneSubagentsFromDirs(context.Background(), job, plan, []string{emptyDir, wfOnly}, false); err != nil {
		t.Fatalf("archiveStandaloneSubagentsFromDirs() error = %v", err)
	}

	after, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("job file modified despite no standalone subagents:\n%s", after)
	}
	if strings.Contains(string(after), subagentsSectionHeader) {
		t.Error("subagents section written for an agent-less session")
	}
}
