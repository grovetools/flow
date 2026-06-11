package status

import (
	"context"
	"testing"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/workflowmon"
)

// dispatch runs one message through Update and returns the updated Model.
func dispatch(t *testing.T, m Model, msg interface{}) Model {
	t.Helper()
	updated, _ := m.Update(msg)
	out, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", updated)
	}
	return out
}

func TestWorkflowEventMsg_MultiJobRouting(t *testing.T) {
	m := *newDisplayTestModel(testJob("job-a"), testJob("job-b"))

	// Events for two different jobs fold into two separate pane states —
	// no more guard-and-drop on the cursor job.
	m = dispatch(t, m, workflowEventMsg{
		JobID: "job-a",
		Event: workflowmon.RunDiscovered{JobID: "job-a", RunID: "wf_1"},
	})
	m = dispatch(t, m, workflowEventMsg{
		JobID: "job-a",
		Event: workflowmon.AgentStarted{JobID: "job-a", RunID: "wf_1", AgentID: "a1", Phase: "P1"},
	})
	m = dispatch(t, m, workflowEventMsg{
		JobID: "job-b",
		Event: workflowmon.AgentStarted{JobID: "job-b", RunID: "wf_2", AgentID: "b1"},
	})

	stA := m.WorkflowStates["job-a"]
	if stA == nil {
		t.Fatal("no state for job-a")
	}
	if run := stA.Runs["wf_1"]; run == nil || run.Agents["a1"] == nil || run.Agents["a1"].Phase != "P1" {
		t.Errorf("job-a state wrong: %+v", stA.Runs)
	}
	stB := m.WorkflowStates["job-b"]
	if stB == nil {
		t.Fatal("no state for job-b")
	}
	if run := stB.Runs["wf_2"]; run == nil || run.Agents["b1"] == nil {
		t.Errorf("job-b state wrong: %+v", stB.Runs)
	}
	if _, leaked := stA.Runs["wf_2"]; leaked {
		t.Error("job-b's run leaked into job-a's state")
	}

	// Completion folds into the right job.
	m = dispatch(t, m, workflowEventMsg{
		JobID: "job-b",
		Event: workflowmon.AgentCompleted{JobID: "job-b", RunID: "wf_2", AgentID: "b1", Result: "ok"},
	})
	if agent := m.WorkflowStates["job-b"].Runs["wf_2"].Agents["b1"]; !agent.Completed || agent.Result != "ok" {
		t.Errorf("b1 completion not folded: %+v", agent)
	}
}

func TestWorkflowEventMsg_DropsUnknownAndUnattributedJobs(t *testing.T) {
	m := *newDisplayTestModel(testJob("job-a"))

	// Unknown job (another plan's activity on the host-wide subscription).
	m = dispatch(t, m, workflowEventMsg{
		JobID: "other-plan-job",
		Event: workflowmon.AgentStarted{JobID: "other-plan-job", RunID: "wf_9", AgentID: "z1"},
	})
	// No job attribution at all (unstamped session).
	m = dispatch(t, m, workflowEventMsg{
		JobID: "",
		Event: workflowmon.AgentStarted{RunID: "wf_9", AgentID: "z2"},
	})

	if len(m.WorkflowStates) != 0 {
		t.Errorf("WorkflowStates should stay empty, got %+v", m.WorkflowStates)
	}
}

func TestSyncWorkflowMonitors_GatedToRunningJobsAndDaemon(t *testing.T) {
	running := testJob("job-run")
	running.Status = orchestration.JobStatusRunning
	done := testJob("job-done")
	done.Status = orchestration.JobStatusCompleted

	m := newDisplayTestModel(running, done)
	m.workflowMonitorCancels = make(map[string]context.CancelFunc)
	m.workflowMonitorPending = make(map[string]bool)

	// Daemon unreachable: monitors start for running jobs only.
	cmds := m.syncWorkflowMonitors()
	if len(cmds) != 1 {
		t.Fatalf("cmds = %d, want 1 (only the running job)", len(cmds))
	}
	if !m.workflowMonitorPending["job-run"] {
		t.Error("running job should be marked pending")
	}
	if m.workflowMonitorPending["job-done"] {
		t.Error("completed job must not get a monitor")
	}

	// Pending jobs are not double-started on the next refresh.
	if cmds := m.syncWorkflowMonitors(); len(cmds) != 0 {
		t.Errorf("pending job re-started: %d cmds", len(cmds))
	}

	// An established monitor is torn down when its job stops running.
	cancelled := false
	delete(m.workflowMonitorPending, "job-run")
	m.workflowMonitorCancels["job-run"] = func() { cancelled = true }
	running.Status = orchestration.JobStatusCompleted
	if cmds := m.syncWorkflowMonitors(); len(cmds) != 0 {
		t.Errorf("no new monitors expected, got %d", len(cmds))
	}
	if !cancelled {
		t.Error("monitor for stopped job was not cancelled")
	}
	if _, ok := m.workflowMonitorCancels["job-run"]; ok {
		t.Error("cancel entry not removed")
	}

	// With the daemon source active, fallback monitors are torn down and
	// none start.
	running.Status = orchestration.JobStatusRunning
	cancelled = false
	m.workflowMonitorCancels["job-run"] = func() { cancelled = true }
	m.workflowDaemonCancel = func() {}
	if cmds := m.syncWorkflowMonitors(); len(cmds) != 0 {
		t.Errorf("daemon active: no fallback monitors expected, got %d", len(cmds))
	}
	if !cancelled {
		t.Error("fallback monitor should be cancelled when the daemon source is active")
	}
}

func TestCloseWorkflowMonitors(t *testing.T) {
	m := newDisplayTestModel(testJob("job-a"))
	daemonCancelled, monitorCancelled := false, false
	m.workflowDaemonCancel = func() { daemonCancelled = true }
	m.workflowMonitorCancels = map[string]context.CancelFunc{
		"job-a": func() { monitorCancelled = true },
	}

	m.closeWorkflowMonitors()

	if !daemonCancelled || !monitorCancelled {
		t.Errorf("cancels not invoked: daemon=%v monitor=%v", daemonCancelled, monitorCancelled)
	}
	if m.workflowDaemonCancel != nil || len(m.workflowMonitorCancels) != 0 {
		t.Error("cancel state not cleared")
	}
}
