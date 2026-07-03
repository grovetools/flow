package orchestration

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dispatchRecordingLLMClient records whether Complete was ever invoked. Used to prove
// that agent-responded chats (responder: agent) are NEVER dispatched to an LLM.
type dispatchRecordingLLMClient struct {
	called bool
}

func (c *dispatchRecordingLLMClient) Complete(ctx context.Context, job *Job, plan *Plan, prompt string, opts LLMOptions, output io.Writer) (string, error) {
	c.called = true
	return "this response must never be produced", nil
}

func TestIsAgentResponded(t *testing.T) {
	tests := []struct {
		name string
		job  Job
		want bool
	}{
		{"chat with responder agent", Job{Type: JobTypeChat, Responder: "agent"}, true},
		{"chat with responder oracle", Job{Type: JobTypeChat, Responder: "oracle"}, false},
		{"chat with empty responder", Job{Type: JobTypeChat}, false},
		{"oneshot with responder agent", Job{Type: JobTypeOneshot, Responder: "agent"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.job.IsAgentResponded(); got != tt.want {
				t.Errorf("IsAgentResponded() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestIsAgentResponded_OracleEquivalentToEmpty: the dispatch guard must treat
// an explicit responder: oracle identically to an absent responder field —
// both mean the oracle path (LLM API dispatch allowed).
func TestIsAgentResponded_OracleEquivalentToEmpty(t *testing.T) {
	empty := Job{Type: JobTypeChat}
	oracle := Job{Type: JobTypeChat, Responder: "oracle"}
	if empty.IsAgentResponded() != oracle.IsAgentResponded() {
		t.Errorf("dispatch guard differs: empty=%v oracle=%v; responder: oracle must behave exactly like an absent field",
			empty.IsAgentResponded(), oracle.IsAgentResponded())
	}
	if oracle.IsAgentResponded() {
		t.Error("responder: oracle must not be treated as agent-responded")
	}
}

// TestExecuteChatJob_AgentRespondedNeverDispatches: an agent-responded chat
// with a trailing NON-EMPTY user-template turn (which would dispatch under the
// normal last-turn rule) must never invoke the LLM client, must end in
// pending_user, and must leave the artifact byte-for-byte unchanged.
func TestExecuteChatJob_AgentRespondedNeverDispatches(t *testing.T) {
	tmpDir := t.TempDir()

	plan := &Plan{
		Name:      "test-plan",
		Directory: tmpDir,
		Jobs:      []*Job{},
		JobsByID:  make(map[string]*Job),
	}

	jobContent := `---
id: design-chat-1
title: design-chat
status: pending_user
type: chat
responder: agent
template: chat
---

<!-- grove: {"template": "chat"} -->

Please implement feature X as described in the requirements.
`
	jobPath := filepath.Join(tmpDir, "01-design-chat.md")
	if err := os.WriteFile(jobPath, []byte(jobContent), 0o600); err != nil {
		t.Fatal(err)
	}

	job, err := LoadJob(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	job.Filename = "01-design-chat.md"
	job.FilePath = jobPath

	if !job.IsAgentResponded() {
		t.Fatal("loaded job should be agent-responded (responder: agent survived LoadJob)")
	}

	client := &dispatchRecordingLLMClient{}
	executor := NewOneShotExecutor(client, nil)

	if err := executor.Execute(context.Background(), job, plan); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if client.called {
		t.Error("LLM client was invoked for an agent-responded chat; responder: agent must never dispatch")
	}
	if job.Status != JobStatusPendingUser {
		t.Errorf("job status = %v, want pending_user", job.Status)
	}

	after, err := os.ReadFile(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != jobContent {
		t.Errorf("job file content changed:\n--- before ---\n%s\n--- after ---\n%s", jobContent, string(after))
	}
}

// TestExecuteChatJob_AgentRespondedAssertsPendingUser: running an
// agent-responded chat whose status drifted to pending re-asserts pending_user
// via the same status write the waiting-for-user skip path uses.
func TestExecuteChatJob_AgentRespondedAssertsPendingUser(t *testing.T) {
	tmpDir := t.TempDir()

	plan := &Plan{
		Name:      "test-plan",
		Directory: tmpDir,
		Jobs:      []*Job{},
		JobsByID:  make(map[string]*Job),
	}

	jobContent := `---
id: design-chat-2
title: design-chat
status: pending
type: chat
responder: agent
template: chat
---

<!-- grove: {"template": "chat"} -->

Draft the design.
`
	jobPath := filepath.Join(tmpDir, "01-design-chat.md")
	if err := os.WriteFile(jobPath, []byte(jobContent), 0o600); err != nil {
		t.Fatal(err)
	}

	job, err := LoadJob(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	job.Filename = "01-design-chat.md"
	job.FilePath = jobPath

	client := &dispatchRecordingLLMClient{}
	executor := NewOneShotExecutor(client, nil)

	if err := executor.Execute(context.Background(), job, plan); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if client.called {
		t.Error("LLM client was invoked for an agent-responded chat")
	}
	if job.Status != JobStatusPendingUser {
		t.Errorf("job status = %v, want pending_user", job.Status)
	}

	after, err := os.ReadFile(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "status: pending_user") {
		t.Errorf("job file not updated to pending_user:\n%s", string(after))
	}
	if !strings.Contains(string(after), "responder: agent") {
		t.Errorf("responder field lost during status write:\n%s", string(after))
	}
}

// TestUpdateFrontmatter_ResponderSurvivesStatusTransition: the responder field
// must ride through UpdateFrontmatter -> UpdateFrontmatterNode untouched when
// other fields (status) are rewritten.
func TestUpdateFrontmatter_ResponderSurvivesStatusTransition(t *testing.T) {
	content := []byte(`---
id: design-chat-3
title: design-chat
status: pending_user
type: chat
responder: agent
---

<!-- grove: {"template": "chat"} -->

Body content.
`)

	updated, err := UpdateFrontmatter(content, map[string]interface{}{
		"status": "completed",
	})
	if err != nil {
		t.Fatalf("UpdateFrontmatter() error = %v", err)
	}

	if !strings.Contains(string(updated), "status: completed") {
		t.Errorf("status not updated:\n%s", string(updated))
	}
	if !strings.Contains(string(updated), "responder: agent") {
		t.Errorf("responder: agent did not survive the status transition:\n%s", string(updated))
	}
}
