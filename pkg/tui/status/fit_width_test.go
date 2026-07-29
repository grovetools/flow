package status

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/grovetools/flow/pkg/orchestration"
)

// newWideContentModel builds a model whose rows carry the kind of content that
// used to defeat the width estimator: long filenames, long titles, and a
// TOKENS cell several times wider than its header.
func newWideContentModel(t *testing.T, n int) Model {
	t.Helper()
	plan := &orchestration.Plan{Name: "t", JobsByID: map[string]*orchestration.Job{}}
	for i := range n {
		job := &orchestration.Job{
			ID:       fmt.Sprintf("j%d", i),
			Filename: fmt.Sprintf("%02d-a-fairly-long-job-filename-like-the-real-ones.md", i),
			Title:    "a title that is also long enough to matter for column widths",
			Type:     orchestration.JobTypeInteractiveAgent,
			Status:   orchestration.JobStatusCompleted,
			Worktree: "some-worktree-name",
		}
		plan.Jobs = append(plan.Jobs, job)
		plan.JobsByID[job.ID] = job
	}
	graph, err := orchestration.BuildDependencyGraph(plan)
	if err != nil {
		t.Fatalf("BuildDependencyGraph: %v", err)
	}
	m := New(Config{Plan: plan, Graph: graph})
	for _, col := range m.availableColumns {
		m.columnVisibility[col] = true
	}
	mdl, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	return mdl.(Model)
}

// TestTableRenderedWidthMatchesRender pins the arithmetic in
// tableRenderedWidth to what gtable actually draws. The fit loop is only as
// honest as this formula: if it under-reports, the table overflows its pane;
// if it over-reports, columns drop that would have fitted.
func TestTableRenderedWidthMatchesRender(t *testing.T) {
	m := newWideContentModel(t, 12)

	for _, visible := range [][]string{
		{"JOB"},
		{"JOB", "TOKENS"},
		{"JOB", "TYPE", "STATUS"},
		{"JOB", "TITLE", "TYPE", "STATUS", "WORKTREE", "UPDATED", "TOKENS"},
	} {
		for col := range m.columnVisibility {
			m.columnVisibility[col] = false
		}
		for _, col := range visible {
			m.columnVisibility[col] = true
		}
		headers := m.tableHeaders()
		want := lipgloss.Width(m.renderTableView())
		if got := tableRenderedWidth(headers, m.measureTableColumns(headers)); got != want {
			t.Errorf("columns %v: tableRenderedWidth = %d, rendered table is %d columns wide", visible, got, want)
		}
	}
}

// TestTableFitsEveryPaneWidth is the guarantee the whole fitting pass exists
// for: whatever the pane, the table renders inside it. Below ~100 columns this
// used to overflow no matter how narrow the pane got — TOKENS was missing from
// the drop list, so it survived every pass, and the JOB column was sized to the
// longest filename with no cap. The rest hung off the right edge, table border
// included.
func TestTableFitsEveryPaneWidth(t *testing.T) {
	m := newWideContentModel(t, 12)

	for width := 20; width <= 220; width += 4 {
		rendered := m.renderTableViewWithWidth(width)
		for i, line := range strings.Split(rendered, "\n") {
			if w := lipgloss.Width(line); w > width {
				t.Fatalf("pane width %d: line %d is %d columns wide:\n%s", width, i, w, rendered)
			}
		}
	}
}

// TestTableKeepsBordersWhenNarrow: fitting must produce a whole table, not a
// clipped one — if the right-hand border is missing the user can't tell where
// the last column ends or whether content was cut.
func TestTableKeepsBordersWhenNarrow(t *testing.T) {
	m := newWideContentModel(t, 12)

	for _, width := range []int{40, 60, 72, 84, 100} {
		rendered := m.renderTableViewWithWidth(width)
		if !strings.Contains(rendered, "╮") || !strings.Contains(rendered, "╯") {
			t.Errorf("pane width %d: table lost its right-hand border:\n%s", width, rendered)
		}
	}
}

// TestWideColumnsSurviveWidePanes: fitting is a response to pressure, not a
// standing cap. A pane with room to spare keeps every column and every
// filename intact (the long-names layout guarantee).
func TestWideColumnsSurviveWidePanes(t *testing.T) {
	m := newWideContentModel(t, 12)
	headers := m.tableHeaders()
	natural := tableRenderedWidth(headers, m.measureTableColumns(headers))

	fitted := m.fitToWidth(natural + 10)
	if fitted.jobCellCap != 0 {
		t.Errorf("jobCellCap = %d in a pane with room to spare, want 0", fitted.jobCellCap)
	}
	if got := len(fitted.tableHeaders()); got != len(headers) {
		t.Errorf("dropped columns unnecessarily: %d headers, want %d", got, len(headers))
	}
	if !strings.Contains(fitted.renderTableView(), "00-a-fairly-long-job-filename-like-the-real-ones.md") {
		t.Error("a filename was truncated in a pane that had room for it")
	}
}

// TestTokensColumnIsDroppable is the specific regression: TOKENS was in
// availableColumns but not in the drop list, so it could never be dropped.
func TestTokensColumnIsDroppable(t *testing.T) {
	m := newWideContentModel(t, 12)
	for _, col := range m.columnDropOrder() {
		if col == "TOKENS" {
			return
		}
	}
	t.Error("TOKENS is not in columnDropOrder — it can never be dropped to make the table fit")
}

// TestEveryColumnIsDroppableExceptJob: the drop order is derived from
// availableColumns, so a column added later cannot silently become permanent.
func TestEveryColumnIsDroppableExceptJob(t *testing.T) {
	m := newWideContentModel(t, 12)
	m.availableColumns = append(m.availableColumns, "BRAND_NEW")

	droppable := make(map[string]bool)
	for _, col := range m.columnDropOrder() {
		droppable[col] = true
	}
	for _, col := range m.availableColumns {
		if col == "JOB" {
			if droppable[col] {
				t.Error("JOB must never be dropped — it carries the row's identity")
			}
			continue
		}
		if !droppable[col] {
			t.Errorf("column %q can never be dropped", col)
		}
	}
}
