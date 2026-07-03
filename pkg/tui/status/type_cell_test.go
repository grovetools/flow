package status

import (
	"strings"
	"testing"

	"github.com/grovetools/core/tui/theme"

	"github.com/grovetools/flow/pkg/orchestration"
)

// TestRenderTableView_AgentRespondedChatRendersAgentChat: a chat job with
// responder: agent renders as "agent chat" in the TYPE column while a plain
// (oracle) chat keeps its "chat" label.
func TestRenderTableView_AgentRespondedChatRendersAgentChat(t *testing.T) {
	agentChat := testJob("design-chat")
	agentChat.Type = orchestration.JobTypeChat
	agentChat.Responder = "agent"
	agentChat.Status = orchestration.JobStatusPendingUser

	chat := testJob("plain-chat")
	chat.Type = orchestration.JobTypeChat
	chat.Status = orchestration.JobStatusPendingUser

	m := newDisplayTestModel(agentChat, chat)
	m.availableColumns = []string{"JOB", "TYPE"}
	m.columnVisibility = map[string]bool{"JOB": true, "TYPE": true}

	out := stripANSI(m.renderTableView())
	if !strings.Contains(out, "agent chat") {
		t.Errorf("TYPE column missing %q label for agent-responded chat:\n%s", "agent chat", out)
	}
	if !strings.Contains(out, "chat") {
		t.Errorf("TYPE column missing %q label for plain chat:\n%s", "chat", out)
	}
}

func TestGetJobIcon_AgentRespondedChat(t *testing.T) {
	agentChat := &orchestration.Job{Type: orchestration.JobTypeChat, Responder: "agent"}
	chat := &orchestration.Job{Type: orchestration.JobTypeChat}

	if got := getJobIcon(agentChat); got != theme.IconRobot {
		t.Errorf("getJobIcon(agent-responded chat) = %q, want IconRobot %q", got, theme.IconRobot)
	}
	if got := getJobIcon(agentChat); got == theme.IconChat {
		t.Error("agent-responded chat must not use the plain chat icon")
	}
	if got := getJobIcon(chat); got != theme.IconChat {
		t.Errorf("getJobIcon(plain chat) = %q, want IconChat %q", got, theme.IconChat)
	}
}
