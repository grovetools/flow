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
	ID        string                 `json:"id,omitempty"`
	Template  string                 `json:"template,omitempty"`
	Model     string                 `json:"model,omitempty"`
	Type      string                 `json:"type,omitempty"` // Job type override for this turn
	Action    string                 `json:"action,omitempty"`
	RulesFile string                 `json:"rules_file,omitempty"` // Archived rules file used for this turn's context
	Vars      map[string]interface{} `json:"vars,omitempty"`
	// AppendDelta / RebaseContext are the per-turn context refresh verbs
	// (spec 19 D4). AppendDelta appends a supersede-annotated delta layer
	// with files changed since their layer was frozen (cache-preserving;
	// also auto-stamped when a completed chat is reopened). RebaseContext
	// archives all layers and re-freezes a fresh base — the one deliberate
	// cache-busting verb. `flow plan run --append-delta/--rebase-context`
	// stamps these into the trailing chat marker so the semantics are
	// identical across local and daemon execution. Mutually exclusive.
	AppendDelta   bool `json:"append_delta,omitempty"`
	RebaseContext bool `json:"rebase_context,omitempty"`
}
