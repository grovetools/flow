// Package workflowmon provides live monitoring of Claude Code workflow runs.
//
// It defines a small typed-event interface (EventSource) between run
// discovery and any UI that renders workflow state. Consumers (e.g. the flow
// status TUI's workflow pane) must depend only on EventSource, never on the
// on-disk journal layout, so the current file-tailing source (FileSource) can
// later be swapped for a daemon-SSE-backed source fed by SubagentStart/Stop
// hook events without UI changes.
package workflowmon

// Event is a typed lifecycle event emitted by an EventSource.
type Event interface{ workflowEvent() }

// RunDiscovered is emitted once per workflow run when it first becomes
// visible to the source.
type RunDiscovered struct {
	RunID string
	// Meta is parsed from the persisted orchestration script when one was
	// found; nil otherwise.
	Meta *ScriptMeta
}

// AgentStarted is emitted when an agent begins executing within a run. It is
// an upsert: a source may re-emit it for the same agent to enrich fields that
// were not available earlier (e.g. the prompt, which lives in the agent
// transcript and can lag the journal's started event).
type AgentStarted struct {
	RunID   string
	AgentID string
	// Prompt is the agent's task prompt; empty until known.
	Prompt string
	// Phase is the workflow phase the agent belongs to. The file-tailing
	// source cannot attribute agents to phases (the journal carries no phase
	// labels); a hooks/daemon-backed source may populate this.
	Phase string
}

// AgentCompleted is emitted when an agent's result is recorded.
type AgentCompleted struct {
	RunID   string
	AgentID string
	// Result is the rendered result payload (plain text for string results,
	// indented JSON otherwise).
	Result string
}

// RunStale is emitted when a run looks abandoned: no journal writes for the
// staleness window while the owning session is gone. An agent that started
// without a result is otherwise always considered in-flight — gaps in the
// journal alone never mean "interrupted".
type RunStale struct {
	RunID string
}

func (RunDiscovered) workflowEvent()  {}
func (AgentStarted) workflowEvent()   {}
func (AgentCompleted) workflowEvent() {}
func (RunStale) workflowEvent()       {}

// EventSource delivers workflow lifecycle events. Events() yields events
// until Close() is called, after which the channel is closed.
type EventSource interface {
	Events() <-chan Event
	Close() error
}
