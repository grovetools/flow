package status

import (
	"strings"
	"testing"

	"github.com/grovetools/flow/pkg/workflowmon"
)

func buildWorkflowState() *workflowPaneState {
	s := newWorkflowPaneState()
	events := []workflowmon.Event{
		workflowmon.RunDiscovered{RunID: "wf_run-1", Meta: &workflowmon.ScriptMeta{
			Name:        "release-survey",
			Description: "Deep survey",
			Phases: []workflowmon.PhaseMeta{
				{Title: "Survey", Detail: "parallel deep-dives"},
				{Title: "Verify", Detail: "targeted verifiers"},
			},
		}},
		workflowmon.AgentStarted{RunID: "wf_run-1", AgentID: "agent-a", Prompt: "survey the nb module\nwith details"},
		workflowmon.AgentStarted{RunID: "wf_run-1", AgentID: "agent-b"},
		workflowmon.AgentCompleted{RunID: "wf_run-1", AgentID: "agent-a", Result: "all good"},
		// Prompt upsert arriving after the started event.
		workflowmon.AgentStarted{RunID: "wf_run-1", AgentID: "agent-b", Prompt: "verify the build"},
	}
	for _, ev := range events {
		applyWorkflowEvent(s, ev)
	}
	return s
}

func TestApplyWorkflowEvent_Accumulates(t *testing.T) {
	s := buildWorkflowState()

	run := s.Runs["wf_run-1"]
	if run == nil {
		t.Fatal("run not created")
	}
	if run.Meta == nil || run.Meta.Name != "release-survey" {
		t.Errorf("meta = %+v", run.Meta)
	}
	started, completed := run.counts()
	if started != 2 || completed != 1 {
		t.Errorf("counts = %d/%d, want 2/1", started, completed)
	}
	if run.Agents["agent-b"].Prompt != "verify the build" {
		t.Errorf("prompt upsert lost: %+v", run.Agents["agent-b"])
	}
	if !run.Agents["agent-a"].Completed || run.Agents["agent-a"].Result != "all good" {
		t.Errorf("agent-a = %+v", run.Agents["agent-a"])
	}
	// Started-without-result is in-flight, so the run is running, not idle.
	if got := run.statusLabel(); got != "running" {
		t.Errorf("statusLabel = %q, want running", got)
	}

	applyWorkflowEvent(s, workflowmon.AgentCompleted{RunID: "wf_run-1", AgentID: "agent-b", Result: "done"})
	if got := run.statusLabel(); got != "idle" {
		t.Errorf("statusLabel after all results = %q, want idle", got)
	}

	applyWorkflowEvent(s, workflowmon.RunStale{RunID: "wf_run-1"})
	if got := run.statusLabel(); got != "stale" {
		t.Errorf("statusLabel after RunStale = %q, want stale", got)
	}
}

func TestApplyWorkflowEvent_OutOfOrder(t *testing.T) {
	s := newWorkflowPaneState()
	// AgentStarted before RunDiscovered must still create the run.
	applyWorkflowEvent(s, workflowmon.AgentStarted{RunID: "wf_x", AgentID: "a1"})
	applyWorkflowEvent(s, workflowmon.RunDiscovered{RunID: "wf_x", Meta: &workflowmon.ScriptMeta{Name: "late"}})
	if len(s.RunOrder) != 1 {
		t.Fatalf("RunOrder = %v", s.RunOrder)
	}
	if s.Runs["wf_x"].Meta == nil || s.Runs["wf_x"].Meta.Name != "late" {
		t.Errorf("late meta not applied: %+v", s.Runs["wf_x"].Meta)
	}
}

func TestResolveWorkflowPaneNodes_FlatWithoutPhaseAttribution(t *testing.T) {
	s := buildWorkflowState()
	nodes := resolveWorkflowPaneNodes(s)

	// File source carries no phase attribution: run + 2 agents, no phase nodes.
	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d: %+v", len(nodes), nodes)
	}
	if nodes[0].Type != "run" || nodes[0].Name != "release-survey" || nodes[0].Depth != 0 {
		t.Errorf("node 0 = %+v", nodes[0])
	}
	if nodes[1].Type != "agent" || nodes[1].AgentID != "agent-a" || nodes[1].Depth != 1 {
		t.Errorf("node 1 = %+v", nodes[1])
	}
	if nodes[2].Type != "agent" || nodes[2].AgentID != "agent-b" {
		t.Errorf("node 2 = %+v", nodes[2])
	}
}

