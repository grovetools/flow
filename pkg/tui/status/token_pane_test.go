package status

import (
	"errors"
	"strings"
	"testing"

	"github.com/grovetools/agentlogs/pkg/usage"
	"github.com/grovetools/flow/pkg/orchestration"
)

func TestFormatTokenCell(t *testing.T) {
	// With ContextSize: cost-forward + peak context (matches /context).
	withCtx := usage.Summary{
		Usage:       usage.Usage{Input: 3707, Output: 411, CacheWrite5m: 27240, CacheRead: 20310},
		CostUSD:     0.3114,
		ContextSize: 27242,
	}
	// With cache traffic the cumulative hit% badge is appended (oracle-plays J6):
	// 20310 / (20310 + 27240) = 43%.
	if got := formatTokenCell(withCtx); got != "$0.31 · 27.2k ctx · 43%" {
		t.Errorf("formatTokenCell(withCtx) = %q, want %q", got, "$0.31 · 27.2k ctx · 43%")
	}
	// Legacy artifact (no ContextSize): fall back to cumulative total.
	// 210280 / (210280 + 11671) = 95% hit.
	legacy := usage.Summary{
		Usage:   usage.Usage{Input: 4448, Output: 2833, CacheWrite5m: 11671, CacheRead: 210280},
		CostUSD: 0.3149,
	}
	if got := formatTokenCell(legacy); got != "$0.31 · 229.2k · 95%" {
		t.Errorf("formatTokenCell(legacy) = %q, want %q", got, "$0.31 · 229.2k · 95%")
	}
	// No cache traffic at all → no hit% badge.
	noCache := usage.Summary{Usage: usage.Usage{Input: 100, Output: 50}, CostUSD: 0.01, ContextSize: 150}
	if got := formatTokenCell(noCache); got != "$0.01 · 150 ctx" {
		t.Errorf("formatTokenCell(noCache) = %q, want %q", got, "$0.01 · 150 ctx")
	}
}

func TestRenderTokenPaneContentError(t *testing.T) {
	out := renderTokenPaneContent(usage.Summary{}, false, errors.New("boom"), 80, nil)
	if !strings.Contains(out, "boom") {
		t.Errorf("expected error text in pane, got: %s", out)
	}
}

func TestRenderTokenPaneContentNotFound(t *testing.T) {
	out := renderTokenPaneContent(usage.Summary{}, false, nil, 80, nil)
	if !strings.Contains(out, "No token usage recorded") {
		t.Errorf("expected not-found message, got: %s", out)
	}
}

func TestRenderTokenPaneContentFull(t *testing.T) {
	s := usage.Summary{
		Usage: usage.Usage{
			Input:        1000,
			Output:       200,
			CacheRead:    5000,
			CacheWrite5m: 300,
			CacheWrite1h: 50,
		},
		CostUSD:      0.42,
		Models:       []string{"claude-opus-4-5"},
		MessageCount: 7,
		ModelBreakdown: []usage.AgentUsage{
			{Model: "claude-opus-4-5", Usage: usage.Usage{Input: 1000, Output: 200}, CostUSD: 0.42},
		},
		Agents: []usage.AgentUsage{
			{AgentType: "explorer", Usage: usage.Usage{Input: 400, Output: 80}, CostUSD: 0.10},
			{AgentID: "agent-bbb", Usage: usage.Usage{Input: 100, Output: 20}, CostUSD: 0.02},
		},
	}
	out := renderTokenPaneContent(s, true, nil, 80, nil)

	for _, want := range []string{
		"Totals",
		"6.5k", // total tokens
		"$0.42",
		"Messages: 7",
		"claude-opus-4-5",
		"Token breakdown",
		"Input:",
		"Cache write 5m:",
		"Cache write 1h:",
		"Per-model",
		"Per-agent",
		"explorer",
		"agent-bbb",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("token pane missing %q\n---\n%s", want, out)
		}
	}
}

func TestFormatAgentTokenCell(t *testing.T) {
	au := usage.AgentUsage{
		Usage:   usage.Usage{Input: 4448, Output: 2833, CacheWrite5m: 11671, CacheRead: 210280},
		CostUSD: 0.42,
	}
	if got := formatAgentTokenCell(au); got != "$0.42 · 229.2k" {
		t.Errorf("formatAgentTokenCell = %q, want %q", got, "$0.42 · 229.2k")
	}
}

func TestAgentUsageMap(t *testing.T) {
	s := usage.Summary{
		Agents: []usage.AgentUsage{
			{AgentID: "parent", Usage: usage.Usage{Input: 999}, CostUSD: 9.99}, // skipped
			{AgentID: "a42aff2", Usage: usage.Usage{Input: 400}, CostUSD: 0.10},
			{AgentID: "", Usage: usage.Usage{Input: 1}, CostUSD: 0.01}, // skipped
		},
	}
	mp := agentUsageMap(s)
	if len(mp) != 1 {
		t.Fatalf("agentUsageMap len = %d, want 1 (parent + empty skipped)", len(mp))
	}
	if _, ok := mp["parent"]; ok {
		t.Errorf("parent entry should be skipped")
	}
	au, ok := mp["a42aff2"]
	if !ok || au.CostUSD != 0.10 {
		t.Errorf("a42aff2 entry missing or wrong: %+v ok=%v", au, ok)
	}

	// No real subagents → nil (so the cell stays blank).
	if mp := agentUsageMap(usage.Summary{Agents: []usage.AgentUsage{{AgentID: "parent"}}}); mp != nil {
		t.Errorf("agentUsageMap with only parent = %v, want nil", mp)
	}
	if mp := agentUsageMap(usage.Summary{}); mp != nil {
		t.Errorf("agentUsageMap with no agents = %v, want nil", mp)
	}
}

func TestIsLiveAgentJob(t *testing.T) {
	mk := func(typ orchestration.JobType, st orchestration.JobStatus) *orchestration.Job {
		return &orchestration.Job{Type: typ, Status: st}
	}
	cases := []struct {
		name string
		job  *orchestration.Job
		want bool
	}{
		{"running interactive", mk(orchestration.JobTypeInteractiveAgent, orchestration.JobStatusRunning), true},
		{"idle interactive", mk(orchestration.JobTypeInteractiveAgent, orchestration.JobStatusIdle), true},
		{"running headless", mk(orchestration.JobTypeHeadlessAgent, orchestration.JobStatusRunning), true},
		{"pending_user interactive", mk(orchestration.JobTypeInteractiveAgent, orchestration.JobStatusPendingUser), true},
		{"completed interactive", mk(orchestration.JobTypeInteractiveAgent, orchestration.JobStatusCompleted), false},
		{"running chat (not an agent)", mk(orchestration.JobTypeChat, orchestration.JobStatusRunning), false},
		{"pending interactive (no session yet)", mk(orchestration.JobTypeInteractiveAgent, orchestration.JobStatusPending), false},
	}
	for _, tc := range cases {
		if got := isLiveAgentJob(tc.job); got != tc.want {
			t.Errorf("%s: isLiveAgentJob = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRenderTokenPaneContentParentAgentFallback(t *testing.T) {
	// An agent with neither type nor id renders as "(parent)".
	s := usage.Summary{
		Usage:   usage.Usage{Input: 10, Output: 5},
		CostUSD: 0.01,
		Agents: []usage.AgentUsage{
			{Usage: usage.Usage{Input: 10, Output: 5}, CostUSD: 0.01},
		},
	}
	out := renderTokenPaneContent(s, true, nil, 80, nil)
	if !strings.Contains(out, "(parent)") {
		t.Errorf("expected (parent) fallback label\n%s", out)
	}
}
