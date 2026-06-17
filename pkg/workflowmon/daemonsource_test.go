package workflowmon

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/models"
)

// Interface compliance.
var _ EventSource = (*DaemonSource)(nil)

// fakeDaemonClient implements DaemonWorkflowClient for tests: each
// StreamState call hands out the next prepared channel (the last repeats);
// GetWorkflowSnapshot returns the configured snapshot and counts calls.
type fakeDaemonClient struct {
	mu          sync.Mutex
	snapshot    *models.WorkflowSnapshot
	snapErr     error
	streams     []chan daemon.StateUpdate
	snapCalls   int
	streamCalls int
}

func (f *fakeDaemonClient) GetWorkflowSnapshot(ctx context.Context) (*models.WorkflowSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapCalls++
	return f.snapshot, f.snapErr
}

func (f *fakeDaemonClient) StreamState(ctx context.Context) (<-chan daemon.StateUpdate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.streams) == 0 {
		return nil, errors.New("no stream")
	}
	ch := f.streams[0]
	if len(f.streams) > 1 {
		f.streams = f.streams[1:]
	}
	f.streamCalls++
	return ch, nil
}

func (f *fakeDaemonClient) calls() (snap, stream int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapCalls, f.streamCalls
}

// sseUpdate builds a StateUpdate whose Payload has been through a JSON
// round-trip, exactly as the SSE decode path delivers it (map[string]any,
// never the typed struct).
func sseUpdate(t *testing.T, updateType string, ev models.WorkflowEvent, runName string, phases []string) daemon.StateUpdate {
	t.Helper()
	payload := map[string]any{"event": ev}
	if runName != "" {
		payload["run_name"] = runName
	}
	if len(phases) > 0 {
		payload["phases"] = phases
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return daemon.StateUpdate{UpdateType: updateType, Payload: generic}
}

func collectEvents(t *testing.T, ch <-chan Event, n int) []Event {
	t.Helper()
	var events []Event
	timeout := time.After(5 * time.Second)
	for len(events) < n {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("events channel closed after %d/%d events", len(events), n)
			}
			events = append(events, ev)
		case <-timeout:
			t.Fatalf("timed out after %d/%d events: %+v", len(events), n, events)
		}
	}
	return events
}

