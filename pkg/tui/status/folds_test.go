package status

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/tui/embed"
	"github.com/grovetools/core/tui/keymap"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/workflowmon"
)

// newFoldKeyModel wraps a display-test model with the keymap and which-key host
// the update loop needs, so a test can drive real keystrokes (including the z…
// chords) through Update instead of calling the fold helpers directly.
func newFoldKeyModel(m *Model) Model {
	mm := *m
	mm.KeyMap = NewKeyMap(nil)
	mm.WhichKey = keymap.NewWhichKeyHost(nil, mm.KeyMap.Namespaces()...)
	return mm
}

// pressKeys drives a sequence of single-key presses through Update, returning
// the resulting model and the last command.
func pressKeys(m Model, keys ...string) (Model, tea.Cmd) {
	var cmd tea.Cmd
	for _, k := range keys {
		var updated tea.Model
		updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		m = updated.(Model)
	}
	return m, cmd
}

// foldTestModel is a completed job (so its tree defaults collapsed) carrying one
// finished run with two agents.
func foldTestModel(t *testing.T) *Model {
	t.Helper()
	job := testJob("j")
	job.Status = orchestration.JobStatusCompleted
	m := newDisplayTestModel(job)
	addTestRun(m, "j", "wf_1", 2, 2)
	m.rebuildDisplayRows()
	if len(m.DisplayRows) != 1 {
		t.Fatalf("completed job should start collapsed, rows = %d", len(m.DisplayRows))
	}
	return m
}

func TestFoldOperators_OpenCloseAtCursor(t *testing.T) {
	m := foldTestModel(t)
	m.Cursor = 0

	if !m.openFoldAtCursor() {
		t.Fatal("zo on a job row with a workflow tree must be handled")
	}
	if countRowType(m, RowTypeRun) != 1 {
		t.Fatalf("zo should reveal the run row, rows = %d", len(m.DisplayRows))
	}
	// zo is idempotent and pins the node open.
	if !m.openFoldAtCursor() || countRowType(m, RowTypeRun) != 1 {
		t.Fatalf("a second zo must leave the fold open, rows = %d", len(m.DisplayRows))
	}
	if collapsed, ok := m.FoldState[jobNodeID("j")]; !ok || collapsed {
		t.Errorf("zo should write an explicit open override, FoldState = %v", m.FoldState)
	}

	m.Cursor = 0
	if !m.closeFoldAtCursor() {
		t.Fatal("zc on an open job row must be handled")
	}
	if len(m.DisplayRows) != 1 {
		t.Errorf("zc should re-collapse the tree, rows = %d", len(m.DisplayRows))
	}
}

// TestFoldClose_WalksOutwardFromLeaf pins vim's zc-on-a-closed-fold semantics,
// which is also what makes the single-stroke "h" alias read as "step out of this
// subtree": from an agent row the cursor lands on the enclosing run and closes
// it, and a second press closes the job.
func TestFoldClose_WalksOutwardFromLeaf(t *testing.T) {
	running := testJob("j")
	running.Status = orchestration.JobStatusRunning
	m := newDisplayTestModel(running)
	addTestRun(m, "j", "wf_1", 2, 0)
	m.rebuildDisplayRows()

	agentIdx := m.rowIndexByNodeID(agentNodeID("j", "wf_1", "ag0"))
	if agentIdx < 0 {
		t.Fatalf("agent row not found in %d rows", len(m.DisplayRows))
	}
	m.Cursor = agentIdx

	if !m.closeFoldAtCursor() {
		t.Fatal("zc on a leaf row must be handled by closing the enclosing fold")
	}
	if row := m.currentRow(); row == nil || row.NodeID != runNodeID("j", "wf_1") {
		t.Fatalf("cursor should walk out to the run row, got %+v", row)
	}
	if countRowType(m, RowTypeAgent) != 0 {
		t.Errorf("the enclosing run should be closed, agent rows = %d", countRowType(m, RowTypeAgent))
	}

	if !m.closeFoldAtCursor() {
		t.Fatal("zc on the now-closed run must walk out again")
	}
	if row := m.currentRow(); row == nil || row.NodeID != jobNodeID("j") {
		t.Fatalf("cursor should walk out to the job row, got %+v", row)
	}
	if len(m.DisplayRows) != 1 {
		t.Errorf("the job row should be closed, rows = %d", len(m.DisplayRows))
	}
	// Top level: nothing left to walk out to.
	if m.closeFoldAtCursor() {
		t.Error("zc at the top level with a closed fold should report unhandled")
	}
}

