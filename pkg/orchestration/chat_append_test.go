package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chatAppendFM is the shared frontmatter prefix for the buildChatAppendBytes
// table (the shared-const pattern from TestInspectTrailingChatTurn, which lives
// in the external package chat_parser_test.go — this internal test copies only
// the pattern, per J2 addendum R5a). template: chat gives the job a frontmatter
// template so minted markers inherit it.
const chatAppendFM = `---
id: test-plan
title: Test Plan
type: chat
template: chat
---

`

// TestBuildChatAppendBytes exercises the pure classifier directly (unexported,
// hence the internal package). Each case confirms the addendum-R1 turn-list
// drive: the decision is keyed on the last parsed turn, never on
// InspectTrailingChatTurn.OrphanBeforeMarker.
func TestBuildChatAppendBytes(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		text       string
		force      bool
		wantErr    bool
		errSubstr  string
		wantMinted bool
	}{
		{
			// Normal pending_user shape: a bare trailing marker after an LLM
			// response. Append after the marker; no marker minted.
			name: "bare trailing marker append (no trip)",
			body: "Initial prompt\n\n<!-- grove: {\"model\": \"claude-3-opus\"} -->\nAn LLM response\n\n<!-- grove: {\"template\": \"chat\"} -->",
			text: "Please add more detail",
		},
		{
			// Body ends with an LLM response and NO trailing marker — case (b).
			// This is the exact state where OrphanBeforeMarker is true, so the
			// classifier must NOT consult it: it mints the marker instead.
			name:       "response without trailing marker mints",
			body:       "Initial prompt\n\n<!-- grove: {\"model\": \"claude-3-opus\"} -->\nAn LLM response",
			text:       "Follow-up question",
			wantMinted: true,
		},
		{
			// Empty body → implicit first user turn (case c).
			name: "empty body implicit first turn",
			body: "",
			text: "My first message",
		},
		{
			// A user turn is already pending: refuse without --force.
			name:      "pending user turn refuses",
			body:      "<!-- grove: {\"template\": \"chat\"} -->\nAlready typed this",
			text:      "More text",
			wantErr:   true,
			errSubstr: "already pending",
		},
		{
			// ...but --force extends that same turn (still one pending turn).
			name:  "pending user turn force extends",
			body:  "<!-- grove: {\"template\": \"chat\"} -->\nAlready typed this",
			text:  "More text",
			force: true,
		},
		{
			// User content stranded above a bare trailing marker — orphan.
			name:      "orphan precondition refuses",
			body:      "My real question\n\n<!-- grove: {\"template\": \"chat\"} -->",
			text:      "x",
			wantErr:   true,
			errSubstr: "new line below",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := []byte(chatAppendFM + tt.body)
			pre, _ := ParseChatFile(content)

			got, minted, err := buildChatAppendBytes(content, tt.text, "chat", "01-chat-job.md", tt.force)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("buildChatAppendBytes() = nil error, want error")
				}
				if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errSubstr)
				}
				if got != nil {
					t.Errorf("want nil bytes on error, got %d bytes", len(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("buildChatAppendBytes() error: %v", err)
			}
			if minted != tt.wantMinted {
				t.Errorf("minted = %v, want %v", minted, tt.wantMinted)
			}

			// The appended text must be the FINAL content — no trailing marker
			// orphaning it. This is the whole point of J2: a said turn is always
			// dispatchable.
			trail := InspectTrailingChatTurn(got)
			if !trail.HasUserTurn || trail.OrphanBeforeMarker {
				t.Errorf("post-state not dispatchable: HasUserTurn=%v OrphanBeforeMarker=%v", trail.HasUserTurn, trail.OrphanBeforeMarker)
			}

			post, perr := ParseChatFile(got)
			if perr != nil {
				t.Fatalf("re-parse: %v", perr)
			}
			last := post[len(post)-1]
			if last.Speaker != "user" || strings.TrimSpace(last.Content) == "" {
				t.Errorf("last turn = {speaker=%q, content=%q}, want a content-bearing user turn", last.Speaker, last.Content)
			}
			if !strings.Contains(last.Content, tt.text) {
				t.Errorf("last turn content %q does not contain appended text %q", last.Content, tt.text)
			}
			if tt.wantMinted {
				if len(post) != len(pre)+1 {
					t.Errorf("minted turn count = %d, want %d (+1)", len(post), len(pre)+1)
				}
			} else if len(post) != len(pre) {
				t.Errorf("extended turn count = %d, want %d (unchanged)", len(post), len(pre))
			}
		})
	}
}

