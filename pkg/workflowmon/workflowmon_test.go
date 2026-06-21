package workflowmon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const sampleScript = `export const meta = {
  name: 'grovetools-release-survey',
  description: 'Deep survey of grovetools for treemux MVP release readiness',
  phases: [
    { title: 'Survey', detail: 'parallel deep-dives per subsystem' },
    { title: 'Probe', detail: 'build check, CLI quality, provider audit' },
    { title: 'Critique', detail: 'completeness critic on combined findings' },
  ],
}

const MISSION = ` + "`MISSION { braces } inside template literal`" + `
phase('Survey')
`

// Scriptmeta parser tests moved to core/pkg/workflows alongside the code.

// buildSession creates a fake Claude session dir with one workflow run.
func buildSession(t *testing.T) (sessionDir, runDir string) {
	t.Helper()
	sessionDir = t.TempDir()
	runDir = filepath.Join(sessionDir, "subagents", "workflows", "wf_test-run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptsDir := filepath.Join(sessionDir, "workflows", "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scriptsDir, "grovetools-release-survey-wf_test-run.js"), []byte(sampleScript), 0o600); err != nil {
		t.Fatal(err)
	}
	return sessionDir, runDir
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func nextEvent(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("event channel closed")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for event")
		return nil
	}
}

func TestFileSource_EmitsLifecycleEvents(t *testing.T) {
	sessionDir, runDir := buildSession(t)
	journal := filepath.Join(runDir, "journal.jsonl")
	appendFile(t, journal, `{"type":"started","key":"v2:aaa","agentId":"agent-one"}`+"\n")
	appendFile(t, filepath.Join(runDir, "agent-agent-one.jsonl"),
		`{"isSidechain":true,"agentId":"agent-one","type":"user","message":{"role":"user","content":"survey the nb module"}}`+"\n")

	src := NewFileSource(sessionDir, FileSourceOptions{PollInterval: 20 * time.Millisecond})
	defer src.Close()

	ev := nextEvent(t, src.Events())
	disc, ok := ev.(RunDiscovered)
	if !ok {
		t.Fatalf("expected RunDiscovered, got %T", ev)
	}
	if disc.RunID != "wf_test-run" || disc.Meta == nil || disc.Meta.Name != "grovetools-release-survey" {
		t.Fatalf("RunDiscovered = %+v meta=%+v", disc, disc.Meta)
	}

	ev = nextEvent(t, src.Events())
	started, ok := ev.(AgentStarted)
	if !ok {
		t.Fatalf("expected AgentStarted, got %T", ev)
	}
	if started.AgentID != "agent-one" || started.Prompt != "survey the nb module" {
		t.Fatalf("AgentStarted = %+v", started)
	}

	// Append a result and a second agent (no transcript yet → prompt upsert later).
	appendFile(t, journal,
		`{"type":"result","key":"v2:aaa","agentId":"agent-one","result":{"summary":"done"}}`+"\n"+
			`{"type":"started","key":"v2:bbb","agentId":"agent-two"}`+"\n")

	ev = nextEvent(t, src.Events())
	completed, ok := ev.(AgentCompleted)
	if !ok {
		t.Fatalf("expected AgentCompleted, got %T", ev)
	}
	if completed.AgentID != "agent-one" || completed.Result == "" {
		t.Fatalf("AgentCompleted = %+v", completed)
	}

	ev = nextEvent(t, src.Events())
	started2, ok := ev.(AgentStarted)
	if !ok {
		t.Fatalf("expected AgentStarted, got %T", ev)
	}
	if started2.AgentID != "agent-two" || started2.Prompt != "" {
		t.Fatalf("AgentStarted = %+v", started2)
	}

	// Transcript appears later → AgentStarted re-emitted with the prompt.
	appendFile(t, filepath.Join(runDir, "agent-agent-two.jsonl"),
		`{"message":{"content":[{"type":"text","text":"verify the build"}]}}`+"\n")

	ev = nextEvent(t, src.Events())
	upsert, ok := ev.(AgentStarted)
	if !ok {
		t.Fatalf("expected AgentStarted upsert, got %T", ev)
	}
	if upsert.AgentID != "agent-two" || upsert.Prompt != "verify the build" {
		t.Fatalf("AgentStarted upsert = %+v", upsert)
	}
}

