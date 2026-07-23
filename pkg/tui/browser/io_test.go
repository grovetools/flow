package browser

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/models"

	"github.com/grovetools/flow/pkg/orchestration"
)

// writeTestPlan creates a minimal loadable plan directory: a
// .grove-plan.yml (with the given config status, if any) plus one job
// markdown file so orchestration.LoadPlan recognizes it.
func writeTestPlan(t *testing.T, dir, status string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	config := "notes: test plan\n"
	if status != "" {
		config = "status: " + status + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, ".grove-plan.yml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write plan config: %v", err)
	}
	job := `---
id: job-1
title: Job One
type: oneshot
status: completed
---

Do the thing.
`
	if err := os.WriteFile(filepath.Join(dir, "01-job-one.md"), []byte(job), 0o600); err != nil {
		t.Fatalf("write job file: %v", err)
	}
}

// setupArchiveFixture builds a temp plans directory holding one active
// plan and one archived plan under <plansDir>/.archive, mirroring what
// `flow plan finish --archive` produces.
func setupArchiveFixture(t *testing.T) string {
	t.Helper()
	plansDir := t.TempDir()
	writeTestPlan(t, filepath.Join(plansDir, "active-plan"), "")
	writeTestPlan(t, filepath.Join(plansDir, ".archive", "old-plan"), "finished")
	return plansDir
}

func TestLoadPlansListHidesArchivedByDefault(t *testing.T) {
	plansDir := setupArchiveFixture(t)

	items, err := loadPlansList(plansDir, "", false, false)
	if err != nil {
		t.Fatalf("loadPlansList: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 plan with archived hidden, got %d", len(items))
	}
	if items[0].Name != "active-plan" {
		t.Errorf("expected active-plan, got %q", items[0].Name)
	}
	if items[0].Archived {
		t.Errorf("active plan must not be flagged Archived")
	}
}

func TestLoadPlansListShowsArchivedWhenEnabled(t *testing.T) {
	plansDir := setupArchiveFixture(t)

	items, err := loadPlansList(plansDir, "", false, true)
	if err != nil {
		t.Fatalf("loadPlansList: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 plans with archived shown, got %d", len(items))
	}

	var archived *PlanListItem
	for i := range items {
		if items[i].Name == "old-plan" {
			archived = &items[i]
		}
	}
	if archived == nil {
		t.Fatalf("archived plan old-plan missing from list: %+v", items)
	}
	if !archived.Archived {
		t.Errorf("old-plan should be flagged Archived")
	}
	if archived.ReviewStatus != "Archived" {
		t.Errorf("old-plan ReviewStatus = %q, want %q", archived.ReviewStatus, "Archived")
	}
	if archived.Plan.Directory != filepath.Join(plansDir, ".archive", "old-plan") {
		t.Errorf("old-plan Directory = %q, want it under .archive", archived.Plan.Directory)
	}
}

func TestLoadPlansListMissingArchiveDir(t *testing.T) {
	plansDir := t.TempDir()
	writeTestPlan(t, filepath.Join(plansDir, "active-plan"), "")

	items, err := loadPlansList(plansDir, "", false, true)
	if err != nil {
		t.Fatalf("loadPlansList with no .archive dir: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 plan, got %d", len(items))
	}
}

func TestPortfolioLoadsPlansAcrossWorkspaces(t *testing.T) {
	root := t.TempDir()
	plansA := filepath.Join(root, "workspace-a", "plans")
	plansB := filepath.Join(root, "workspace-b", "plans")
	writeTestPlan(t, filepath.Join(plansA, "plan-a"), "")
	writeTestPlan(t, filepath.Join(plansB, "plan-b"), "")
	summaries := map[string]models.PlanSummary{
		filepath.Join(plansA, "plan-a"): {PlanDir: filepath.Join(plansA, "plan-a"), PlanName: "plan-a", PlansDir: plansA, WorkspaceRoot: filepath.Join(root, "workspace-a"), Selected: true},
		filepath.Join(plansB, "plan-b"): {PlanDir: filepath.Join(plansB, "plan-b"), PlanName: "plan-b", PlansDir: plansB, WorkspaceRoot: filepath.Join(root, "workspace-b")},
	}
	msg := loadPortfolioCmd(summaries, false)()
	loaded := msg.(planListLoadCompleteMsg)
	if loaded.error != nil || len(loaded.plans) != 2 {
		t.Fatalf("portfolio load: plans=%d err=%v", len(loaded.plans), loaded.error)
	}
	seen := map[string]bool{}
	for _, item := range loaded.plans {
		seen[item.Workspace] = true
	}
	if !seen["workspace-a"] || !seen["workspace-b"] {
		t.Fatalf("workspace identities missing: %+v", seen)
	}
}

