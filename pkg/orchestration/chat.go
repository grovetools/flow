package orchestration

import (
	"time"
)

// ChatTurn represents a single entry in the conversation
type ChatTurn struct {
	Speaker   string         // "user" or "llm"
	Content   string         // The markdown content of the turn
	Directive *ChatDirective // Parsed from the grove HTML comment
	Timestamp time.Time      // When the turn was recorded
}

// ChatDirective represents the JSON payload in the user's comment
type ChatDirective struct {
	ID       string                 `json:"id,omitempty"`
	Template string                 `json:"template,omitempty"`
	Model    string                 `json:"model,omitempty"`
	Action   string                 `json:"action,omitempty"`
	Vars     map[string]interface{} `json:"vars,omitempty"`
}