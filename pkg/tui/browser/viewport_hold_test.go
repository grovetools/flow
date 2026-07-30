package browser

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	coreplan "github.com/grovetools/core/pkg/plan"

	"github.com/grovetools/flow/pkg/orchestration"
)

func browserRows(t *testing.T, n int) []PlanListItem {
	t.Helper()
	root := t.TempDir()
	rows := make([]PlanListItem, n)
	for i := range rows {
		dir := filepath.Join(root, fmt.Sprintf("plan-%02d", i))
		rows[i] = PlanListItem{
			Name: fmt.Sprintf("plan-%02d", i),
			Plan: &orchestration.Plan{Name: fmt.Sprintf("plan-%02d", i), Directory: dir},
			Key:  coreplan.NewPlanKey(dir),
		}
	}
	return rows
}

func TestViewportNavigationResizeAndRange(t *testing.T) {
	rows := browserRows(t, 12)
	m := Model{plans: rows, height: 10, embedMode: true, keys: NewKeyMap(nil), holdPending: map[string]bool{}}
	m.ensureCursorVisible()
	if got := m.visibleRowCount(); got != 4 {
		t.Fatalf("visible rows=%d, want 4", got)
	}

	updated, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyPgDown})
	m = updated.(Model)
	if m.cursor != 4 || m.scrollOffset != 4 {
		t.Fatalf("page down cursor/offset=%d/%d, want 4/4", m.cursor, m.scrollOffset)
	}
	if view := m.renderPlanTable(); !strings.Contains(view, "5–8 of 12") {
		t.Fatalf("range indicator missing from %q", view)
	}

	updated, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 8})
	m = updated.(Model)
	if m.cursor < m.scrollOffset || m.cursor >= m.scrollOffset+m.visibleRowCount() {
		t.Fatalf("resize hid cursor: cursor=%d offset=%d visible=%d", m.cursor, m.scrollOffset, m.visibleRowCount())
	}

	updated, _ = m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnd})
	m = updated.(Model)
	if m.cursor != len(rows)-1 || m.scrollOffset != len(rows)-m.visibleRowCount() {
		t.Fatalf("end cursor/offset=%d/%d", m.cursor, m.scrollOffset)
	}
	updated, _ = m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyHome})
	m = updated.(Model)
	if m.cursor != 0 || m.scrollOffset != 0 {
		t.Fatalf("home cursor/offset=%d/%d", m.cursor, m.scrollOffset)
	}
}

func TestQualifiedSelectionSurvivesFilterAndReorder(t *testing.T) {
	root := t.TempDir()
	planA := &orchestration.Plan{Name: "same", Directory: filepath.Join(root, "a", "plans", "same")}
	planB := &orchestration.Plan{Name: "same", Directory: filepath.Join(root, "b", "plans", "same")}
	other := &orchestration.Plan{Name: "other", Directory: filepath.Join(root, "b", "plans", "other")}
	item := func(p *orchestration.Plan) PlanListItem {
		return PlanListItem{Name: p.Name, Plan: p, Key: coreplan.NewPlanKey(p.Directory)}
	}
	m := Model{plans: []PlanListItem{item(planA), item(planB), item(other)}, cursor: 1, height: 7, embedMode: true}
	m.ensureCursorVisible()

	m = m.replacePlanRows([]PlanListItem{item(other), item(planB), item(planA)}, m.selectedPlanKey())
	if m.selectedPlanKey() != planB.Directory {
		t.Fatalf("duplicate slug selected %q, want %q", m.selectedPlanKey(), planB.Directory)
	}
	// Simulate a filter removing rows before the selected identity.
	m = m.replacePlanRows([]PlanListItem{item(planB), item(planA)}, m.selectedPlanKey())
	if m.cursor != 0 || m.scrollOffset != 0 || m.selectedPlanKey() != planB.Directory {
		t.Fatalf("filtered selection cursor/offset/key=%d/%d/%q", m.cursor, m.scrollOffset, m.selectedPlanKey())
	}
}

func TestHoldCompletionRemovesSelectedRowAndPreservesNearest(t *testing.T) {
	rows := browserRows(t, 6)
	key := planItemKey(rows[4])
	m := Model{plans: rows, cursor: 4, height: 8, embedMode: true, holdPending: map[string]bool{key: true}}
	m.ensureCursorVisible()
	updated, cmd := m.Update(holdCompleteMsg{key: key, hold: true})
	m = updated.(Model)
	if cmd == nil || len(m.plans) != 5 {
		t.Fatalf("fallback hold must schedule one reload; command/rows=%v/%d", cmd, len(m.plans))
	}
	if m.cursor != 4 || m.plans[m.cursor].Name != "plan-05" {
		t.Fatalf("nearest row not preserved: cursor=%d row=%q", m.cursor, m.plans[m.cursor].Name)
	}
	if _, pending := m.holdPending[key]; pending {
		t.Fatal("hold remained pending")
	}
}

func TestInvalidBindingDisablesHoldMutation(t *testing.T) {
	rows := browserRows(t, 1)
	rows[0].Plan.Config = &orchestration.PlanConfig{Worktree: "plan-00"}
	rows[0].Binding = coreplan.PlanBinding{Health: coreplan.BindingMismatch}
	m := Model{plans: rows, keys: NewKeyMap(nil), holdPending: map[string]bool{}}

	m, cmd := pressChord(t, m, "ch")
	if cmd != nil || len(m.holdPending) != 0 || !strings.Contains(m.statusMessage, "binding mismatch") {
		t.Fatalf("invalid binding allowed Hold: cmd=%v pending=%v status=%q", cmd, m.holdPending, m.statusMessage)
	}
}

func TestHoldFailureIsDurableAndLeavesRow(t *testing.T) {
	rows := browserRows(t, 1)
	key := planItemKey(rows[0])
	m := Model{plans: rows, holdPending: map[string]bool{key: true}}
	updated, _ := m.Update(holdCompleteMsg{key: key, hold: true, err: errors.New("disk full")})
	m = updated.(Model)
	if len(m.plans) != 1 || !strings.Contains(m.statusMessage, "disk full") {
		t.Fatalf("failure lost row/error: rows=%d status=%q", len(m.plans), m.statusMessage)
	}
}

func TestGitLogIsLazyAndInvalidBindingDisablesAction(t *testing.T) {
	rows := browserRows(t, 1)
	m := Model{plans: rows, height: 20, keys: NewKeyMap(nil), holdPending: map[string]bool{}}
	if m.gitLogLoaded || m.showGitLog {
		t.Fatal("git log was eager")
	}
	m, cmd := pressChord(t, m, "tg")
	if !m.showGitLog || cmd == nil {
		t.Fatal("opening git detail did not lazily request git log")
	}

	m.showGitLog = false
	updated, cmd := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	m = updated.(Model)
	if cmd != nil || !strings.Contains(m.statusMessage, "unbound") {
		t.Fatalf("invalid binding action was not disabled: cmd=%v status=%q", cmd, m.statusMessage)
	}
}
