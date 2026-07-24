package browser

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/pkg/worktreeregistry"
	"github.com/grovetools/core/tui/embed"
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

func TestHostedOpenRequestsWorkspaceNavigation(t *testing.T) {
	const container = "/worktrees/feature-plan"
	plan := &orchestration.Plan{Name: "feature-plan", Directory: "/plans/feature-plan"}
	m := Model{
		plans: []PlanListItem{{
			Name: "feature-plan", Plan: plan,
			Binding:      coreplan.PlanBinding{Health: coreplan.BindingValid, ContainerPath: container},
			ActionTarget: coreplan.PlanActionTarget{ContainerPath: container},
		}},
		keys: NewKeyMap(nil), hosted: true,
	}
	_, cmd := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	if cmd == nil {
		t.Fatal("hosted open returned no navigation command")
	}
	msg, ok := cmd().(embed.SwitchWorkspaceRequestMsg)
	if !ok || msg.Path != container || msg.FocusPanel != "shell" {
		t.Fatalf("hosted open = %#v", msg)
	}
}

func TestFixedWorktreePlanComesFromRegistry(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	root := filepath.Join(t.TempDir(), ".grove-worktrees", "feature-plan")
	if err := worktreeregistry.Save(&worktreeregistry.Entry{AbsPath: root, Plan: "feature-plan"}); err != nil {
		t.Fatal(err)
	}
	plan, fixed := fixedWorktreePlan(filepath.Join(root, "repo"))
	if !fixed || plan != "feature-plan" {
		t.Fatalf("fixed worktree plan = %q, %v", plan, fixed)
	}
}

func TestWorktreeColumnOnlyAppearsForDistinctWorktree(t *testing.T) {
	m := Model{plans: []PlanListItem{
		{Name: "feature", Worktree: "feature"},
		{Name: RollingPlanName},
	}}
	if m.hasDistinctWorktree() {
		t.Fatal("redundant plan-name worktree should not show the column")
	}
	m.plans = append(m.plans, PlanListItem{Name: "alternate-plan", Worktree: "shared-worktree"})
	if !m.hasDistinctWorktree() {
		t.Fatal("a plan using a differently named worktree should show the column")
	}
}

func TestPlanIdentityUsesBoundedRepositoryCount(t *testing.T) {
	if got := formatPlanIdentity("grovetools", 31); got != "grovetools / 31 repos" {
		t.Fatalf("identity = %q", got)
	}
	if got := formatPlanIdentity("grovetools", 1); got != "grovetools / 1 repo" {
		t.Fatalf("singular identity = %q", got)
	}
}

func TestColumnSelectorDefaultsWorkspaceHiddenAndCanEnableIt(t *testing.T) {
	m := Model{
		plans: []PlanListItem{{Name: "plan", Workspace: "grovetools"}},
		keys:  NewKeyMap(nil), columnVisibility: defaultBrowserColumnVisibility(),
	}
	if out := m.renderPlanTable(); strings.Contains(out, "WORKSPACE / REPOS") {
		t.Fatalf("workspace column should be hidden by default:\n%s", out)
	}
	updated, _ := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'T'}})
	m = updated.(Model)
	updated, _ = m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if out := m.renderPlanTable(); !strings.Contains(out, "WORKSPACE / REPOS") {
		t.Fatalf("workspace column was not enabled:\n%s", out)
	}
}

func TestRollingPlanRendersInMainWorkspace(t *testing.T) {
	m := Model{
		plans:            []PlanListItem{{Name: RollingPlanName, WorkspaceRoot: "/workspace"}},
		columnVisibility: defaultBrowserColumnVisibility(),
	}
	if out := m.renderPlanTable(); !strings.Contains(out, "main workspace") || strings.Contains(out, "unbound") {
		t.Fatalf("rolling binding is misleading:\n%s", out)
	}
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
