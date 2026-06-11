// Package workflowmon provides live monitoring of Claude Code workflow runs.
//
// It defines a small typed-event interface (EventSource) between run
// discovery and any UI that renders workflow state. Consumers (e.g. the flow
// status TUI's inline workflow tree) must depend only on EventSource, never
// on the on-disk journal layout, so the file-tailing source (FileSource) and
// the daemon-SSE-backed source (DaemonSource) are interchangeable without UI
// changes.
package workflowmon

import "time"

// AdhocRunID is the pseudo-run bucket for run-less subagents (ad-hoc
// Agent-tool spawns, or workflow agents whose run attribution hasn't arrived
// yet). Daemon-backed sources map empty wire RunIDs here so consumers never
// see an empty run key.
const AdhocRunID = "adhoc"

// Event is a typed lifecycle event emitted by an EventSource.
type Event interface{ workflowEvent() }

// RunDiscovered is emitted once per workflow run when it first becomes
// visible to the source.
type RunDiscovered struct {
	// JobID is the owning flow job, when the source can attribute it
	// (DaemonSource). FileSource leaves it empty — its caller already
	// scopes the source to one job.
	JobID string
	RunID string
	// Meta is parsed from the persisted orchestration script when one was
	// found (FileSource), or synthesized from the daemon's run name/phase
	// enrichment (DaemonSource); nil otherwise.
	Meta *ScriptMeta
}

// AgentStarted is emitted when an agent begins executing within a run. It is
// an upsert: a source may re-emit it for the same agent to enrich fields that
// were not available earlier (e.g. the prompt, which lives in the agent
// transcript and can lag the journal's started event).
type AgentStarted struct {
	// JobID is the owning flow job, when known (DaemonSource only).
	JobID   string
	RunID   string
	AgentID string
	// Prompt is the agent's task prompt; empty until known.
	Prompt string
	// Phase is the workflow phase the agent belongs to. The file-tailing
	// source cannot attribute agents to phases (the journal carries no phase
	// labels); a hooks/daemon-backed source may populate this.
	Phase string
	// StartedAt is the agent's start time when the source knows it
	// (DaemonSource: hook wall-clock); zero otherwise.
	StartedAt time.Time
}

// AgentCompleted is emitted when an agent's result is recorded.
type AgentCompleted struct {
	// JobID is the owning flow job, when known (DaemonSource only).
	JobID   string
	RunID   string
	AgentID string
	// Result is the rendered result payload (plain text for string results,
	// indented JSON otherwise).
	Result string
	// CompletedAt is the agent's completion time when the source knows it
	// (DaemonSource: hook wall-clock); zero otherwise.
	CompletedAt time.Time
}

// RunStale is emitted when a run looks abandoned: no journal writes for the
// staleness window while the owning session is gone. An agent that started
// without a result is otherwise always considered in-flight — gaps in the
// journal alone never mean "interrupted".
type RunStale struct {
	// JobID is the owning flow job, when known (DaemonSource only).
	JobID string
	RunID string
}

func (RunDiscovered) workflowEvent()  {}
func (AgentStarted) workflowEvent()   {}
func (AgentCompleted) workflowEvent() {}
func (RunStale) workflowEvent()       {}

// EventJobID returns the event's job attribution, or "" when the source
// could not attribute it (FileSource events; daemon events for unstamped
// sessions). Consumers multiplexing one source across jobs route on this.
func EventJobID(ev Event) string {
	switch ev := ev.(type) {
	case RunDiscovered:
		return ev.JobID
	case AgentStarted:
		return ev.JobID
	case AgentCompleted:
		return ev.JobID
	case RunStale:
		return ev.JobID
	}
	return ""
}

// EventSource delivers workflow lifecycle events. Events() yields events
// until Close() is called, after which the channel is closed.
type EventSource interface {
	Events() <-chan Event
	Close() error
}