func TestFileSource_StaleRequiresQuietJournalAndGoneSession(t *testing.T) {
	sessionDir, runDir := buildSession(t)
	journal := filepath.Join(runDir, "journal.jsonl")
	appendFile(t, journal, `{"type":"started","key":"v2:aaa","agentId":"agent-one"}`+"\n")
	// Backdate the journal beyond the staleness window.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(journal, old, old); err != nil {
		t.Fatal(err)
	}

	alive := true
	src := NewFileSource(sessionDir, FileSourceOptions{
		PollInterval: 20 * time.Millisecond,
		StaleAfter:   time.Minute,
		SessionAlive: func() bool { return alive },
	})
	defer src.Close()

	if _, ok := nextEvent(t, src.Events()).(RunDiscovered); !ok {
		t.Fatal("expected RunDiscovered first")
	}
	if _, ok := nextEvent(t, src.Events()).(AgentStarted); !ok {
		t.Fatal("expected AgentStarted")
	}

	// Session alive: quiet journal alone must NOT mark the run stale.
	select {
	case ev := <-src.Events():
		t.Fatalf("unexpected event while session alive: %#v", ev)
	case <-time.After(150 * time.Millisecond):
	}

	alive = false
	ev := nextEvent(t, src.Events())
	stale, ok := ev.(RunStale)
	if !ok {
		t.Fatalf("expected RunStale, got %T", ev)
	}
	if stale.RunID != "wf_test-run" {
		t.Fatalf("RunStale = %+v", stale)
	}
}

// drainUntil reads events until one of type T is seen (returns it, true) or
// the deadline elapses (returns zero, false). Used to assert that a specific
// terminal event does — or does not — arrive.
func waitForEventType[T Event](t *testing.T, ch <-chan Event, d time.Duration) (T, bool) {
	t.Helper()
	var zero T
	deadline := time.After(d)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return zero, false
			}
			if got, is := ev.(T); is {
				return got, true
			}
		case <-deadline:
			return zero, false
		}
	}
}

// TestFileSource_NoFalsePositiveOnLiveMidRunCountEquality is the critical
// no-false-positive case: a multi-phase run where started==completed occurs
// mid-run (a phase boundary) while the session is STILL LIVE must NOT emit
// RunCompleted. Completion is gated on session-end, never on count equality.
func TestFileSource_NoFalsePositiveOnLiveMidRunCountEquality(t *testing.T) {
	sessionDir, runDir := buildSession(t)
	journal := filepath.Join(runDir, "journal.jsonl")
	// Phase 1: one agent starts and completes → started==completed==1.
	appendFile(t, journal,
		`{"type":"started","key":"v2:p1","agentId":"agent-1"}`+"\n"+
			`{"type":"result","key":"v2:p1","agentId":"agent-1","result":{"summary":"done"}}`+"\n")
	// Backdate the journal beyond the staleness window so the mtime gate is
	// satisfied — isolating the session-alive factor as the only thing
	// holding back a terminal event.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(journal, old, old); err != nil {
		t.Fatal(err)
	}

	src := NewFileSource(sessionDir, FileSourceOptions{
		PollInterval: 20 * time.Millisecond,
		StaleAfter:   time.Minute,
		SessionAlive: func() bool { return true }, // mid-run: session LIVE
	})
	defer src.Close()

	// Despite started==completed AND a quiet (backdated) journal, a live
	// session means NO terminal event (neither RunCompleted nor RunStale).
	if ev, ok := waitForEventType[RunCompleted](t, src.Events(), 200*time.Millisecond); ok {
		t.Fatalf("false-positive RunCompleted on live mid-run count equality: %#v", ev)
	}
}

