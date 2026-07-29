package status

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/flow/pkg/orchestration"
)

// TestScrollOffsetRefillsWhenPaneGrows: the viewport must never leave rows
// above it while blank space sits below the table. Growing the pane (terminal
// resize, closing a split) used to keep the stale offset, so a table that had
// been scrolled to the bottom of a short pane kept rendering only that many
// rows inside a much taller one — the earlier jobs stayed hidden above and the
// reclaimed rows showed as dead space under the table.
func TestScrollOffsetRefillsWhenPaneGrows(t *testing.T) {
	m := newSizedModel(t, 60, 120, 20)

	// Land at the bottom of the short pane, as the TUI does on open.
	m.Cursor = len(m.DisplayRows) - 1
	m.adjustScrollOffset()
	if m.ScrollOffset == 0 {
		t.Fatalf("fixture should be scrolled: offset %d", m.ScrollOffset)
	}

	mdl, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 50})
	m = mdl.(Model)

	visible := m.getVisibleJobCount()
	if got := len(m.getVisibleRows()); got != visible {
		t.Errorf("table renders %d rows in a pane with room for %d — dead space below it", got, visible)
	}
	if want := len(m.DisplayRows) - visible; m.ScrollOffset != want {
		t.Errorf("ScrollOffset = %d, want %d (last full page)", m.ScrollOffset, want)
	}
}

// TestScrollOffsetFollowsAppendedRows: rows appended below a viewport that was
// already showing the last row (a new job lands in the plan, a running job's
// workflow rows expand) must come into view. They used to sit one row below the
// bottom edge — the list looked unchanged and the new job was invisible until
// the user scrolled down.
func TestScrollOffsetFollowsAppendedRows(t *testing.T) {
	m := newSizedModel(t, 30, 120, 24)
	m.Cursor = len(m.DisplayRows) - 1
	m.adjustScrollOffset()

	visible := m.getVisibleJobCount()
	if m.ScrollOffset+visible != len(m.DisplayRows) {
		t.Fatalf("fixture should be pinned to the bottom: offset %d + visible %d != %d rows",
			m.ScrollOffset, visible, len(m.DisplayRows))
	}

	// A refresh picks up a newly created job at the end of the plan.
	job := &orchestration.Job{ID: "new", Filename: "new-job.md", Title: "new job"}
	m.Plan.Jobs = append(m.Plan.Jobs, job)
	m.Plan.JobsByID[job.ID] = job
	m.Jobs = append(m.Jobs, job)
	m.rebuildDisplayRows()
	m.adjustScrollOffset()

	if !strings.Contains(m.View(), "new-job.md") {
		t.Errorf("appended row is not on screen — it takes a manual scroll to see the new job:\n%s", m.View())
	}
}

// TestScrollOffsetKeepsPositionWhenNotAtBottom: following the tail is limited
// to viewports that were already showing it. A user parked mid-list must not be
// yanked to the bottom every time a job is added or a workflow row appears.
func TestScrollOffsetKeepsPositionWhenNotAtBottom(t *testing.T) {
	m := newSizedModel(t, 60, 120, 24)
	m.Cursor = 5
	m.adjustScrollOffset()
	before := m.ScrollOffset

	job := &orchestration.Job{ID: "new", Filename: "new-job.md", Title: "new job"}
	m.Plan.Jobs = append(m.Plan.Jobs, job)
	m.Plan.JobsByID[job.ID] = job
	m.Jobs = append(m.Jobs, job)
	m.rebuildDisplayRows()
	m.adjustScrollOffset()

	if m.ScrollOffset != before {
		t.Errorf("ScrollOffset moved from %d to %d — a mid-list view should stay put", before, m.ScrollOffset)
	}
}

// TestInitialCursorIsLastDisplayRow: the cursor opens on the bottom-most row.
// It indexes DisplayRows, not m.Jobs — the two differ whenever folding hides a
// job row or a workflow adds a virtual one.
func TestInitialCursorIsLastDisplayRow(t *testing.T) {
	plan := &orchestration.Plan{Name: "t", JobsByID: map[string]*orchestration.Job{}}
	for i := range 4 {
		job := &orchestration.Job{
			ID:       fmt.Sprintf("j%d", i),
			Filename: fmt.Sprintf("j%d.md", i),
			Title:    fmt.Sprintf("job %d", i),
		}
		plan.Jobs = append(plan.Jobs, job)
		plan.JobsByID[job.ID] = job
	}
	// j1 owns j2: a collapsed owner hides its child, so DisplayRows is
	// shorter than m.Jobs.
	plan.Jobs[2].ParentJobID = "j1"

	graph, err := orchestration.BuildDependencyGraph(plan)
	if err != nil {
		t.Fatalf("BuildDependencyGraph: %v", err)
	}
	m := New(Config{Plan: plan, Graph: graph})

	if len(m.DisplayRows) == len(m.Jobs) {
		t.Fatalf("fixture should hide a row: %d rows for %d jobs", len(m.DisplayRows), len(m.Jobs))
	}
	if want := len(m.DisplayRows) - 1; m.Cursor != want {
		t.Errorf("initial Cursor = %d, want %d (last display row)", m.Cursor, want)
	}
}