func TestResolveWorkflowPaneNodes_PhaseGrouping(t *testing.T) {
	s := buildWorkflowState()
	// Simulate a phase-aware source (e.g. daemon/hooks-backed).
	s.Runs["wf_run-1"].Agents["agent-a"].Phase = "Survey"
	s.Runs["wf_run-1"].Agents["agent-b"].Phase = "Verify"
	applyWorkflowEvent(s, workflowmon.AgentStarted{RunID: "wf_run-1", AgentID: "agent-c"}) // unattributed

	nodes := resolveWorkflowPaneNodes(s)
	var shape []string
	for _, n := range nodes {
		shape = append(shape, n.Type+":"+n.Name)
	}
	want := []string{
		"run:release-survey",
		"phase:Survey", "agent:agent-a",
		"phase:Verify", "agent:agent-b",
		"agent:agent-c",
	}
	if strings.Join(shape, ",") != strings.Join(want, ",") {
		t.Errorf("shape = %v, want %v", shape, want)
	}
	// Phase-grouped agents are indented one deeper than unattributed ones.
	if nodes[2].Depth != 2 || nodes[5].Depth != 1 {
		t.Errorf("depths: grouped=%d unattributed=%d", nodes[2].Depth, nodes[5].Depth)
	}
}

func TestRenderWorkflowPane_Scoreboard(t *testing.T) {
	s := buildWorkflowState()
	result := renderWorkflowPane(s, 0, 100)

	if len(result.nodes) != 3 {
		t.Fatalf("nodes = %d", len(result.nodes))
	}
	for _, want := range []string{"Workflow Runs", "release-survey", "1/2", "1 run(s) • 2 started / 1 completed"} {
		if !strings.Contains(result.treeContent, want) {
			t.Errorf("tree missing %q\n---\n%s", want, result.treeContent)
		}
	}
	// Run selected: detail shows meta + counts + phases.
	for _, want := range []string{"release-survey", "Deep survey", "2 started / 1 completed", "Survey", "Verify"} {
		if !strings.Contains(result.detailContent, want) {
			t.Errorf("detail missing %q\n---\n%s", want, result.detailContent)
		}
	}
	if strings.Contains(result.treeContent, "interrupted") || strings.Contains(result.detailContent, "interrupted") {
		t.Error("in-flight agents must never be labelled interrupted")
	}
}

func TestRenderWorkflowPane_EmptyAndErrorStates(t *testing.T) {
	if result := renderWorkflowPane(nil, 0, 80); !strings.Contains(result.treeContent, "Discovering") {
		t.Errorf("nil state: %q", result.treeContent)
	}

	errState := &workflowPaneState{Err: errTest}
	if result := renderWorkflowPane(errState, 0, 80); !strings.Contains(result.treeContent, "Workflow discovery unavailable") {
		t.Errorf("error state: %q", result.treeContent)
	}

	empty := newWorkflowPaneState()
	empty.SessionDir = "/tmp/session"
	result := renderWorkflowPane(empty, 0, 80)
	if !strings.Contains(result.treeContent, "No workflow runs discovered yet") {
		t.Errorf("empty state: %q", result.treeContent)
	}
	if len(result.nodes) != 0 {
		t.Errorf("empty state nodes = %d", len(result.nodes))
	}
}

var errTest = &testErr{}

type testErr struct{}

func (*testErr) Error() string { return "no session" }

func TestPromptSummary(t *testing.T) {
	if got := promptSummary("line one\nline two", 50); got != "line one" {
		t.Errorf("promptSummary = %q", got)
	}
	long := strings.Repeat("x", 100)
	if got := promptSummary(long, 20); len([]rune(got)) != 20 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncated = %q (len %d)", got, len([]rune(got)))
	}
	if got := promptSummary("", 20); got != "" {
		t.Errorf("empty = %q", got)
	}
}