func TestPlanIndexDeltaAdvancesRevisionAndKeepsListening(t *testing.T) {
	updates := make(chan daemon.StateUpdate, 1)
	m := Model{planIndexRevision: 4, dataSource: "daemon live"}
	updated, cmd := m.Update(planIndexStreamMsg{
		update:  daemon.StateUpdate{PlanIndex: &models.PlanIndexDelta{Revision: 5}},
		updates: updates,
	})
	got := updated.(Model)
	if got.planIndexRevision != 5 {
		t.Fatalf("revision=%d want 5", got.planIndexRevision)
	}
	if cmd == nil {
		t.Fatal("delta should schedule refresh and continue listening")
	}
}

func TestDaemonLossRetainsQualifiedPortfolioAsStale(t *testing.T) {
	selected := &orchestration.Plan{Name: "beta", Directory: "/workspace-b/plans/beta"}
	m := Model{
		plans:             []PlanListItem{{Name: "alpha", Plan: &orchestration.Plan{Name: "alpha", Directory: "/workspace-a/plans/alpha"}}, {Name: "beta", Plan: selected}},
		cursor:            1,
		dataSource:        "daemon live",
		hasDaemonSnapshot: true,
		streamGeneration:  3,
	}

	updated, cmd := m.Update(planIndexStreamClosedMsg{generation: 3})
	got := updated.(Model)
	if got.dataSource != "stale · reconnecting" {
		t.Fatalf("dataSource=%q", got.dataSource)
	}
	if len(got.plans) != 2 || got.selectedPlanKey() != selected.Directory {
		t.Fatalf("stale portfolio/selection changed: plans=%d key=%q", len(got.plans), got.selectedPlanKey())
	}
	if got.statusMessage != "" {
		t.Fatalf("contradictory status remained: %q", got.statusMessage)
	}
	if cmd == nil {
		t.Fatal("daemon loss must schedule reconnect")
	}
}

func TestReconnectUsesFreshFactoryAndClearsFallbackStatus(t *testing.T) {
	updates := make(chan daemon.StateUpdate)
	calls := 0
	client := &fakePlanIndexClient{
		updates:  updates,
		snapshot: &models.PlanIndexSnapshot{Revision: 8},
	}
	m := Model{
		cfg: Config{DaemonClientFactory: func() daemon.Client {
			calls++
			return client
		}},
		dataSource:        "stale · reconnecting",
		statusMessage:     "local fallback — daemon unavailable",
		hasDaemonSnapshot: true,
		streamGeneration:  4,
	}

	updated, cmd := m.Update(planIndexReconnectMsg{})
	got := updated.(Model)
	if got.streamGeneration != 5 || cmd == nil {
		t.Fatalf("reconnect generation=%d cmd=%v", got.streamGeneration, cmd)
	}
	connected := cmd().(planIndexConnectedMsg)
	if calls != 1 || connected.generation != 5 {
		t.Fatalf("factory calls=%d message generation=%d", calls, connected.generation)
	}
	updated, _ = got.Update(connected)
	got = updated.(Model)
	if got.dataSource != "daemon live" || got.statusMessage != "" {
		t.Fatalf("reconnect source/status=%q/%q", got.dataSource, got.statusMessage)
	}
	got.Close()
}

