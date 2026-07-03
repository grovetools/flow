package status

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grovetools/agentlogs/pkg/usage"
	"github.com/grovetools/flow/pkg/orchestration"
)

// writeArtifact writes a usage.Summary to a job's token-usage.json under planDir.
func writeArtifact(t *testing.T, planDir, jobID string, s usage.Summary) {
	t.Helper()
	p := orchestration.TokenUsageArtifactPath(planDir, jobID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func newModelForCells(planDir string) *Model {
	return &Model{
		PlanDir:          planDir,
		tokenColumnCache: map[string]string{},
		runningTokenCell: map[string]usage.Summary{},
	}
}

func TestRenderTokenColumnCell_ChatWithArtifact(t *testing.T) {
	dir := t.TempDir()
	m := newModelForCells(dir)
	writeArtifact(t, dir, "chat-1", usage.Summary{
		Usage:       usage.Usage{Input: 3707, Output: 411, CacheWrite5m: 27240, CacheRead: 20310},
		CostUSD:     0.31,
		ContextSize: 27242,
	})
	job := &orchestration.Job{ID: "chat-1", Type: orchestration.JobTypeChat, Status: orchestration.JobStatusPendingUser}

	got := m.renderTokenColumnCell(job)
	if !strings.Contains(got, "27.2k ctx") || !strings.Contains(got, "$0.31") {
		t.Errorf("pending_user chat with artifact = %q, want cost+ctx cell", got)
	}
}

func TestRenderTokenColumnCell_ChatWithoutArtifact(t *testing.T) {
	dir := t.TempDir()
	m := newModelForCells(dir)
	job := &orchestration.Job{ID: "chat-2", Type: orchestration.JobTypeChat, Status: orchestration.JobStatusPendingUser}

	got := m.renderTokenColumnCell(job)
	if !strings.Contains(got, "·") {
		t.Errorf("chat without artifact = %q, want placeholder ·", got)
	}
}

func TestRenderTokenColumnCell_ResponderAgentChat(t *testing.T) {
	// responder: agent chats never dispatch an LLM, so no artifact → placeholder,
	// never a misleading cost.
	dir := t.TempDir()
	m := newModelForCells(dir)
	job := &orchestration.Job{ID: "chat-3", Type: orchestration.JobTypeChat, Status: orchestration.JobStatusPendingUser, Responder: "agent"}

	got := m.renderTokenColumnCell(job)
	if !strings.Contains(got, "·") {
		t.Errorf("responder:agent chat = %q, want placeholder ·", got)
	}
}

func TestRenderTokenColumnCell_CompletedOneshot(t *testing.T) {
	dir := t.TempDir()
	m := newModelForCells(dir)
	writeArtifact(t, dir, "os-1", usage.Summary{
		Usage:       usage.Usage{Input: 1000, Output: 200},
		CostUSD:     0.02,
		ContextSize: 1000,
	})
	job := &orchestration.Job{ID: "os-1", Type: orchestration.JobTypeOneshot, Status: orchestration.JobStatusCompleted}

	got := m.renderTokenColumnCell(job)
	if !strings.Contains(got, "$0.02") {
		t.Errorf("completed oneshot with artifact = %q, want cost cell", got)
	}
}

func TestRenderTokenColumnCell_CompletedNoArtifact(t *testing.T) {
	dir := t.TempDir()
	m := newModelForCells(dir)
	job := &orchestration.Job{ID: "os-2", Type: orchestration.JobTypeOneshot, Status: orchestration.JobStatusCompleted}

	got := m.renderTokenColumnCell(job)
	if !strings.Contains(got, "-") {
		t.Errorf("completed job without artifact = %q, want -", got)
	}
}

func TestIsLiveAPIDirectJob(t *testing.T) {
	mk := func(typ orchestration.JobType, st orchestration.JobStatus) *orchestration.Job {
		return &orchestration.Job{Type: typ, Status: st}
	}
	cases := []struct {
		name string
		job  *orchestration.Job
		want bool
	}{
		{"pending_user chat", mk(orchestration.JobTypeChat, orchestration.JobStatusPendingUser), true},
		{"running chat", mk(orchestration.JobTypeChat, orchestration.JobStatusRunning), true},
		{"pending_llm oneshot", mk(orchestration.JobTypeOneshot, orchestration.JobStatusPendingLLM), true},
		{"completed chat", mk(orchestration.JobTypeChat, orchestration.JobStatusCompleted), false},
		{"running agent (not api-direct)", mk(orchestration.JobTypeInteractiveAgent, orchestration.JobStatusRunning), false},
	}
	for _, tc := range cases {
		if got := isLiveAPIDirectJob(tc.job); got != tc.want {
			t.Errorf("%s: isLiveAPIDirectJob = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestMaybeRefreshEvictsLiveChatCells(t *testing.T) {
	dir := t.TempDir()
	m := newModelForCells(dir)
	m.Jobs = []*orchestration.Job{
		{ID: "chat-live", Type: orchestration.JobTypeChat, Status: orchestration.JobStatusPendingUser},
		{ID: "chat-done", Type: orchestration.JobTypeChat, Status: orchestration.JobStatusCompleted},
	}
	m.tokenColumnCache["chat-live"] = "stale"
	m.tokenColumnCache["chat-done"] = "cached"

	// First tick (lastRunningTokenRefresh zero) passes the interval and evicts.
	m.maybeRefreshRunningTokenCells(time.Now())

	if _, ok := m.tokenColumnCache["chat-live"]; ok {
		t.Errorf("live chat cell should be evicted")
	}
	if _, ok := m.tokenColumnCache["chat-done"]; !ok {
		t.Errorf("completed chat cell should be retained")
	}
}
