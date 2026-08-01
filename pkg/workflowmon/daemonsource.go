package workflowmon

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/models"
)

// Daemon SSE update_type strings for workflow lifecycle deltas. These mirror
// the daemon store's UpdateType constants — each kind keeps its DISTINCT
// string (the job_* pattern, not the collapsed "session" pattern).
const (
	updateWorkflowRunDiscovered  = "workflow_run_discovered"
	updateWorkflowAgentStarted   = "workflow_agent_started"
	updateWorkflowAgentCompleted = "workflow_agent_completed"
	updateWorkflowRunStale       = "workflow_run_stale"
	updateWorkflowRunCompleted   = "workflow_run_completed"
)

// DaemonWorkflowClient is the slice of daemon.Client a DaemonSource needs:
// the snapshot endpoint for reconciliation and the SSE stream for deltas.
// daemon.Client satisfies it; tests substitute a fake.
type DaemonWorkflowClient interface {
	GetWorkflowSnapshot(ctx context.Context) (*models.WorkflowSnapshot, error)
	StreamState(ctx context.Context, filter ...daemon.StreamFilter) (<-chan daemon.StateUpdate, error)
}

// DaemonSourceOptions configures a DaemonSource.
type DaemonSourceOptions struct {
	// InitialBackoff is the first reconnect delay after the stream drops or
	// fails to open. Doubles per consecutive failure. Defaults to 1s.
	InitialBackoff time.Duration
	// MaxBackoff caps the reconnect delay. Defaults to 30s.
	MaxBackoff time.Duration
}

// DaemonSource implements EventSource against the groved daemon: ONE
// subscription covering every job on the host. On each (re)connect it
// subscribes to the SSE state stream, replays the GET /api/workflows
// snapshot as synthetic events (the broadcast is lossy-by-design — the
// snapshot is the reconciliation point), then forwards workflow_* deltas.
// The stream reconnects with exponential backoff until Close(); unlike the
// HUD streams of old, a dead stream never leaves the source permanently
// static. Events carry JobID so a multiplexing consumer can route them.
type DaemonSource struct {
	client    DaemonWorkflowClient
	opts      DaemonSourceOptions
	events    chan Event
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
}

// NewDaemonSource creates and starts a daemon-backed event source. Call
// Close() to stop it. The client must outlive the source.
func NewDaemonSource(client DaemonWorkflowClient, opts DaemonSourceOptions) *DaemonSource {
	if opts.InitialBackoff <= 0 {
		opts.InitialBackoff = time.Second
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = 30 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &DaemonSource{
		client: client,
		opts:   opts,
		events: make(chan Event, 256),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go s.run(ctx)
	return s
}

// Events implements EventSource.
func (s *DaemonSource) Events() <-chan Event { return s.events }

// Close implements EventSource. It stops the stream loop and closes the
// events channel.
func (s *DaemonSource) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		<-s.done
	})
	return nil
}

