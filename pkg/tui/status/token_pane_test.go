package status

import (
	"errors"
	"strings"
	"testing"

	"github.com/grovetools/agentlogs/pkg/usage"
)

func TestRenderTokenPaneContentError(t *testing.T) {
	out := renderTokenPaneContent(usage.Summary{}, false, errors.New("boom"), 80)
	if !strings.Contains(out, "boom") {
		t.Errorf("expected error text in pane, got: %s", out)
	}
}

func TestRenderTokenPaneContentNotFound(t *testing.T) {
	out := renderTokenPaneContent(usage.Summary{}, false, nil, 80)
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
	out := renderTokenPaneContent(s, true, nil, 80)

	for _, want := range []string{
		"Totals",
		"6,550", // total tokens
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

func TestRenderTokenPaneContentParentAgentFallback(t *testing.T) {
	// An agent with neither type nor id renders as "(parent)".
	s := usage.Summary{
		Usage:   usage.Usage{Input: 10, Output: 5},
		CostUSD: 0.01,
		Agents: []usage.AgentUsage{
			{Usage: usage.Usage{Input: 10, Output: 5}, CostUSD: 0.01},
		},
	}
	out := renderTokenPaneContent(s, true, nil, 80)
	if !strings.Contains(out, "(parent)") {
		t.Errorf("expected (parent) fallback label\n%s", out)
	}
}