// TestFileSource_RunCompletedOnSessionEnd covers the clean terminal case:
// session ended + every started agent completed → RunCompleted.
func TestFileSource_RunCompletedOnSessionEnd(t *testing.T) {
	sessionDir, runDir := buildSession(t)
	journal := filepath.Join(runDir, "journal.jsonl")
	appendFile(t, journal,
		`{"type":"started","key":"v2:a","agentId":"agent-1"}`+"\n"+
			`{"type":"result","key":"v2:a","agentId":"agent-1","result":{"summary":"done"}}`+"\n")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(journal, old, old); err != nil {
		t.Fatal(err)
	}

	alive := true
	src := NewFileSource(sessionDir, FileSourceOptions{
		PollInterval: 20 * time.Millisecond,
		StaleAfter:   time.Minute,
		SessionAlive: func() bool { return alive },
	})
	defer src.Close()

	// While alive, no terminal event.
	if ev, ok := waitForEventType[RunCompleted](t, src.Events(), 150*time.Millisecond); ok {
		t.Fatalf("unexpected RunCompleted while session alive: %#v", ev)
	}

	// Session ends → clean completion (all started agents finished).
	alive = false
	done, ok := waitForEventType[RunCompleted](t, src.Events(), time.Second)
	if !ok {
		t.Fatal("expected RunCompleted after session end")
	}
	if done.RunID != "wf_test-run" {
		t.Fatalf("RunCompleted = %+v", done)
	}
}

// TestFileSource_RunStaleOnSessionEndWithStragglers covers the straggler
// case: session ended but a started agent never recorded a result → RunStale,
// never RunCompleted.
func TestFileSource_RunStaleOnSessionEndWithStragglers(t *testing.T) {
	sessionDir, runDir := buildSession(t)
	journal := filepath.Join(runDir, "journal.jsonl")
	// agent-1 completes; agent-2 starts but never finishes (straggler).
	appendFile(t, journal,
		`{"type":"started","key":"v2:a","agentId":"agent-1"}`+"\n"+
			`{"type":"result","key":"v2:a","agentId":"agent-1","result":{"summary":"done"}}`+"\n"+
			`{"type":"started","key":"v2:b","agentId":"agent-2"}`+"\n")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(journal, old, old); err != nil {
		t.Fatal(err)
	}

	src := NewFileSource(sessionDir, FileSourceOptions{
		PollInterval: 20 * time.Millisecond,
		StaleAfter:   time.Minute,
		SessionAlive: func() bool { return false }, // session already ended
	})
	defer src.Close()

	ev, ok := waitForEventType[RunStale](t, src.Events(), time.Second)
	if !ok {
		t.Fatal("expected RunStale for session-ended-with-stragglers")
	}
	if ev.RunID != "wf_test-run" {
		t.Fatalf("RunStale = %+v", ev)
	}
}

func TestFileSource_NoWorkflowsDirIsQuiet(t *testing.T) {
	src := NewFileSource(t.TempDir(), FileSourceOptions{PollInterval: 20 * time.Millisecond})
	select {
	case ev := <-src.Events():
		t.Fatalf("unexpected event: %#v", ev)
	case <-time.After(100 * time.Millisecond):
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-src.Events(); ok {
		t.Fatal("events channel should be closed after Close")
	}
}

func TestRenderResult(t *testing.T) {
	if got := renderResult([]byte(`"plain text"`)); got != "plain text" {
		t.Errorf("string result = %q", got)
	}
	if got := renderResult([]byte(`{"a":1}`)); got != "{\n  \"a\": 1\n}" {
		t.Errorf("object result = %q", got)
	}
	if got := renderResult(nil); got != "" {
		t.Errorf("empty result = %q", got)
	}
}