// TestBuildChatAppendBytes_ParseError confirms an unparseable body surfaces a
// parse error wrapped with the job path, BEFORE any trailing-state logic
// (addendum R2).
func TestBuildChatAppendBytes_ParseError(t *testing.T) {
	content := []byte("---\nid: x\nno closing delimiter here")
	got, _, err := buildChatAppendBytes(content, "hi", "chat", "broken.md", false)
	if err == nil {
		t.Fatal("want a parse error, got nil")
	}
	if !strings.Contains(err.Error(), "broken.md") {
		t.Errorf("parse error %q does not name the job path", err.Error())
	}
	if got != nil {
		t.Errorf("want nil bytes on parse error, got %d", len(got))
	}
}

// TestBuildChatAppendBytes_MintTemplateInheritance confirms addendum R4: a
// minted marker inherits the most recent user directive's template, not the
// "chat" default, when the chat uses per-turn directive templates.
func TestBuildChatAppendBytes_MintTemplateInheritance(t *testing.T) {
	// Last user directive uses "refine-plan"; body ends on an LLM turn (case b).
	body := "First\n\n<!-- grove: {\"template\": \"refine-plan\"} -->\n> a follow-up\n\n<!-- grove: {\"model\": \"x\"} -->\nAn answer"
	content := []byte(chatAppendFM + body)
	got, minted, err := buildChatAppendBytes(content, "next", "", "01-chat-job.md", false)
	if err != nil {
		t.Fatalf("buildChatAppendBytes: %v", err)
	}
	if !minted {
		t.Fatal("expected a minted marker for a case-(b) body")
	}
	if !strings.Contains(string(got), `"template": "refine-plan"`) {
		t.Errorf("minted marker did not inherit the directive template refine-plan:\n%s", string(got))
	}
}

// TestAppendChatUserTurn_BareMarkerRoundTrip is the persister happy path: append
// after a bare trailing marker, then confirm the file grew and the trailing
// state is dispatchable (no trip).
func TestAppendChatUserTurn_BareMarkerRoundTrip(t *testing.T) {
	plan, job := newChatJobFixture(t, "", "Initial prompt\n\n<!-- grove: {\"model\": \"x\"} -->\nAn answer\n\n<!-- grove: {\"template\": \"chat\"} -->")
	_ = plan
	before, _ := os.ReadFile(job.FilePath)

	if err := NewStatePersister().AppendChatUserTurn(job, "Please continue", false); err != nil {
		t.Fatalf("AppendChatUserTurn: %v", err)
	}

	after, _ := os.ReadFile(job.FilePath)
	if len(after) <= len(before) {
		t.Errorf("file did not grow: before=%d after=%d", len(before), len(after))
	}
	if !strings.HasSuffix(strings.TrimSpace(string(after)), "Please continue") {
		t.Errorf("appended text is not the final content:\n%s", string(after))
	}
	trail := InspectTrailingChatTurn(after)
	if !trail.HasUserTurn || trail.OrphanBeforeMarker {
		t.Errorf("post-state not dispatchable: %+v", trail)
	}
}

// TestAppendChatUserTurn_TrailingMarkerTrap is the trap J2 exists to prevent: a
// hand-edit that leaves a trailing marker after the text orphans the turn. `say`
// must always land the text as the final bytes, leaving a dispatchable state.
func TestAppendChatUserTurn_TrailingMarkerTrap(t *testing.T) {
	// Body ends with an LLM response and no trailing marker (case b) — the exact
	// state a naive appender would mishandle by pasting a marker after the text.
	_, job := newChatJobFixture(t, "", "Q\n\n<!-- grove: {\"model\": \"x\"} -->\nAn answer")

	if err := NewStatePersister().AppendChatUserTurn(job, "next turn", false); err != nil {
		t.Fatalf("AppendChatUserTurn: %v", err)
	}
	after, _ := os.ReadFile(job.FilePath)
	trail := InspectTrailingChatTurn(after)
	if !trail.HasUserTurn || trail.OrphanBeforeMarker {
		t.Errorf("trap not avoided — post-state undispatchable: %+v\n%s", trail, string(after))
	}
}

// TestAppendChatUserTurn_PendingTurn covers the pending-turn refusal and the
// --force extend, asserting the refusal leaves the bytes byte-identical.
func TestAppendChatUserTurn_PendingTurn(t *testing.T) {
	_, job := newChatJobFixture(t, "", "<!-- grove: {\"template\": \"chat\"} -->\nAlready pending")
	before, _ := os.ReadFile(job.FilePath)

	err := NewStatePersister().AppendChatUserTurn(job, "more", false)
	if err == nil || !strings.Contains(err.Error(), "already pending") {
		t.Fatalf("want an 'already pending' refusal, got %v", err)
	}
	after, _ := os.ReadFile(job.FilePath)
	if string(after) != string(before) {
		t.Errorf("refusal mutated the file:\n%s", string(after))
	}

	if err := NewStatePersister().AppendChatUserTurn(job, "more", true); err != nil {
		t.Fatalf("--force extend: %v", err)
	}
	extended, _ := os.ReadFile(job.FilePath)
	if !strings.Contains(string(extended), "Already pending") || !strings.HasSuffix(strings.TrimSpace(string(extended)), "more") {
		t.Errorf("force did not extend the same turn:\n%s", string(extended))
	}
}