func TestDecodeWorkflowUpdate(t *testing.T) {
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

	t.Run("agent started with phase and job attribution", func(t *testing.T) {
		u := sseUpdate(t, "workflow_agent_started", models.WorkflowEvent{
			Kind: models.WorkflowAgentStarted, JobID: "job-1", RunID: "wf_a",
			AgentID: "a1", Prompt: "do it", Phase: "Phase 1", Timestamp: ts,
		}, "", nil)
		events := decodeWorkflowUpdate(u)
		if len(events) != 1 {
			t.Fatalf("events = %d, want 1", len(events))
		}
		got, ok := events[0].(AgentStarted)
		if !ok {
			t.Fatalf("event type = %T, want AgentStarted", events[0])
		}
		want := AgentStarted{JobID: "job-1", RunID: "wf_a", AgentID: "a1", Prompt: "do it", Phase: "Phase 1", StartedAt: ts}
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("run-less agent events map to the adhoc run", func(t *testing.T) {
		u := sseUpdate(t, "workflow_agent_started", models.WorkflowEvent{
			Kind: models.WorkflowAgentStarted, JobID: "job-1", AgentID: "a1", Timestamp: ts,
		}, "", nil)
		events := decodeWorkflowUpdate(u)
		if len(events) != 1 {
			t.Fatalf("events = %d, want 1", len(events))
		}
		if got := events[0].(AgentStarted).RunID; got != AdhocRunID {
			t.Errorf("RunID = %q, want %q", got, AdhocRunID)
		}
	})

	t.Run("run discovered carries name and phase meta", func(t *testing.T) {
		u := sseUpdate(t, "workflow_run_discovered", models.WorkflowEvent{
			Kind: models.WorkflowRunDiscovered, JobID: "job-1", RunID: "wf_a", Timestamp: ts,
		}, "my-workflow", []string{"P1", "P2"})
		events := decodeWorkflowUpdate(u)
		if len(events) != 1 {
			t.Fatalf("events = %d, want 1", len(events))
		}
		got := events[0].(RunDiscovered)
		if got.JobID != "job-1" || got.RunID != "wf_a" {
			t.Errorf("attribution wrong: %+v", got)
		}
		if got.Meta == nil || got.Meta.Name != "my-workflow" || len(got.Meta.Phases) != 2 || got.Meta.Phases[1].Title != "P2" {
			t.Errorf("meta wrong: %+v", got.Meta)
		}
	})

	t.Run("run discovered falls back to event WorkflowName", func(t *testing.T) {
		u := sseUpdate(t, "workflow_run_discovered", models.WorkflowEvent{
			Kind: models.WorkflowRunDiscovered, RunID: "wf_a", WorkflowName: "hook-name", Timestamp: ts,
		}, "", nil)
		got := decodeWorkflowUpdate(u)[0].(RunDiscovered)
		if got.Meta == nil || got.Meta.Name != "hook-name" {
			t.Errorf("meta = %+v, want name hook-name", got.Meta)
		}
	})

	t.Run("agent completed prefers result summary over last message", func(t *testing.T) {
		u := sseUpdate(t, "workflow_agent_completed", models.WorkflowEvent{
			Kind: models.WorkflowAgentCompleted, JobID: "job-1", RunID: "wf_a",
			AgentID: "a1", ResultSummary: "summary", LastMessage: "long tail", Timestamp: ts,
		}, "", nil)
		got := decodeWorkflowUpdate(u)[0].(AgentCompleted)
		if got.Result != "summary" || !got.CompletedAt.Equal(ts) {
			t.Errorf("got %+v", got)
		}

		u = sseUpdate(t, "workflow_agent_completed", models.WorkflowEvent{
			Kind: models.WorkflowAgentCompleted, RunID: "wf_a", AgentID: "a1", LastMessage: "tail", Timestamp: ts,
		}, "", nil)
		if got := decodeWorkflowUpdate(u)[0].(AgentCompleted); got.Result != "tail" {
			t.Errorf("Result = %q, want tail", got.Result)
		}
	})

	t.Run("run stale", func(t *testing.T) {
		u := sseUpdate(t, "workflow_run_stale", models.WorkflowEvent{
			Kind: models.WorkflowRunStale, JobID: "job-1", RunID: "wf_a", Timestamp: ts,
		}, "", nil)
		if got := decodeWorkflowUpdate(u)[0].(RunStale); got != (RunStale{JobID: "job-1", RunID: "wf_a"}) {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("non-workflow and malformed updates are skipped", func(t *testing.T) {
		for _, u := range []daemon.StateUpdate{
			{UpdateType: "session"},
			{UpdateType: "workflow_agent_started"},                             // nil payload
			{UpdateType: "workflow_agent_started", Payload: "not-a-map"},       // wrong shape
			{UpdateType: "workflow_run_stale", Payload: map[string]any{}},      // no run id
			{UpdateType: "workflow_run_discovered", Payload: map[string]any{}}, // no run id
		} {
			if events := decodeWorkflowUpdate(u); events != nil {
				t.Errorf("update %+v yielded %+v, want nil", u, events)
			}
		}
	})
}

func TestDaemonSourceSnapshotReplay(t *testing.T) {
	started := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	completed := started.Add(time.Minute)

	snap := &models.WorkflowSnapshot{
		Runs: map[string]*models.WorkflowRunState{
			"wf_a": {
				RunID: "wf_a", JobID: "job-1", ClaudeSessionID: "sess-1",
				Name: "deep-integration", Phases: []string{"P1"},
				Agents: map[string]*models.Subagent{
					"a2": {ID: "a2", StartedAt: started.Add(time.Second)},
					"a1": {
						ID: "a1", TaskDescription: "prompt-1",
						StartedAt: started, CompletedAt: completed, Status: "completed",
						ResultSummary: map[string]any{"text": "done"},
					},
				},
				StartedCount: 2, CompletedCount: 1, Stale: true,
			},
		},
		Adhoc: map[string]map[string]*models.Subagent{
			"job-2": {
				"x1": {ID: "x1", StartedAt: started, Status: "running"},
			},
		},
	}

	stream := make(chan daemon.StateUpdate)
	client := &fakeDaemonClient{snapshot: snap, streams: []chan daemon.StateUpdate{stream}}
	source := NewDaemonSource(client, DaemonSourceOptions{InitialBackoff: time.Millisecond})
	defer source.Close()

	// Expected replay: RunDiscovered, a1 started+completed, a2 started,
	// RunStale, then the adhoc agent started.
	events := collectEvents(t, source.Events(), 6)

	rd, ok := events[0].(RunDiscovered)
	if !ok || rd.JobID != "job-1" || rd.RunID != "wf_a" || rd.Meta == nil || rd.Meta.Name != "deep-integration" || len(rd.Meta.Phases) != 1 {
		t.Fatalf("events[0] = %+v, want RunDiscovered for wf_a with meta", events[0])
	}
	a1s, ok := events[1].(AgentStarted)
	if !ok || a1s.AgentID != "a1" || a1s.Prompt != "prompt-1" || !a1s.StartedAt.Equal(started) || a1s.JobID != "job-1" {
		t.Fatalf("events[1] = %+v, want a1 AgentStarted", events[1])
	}
	a1c, ok := events[2].(AgentCompleted)
	if !ok || a1c.AgentID != "a1" || a1c.Result != "done" || !a1c.CompletedAt.Equal(completed) {
		t.Fatalf("events[2] = %+v, want a1 AgentCompleted", events[2])
	}
	if a2s, ok := events[3].(AgentStarted); !ok || a2s.AgentID != "a2" {
		t.Fatalf("events[3] = %+v, want a2 AgentStarted (StartedAt ordering)", events[3])
	}
	if rs, ok := events[4].(RunStale); !ok || rs.RunID != "wf_a" {
		t.Fatalf("events[4] = %+v, want RunStale", events[4])
	}
	if xs, ok := events[5].(AgentStarted); !ok || xs.JobID != "job-2" || xs.RunID != AdhocRunID || xs.AgentID != "x1" {
		t.Fatalf("events[5] = %+v, want adhoc x1 AgentStarted", events[5])
	}

	// A live delta after the snapshot flows through.
	stream <- sseUpdate(t, "workflow_agent_completed", models.WorkflowEvent{
		Kind: models.WorkflowAgentCompleted, JobID: "job-1", RunID: "wf_a", AgentID: "a2", Timestamp: completed,
	}, "", nil)
	delta := collectEvents(t, source.Events(), 1)
	if dc, ok := delta[0].(AgentCompleted); !ok || dc.AgentID != "a2" {
		t.Fatalf("delta = %+v, want a2 AgentCompleted", delta[0])
	}
}

func TestDaemonSourceReconnect(t *testing.T) {
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	stream1 := make(chan daemon.StateUpdate, 1)
	stream2 := make(chan daemon.StateUpdate, 1)
	client := &fakeDaemonClient{
		snapshot: &models.WorkflowSnapshot{Runs: map[string]*models.WorkflowRunState{}},
		streams:  []chan daemon.StateUpdate{stream1, stream2},
	}

	source := NewDaemonSource(client, DaemonSourceOptions{InitialBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond})
	defer source.Close()

	stream1 <- sseUpdate(t, "workflow_agent_started", models.WorkflowEvent{
		Kind: models.WorkflowAgentStarted, JobID: "job-1", RunID: "wf_a", AgentID: "a1", Timestamp: ts,
	}, "", nil)
	first := collectEvents(t, source.Events(), 1)
	if first[0].(AgentStarted).AgentID != "a1" {
		t.Fatalf("first = %+v", first[0])
	}

	// Kill the stream: the source must reconnect (new StreamState + fresh
	// snapshot) instead of going permanently static.
	close(stream1)

	stream2 <- sseUpdate(t, "workflow_agent_started", models.WorkflowEvent{
		Kind: models.WorkflowAgentStarted, JobID: "job-1", RunID: "wf_a", AgentID: "a2", Timestamp: ts,
	}, "", nil)
	second := collectEvents(t, source.Events(), 1)
	if second[0].(AgentStarted).AgentID != "a2" {
		t.Fatalf("second = %+v", second[0])
	}

	snapCalls, streamCalls := client.calls()
	if streamCalls < 2 {
		t.Errorf("StreamState calls = %d, want >= 2 (reconnect)", streamCalls)
	}
	if snapCalls < 2 {
		t.Errorf("GetWorkflowSnapshot calls = %d, want >= 2 (re-reconcile on reconnect)", snapCalls)
	}
}

func TestDaemonSourceCloseUnblocks(t *testing.T) {
	stream := make(chan daemon.StateUpdate)
	client := &fakeDaemonClient{
		snapshot: &models.WorkflowSnapshot{},
		streams:  []chan daemon.StateUpdate{stream},
	}
	source := NewDaemonSource(client, DaemonSourceOptions{InitialBackoff: time.Millisecond})

	done := make(chan struct{})
	go func() {
		_ = source.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}
	// Events channel must be closed after Close.
	if _, ok := <-source.Events(); ok {
		t.Error("events channel should be closed after Close")
	}
}

func TestDaemonSourceToleratesSnapshotError(t *testing.T) {
	ts := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	stream := make(chan daemon.StateUpdate, 1)
	client := &fakeDaemonClient{
		snapErr: errors.New("404 from older daemon"),
		streams: []chan daemon.StateUpdate{stream},
	}
	source := NewDaemonSource(client, DaemonSourceOptions{InitialBackoff: time.Millisecond})
	defer source.Close()

	// Deltas still flow even when the snapshot endpoint is unavailable.
	stream <- sseUpdate(t, "workflow_agent_started", models.WorkflowEvent{
		Kind: models.WorkflowAgentStarted, JobID: "job-1", RunID: "wf_a", AgentID: "a1", Timestamp: ts,
	}, "", nil)
	events := collectEvents(t, source.Events(), 1)
	if events[0].(AgentStarted).AgentID != "a1" {
		t.Fatalf("got %+v", events[0])
	}
}
