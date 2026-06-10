package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseWorkflowJournal_Complete(t *testing.T) {
	events, err := parseWorkflowJournal(filepath.Join("testdata", "workflows", "journal_complete.jsonl"))
	if err != nil {
		t.Fatalf("parseWorkflowJournal() error = %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d", len(events))
	}
	if events[0].Type != "started" || events[0].AgentID != "a6053d9fe85440cfe" {
		t.Errorf("unexpected first event: %+v", events[0])
	}
	if !strings.HasPrefix(events[0].Key, "v2:") {
		t.Errorf("expected v2: key prefix, got %q", events[0].Key)
	}
	if events[2].Type != "result" || len(events[2].Result) == 0 {
		t.Errorf("expected result event with payload, got %+v", events[2])
	}
	if events[1].Result != nil {
		t.Errorf("started event should have no result, got %s", events[1].Result)
	}
}

func TestParseWorkflowJournal_SkipsMalformedLines(t *testing.T) {
	events, err := parseWorkflowJournal(filepath.Join("testdata", "workflows", "journal_malformed.jsonl"))
	if err != nil {
		t.Fatalf("parseWorkflowJournal() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 parseable events, got %d", len(events))
	}
	if events[0].Type != "started" || events[1].Type != "result" {
		t.Errorf("unexpected events: %+v", events)
	}
}

