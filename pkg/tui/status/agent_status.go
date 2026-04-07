package status

import (
	"github.com/grovetools/agentlogs/pkg/agentstream"
)

// AgentStatus is an alias to the canonical type in agentstream.
type AgentStatus = agentstream.AgentStatus

// TodoItem is an alias to the canonical type in agentstream.
type TodoItem = agentstream.TodoItem

// ParseAgentPane parses raw tmux pane output to extract agent session status.
// Delegates to agentstream.ParsePaneOutput.
func ParseAgentPane(output string) *AgentStatus {
	return agentstream.ParsePaneOutput(output)
}
