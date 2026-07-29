package browser

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/grovetools/core/git"
	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/flow/pkg/orchestration"
)

// newWidePlanModel builds a portfolio whose rows carry the content that makes
// the table wide in practice: long plan slugs, a workspace identity, several
// job states at once, and a dirty/ahead git status.
func newWidePlanModel(n int) Model {
	plans := make([]PlanListItem, n)
	for i := range plans {
		dir := fmt.Sprintf("/workspace/plans/a-fairly-long-plan-name-%02d", i)
		plans[i] = PlanListItem{
			Name:         fmt.Sprintf("a-fairly-long-plan-name-%02d", i),
			Plan:         &orchestration.Plan{Name: fmt.Sprintf("plan-%02d", i), Directory: dir},
			Key:          coreplan.NewPlanKey(dir),
			Workspace:    "grovetools",
			Repositories: []string{"core", "flow", "grove"},
			Worktree:     "some-other-worktree-name",
			Binding:      coreplan.PlanBinding{Health: coreplan.BindingValid, ContainerPath: dir},
			StatusParts:  map[string]int{"completed": 124, "running": 2, "pending": 6},
			GitStatus:    &git.StatusInfo{IsDirty: true, AheadCount: 4, BehindCount: 86},
			ReviewStatus: "Review",
			NoteCount:    18,
		}
	}
	m := Model{
		plans: plans, height: 40, keys: NewKeyMap(nil),
		holdPending: map[string]bool{}, columnVisibility: defaultBrowserColumnVisibility(),
	}
	for _, col := range browserOptionalColumns {
		m.columnVisibility[col] = true
	}
	return m
}

// TestPlanTableFitsEveryPaneWidth is the guarantee the fitting pass exists
// for: whatever the pane, the table renders inside it. Without it the plan
// table simply overflowed — columns, and eventually the right-hand border,
// painted over whatever shared the split.
func TestPlanTableFitsEveryPaneWidth(t *testing.T) {
	for width := 20; width <= 220; width += 4 {
		m := newWidePlanModel(12)
		m.width = width
		m.ensureCursorVisible()
		// View pads the table one column from the left.
		budget := width - 1
		for _, line := range strings.Split(m.renderPlanTable(), "\n") {
			if got := lipgloss.Width(line); got > budget {
				t.Fatalf("pane %d: table line is %d columns wide (budget %d):\n%s",
					width, got, budget, m.renderPlanTable())
			}
		}
	}
}

// TestNarrowPaneKeepsPlanColumn pins the drop order's one invariant: the
// column that identifies the row survives every other column.
func TestNarrowPaneKeepsPlanColumn(t *testing.T) {
	m := newWidePlanModel(4)
	m.width = 30
	if out := m.renderPlanTable(); !strings.Contains(out, "PLAN") {
		t.Fatalf("narrow pane dropped the identity column:\n%s", out)
	}
}

// TestFitDoesNotPersistDroppedColumns keeps a narrow pane from rewriting the
// user's column choices: reopening the pane wide must restore them.
func TestFitDoesNotPersistDroppedColumns(t *testing.T) {
	m := newWidePlanModel(4)
	m.width = 40
	_ = m.renderPlanTable()
	for _, col := range browserOptionalColumns {
		if !m.columnVisibility[col] {
			t.Fatalf("%s was hidden in the model, not just in the render", col)
		}
	}
	m.width = 400
	out := m.renderPlanTable()
	for _, col := range browserOptionalColumns {
		if !strings.Contains(out, col) {
			t.Fatalf("%s did not come back on a wide pane:\n%s", col, out)
		}
	}
}

// TestStatusCellUsesPerStateIcons pins the STATUS cell to the Jobs tab's
// vocabulary: one icon per state present, and plain text for the counts.
func TestStatusCellUsesPerStateIcons(t *testing.T) {
	m := Model{plans: []PlanListItem{{
		Name:        "plan",
		StatusParts: map[string]int{"completed": 59, "running": 2, "pending": 2, "hold": 1},
	}}}
	want := strings.Join([]string{
		theme.IconStatusCompleted + " 59 completed",
		theme.IconStatusRunning + " 2 running",
		theme.IconStatusPendingUser + " 2 pending",
		theme.IconStatusHold + " 1 on hold",
	}, "  ")
	if got := ansi.Strip(m.formatStatusCell(m.plans[0])); got != want {
		t.Fatalf("status cell = %q, want %q", got, want)
	}
}

func TestStatusCellWithoutJobs(t *testing.T) {
	m := Model{plans: []PlanListItem{{Name: "plan"}}}
	want := theme.IconStatusPendingUser + " no jobs"
	if got := ansi.Strip(m.formatStatusCell(m.plans[0])); got != want {
		t.Fatalf("empty status cell = %q, want %q", got, want)
	}
}