// TestAppendChatUserTurn_OrphanRefused confirms an orphan precondition refuses
// with bytes unchanged.
func TestAppendChatUserTurn_OrphanRefused(t *testing.T) {
	_, job := newChatJobFixture(t, "", "My real question\n\n<!-- grove: {\"template\": \"chat\"} -->")
	before, _ := os.ReadFile(job.FilePath)

	err := NewStatePersister().AppendChatUserTurn(job, "x", false)
	if err == nil {
		t.Fatal("want an orphan refusal, got nil")
	}
	after, _ := os.ReadFile(job.FilePath)
	if string(after) != string(before) {
		t.Errorf("orphan refusal mutated the file:\n%s", string(after))
	}
}

// TestAppendChatUserTurn_RunningRefused confirms a running job refuses loudly
// with no write.
func TestAppendChatUserTurn_RunningRefused(t *testing.T) {
	_, job := newChatJobFixture(t, "", "<!-- grove: {\"template\": \"chat\"} -->")
	job.Status = JobStatusRunning
	before, _ := os.ReadFile(job.FilePath)

	err := NewStatePersister().AppendChatUserTurn(job, "hi", false)
	if err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("want a running refusal, got %v", err)
	}
	after, _ := os.ReadFile(job.FilePath)
	if string(after) != string(before) {
		t.Errorf("running refusal mutated the file")
	}
}

// TestAppendChatUserTurn_NonChatRefused confirms a non-chat job is refused (the
// universal guard, mech contract A4).
func TestAppendChatUserTurn_NonChatRefused(t *testing.T) {
	_, job := newChatJobFixture(t, "", "<!-- grove: {\"template\": \"chat\"} -->")
	job.Type = JobTypeOneshot

	err := NewStatePersister().AppendChatUserTurn(job, "hi", false)
	if err == nil || !strings.Contains(err.Error(), "not chat") {
		t.Fatalf("want a non-chat refusal, got %v", err)
	}
}

// TestAppendChatUserTurn_LiveLockRefused confirms a live lock PID refuses, while
// a dead PID proceeds (read-only liveness probe).
func TestAppendChatUserTurn_LiveLockRefused(t *testing.T) {
	_, job := newChatJobFixture(t, "", "<!-- grove: {\"template\": \"chat\"} -->")

	// A live lock (this process) blocks the append.
	if err := CreateLockFile(job.FilePath, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	err := NewStatePersister().AppendChatUserTurn(job, "hi", false)
	if err == nil || !strings.Contains(err.Error(), "in flight") {
		t.Fatalf("want a turn-in-flight refusal, got %v", err)
	}
	_ = RemoveLockFile(job.FilePath)

	// A dead PID's lock is stale — the append proceeds (sp.lockFile steals it).
	if err := CreateLockFile(job.FilePath, 2147483646); err != nil {
		t.Fatal(err)
	}
	if err := NewStatePersister().AppendChatUserTurn(job, "hi", false); err != nil {
		t.Fatalf("dead-PID lock should not block the append: %v", err)
	}
}

// TestAppendChatUserTurn_MockDispatch is the end-to-end round trip: append a
// turn, then dispatch it against the mock LLM and confirm a response landed —
// proving a said turn actually fires.
func TestAppendChatUserTurn_MockDispatch(t *testing.T) {
	mockResponse := filepath.Join(t.TempDir(), "mock-response.md")
	if err := os.WriteFile(mockResponse, []byte("mock oracle response"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROVE_MOCK_LLM_RESPONSE_FILE", mockResponse)

	plan, job := newChatJobFixture(t, "rules_file: ctx.rules\n", "<!-- grove: {\"template\": \"chat\"} -->")
	gitInitDir(t, plan.Directory)
	if err := os.MkdirAll(filepath.Join(plan.Directory, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plan.Directory, "src", "a.go"), []byte("package src\n\nvar A = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plan.Directory, "ctx.rules"), []byte("src/a.go\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := NewStatePersister().AppendChatUserTurn(job, "Please answer.", false); err != nil {
		t.Fatalf("AppendChatUserTurn: %v", err)
	}

	// Re-load so the executor sees the appended user turn and fresh status.
	fresh, err := LoadJob(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	fresh.FilePath = job.FilePath
	fresh.Filename = job.Filename

	executor := NewOneShotExecutor(NewMockLLMClient(), nil)
	if err := executor.Execute(context.Background(), fresh, plan); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	after, _ := os.ReadFile(job.FilePath)
	if !strings.Contains(string(after), "## LLM Response") || !strings.Contains(string(after), "mock oracle response") {
		t.Errorf("said turn did not dispatch a response:\n%s", string(after))
	}
}