// TestFoldAll_OpenThenCloseEveryLevel asserts zR reaches nodes that were hidden
// behind a closed ancestor, and that zM writes the whole set (not just the
// visible top level) so re-opening a job shows its descendants still closed.
func TestFoldAll_OpenThenCloseEveryLevel(t *testing.T) {
	parent := testJob("parent")
	parent.Status = orchestration.JobStatusCompleted
	child := testJob("child")
	child.ParentJobID = parent.ID
	child.Status = orchestration.JobStatusCompleted
	m := newOwnershipDisplayTestModel(parent, child)
	addTestRun(m, child.ID, "wf_1", maxWorkflowAgentRows+2, maxWorkflowAgentRows+2)
	m.rebuildDisplayRows()

	if len(m.DisplayRows) != 1 {
		t.Fatalf("terminal family should start collapsed, rows = %d", len(m.DisplayRows))
	}

	m.setAllFolds(false)
	// parent → child (hidden behind parent) → run (hidden behind child) →
	// every agent (the cap row is itself a fold node zR uncaps).
	if got := countRowType(m, RowTypeAgent); got != maxWorkflowAgentRows+2 {
		t.Errorf("zR should uncap the agent list, agent rows = %d", got)
	}
	if got := countRowType(m, RowTypeMore); got != 0 {
		t.Errorf("zR should leave no cap row, got %d", got)
	}
	if got := countRowType(m, RowTypeRun); got != 1 {
		t.Errorf("zR should reveal the run nested two levels down, run rows = %d", got)
	}

	m.setAllFolds(true)
	if len(m.DisplayRows) != 1 {
		t.Fatalf("zM should collapse to the root row, rows = %d", len(m.DisplayRows))
	}
	// Re-open just the parent: the child stays closed because zM wrote it too.
	m.Cursor = 0
	m.openFoldAtCursor()
	if len(m.DisplayRows) != 2 {
		t.Fatalf("re-opening the parent should reveal only the child, rows = %d", len(m.DisplayRows))
	}
	if collapsed, ok := m.FoldState[runNodeID(child.ID, "wf_1")]; !ok || !collapsed {
		t.Errorf("zM should have written the hidden run's fold too, FoldState = %v", m.FoldState)
	}
}

// TestFoldKeys_DispatchThroughChordSeam drives the real keystrokes: the z…
// operators resolve through the shared sequence engine, and the single-stroke
// h/l aliases resolve to the same bindings.
func TestFoldKeys_DispatchThroughChordSeam(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []string
	}{
		{"zo", []string{"z", "o"}},
		{"za", []string{"z", "a"}},
		{"l", []string{"l"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newFoldKeyModel(foldTestModel(t))
			m.Cursor = 0
			m, _ = pressKeys(m, tc.keys...)
			if countRowType(&m, RowTypeRun) != 1 {
				t.Fatalf("%s should expand the job tree, rows = %d", tc.name, len(m.DisplayRows))
			}
		})
	}

	// h closes what l opened.
	m := newFoldKeyModel(foldTestModel(t))
	m.Cursor = 0
	m, _ = pressKeys(m, "l")
	m, _ = pressKeys(m, "h")
	if len(m.DisplayRows) != 1 {
		t.Errorf("h should close the fold l opened, rows = %d", len(m.DisplayRows))
	}

	// zM / zR from the keyboard, on a live job that defaults expanded.
	running := testJob("j")
	running.Status = orchestration.JobStatusRunning
	dm := newDisplayTestModel(running)
	addTestRun(dm, "j", "wf_1", 2, 0)
	dm.rebuildDisplayRows()
	mm := newFoldKeyModel(dm)
	mm, _ = pressKeys(mm, "z", "M")
	if len(mm.DisplayRows) != 1 {
		t.Errorf("zM should collapse the running job, rows = %d", len(mm.DisplayRows))
	}
	mm, _ = pressKeys(mm, "z", "R")
	if countRowType(&mm, RowTypeAgent) != 2 {
		t.Errorf("zR should re-expand the whole tree, agent rows = %d", countRowType(&mm, RowTypeAgent))
	}
}

