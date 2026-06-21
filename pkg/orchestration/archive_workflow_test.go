package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grovetools/agentlogs/pkg/transcript"
	agentworkflow "github.com/grovetools/agentlogs/pkg/workflow"
	"github.com/grovetools/core/pkg/workflows"
)

// userEntry builds a minimal user transcript entry with one text part.
func userEntry(text string) transcript.UnifiedEntry {
	return transcript.UnifiedEntry{
		Role:  "user",
		Parts: []transcript.UnifiedPart{{Type: "text", Content: transcript.UnifiedTextContent{Text: text}}},
	}
}

func TestGenerateWorkflowSummary_Complete(t *testing.T) {
	run := &agentworkflow.WorkflowRun{
		RunID: "wf_4650c05a-c39",
		Agents: map[string]*agentworkflow.AgentRun{
			"a6053d9fe85440cfe": {Started: true, Result: json.RawMessage(`{"summary":"first agent finding","files":["a.go","b.go"]}`)},
			"a9172cda79b99f9ad": {Started: true, Result: json.RawMessage(`"plain text result from a schemaless agent"`)},
		},
	}

	summary := generateWorkflowSummary("release-survey", "wf_4650c05a-c39", run,
		map[string]bool{"a6053d9fe85440cfe": true}, nil)

	for _, want := range []string{
		"# Workflow Run: release-survey",
		"Run ID: `wf_4650c05a-c39`",
		"Agents started: 2",
		"Agents completed: 2",
		"## Results",
		"### Agent `a6053d9fe85440cfe`",
		"[Transcript](agents/agent-a6053d9fe85440cfe.md)",
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
	// The second agent has no rendered markdown transcript; no link.
	if strings.Contains(summary, "agents/agent-a9172cda79b99f9ad.md") {
		t.Error("transcript link rendered for an agent without a markdown doc")
	}
}

func TestGenerateWorkflowSummary_Interrupted(t *testing.T) {
	run := &agentworkflow.WorkflowRun{
		RunID: "wf_2dc6d7f2-bab",
		Agents: map[string]*agentworkflow.AgentRun{
			"agent-one":   {Started: true, Result: json.RawMessage(`{"ok":true}`)},
			"agent-two":   {Started: true},
			"agent-three": {Started: true},
		},
	}

	summary := generateWorkflowSummary("verify-round", "wf_2dc6d7f2-bab", run, nil, nil)

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

func TestGenerateWorkflowSummary_NoAgents(t *testing.T) {
	run := &agentworkflow.WorkflowRun{RunID: "wf_empty-run", Agents: map[string]*agentworkflow.AgentRun{}}
	summary := generateWorkflowSummary("wf_empty-run", "wf_empty-run", run, nil, nil)
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

	path, title := findWorkflowScript([]string{scriptsDir}, "wf_4650c05a-c39")
	if path != scriptPath {
		t.Errorf("path = %q, want %q", path, scriptPath)
	}
	if title != "release-survey" {
		t.Errorf("title = %q, want %q", title, "release-survey")
	}

	if path, title := findWorkflowScript([]string{scriptsDir}, "wf_other-run"); path != "" || title != "" {
		t.Errorf("expected no match, got path=%q title=%q", path, title)
	}
}

// sampleAgentTranscript is a minimal Claude agent transcript: one user
// prompt and one assistant text response.
const sampleAgentTranscript = `{"type":"user","isSidechain":true,"message":{"role":"user","content":"Survey the nb module for release readiness"}}
{"type":"assistant","isSidechain":true,"message":{"id":"msg_1","content":[{"type":"text","text":"Survey complete."}]}}
`

// buildWorkflowRunDir assembles a fake wf_* source directory from a journal
// fixture plus agent transcript stubs (a parseable transcript for the first
// fixture agent, and a meta.json sidecar).
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
	if err := os.WriteFile(filepath.Join(runDir, "agent-a6053d9fe85440cfe.jsonl"), []byte(sampleAgentTranscript), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "agent-a6053d9fe85440cfe.meta.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return runDir
}

func TestArchiveWorkflowRun_DefaultSkipsRawTranscripts(t *testing.T) {
	runDir := buildWorkflowRunDir(t, "journal_complete.jsonl")
	scriptsDir := t.TempDir()
	scriptContent := "export const meta = { name: 'release-survey' }"
	if err := os.WriteFile(filepath.Join(scriptsDir, "release-survey-wf_4650c05a-c39.js"), []byte(scriptContent), 0o600); err != nil {
		t.Fatal(err)
	}
	destDir := filepath.Join(t.TempDir(), "workflows", "wf_4650c05a-c39")

	ar, err := archiveWorkflowRun(context.Background(), runDir, []string{scriptsDir}, destDir, t.TempDir(), false)
	if err != nil {
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
	if !strings.Contains(string(summary), "[Transcript](agents/agent-a6053d9fe85440cfe.md)") {
		t.Errorf("summary missing agent markdown transcript link:\n%s", summary)
	}

	// Rendered transcript is always on, in the canonical glyph style
	// (theme icons + summarized rows), ANSI-clean on disk.
	agentMD, err := os.ReadFile(filepath.Join(destDir, "agents", "agent-a6053d9fe85440cfe.md"))
	if err != nil {
		t.Fatalf("agent transcript not rendered: %v", err)
	}
	for _, want := range []string{"Survey the nb module", "Survey complete."} {
		if !strings.Contains(string(agentMD), want) {
			t.Errorf("agent transcript missing %q:\n%s", want, agentMD)
		}
	}
	if strings.Contains(string(agentMD), "**User:**") || strings.Contains(string(agentMD), "**Tool:") {
		t.Errorf("agent transcript should use the glyph style, not legacy markdown markers:\n%s", agentMD)
	}
	if strings.Contains(string(agentMD), "\x1b") {
		t.Errorf("agent transcript should be ANSI-clean (no escape sequences):\n%s", agentMD)
	}
	if !ar.AgentDocs["a6053d9fe85440cfe"] {
		t.Error("AgentDocs missing rendered agent")
	}

	// ...but raw jsonl copies stay behind archive_agent_transcripts.
	for _, name := range []string{"agent-a6053d9fe85440cfe.jsonl", "agent-a6053d9fe85440cfe.meta.json"} {
		if _, err := os.Stat(filepath.Join(destDir, "agents", name)); !os.IsNotExist(err) {
			t.Errorf("raw transcript %s archived despite archive_agent_transcripts=false", name)
		}
	}
}

func TestArchiveWorkflowRun_RawTranscriptsOptIn(t *testing.T) {
	runDir := buildWorkflowRunDir(t, "journal_complete.jsonl")
	destDir := filepath.Join(t.TempDir(), "workflows", "wf_4650c05a-c39")

	if _, err := archiveWorkflowRun(context.Background(), runDir, []string{t.TempDir()}, destDir, t.TempDir(), true); err != nil {
		t.Fatalf("archiveWorkflowRun() error = %v", err)
	}

	for _, name := range []string{"agent-a6053d9fe85440cfe.jsonl", "agent-a6053d9fe85440cfe.meta.json", "agent-a6053d9fe85440cfe.md"} {
		if _, err := os.Stat(filepath.Join(destDir, "agents", name)); err != nil {
			t.Errorf("expected archived transcript %s: %v", name, err)
		}
	}
}

func TestArchiveWorkflowRun_MissingScriptFallsBackToRunID(t *testing.T) {
	runDir := buildWorkflowRunDir(t, "journal_interrupted.jsonl")
	destDir := filepath.Join(t.TempDir(), "workflows", "wf_4650c05a-c39")

	// Empty scripts dir: title must fall back to the run ID.
	if _, err := archiveWorkflowRun(context.Background(), runDir, []string{t.TempDir()}, destDir, t.TempDir(), false); err != nil {
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

func TestArchiveWorkflowRun_CopiesDaemonEventsAndDurations(t *testing.T) {
	runDir := buildWorkflowRunDir(t, "journal_complete.jsonl")
	destDir := filepath.Join(t.TempDir(), "workflows", "wf_4650c05a-c39")

	daemonDir := t.TempDir()
	events := `{"event":{"kind":"agent_started","agent_id":"a6053d9fe85440cfe","timestamp":"2026-06-10T10:00:00Z"}}
{"event":{"kind":"agent_completed","agent_id":"a6053d9fe85440cfe","timestamp":"2026-06-10T10:03:20Z"}}
{"event":{"kind":"agent_started","agent_id":"a9172cda79b99f9ad","timestamp":"2026-06-10T10:00:05Z"}}
`
	if err := os.WriteFile(filepath.Join(daemonDir, "wf_4650c05a-c39.jsonl"), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}

	ar, err := archiveWorkflowRun(context.Background(), runDir, []string{t.TempDir()}, destDir, daemonDir, false)
	if err != nil {
		t.Fatalf("archiveWorkflowRun() error = %v", err)
	}

	copied, err := os.ReadFile(filepath.Join(destDir, "events.jsonl"))
	if err != nil {
		t.Fatalf("daemon events not merged as events.jsonl: %v", err)
	}
	if string(copied) != events {
		t.Errorf("events.jsonl content mismatch:\n%s", copied)
	}

	if d := ar.Durations["a6053d9fe85440cfe"]; d != 3*time.Minute+20*time.Second {
		t.Errorf("duration = %v, want 3m20s", d)
	}
	// Started but never completed: no duration entry.
	if _, ok := ar.Durations["a9172cda79b99f9ad"]; ok {
		t.Error("duration computed for agent without a completed event")
	}
}

// TestArchiveWorkflowRun_RemovesSupersededSubTranscripts asserts the archiver
// removes the upstream runtime's stray run-*-sub.* duplicate transcripts from
// the source run dir, while leaving everything else untouched.
func TestArchiveWorkflowRun_RemovesSupersededSubTranscripts(t *testing.T) {
	runDir := buildWorkflowRunDir(t, "journal_complete.jsonl")
	destDir := filepath.Join(t.TempDir(), "workflows", "wf_4650c05a-c39")

	// Stray run-*-sub.* duplicates the upstream Claude runtime leaves behind.
	stray := []string{"run-abc123-sub.jsonl", "run-abc123-sub.md"}
	for _, name := range stray {
		if err := os.WriteFile(filepath.Join(runDir, name), []byte("dup"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := archiveWorkflowRun(context.Background(), runDir, []string{t.TempDir()}, destDir, t.TempDir(), false); err != nil {
		t.Fatalf("archiveWorkflowRun() error = %v", err)
	}

	for _, name := range stray {
		if _, err := os.Stat(filepath.Join(runDir, name)); !os.IsNotExist(err) {
			t.Errorf("superseded %s not removed from source run dir", name)
		}
	}
	// The real journal and agent transcript must survive (not run-*-sub.*).
	for _, name := range []string{"journal.jsonl", "agent-a6053d9fe85440cfe.jsonl"} {
		if _, err := os.Stat(filepath.Join(runDir, name)); err != nil {
			t.Errorf("archiver removed non-superseded file %s: %v", name, err)
		}
	}
}

// TestSortedAgentIDs_ChronologicalFromEvents asserts agents render in
// start-time order (matching the live TUI), derived from a synthetic daemon
// events.jsonl, with a lexical fallback for agents missing a start timestamp
// and a stable order when no event log is present at all.
func TestSortedAgentIDs_ChronologicalFromEvents(t *testing.T) {
	// Lexical order of these IDs is z-inventory, z-no-event, z-synthesize.
	// Chronological order (by started time) must put inventory first and
	// synthesize last, with the timestamp-less agent sorting after the two
	// that have a known start time.
	run := &agentworkflow.WorkflowRun{
		RunID: "wf_chrono",
		Agents: map[string]*agentworkflow.AgentRun{
			"z-synthesize": {Started: true},
			"z-inventory":  {Started: true},
			"z-no-event":   {Started: true},
		},
	}

	eventsPath := filepath.Join(t.TempDir(), "events.jsonl")
	events := `{"event":{"kind":"agent_started","agent_id":"z-synthesize","timestamp":"2026-06-10T10:05:00Z"}}
{"event":{"kind":"agent_started","agent_id":"z-inventory","timestamp":"2026-06-10T10:00:00Z"}}
{"event":{"kind":"agent_completed","agent_id":"z-inventory","timestamp":"2026-06-10T10:02:00Z"}}
`
	if err := os.WriteFile(eventsPath, []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}

	durations, started := loadAgentDurations(eventsPath)
	if started == nil {
		t.Fatal("loadAgentDurations returned nil started map")
	}
	if _, ok := started["z-inventory"]; !ok {
		t.Error("started map missing z-inventory")
	}
	if _, ok := started["z-synthesize"]; !ok {
		t.Error("started map missing z-synthesize")
	}
	if _, ok := started["z-no-event"]; ok {
		t.Error("started map should not contain an agent with no event")
	}
	if d := durations["z-inventory"]; d != 2*time.Minute {
		t.Errorf("z-inventory duration = %v, want 2m", d)
	}

	got := sortedAgentIDs(run, started)
	want := []string{"z-inventory", "z-synthesize", "z-no-event"}
	if len(got) != len(want) {
		t.Fatalf("sortedAgentIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sortedAgentIDs = %v, want %v", got, want)
			break
		}
	}

	// With no event log (nil started), ordering falls back to lexical.
	lexical := sortedAgentIDs(run, nil)
	wantLexical := []string{"z-inventory", "z-no-event", "z-synthesize"}
	for i := range wantLexical {
		if lexical[i] != wantLexical[i] {
			t.Errorf("lexical fallback = %v, want %v", lexical, wantLexical)
			break
		}
	}
}

func TestWorkflowRunsSection_Golden(t *testing.T) {
	run := &agentworkflow.WorkflowRun{
		RunID: "wf_run1",
		Meta: &workflows.ScriptMeta{
			Name:   "release-survey",
			Phases: []workflows.PhaseMeta{{Title: "Survey"}, {Title: "Critique"}},
		},
		Agents: map[string]*agentworkflow.AgentRun{
			"agent-one": {
				Started: true,
				Result:  json.RawMessage(`"all good\nsecond line"`),
				Entries: []transcript.UnifiedEntry{userEntry("Survey the nb module\nwith extra detail")},
			},
			"agent-two": {Started: true},
		},
	}
	ar := &archivedWorkflowRun{
		RunID:     "wf_run1",
		Title:     "release-survey",
		Run:       run,
		Durations: map[string]time.Duration{"agent-one": 3*time.Minute + 20*time.Second},
		AgentDocs: map[string]bool{"agent-one": true},
	}

	got := renderWorkflowRunsSection("job-1", []*archivedWorkflowRun{ar})

	want := "## Workflow Run: release-survey\n" +
		"\n" +
		"- Run ID: `wf_run1`\n" +
		"- Agents: 2 started / 1 completed\n" +
		"- Phases: Survey → Critique\n" +
		"- Artifacts: [.artifacts/job-1/workflows/wf_run1/](.artifacts/job-1/workflows/wf_run1/)\n" +
		"\n" +
		"### Agents\n" +
		"\n" +
		"#### `agent-one`\n" +
		"\n" +
		"- Prompt: Survey the nb module\n" +
		"- Duration: 3m20s\n" +
		"- Transcript: [agent-agent-one.md](.artifacts/job-1/workflows/wf_run1/agents/agent-agent-one.md)\n" +
		"- Result:\n" +
		"\n" +
		"    all good\n" +
		"    second line\n" +
		"\n" +
		"#### `agent-two`\n" +
		"\n" +
		"- Result: _(none — agent never returned a result)_\n"

	if got != want {
		t.Errorf("section mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestWorkflowRunsSection_ResultCapTruncation(t *testing.T) {
	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	result, err := json.Marshal(strings.Join(lines, "\n"))
	if err != nil {
		t.Fatal(err)
	}

	ar := &archivedWorkflowRun{
		RunID: "wf_run1",
		Title: "big-results",
		Run: &agentworkflow.WorkflowRun{
			RunID: "wf_run1",
			Agents: map[string]*agentworkflow.AgentRun{
				"agent-one": {Started: true, Result: json.RawMessage(result)},
			},
		},
	}

	got := renderWorkflowRunsSection("job-1", []*archivedWorkflowRun{ar})

	if !strings.Contains(got, "    line 15\n") {
		t.Errorf("line 15 (cap boundary) missing:\n%s", got)
	}
	if strings.Contains(got, "line 16") {
		t.Errorf("result not capped at %d lines:\n%s", workflowResultCapLines, got)
	}
	wantLink := "[full result in .artifacts/job-1/workflows/wf_run1/summary.md](.artifacts/job-1/workflows/wf_run1/summary.md)"
	if !strings.Contains(got, wantLink) {
		t.Errorf("truncation link missing %q:\n%s", wantLink, got)
	}
}

func TestArchiveWorkflowRunsFromDirs_NoRunsNoSection(t *testing.T) {
	job := writeTranscriptTestJob(t, transcriptTestJobContent)
	before, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}

	plan := &Plan{Directory: t.TempDir()}
	// Session dir exists but holds no wf_* runs.
	if err := archiveWorkflowRunsFromDirs(context.Background(), job, plan, []string{t.TempDir()}, t.TempDir(), false); err != nil {
		t.Fatalf("archiveWorkflowRunsFromDirs() error = %v", err)
	}

	after, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("job file modified despite no workflow runs:\n%s", after)
	}
	if strings.Contains(string(after), workflowRunsSectionHeader) {
		t.Error("workflow runs section written for a run-less session")
	}
}

func TestArchiveWorkflowRunsFromDirs_SectionIdempotentReArchive(t *testing.T) {
	slug := t.TempDir()
	runDir := filepath.Join(slug, "subagents", "workflows", "wf_aaa")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	journal, err := os.ReadFile(filepath.Join("testdata", "workflows", "journal_complete.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "journal.jsonl"), journal, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "agent-a6053d9fe85440cfe.jsonl"), []byte(sampleAgentTranscript), 0o600); err != nil {
		t.Fatal(err)
	}

	job := writeTranscriptTestJob(t, transcriptTestJobContent+"\n# Agent Chat Transcript\n\nthe transcript\n")
	plan := &Plan{Directory: t.TempDir()}

	if err := archiveWorkflowRunsFromDirs(context.Background(), job, plan, []string{slug}, t.TempDir(), false); err != nil {
		t.Fatalf("archiveWorkflowRunsFromDirs() error = %v", err)
	}

	first, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(first)
	secIdx := strings.Index(got, workflowRunsSectionHeader)
	trIdx := strings.Index(got, transcriptSectionHeader)
	if secIdx == -1 {
		t.Fatalf("workflow runs section missing:\n%s", got)
	}
	if trIdx == -1 || secIdx > trIdx {
		t.Errorf("section not inserted before the transcript section:\n%s", got)
	}
	for _, want := range []string{
		"## Workflow Run: wf_aaa",
		"- Agents: 2 started / 2 completed",
		"- Prompt: Survey the nb module for release readiness",
		"[agent-a6053d9fe85440cfe.md](.artifacts/test-job/workflows/wf_aaa/agents/agent-a6053d9fe85440cfe.md)",
		"the transcript",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("job file missing %q\n---\n%s", want, got)
		}
	}

	// Re-archiving rebuilds the section wholesale: no duplicate sections,
	// byte-identical file.
	if err := archiveWorkflowRunsFromDirs(context.Background(), job, plan, []string{slug}, t.TempDir(), false); err != nil {
		t.Fatalf("repeat archiveWorkflowRunsFromDirs() error = %v", err)
	}
	second, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("re-archive changed the job file:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	if n := strings.Count(string(second), workflowRunsSectionHeader); n != 1 {
		t.Errorf("expected exactly one workflow runs section, got %d", n)
	}
}

func TestArchiveWorkflowRunsFromDirs_MergesAcrossSlugDirs(t *testing.T) {
	// Simulate slug fragmentation: the session id appears under two
	// ~/.claude/projects/<slug>/ dirs. Slug A holds run wf_aaa (no scripts);
	// slug B holds run wf_bbb AND the workflows/scripts dir containing the
	// script for wf_aaa.
	slugA := t.TempDir()
	slugB := t.TempDir()

	journal, err := os.ReadFile(filepath.Join("testdata", "workflows", "journal_complete.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	runA := filepath.Join(slugA, "subagents", "workflows", "wf_aaa")
	runB := filepath.Join(slugB, "subagents", "workflows", "wf_bbb")
	for _, dir := range []string{runA, runB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "journal.jsonl"), journal, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	scriptsB := filepath.Join(slugB, "workflows", "scripts")
	if err := os.MkdirAll(scriptsB, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scriptsB, "cross-slug-flow-wf_aaa.js"),
		[]byte("export const meta = { name: 'cross-slug-flow' }"), 0o600); err != nil {
		t.Fatal(err)
	}

	planDir := t.TempDir()
	job := &Job{ID: "job-merge", Type: JobTypeHeadlessAgent}
	plan := &Plan{Directory: planDir}

	if err := archiveWorkflowRunsFromDirs(context.Background(), job, plan, []string{slugA, slugB}, t.TempDir(), false); err != nil {
		t.Fatalf("archiveWorkflowRunsFromDirs() error = %v", err)
	}

	// Run from slug A was archived, with its script found under slug B.
	destA := filepath.Join(planDir, ".artifacts", "job-merge", "workflows", "wf_aaa")
	if _, err := os.Stat(filepath.Join(destA, "journal.jsonl")); err != nil {
		t.Errorf("wf_aaa journal not archived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destA, "script.js")); err != nil {
		t.Errorf("wf_aaa script not found across slug dirs: %v", err)
	}
	summaryA, err := os.ReadFile(filepath.Join(destA, "summary.md"))
	if err != nil {
		t.Fatalf("wf_aaa summary.md not written: %v", err)
	}
	if !strings.Contains(string(summaryA), "# Workflow Run: cross-slug-flow") {
		t.Errorf("wf_aaa summary missing cross-slug script title:\n%s", summaryA)
	}

	// Run from slug B was archived too (no script: run-ID fallback title).
	destB := filepath.Join(planDir, ".artifacts", "job-merge", "workflows", "wf_bbb")
	if _, err := os.Stat(filepath.Join(destB, "journal.jsonl")); err != nil {
		t.Errorf("wf_bbb journal not archived: %v", err)
	}
	summaryB, err := os.ReadFile(filepath.Join(destB, "summary.md"))
	if err != nil {
		t.Fatalf("wf_bbb summary.md not written: %v", err)
	}
	if !strings.Contains(string(summaryB), "# Workflow Run: wf_bbb") {
		t.Errorf("wf_bbb summary missing runId fallback title:\n%s", summaryB)
	}

	// Re-archiving is an idempotent overwrite, not an error.
	if err := archiveWorkflowRunsFromDirs(context.Background(), job, plan, []string{slugA, slugB}, t.TempDir(), false); err != nil {
		t.Fatalf("repeat archiveWorkflowRunsFromDirs() error = %v", err)
	}
}

func TestArchiveWorkflowRun_MalformedJournalStillWritesSummary(t *testing.T) {
	runDir := buildWorkflowRunDir(t, "journal_malformed.jsonl")
	destDir := filepath.Join(t.TempDir(), "workflows", "wf_4650c05a-c39")

	if _, err := archiveWorkflowRun(context.Background(), runDir, []string{t.TempDir()}, destDir, t.TempDir(), false); err != nil {
		t.Fatalf("archiveWorkflowRun() error = %v", err)
	}

	summary, err := os.ReadFile(filepath.Join(destDir, "summary.md"))
	if err != nil {
		t.Fatal(err)
	}
	// Only the parseable journal lines should be counted (agent-one started
	// and completed; the transcript-only agent adds another started entry).
	if !strings.Contains(string(summary), "Agents completed: 1") {
		t.Errorf("summary should count only parseable events:\n%s", summary)
	}
}
