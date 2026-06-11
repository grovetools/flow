package status

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/flow/pkg/orchestration"
)

// Workflow event-source lifecycle. Two mutually exclusive paths feed the
// per-job WorkflowStates registry:
//
//   - Daemon path (preferred): ONE workflowmon.DaemonSource subscription
//     covering every job, started when the daemon SSE stream connects. The
//     source reconnects with backoff internally, so a daemon restart never
//     leaves the tree static.
//   - FileSource fallback (daemon unreachable): one journal-tailing monitor
//     per RUNNING job, reconciled on every RefreshMsg and torn down when
//     the job stops or the daemon source takes over.

// syncWorkflowMonitors reconciles the FileSource fallback monitors with the
// current job statuses. No-op (plus teardown) while the daemon source is
// active or the daemon stream is connected. Returns the discovery commands
// for newly running jobs.
func (m *Model) syncWorkflowMonitors() []tea.Cmd {
	if m.workflowMonitorCancels == nil {
		return nil
	}
	if m.workflowDaemonCancel != nil || m.DaemonConnected {
		for id, cancel := range m.workflowMonitorCancels {
			cancel()
			delete(m.workflowMonitorCancels, id)
		}
		return nil
	}

	running := make(map[string]bool, len(m.Jobs))
	for _, job := range m.Jobs {
		if job.Status == orchestration.JobStatusRunning {
			running[job.ID] = true
		}
	}

	// Tear down monitors whose job is no longer running.
	for id, cancel := range m.workflowMonitorCancels {
		if !running[id] {
			cancel()
			delete(m.workflowMonitorCancels, id)
		}
	}

	// Start monitors for running jobs without one (and without an in-flight
	// session discovery).
	var cmds []tea.Cmd
	for _, job := range m.Jobs {
		if job.Status != orchestration.JobStatusRunning {
			continue
		}
		if m.workflowMonitorCancels[job.ID] != nil || m.workflowMonitorPending[job.ID] {
			continue
		}
		m.workflowMonitorPending[job.ID] = true
		cmds = append(cmds, startWorkflowMonitorCmd(job, m.MsgCh))
	}
	return cmds
}

// closeWorkflowMonitors tears down the daemon source and every FileSource
// fallback monitor. Called from Model.Close().
func (m *Model) closeWorkflowMonitors() {
	if m.workflowDaemonCancel != nil {
		m.workflowDaemonCancel()
		m.workflowDaemonCancel = nil
	}
	for id, cancel := range m.workflowMonitorCancels {
		cancel()
		delete(m.workflowMonitorCancels, id)
	}
}