func (s *DaemonSource) run(ctx context.Context) {
	defer close(s.done)
	defer close(s.events)

	backoff := s.opts.InitialBackoff
	for {
		if s.connectOnce(ctx) {
			// The stream was established (and later dropped) — reconnect
			// promptly, resetting the failure backoff.
			backoff = s.opts.InitialBackoff
		} else {
			backoff *= 2
			if backoff > s.opts.MaxBackoff {
				backoff = s.opts.MaxBackoff
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// connectOnce opens one stream subscription, replays the snapshot, and
// forwards deltas until the stream dies or ctx is cancelled. Returns true
// when a stream was established (controls backoff growth).
func (s *DaemonSource) connectOnce(ctx context.Context) bool {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch, err := s.client.StreamState(streamCtx)
	if err != nil {
		return false
	}

	// Snapshot AFTER subscribing so no delta is lost in the gap; replayed
	// events that overlap buffered deltas are harmless (the consumer fold
	// is an idempotent upsert). Snapshot errors are tolerated (e.g. an
	// older daemon without the endpoint) — deltas still flow.
	if snap, err := s.client.GetWorkflowSnapshot(ctx); err == nil && snap != nil {
		for _, ev := range snapshotEvents(snap) {
			if !s.emit(ctx, ev) {
				return true
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			return true
		case u, ok := <-ch:
			if !ok {
				return true // stream died — reconnect
			}
			for _, ev := range decodeWorkflowUpdate(u) {
				if !s.emit(ctx, ev) {
					return true
				}
			}
		}
	}
}

// emit delivers an event, blocking until the consumer takes it or the
// context is cancelled. Returns false on cancellation.
func (s *DaemonSource) emit(ctx context.Context, ev Event) bool {
	select {
	case s.events <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// daemonWorkflowPayload mirrors the daemon store's WorkflowEventPayload as
// serialized into StateUpdate.Payload: the wire-protocol event plus the
// journal watcher's run-level enrichment (parsed script meta).
type daemonWorkflowPayload struct {
	Event   models.WorkflowEvent `json:"event"`
	RunName string               `json:"run_name,omitempty"`
	Phases  []string             `json:"phases,omitempty"`
}

// decodeWorkflowUpdate converts one SSE state update into workflowmon
// events. Non-workflow update types yield nil. Payload arrives as the
// generic JSON decode (map[string]any), so it is re-marshaled into the
// typed payload struct; undecodable payloads are skipped rather than
// failing the stream.
func decodeWorkflowUpdate(u daemon.StateUpdate) []Event {
	switch u.UpdateType {
	case updateWorkflowRunDiscovered, updateWorkflowAgentStarted,
		updateWorkflowAgentCompleted, updateWorkflowRunStale,
		updateWorkflowRunCompleted:
	default:
		return nil
	}
	if u.Payload == nil {
		return nil
	}
	raw, err := json.Marshal(u.Payload)
	if err != nil {
		return nil
	}
	var p daemonWorkflowPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil
	}
	ev := p.Event

	runID := ev.RunID
	if runID == "" {
		runID = AdhocRunID
	}

	switch u.UpdateType {
	case updateWorkflowRunDiscovered:
		if ev.RunID == "" {
			return nil
		}
		name := p.RunName
		if name == "" {
			name = ev.WorkflowName
		}
		return []Event{RunDiscovered{
			JobID: ev.JobID,
			RunID: ev.RunID,
			Meta:  syntheticMeta(name, p.Phases),
		}}
	case updateWorkflowAgentStarted:
		if ev.AgentID == "" {
			return nil
		}
		return []Event{AgentStarted{
			JobID:     ev.JobID,
			RunID:     runID,
			AgentID:   ev.AgentID,
			Name:      ev.Name,
			Prompt:    ev.Prompt,
			Phase:     ev.Phase,
			StartedAt: ev.Timestamp,
		}}
	case updateWorkflowAgentCompleted:
		if ev.AgentID == "" {
			return nil
		}
		result := ev.ResultSummary
		if result == "" {
			result = ev.LastMessage
		}
		return []Event{AgentCompleted{
			JobID:       ev.JobID,
			RunID:       runID,
			AgentID:     ev.AgentID,
			Result:      result,
			CompletedAt: ev.Timestamp,
		}}
	case updateWorkflowRunStale:
		if ev.RunID == "" {
			return nil
		}
		return []Event{RunStale{JobID: ev.JobID, RunID: ev.RunID}}
	case updateWorkflowRunCompleted:
		if ev.RunID == "" {
			return nil
		}
		return []Event{RunCompleted{JobID: ev.JobID, RunID: ev.RunID}}
	}
	return nil
}

// snapshotEvents converts a GET /api/workflows snapshot into the synthetic
// event sequence a consumer would have observed live: RunDiscovered (with
// name/phase meta), AgentStarted/AgentCompleted per agent (with the daemon's
// hook-sourced timestamps; the durable Subagent record carries no phase, so
// snapshot-replayed agents are phase-unattributed until a live delta
// enriches them), then RunCompleted/RunStale for terminal runs. Iteration order is
// deterministic (run IDs and adhoc session keys sorted; agents by StartedAt
// then ID).
func snapshotEvents(snap *models.WorkflowSnapshot) []Event {
	var events []Event

	runIDs := make([]string, 0, len(snap.Runs))
	for id := range snap.Runs {
		runIDs = append(runIDs, id)
	}
	sort.Strings(runIDs)

	for _, runID := range runIDs {
		run := snap.Runs[runID]
		events = append(events, RunDiscovered{
			JobID: run.JobID,
			RunID: runID,
			Meta:  syntheticMeta(run.Name, run.Phases),
		})
		for _, agent := range sortedAgents(run.Agents) {
			events = append(events, agentSnapshotEvents(run.JobID, runID, agent)...)
		}
		if run.Completed {
			events = append(events, RunCompleted{JobID: run.JobID, RunID: runID})
		}
		if run.Stale {
			events = append(events, RunStale{JobID: run.JobID, RunID: runID})
		}
	}

	adhocKeys := make([]string, 0, len(snap.Adhoc))
	for key := range snap.Adhoc {
		adhocKeys = append(adhocKeys, key)
	}
	sort.Strings(adhocKeys)

	for _, key := range adhocKeys {
		// The adhoc bucket is keyed by job ID when the session was stamped,
		// else by claude session ID — consumers routing by job ID simply
		// won't match the latter.
		for _, agent := range sortedAgents(snap.Adhoc[key]) {
			events = append(events, agentSnapshotEvents(key, AdhocRunID, agent)...)
		}
	}
	return events
}

// agentSnapshotEvents replays one durable Subagent record as its started
// (and, when finished, completed) events.
func agentSnapshotEvents(jobID, runID string, agent *models.Subagent) []Event {
	events := []Event{AgentStarted{
		JobID:     jobID,
		RunID:     runID,
		AgentID:   agent.ID,
		Name:      agent.Name,
		Prompt:    agent.TaskDescription,
		StartedAt: agent.StartedAt,
	}}
	if agent.Status == "completed" || !agent.CompletedAt.IsZero() {
		result, _ := agent.ResultSummary["text"].(string)
		events = append(events, AgentCompleted{
			JobID:       jobID,
			RunID:       runID,
			AgentID:     agent.ID,
			Result:      result,
			CompletedAt: agent.CompletedAt,
		})
	}
	return events
}

// syntheticMeta builds a ScriptMeta from the daemon's run name and phase
// titles, or nil when there is no enrichment to carry.
func syntheticMeta(name string, phases []string) *ScriptMeta {
	if name == "" && len(phases) == 0 {
		return nil
	}
	meta := &ScriptMeta{Name: name}
	for _, title := range phases {
		meta.Phases = append(meta.Phases, PhaseMeta{Title: title})
	}
	return meta
}

// sortedAgents orders a snapshot agent map by StartedAt then ID for
// deterministic replay.
func sortedAgents(agents map[string]*models.Subagent) []*models.Subagent {
	out := make([]*models.Subagent, 0, len(agents))
	for _, a := range agents {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.Before(out[j].StartedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}