// TestEnterOpensNote_FromJobAndVirtualRows is the point of the exercise: enter
// is no longer overloaded for folding, so it opens the job note in its own pane
// from every row — including a virtual workflow row, where the note it opens is
// the owning job's.
func TestEnterOpensNote_FromJobAndVirtualRows(t *testing.T) {
	running := testJob("j")
	running.Status = orchestration.JobStatusRunning
	running.FilePath = "/plans/p/01-j.md"
	m := newDisplayTestModel(running)
	addTestRun(m, "j", "wf_1", 2, 0)
	m.rebuildDisplayRows()

	agentIdx := m.rowIndexByNodeID(agentNodeID("j", "wf_1", "ag0"))
	if agentIdx < 0 {
		t.Fatalf("agent row not found in %d rows", len(m.DisplayRows))
	}

	for _, tc := range []struct {
		name   string
		cursor int
	}{
		{"job row", 0},
		{"agent row", agentIdx},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mm := newFoldKeyModel(m)
			mm.Hosted = true
			mm.Cursor = tc.cursor
			rowsBefore := len(mm.DisplayRows)

			updated, cmd := mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
			mm = updated.(Model)
			if cmd == nil {
				t.Fatal("enter must emit an open request")
			}
			req, ok := cmd().(embed.EditRequestMsg)
			if !ok {
				t.Fatalf("enter emitted %T, want embed.EditRequestMsg", cmd())
			}
			if req.Path != running.FilePath || !req.Dedicated {
				t.Errorf("enter opened %+v, want the owning job's note in its own pane", req)
			}
			if len(mm.DisplayRows) != rowsBefore {
				t.Errorf("enter must not fold: rows %d → %d", rowsBefore, len(mm.DisplayRows))
			}
			if len(mm.FoldState) != 0 {
				t.Errorf("enter must not touch FoldState, got %v", mm.FoldState)
			}
		})
	}
}

// TestFoldPolicyOverridePinsAcrossStatusFlip guards the reason the operators
// write an explicit override even when it matches the effective state: a run
// the user closed must stay closed when the default policy would re-expand it.
func TestFoldPolicyOverridePinsAcrossStatusFlip(t *testing.T) {
	running := testJob("j")
	running.Status = orchestration.JobStatusRunning
	m := newDisplayTestModel(running)
	addTestRun(m, "j", "wf_1", 2, 0)
	m.rebuildDisplayRows()

	// The live run defaults open; zo is a visual no-op that still pins it.
	m.Cursor = m.rowIndexByNodeID(runNodeID("j", "wf_1"))
	if m.Cursor < 0 {
		t.Fatal("run row not found")
	}
	if !m.openFoldAtCursor() {
		t.Fatal("zo on the run row must be handled")
	}

	// The run goes stale, which the default policy collapses.
	applyWorkflowEvent(m.WorkflowStates["j"], workflowmon.RunStale{RunID: "wf_1"})
	m.rebuildDisplayRows()
	if got := countRowType(m, RowTypeAgent); got != 2 {
		t.Errorf("a pinned-open run must stay open when it goes stale, agent rows = %d", got)
	}
}
