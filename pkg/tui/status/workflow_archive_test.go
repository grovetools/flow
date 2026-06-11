package status

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/grovetools/flow/pkg/orchestration"
)

// writeArchivedRun lays out one archived wf_* run dir under
// <planDir>/.artifacts/<jobID>/workflows/.
func writeArchivedRun(t *testing.T, planDir, jobID, runID, journal, script string) {
	t.Helper()
	runDir := filepath.Join(planDir, ".artifacts", jobID, "workflows", runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if journal != "" {
		if err := os.WriteFile(filepath.Join(runDir, "journal.jsonl"), []byte(journal), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if script != "" {
		if err := os.WriteFile(filepath.Join(runDir, "script.js"), []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadArchivedWorkflowState(t *testing.T) {
	planDir := t.TempDir()
	journal := `{"type":"started","key":"v2:a","agentId":"a1"}
{"type":"result","key":"v2:a","agentId":"a1","result":"done"}
{"type":"started","key":"v2:b","agentId":"a2"}
not json — tolerated
`
	script := "export const meta = { name: 'review-flow' };\n"
	writeArchivedRun(t, planDir, "job-x", "wf_arch", journal, script)

	msg := loadArchivedWorkflowRunsCmd(planDir, "job-x")().(workflowArchivedRunsMsg)
	if msg.JobID != "job-x" || msg.State == nil {
		t.Fatalf("msg = %+v, want populated state for job-x", msg)
	}
	run := msg.State.Runs["wf_arch"]
	if run == nil {
		t.Fatal("run wf_arch not reconstructed")
	}
	started, completed := run.counts()
	if started != 2 || completed != 1 {
		t.Errorf("counts = %d/%d, want 2 started 1 completed", started, completed)
	}
	if run.Agents["a1"].Result != "done" {
		t.Errorf("a1 result = %q", run.Agents["a1"].Result)
	}
	if run.Meta == nil || run.Meta.Name != "review-flow" {
		t.Errorf("script meta not parsed: %+v", run.Meta)
	}
	// An archived run with unfinished agents is stale, never "running".
	if !run.Stale {
		t.Error("incomplete archived run must be marked stale")
	}

	// No artifacts → nil state.
	if msg := loadArchivedWorkflowRunsCmd(planDir, "job-without-runs")().(workflowArchivedRunsMsg); msg.State != nil {
		t.Errorf("expected nil state for job without artifacts, got %+v", msg.State)
	}
}

func TestArchivedRunsMsg_InstallsStateWithoutClobberingLive(t *testing.T) {
	done := testJob("job-x")
	done.Status = orchestration.JobStatusCompleted
	m := *newDisplayTestModel(done)
	m.workflowDirtyJobs = make(map[string]bool)

	st := newWorkflowPaneState()
	addStub := func(s *workflowPaneState, runID string) {
		s.ensureRun(runID)
	}
	addStub(st, "wf_arch")

	updated, cmd := m.Update(workflowArchivedRunsMsg{JobID: "job-x", State: st})
	m = updated.(Model)
	if m.WorkflowStates["job-x"] != st {
		t.Fatal("archived state not installed")
	}
	if cmd == nil {
		t.Error("install must schedule the coalesced rebuild")
	}
	// After the tick, the collapsed tree adds no rows (job is completed)
	// but the job badge data is present.
	updated, _ = m.Update(workflowRebuildTickMsg{})
	m = updated.(Model)
	if countRowType(&m, RowTypeRun) != 0 {
		t.Error("archived tree must default collapsed")
	}
	if !m.jobHasWorkflowTree(done) {
		t.Error("job should report a workflow tree for the badge")
	}

	// A live state already present is never clobbered.
	live := newWorkflowPaneState()
	addStub(live, "wf_live")
	m.WorkflowStates["job-x"] = live
	updated, _ = m.Update(workflowArchivedRunsMsg{JobID: "job-x", State: st})
	m = updated.(Model)
	if m.WorkflowStates["job-x"] != live {
		t.Error("archived load must not clobber live state")
	}
}

func TestSyncArchivedWorkflowLoads_OncePerCompletedJob(t *testing.T) {
	done := testJob("job-done")
	done.Status = orchestration.JobStatusCompleted
	running := testJob("job-run")
	running.Status = orchestration.JobStatusRunning

	m := newDisplayTestModel(done, running)
	m.workflowArchiveChecked = make(map[string]bool)

	cmds := m.syncArchivedWorkflowLoads()
	if len(cmds) != 1 {
		t.Fatalf("cmds = %d, want 1 (completed job only)", len(cmds))
	}
	if !m.workflowArchiveChecked["job-done"] {
		t.Error("completed job must be marked checked")
	}
	if m.workflowArchiveChecked["job-run"] {
		t.Error("running job must not be archive-loaded")
	}
	// Second reconcile: cached, no new loads.
	if cmds := m.syncArchivedWorkflowLoads(); len(cmds) != 0 {
		t.Errorf("archive load re-fired: %d cmds", len(cmds))
	}
}
