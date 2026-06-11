package status

import (
	"testing"

	"github.com/grovetools/flow/pkg/orchestration"
)

// newDisplayTestModel builds a minimal Model around real jobs, bypassing
// New() (which loads config, plans, and orchestrator state).
func newDisplayTestModel(jobs ...*orchestration.Job) *Model {
	indents := make(map[string]int)
	for _, j := range jobs {
		indents[j.ID] = 0
	}
	m := &Model{
		Jobs:       jobs,
		JobIndents: indents,
		JobParents: make(map[string]*orchestration.Job),
		Selected:   make(map[string]bool),
		FoldState:  make(map[string]bool),
	}
	m.DisplayRows = m.buildDisplayRows()
	return m
}

func testJob(id string) *orchestration.Job {
	return &orchestration.Job{ID: id, Filename: id + ".md", Title: id}
}

func TestBuildDisplayRows_JobsOnly(t *testing.T) {
	m := newDisplayTestModel(testJob("a"), testJob("b"), testJob("c"))

	if len(m.DisplayRows) != 3 {
		t.Fatalf("rows = %d, want 3", len(m.DisplayRows))
	}
	for i, row := range m.DisplayRows {
		if row.Type != RowTypeJob {
			t.Errorf("row %d type = %v, want RowTypeJob", i, row.Type)
		}
		if row.Job != m.Jobs[i] {
			t.Errorf("row %d job mismatch", i)
		}
		if row.NodeID != jobNodeID(m.Jobs[i].ID) {
			t.Errorf("row %d NodeID = %q", i, row.NodeID)
		}
	}
}

func TestCurrentJob_ResolvesThroughRow(t *testing.T) {
	m := newDisplayTestModel(testJob("a"), testJob("b"))
	m.Cursor = 1
	if got := m.CurrentJob(); got == nil || got.ID != "b" {
		t.Errorf("CurrentJob = %+v, want b", got)
	}
	m.Cursor = 99
	if got := m.CurrentJob(); got != nil {
		t.Errorf("CurrentJob out of bounds = %+v, want nil", got)
	}
}

func TestRebuildDisplayRows_CursorStableAcrossReload(t *testing.T) {
	m := newDisplayTestModel(testJob("a"), testJob("b"), testJob("c"))
	m.Cursor = 1 // on job b

	// Simulate a RefreshMsg reflatten: fresh job pointers, one job removed,
	// order changed.
	m.Jobs = []*orchestration.Job{testJob("c"), testJob("b")}
	m.JobIndents = map[string]int{"c": 0, "b": 0}
	m.rebuildDisplayRows()

	if got := m.CurrentJob(); got == nil || got.ID != "b" {
		t.Errorf("cursor should follow job b across rebuild, got %+v", got)
	}

	// Removing the cursor job clamps within bounds.
	m.Jobs = []*orchestration.Job{testJob("c")}
	m.JobIndents = map[string]int{"c": 0}
	m.rebuildDisplayRows()
	if m.Cursor < 0 || m.Cursor >= len(m.DisplayRows) {
		t.Errorf("cursor out of bounds after job removal: %d", m.Cursor)
	}
}

func TestToggleFold_NoOpOnPlainJobRow(t *testing.T) {
	m := newDisplayTestModel(testJob("a"))
	m.Cursor = 0
	// A job row without workflow children is not foldable — Enter must
	// fall through to the default action (edit).
	if m.toggleFoldAtCursor() {
		t.Error("toggleFoldAtCursor should return false on a job row without workflow children")
	}
	if len(m.FoldState) != 0 {
		t.Errorf("FoldState should be untouched, got %v", m.FoldState)
	}
}

func TestVirtualRowsNeverInJobs(t *testing.T) {
	m := newDisplayTestModel(testJob("a"), testJob("b"))
	// Invariant: building display rows never mutates m.Jobs.
	if len(m.Jobs) != 2 {
		t.Fatalf("m.Jobs mutated: %d", len(m.Jobs))
	}
	for _, j := range m.Jobs {
		if j == nil {
			t.Fatal("nil job in m.Jobs")
		}
	}
}