func TestRefreshPreservesQualifiedSelection(t *testing.T) {
	workspaceA := &orchestration.Plan{Name: "same", Directory: "/workspace-a/plans/same"}
	selected := &orchestration.Plan{Name: "same", Directory: "/workspace-b/plans/same"}
	m := Model{
		plans:  []PlanListItem{{Name: workspaceA.Name, Plan: workspaceA}, {Name: selected.Name, Plan: selected}},
		cursor: 1,
	}

	updated, _ := m.Update(planListLoadCompleteMsg{plans: []PlanListItem{
		{Name: selected.Name, Plan: selected},
		{Name: workspaceA.Name, Plan: workspaceA},
	}})
	got := updated.(Model)
	if got.cursor != 0 || got.selectedPlanKey() != selected.Directory {
		t.Fatalf("selection moved after reorder: cursor=%d key=%q", got.cursor, got.selectedPlanKey())
	}
}

func TestOlderPortfolioLoadCannotOverwriteNewRevision(t *testing.T) {
	current := &orchestration.Plan{Name: "current", Directory: "/plans/current"}
	m := Model{
		plans:             []PlanListItem{{Name: current.Name, Plan: current}},
		hasDaemonSnapshot: true,
		planIndexRevision: 9,
	}
	old := &orchestration.Plan{Name: "old", Directory: "/plans/old"}
	updated, _ := m.Update(planListLoadCompleteMsg{
		plans:             []PlanListItem{{Name: old.Name, Plan: old}},
		portfolio:         true,
		planIndexRevision: 8,
	})
	got := updated.(Model)
	if len(got.plans) != 1 || got.plans[0].Name != "current" {
		t.Fatalf("stale generation replaced portfolio: %+v", got.plans)
	}
}

// Embedding daemon.Client keeps the fake focused on the three methods used by
// the plan-index connection command.
type fakePlanIndexClient struct {
	daemon.Client
	updates  <-chan daemon.StateUpdate
	snapshot *models.PlanIndexSnapshot
}

func (f *fakePlanIndexClient) StreamState(context.Context) (<-chan daemon.StateUpdate, error) {
	return f.updates, nil
}

func (f *fakePlanIndexClient) GetPlanIndex(context.Context) (*models.PlanIndexSnapshot, error) {
	return f.snapshot, nil
}

func (f *fakePlanIndexClient) Close() error { return nil }

func TestArchivedRowRefusesMutatingActions(t *testing.T) {
	plansDir := setupArchiveFixture(t)

	items, err := loadPlansList(plansDir, "", false, true)
	if err != nil {
		t.Fatalf("loadPlansList: %v", err)
	}

	m := Model{
		plans:          items,
		plansDirectory: plansDir,
		keys:           NewKeyMap(nil),
	}
	for i := range items {
		if items[i].Archived {
			m.cursor = i
		}
	}

	// FinishPlan (ctrl+x) is a mutating row-action: it must be refused
	// with the read-only status message and produce no command.
	updated, cmd := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyCtrlX})
	got := updated.(Model)
	if got.statusMessage != archivedReadOnlyMessage {
		t.Errorf("statusMessage = %q, want %q", got.statusMessage, archivedReadOnlyMessage)
	}
	if cmd != nil {
		t.Errorf("expected no command for refused mutating action")
	}

	// SetHoldStatus (h) must also be refused.
	updated, cmd = m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	got = updated.(Model)
	if got.statusMessage != archivedReadOnlyMessage {
		t.Errorf("hold statusMessage = %q, want %q", got.statusMessage, archivedReadOnlyMessage)
	}
	if cmd != nil {
		t.Errorf("expected no command for refused hold action")
	}

	// ViewPlan (enter) stays allowed: it should emit a plan-selected
	// command instead of the read-only refusal.
	updated, cmd = m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	got = updated.(Model)
	if got.statusMessage == archivedReadOnlyMessage {
		t.Errorf("ViewPlan must remain allowed on archived rows")
	}
	if cmd == nil {
		t.Fatalf("expected a command from ViewPlan on an archived row")
	}
	msg := cmd()
	sel, ok := msg.(BrowserPlanSelectedMsg)
	if !ok {
		t.Fatalf("expected BrowserPlanSelectedMsg, got %T", msg)
	}
	if sel.PlanName != "old-plan" {
		t.Errorf("selected plan = %q, want old-plan", sel.PlanName)
	}
}