func TestParseWorkflowJournal_MissingFile(t *testing.T) {
	if _, err := parseWorkflowJournal(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestGenerateWorkflowSummary_Complete(t *testing.T) {
	events, err := parseWorkflowJournal(filepath.Join("testdata", "workflows", "journal_complete.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	summary := generateWorkflowSummary("release-survey", "wf_4650c05a-c39", events)

	for _, want := range []string{
		"# Workflow Run: release-survey",
		"Run ID: `wf_4650c05a-c39`",
		"Agents started: 2",
		"Agents completed: 2",
		"## Results",
		"### Agent `a6053d9fe85440cfe`",
		`"summary": "first agent finding"`,
		"plain text result from a schemaless agent",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary missing %q\n---\n%s", want, summary)
		}
	}
	if strings.Contains(summary, "Run interrupted") {
		t.Error("complete run should not be marked interrupted")
	}
	// The schemaless string result must render as plain text, not a quoted
	// JSON literal.
	if strings.Contains(summary, `"plain text result`) {
		t.Error("string result rendered as quoted JSON instead of plain text")
	}
}

func TestGenerateWorkflowSummary_Interrupted(t *testing.T) {
	events, err := parseWorkflowJournal(filepath.Join("testdata", "workflows", "journal_interrupted.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	summary := generateWorkflowSummary("verify-round", "wf_2dc6d7f2-bab", events)

	for _, want := range []string{
		"Agents started: 3",
		"Agents completed: 1",
		"Run interrupted: 2 agent(s)",
		"agent-two",
		"agent-three",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary missing %q\n---\n%s", want, summary)
		}
	}
}

func TestGenerateWorkflowSummary_NoEvents(t *testing.T) {
	summary := generateWorkflowSummary("wf_empty-run", "wf_empty-run", nil)
	for _, want := range []string{
		"# Workflow Run: wf_empty-run",
		"Agents started: 0",
		"Agents completed: 0",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary missing %q\n---\n%s", want, summary)
		}
	}
	if strings.Contains(summary, "## Results") {
		t.Error("empty run should have no results section")
	}
}

func TestFindWorkflowScript(t *testing.T) {
	scriptsDir := t.TempDir()
	scriptPath := filepath.Join(scriptsDir, "release-survey-wf_4650c05a-c39.js")
	if err := os.WriteFile(scriptPath, []byte("export const meta = {}"), 0o600); err != nil {
		t.Fatal(err)
	}

	path, title := findWorkflowScript(scriptsDir, "wf_4650c05a-c39")
	if path != scriptPath {
		t.Errorf("path = %q, want %q", path, scriptPath)
	}
	if title != "release-survey" {
		t.Errorf("title = %q, want %q", title, "release-survey")
	}

	if path, title := findWorkflowScript(scriptsDir, "wf_other-run"); path != "" || title != "" {
		t.Errorf("expected no match, got path=%q title=%q", path, title)
	}
}

// buildWorkflowRunDir assembles a fake wf_* source directory from a journal
// fixture plus agent transcript stubs.
func buildWorkflowRunDir(t *testing.T, journalFixture string) string {
	t.Helper()
	dir := t.TempDir()
	runDir := filepath.Join(dir, "wf_4650c05a-c39")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	journal, err := os.ReadFile(filepath.Join("testdata", "workflows", journalFixture))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "journal.jsonl"), journal, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"agent-a6053d9fe85440cfe.jsonl", "agent-a6053d9fe85440cfe.meta.json"} {
		if err := os.WriteFile(filepath.Join(runDir, name), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return runDir
}

func TestArchiveWorkflowRun_DefaultSkipsTranscripts(t *testing.T) {
	runDir := buildWorkflowRunDir(t, "journal_complete.jsonl")
	scriptsDir := t.TempDir()
	scriptContent := "export const meta = { name: 'release-survey' }"
	if err := os.WriteFile(filepath.Join(scriptsDir, "release-survey-wf_4650c05a-c39.js"), []byte(scriptContent), 0o600); err != nil {
		t.Fatal(err)
	}
	destDir := filepath.Join(t.TempDir(), "workflows", "wf_4650c05a-c39")

	if err := archiveWorkflowRun(context.Background(), runDir, scriptsDir, destDir, false); err != nil {
		t.Fatalf("archiveWorkflowRun() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(destDir, "journal.jsonl")); err != nil {
		t.Errorf("journal.jsonl not archived: %v", err)
	}
	script, err := os.ReadFile(filepath.Join(destDir, "script.js"))
	if err != nil {
		t.Fatalf("script.js not archived: %v", err)
	}
	if string(script) != scriptContent {
		t.Errorf("script.js content mismatch: %q", script)
	}
	summary, err := os.ReadFile(filepath.Join(destDir, "summary.md"))
	if err != nil {
		t.Fatalf("summary.md not written: %v", err)
	}
	if !strings.Contains(string(summary), "# Workflow Run: release-survey") {
		t.Errorf("summary missing script-derived title:\n%s", summary)
	}
	if _, err := os.Stat(filepath.Join(destDir, "agents")); !os.IsNotExist(err) {
		t.Error("agent transcripts archived despite archive_agent_transcripts=false")
	}
}

func TestArchiveWorkflowRun_TranscriptsOptIn(t *testing.T) {
	runDir := buildWorkflowRunDir(t, "journal_complete.jsonl")
	destDir := filepath.Join(t.TempDir(), "workflows", "wf_4650c05a-c39")

	if err := archiveWorkflowRun(context.Background(), runDir, t.TempDir(), destDir, true); err != nil {
		t.Fatalf("archiveWorkflowRun() error = %v", err)
	}

	for _, name := range []string{"agent-a6053d9fe85440cfe.jsonl", "agent-a6053d9fe85440cfe.meta.json"} {
		if _, err := os.Stat(filepath.Join(destDir, "agents", name)); err != nil {
			t.Errorf("expected archived transcript %s: %v", name, err)
		}
	}
}

func TestArchiveWorkflowRun_MissingScriptFallsBackToRunID(t *testing.T) {
	runDir := buildWorkflowRunDir(t, "journal_interrupted.jsonl")
	destDir := filepath.Join(t.TempDir(), "workflows", "wf_4650c05a-c39")

	// Empty scripts dir: title must fall back to the run ID.
	if err := archiveWorkflowRun(context.Background(), runDir, t.TempDir(), destDir, false); err != nil {
		t.Fatalf("archiveWorkflowRun() error = %v", err)
	}

	summary, err := os.ReadFile(filepath.Join(destDir, "summary.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(summary), "# Workflow Run: wf_4650c05a-c39") {
		t.Errorf("summary missing runId fallback title:\n%s", summary)
	}
	if !strings.Contains(string(summary), "Run interrupted: 2 agent(s)") {
		t.Errorf("summary missing interrupted note:\n%s", summary)
	}
	if _, err := os.Stat(filepath.Join(destDir, "script.js")); !os.IsNotExist(err) {
		t.Error("script.js should not exist when no script matched")
	}
}

func TestArchiveWorkflowRun_MalformedJournalStillWritesSummary(t *testing.T) {
	runDir := buildWorkflowRunDir(t, "journal_malformed.jsonl")
	destDir := filepath.Join(t.TempDir(), "workflows", "wf_4650c05a-c39")

	if err := archiveWorkflowRun(context.Background(), runDir, t.TempDir(), destDir, false); err != nil {
		t.Fatalf("archiveWorkflowRun() error = %v", err)
	}

	summary, err := os.ReadFile(filepath.Join(destDir, "summary.md"))
	if err != nil {
		t.Fatal(err)
	}
	// Only the two well-formed lines should be counted.
	if !strings.Contains(string(summary), "Agents started: 1") {
		t.Errorf("summary should count only parseable events:\n%s", summary)
	}
}
