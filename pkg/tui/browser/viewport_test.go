package browser

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/flow/pkg/orchestration"
)

func viewportPlans(n int) []PlanListItem {
	plans := make([]PlanListItem, n)
	for i := range plans {
		dir := fmt.Sprintf("/workspace/plans/p-%02d", i)
		plans[i] = PlanListItem{Name: fmt.Sprintf("p-%02d", i), Plan: &orchestration.Plan{Name: fmt.Sprintf("p-%02d", i), Directory: dir}, Key: coreplan.NewPlanKey(dir)}
	}
	return plans
}

func TestViewportResizeFilterAndReorderKeepsQualifiedSelectionVisible(t *testing.T) {
	plans := viewportPlans(20)
	m := Model{plans: plans, cursor: 14, height: 12, keys: NewKeyMap(nil), holdPending: map[string]bool{}}
	m.ensureCursorVisible()
	if m.scrollOffset == 0 {
		t.Fatal("cursor below viewport did not scroll")
	}
	selected := m.selectedPlanKey()

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 8})
	m = updated.(Model)
	if m.cursor < m.scrollOffset || m.cursor >= m.scrollOffset+m.visibleRowCount() {
		t.Fatal("resize hid cursor")
	}

	reordered := append([]PlanListItem(nil), plans...)
	reordered[2], reordered[14] = reordered[14], reordered[2]
	m = m.replacePlanRows(reordered, selected)
	if m.selectedPlanKey() != selected || m.cursor != 2 {
		t.Fatalf("reorder lost qualified selection: cursor=%d key=%q", m.cursor, m.selectedPlanKey())
	}

	m = m.replacePlanRows(reordered[:5], selected) // selected remains present at index 2
	if m.cursor < m.scrollOffset || m.cursor >= m.scrollOffset+m.visibleRowCount() {
		t.Fatal("filter clamp hid cursor")
	}
	view := m.renderPlanTable()
	if !strings.Contains(view, "of 5") {
		t.Fatalf("range indicator missing: %q", view)
	}
}

func TestHeldRowRemovalSelectsNearestAndClampsViewport(t *testing.T) {
	plans := viewportPlans(8)
	for i := range plans {
		plans[i].Plan.Config = &orchestration.PlanConfig{}
	}
	key := planItemKey(plans[7])
	m := Model{plans: plans, cursor: 7, height: 8, holdPending: map[string]bool{key: true}}
	m.ensureCursorVisible()
	updated, _ := m.Update(holdCompleteMsg{key: key, hold: true})
	m = updated.(Model)
	if len(m.plans) != 7 || m.cursor != 6 {
		t.Fatalf("held removal plans=%d cursor=%d", len(m.plans), m.cursor)
	}
	if m.scrollOffset > m.cursor {
		t.Fatalf("viewport not clamped: offset=%d cursor=%d", m.scrollOffset, m.cursor)
	}
	if _, pending := m.holdPending[key]; pending {
		t.Fatal("pending state was not cleared")
	}
}

func TestGitLogIsLazyUntilToggle(t *testing.T) {
	m := New(Config{PlansDir: t.TempDir(), WorkspaceDir: t.TempDir()})
	if msg := m.Init(); msg == nil {
		t.Fatal("init should still load plans")
	}
	if m.gitLogLoaded || m.gitLogContent != "" {
		t.Fatal("git log loaded eagerly")
	}
	m.plans = viewportPlans(1)
	updated, cmd := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = updated.(Model)
	if !m.showGitLog || cmd == nil {
		t.Fatal("opening git log did not start lazy fetch")
	}
}

func TestInvalidBindingDisablesPathSensitiveAction(t *testing.T) {
	plans := viewportPlans(1)
	plans[0].Binding = coreplan.PlanBinding{Health: coreplan.BindingMismatch}
	m := Model{plans: plans, keys: NewKeyMap(nil)}
	updated, cmd := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	m = updated.(Model)
	if cmd != nil || !strings.Contains(m.statusMessage, "binding mismatch") {
		t.Fatalf("invalid binding action was not refused: cmd=%v status=%q", cmd, m.statusMessage)
	}

	m.statusMessage = ""
	updated, cmd = m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m = updated.(Model)
	if cmd != nil || !strings.Contains(m.statusMessage, "binding mismatch") || len(m.holdPending) != 0 {
		t.Fatalf("invalid binding hold was not refused: cmd=%v status=%q pending=%v", cmd, m.statusMessage, m.holdPending)
	}
}
