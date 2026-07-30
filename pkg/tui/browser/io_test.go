package browser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/models"
	"github.com/grovetools/core/tui/embed"

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

type noteIndexClient struct {
	daemon.Client
	notes []*models.NoteIndexEntry
}

func (c noteIndexClient) GetNoteIndex(context.Context, string) ([]*models.NoteIndexEntry, error) {
	return c.notes, nil
}

func TestTaggedNotebookNotesAreAttachedToPlans(t *testing.T) {
	summaries := []models.PlanSummary{{PlanName: "feature-plan", Worktree: "feature-worktree"}}
	client := noteIndexClient{notes: []*models.NoteIndexEntry{
		{Title: "Relevant note", Tags: []string{"plan:feature-plan"}, ContentDir: "notes"},
		{Title: "Worktree note", Tags: []string{"worktree:feature-worktree"}, ContentDir: "notes"},
		{Title: "Unrelated", Tags: []string{"other"}, ContentDir: "notes"},
	}}
	attachTaggedPlanNotes(client, "/notebooks/grovetools/plans", summaries)
	if got := summaries[0].NoteCount; got != 2 {
		t.Fatalf("tagged note count = %d, want 2", got)
	}
	if summaries[0].Notes != "" {
		t.Fatalf("tagged note titles leaked into editable plan notes: %q", summaries[0].Notes)
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
	msg := loadPortfolioCmd(summaries, false, false)()
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

func TestPlanWorkspaceDisplayNamePrefersCentralizedNotebookScope(t *testing.T) {
	summary := models.PlanSummary{
		PlansDir:      "/Users/solair/notebooks/grovetools/workspaces/grovetools/plans",
		WorkspaceRoot: "/Users/solair/.local/share/grove/worktrees/grovetools-0bd46c64/Users",
	}
	if got := planWorkspaceDisplayName(summary); got != "grovetools" {
		t.Fatalf("workspace display name = %q, want grovetools", got)
	}
}

func TestPortfolioScopeExcludesOtherNotebookWorkspaces(t *testing.T) {
	root := t.TempDir()
	localPlans := filepath.Join(root, "notebooks", "grovetools", "plans")
	foreignPlans := filepath.Join(root, "notebooks", "matts-website", "plans")
	localDir := filepath.Join(localPlans, "local-plan")
	foreignDir := filepath.Join(foreignPlans, "foreign-plan")
	summaries := map[string]models.PlanSummary{
		localDir:   {PlanDir: localDir, PlanName: "local-plan", PlansDir: localPlans},
		foreignDir: {PlanDir: foreignDir, PlanName: "foreign-plan", PlansDir: foreignPlans},
	}

	filtered := scopedPlanSummaries(summaries, localPlans)
	if len(filtered) != 1 {
		t.Fatalf("scoped summaries=%d, want 1: %+v", len(filtered), filtered)
	}
	if _, ok := filtered[localDir]; !ok {
		t.Fatalf("local plan missing from scoped summaries: %+v", filtered)
	}
}

func TestPortfolioShowsQualifiedArchivesWithoutIndexingArchiveContainer(t *testing.T) {
	root := t.TempDir()
	plansDir := filepath.Join(root, "workspace-a", "plans")
	liveDir := filepath.Join(plansDir, "live")
	archivedDir := filepath.Join(plansDir, ".archive", "old")
	writeTestPlan(t, liveDir, "")
	writeTestPlan(t, archivedDir, "finished")

	summaries := map[string]models.PlanSummary{
		liveDir:     {PlanDir: liveDir, PlanName: "live", PlansDir: plansDir, WorkspaceRoot: filepath.Join(root, "workspace-a")},
		archivedDir: {PlanDir: archivedDir, PlanName: "old", PlansDir: plansDir, WorkspaceRoot: filepath.Join(root, "workspace-a"), Lifecycle: "finished", Archived: true},
	}
	items, err := loadPortfolio(summaries, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("portfolio rows=%d, want live + archived: %+v", len(items), items)
	}
	seen := map[string]PlanListItem{}
	for _, item := range items {
		seen[item.Name] = item
	}
	if _, exists := seen[".archive"]; exists {
		t.Fatal("archive container was represented as a plan")
	}
	archived, exists := seen["old"]
	if !exists || !archived.Archived || archived.Workspace != "workspace-a" || archived.Binding.Health != "archived" {
		t.Fatalf("archive row is not qualified/read-only: %+v", archived)
	}
}

func TestDaemonProjectionColdStartDoesNotHydrateDisk(t *testing.T) {
	summaries := make(map[string]models.PlanSummary, 24)
	for i := 0; i < 24; i++ {
		dir := filepath.Join("/definitely-not-present", fmt.Sprintf("workspace-%02d", i), "plans", fmt.Sprintf("plan-%02d", i))
		summaries[dir] = models.PlanSummary{
			PlanDir: dir, PlanName: filepath.Base(dir), WorkspaceRoot: filepath.Dir(filepath.Dir(dir)),
			PlansDir: filepath.Dir(dir), JobCounts: map[string]int{"completed": i + 1},
			UpdatedAt: time.Unix(int64(i+1), 0), ScannedAt: time.Now(),
		}
	}
	started := time.Now()
	items, err := loadPortfolio(summaries, false, false)
	if err != nil || len(items) != 24 {
		t.Fatalf("cheap projection rows=%d err=%v", len(items), err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("24-row in-memory projection took %s", elapsed)
	}
}

func TestRapidPlanIndexDeltasAreCoalesced(t *testing.T) {
	updates := make(chan daemon.StateUpdate, 3)
	for revision := uint64(5); revision <= 7; revision++ {
		dir := fmt.Sprintf("/plans/p%d", revision)
		updates <- daemon.StateUpdate{PlanIndex: &models.PlanIndexDelta{
			Revision: revision, ScannedAt: time.Now(), Upserts: []models.PlanSummary{{PlanDir: dir}},
		}}
	}
	msg := listenPlanIndexCmd(updates, 9)().(planIndexStreamMsg)
	if msg.firstRevision != 5 || msg.update.PlanIndex.Revision != 7 || len(msg.update.PlanIndex.Upserts) != 3 {
		t.Fatalf("coalesced delta=%+v first=%d", msg.update.PlanIndex, msg.firstRevision)
	}
}

func TestStaleLocalLoadGenerationIsIgnored(t *testing.T) {
	current := &orchestration.Plan{Name: "current", Directory: "/plans/current"}
	m := Model{plans: []PlanListItem{{Name: current.Name, Plan: current}}, loadGeneration: 4}
	stale := &orchestration.Plan{Name: "stale", Directory: "/plans/stale"}
	updated, _ := m.Update(planListLoadCompleteMsg{plans: []PlanListItem{{Name: stale.Name, Plan: stale}}, loadGeneration: 3})
	got := updated.(Model)
	if got.plans[0].Name != "current" {
		t.Fatalf("stale generation replaced rows: %+v", got.plans)
	}
}

func TestDaemonFocusUsesCheapProjectionOnly(t *testing.T) {
	dir := "/not-on-disk/plans/selected"
	m := Model{
		hasDaemonSnapshot: true, dataSource: "daemon live", streamGeneration: 2, loadGeneration: 1,
		planIndexRevision: 3, planSummaries: map[string]models.PlanSummary{dir: {PlanDir: dir, PlanName: "selected", PlansDir: "/not-on-disk/plans"}},
	}
	updated, cmd := m.Update(embed.FocusMsg{})
	got := updated.(Model)
	if cmd == nil {
		t.Fatal("focus did not schedule daemon projection")
	}
	loaded := cmd().(planListLoadCompleteMsg)
	if loaded.error != nil || len(loaded.plans) != 1 || got.loadGeneration != 2 {
		t.Fatalf("focus projection=%+v generation=%d", loaded, got.loadGeneration)
	}
}

func TestStaleSelectedDetailIsIgnored(t *testing.T) {
	plan := &orchestration.Plan{Name: "selected", Directory: "/plans/selected"}
	m := Model{plans: []PlanListItem{{Name: plan.Name, Plan: plan}}, detailGeneration: 3, detailPendingKey: plan.Directory}
	updated, _ := m.Update(planDetailMsg{key: plan.Directory, generation: 2, item: PlanListItem{Name: "stale"}})
	if got := updated.(Model); got.plans[0].Name != "selected" || got.detailPendingKey == "" {
		t.Fatalf("stale selected detail was accepted: %+v", got)
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
	if got.streamGeneration != 5 || !got.streamConnecting || cmd == nil {
		t.Fatalf("reconnect generation=%d connecting=%v cmd=%v", got.streamGeneration, got.streamConnecting, cmd)
	}
	duplicate, duplicateCmd := got.Update(planIndexReconnectMsg{})
	if duplicate.(Model).streamGeneration != 5 || duplicateCmd != nil {
		t.Fatal("a reconnect tick launched a concurrent connection attempt")
	}
	connected := cmd().(planIndexConnectedMsg)
	if calls != 1 || connected.generation != 5 {
		t.Fatalf("factory calls=%d message generation=%d", calls, connected.generation)
	}
	updated, _ = got.Update(connected)
	got = updated.(Model)
	if got.dataSource != "daemon live" || got.statusMessage != "" || got.streamConnecting {
		t.Fatalf("reconnect source/status/connecting=%q/%q/%v", got.dataSource, got.statusMessage, got.streamConnecting)
	}
	got.Close()
}

func TestReconnectCommitsFreshProjectionAtomically(t *testing.T) {
	oldBeta := &orchestration.Plan{Name: "beta-live", Directory: "/workspace-b/plans/beta-live"}
	freshBeta := &orchestration.Plan{Name: "beta-live", Directory: oldBeta.Directory}
	alphaGap := &orchestration.Plan{Name: "alpha-gap", Directory: "/workspace-a/plans/alpha-gap"}
	updates := make(chan daemon.StateUpdate)
	m := Model{
		plans:             []PlanListItem{{Name: "alpha-old", Plan: &orchestration.Plan{Directory: "/workspace-a/plans/alpha-old"}}, {Name: oldBeta.Name, Plan: oldBeta}},
		cursor:            1,
		dataSource:        "stale · reconnecting",
		hasDaemonSnapshot: true,
		streamGeneration:  5,
	}

	updated, cmd := m.Update(planIndexConnectedMsg{
		snapshot: &models.PlanIndexSnapshot{Revision: 12, Plans: []models.PlanSummary{
			{PlanDir: alphaGap.Directory}, {PlanDir: freshBeta.Directory},
		}},
		plans:      []PlanListItem{{Name: alphaGap.Name, Plan: alphaGap}, {Name: freshBeta.Name, Plan: freshBeta}},
		updates:    updates,
		cancel:     func() {},
		generation: 5,
	})
	got := updated.(Model)
	if got.dataSource != "daemon live" || got.statusMessage != "" || got.planIndexRevision != 12 {
		t.Fatalf("source/status/revision=%q/%q/%d", got.dataSource, got.statusMessage, got.planIndexRevision)
	}
	if len(got.plans) != 2 || got.plans[0].Name != "alpha-gap" {
		t.Fatalf("fresh projection not committed exactly once: %+v", got.plans)
	}
	if got.selectedPlanKey() != freshBeta.Directory {
		t.Fatalf("qualified selection moved to %q", got.selectedPlanKey())
	}
	if cmd == nil {
		t.Fatal("connected projection must begin listening")
	}
	got.Close()
}

func TestBufferedSnapshotRevisionDoesNotTriggerReconnect(t *testing.T) {
	updates := make(chan daemon.StateUpdate, 1)
	m := Model{planIndexRevision: 9, dataSource: "daemon live", streamGeneration: 3}
	updated, cmd := m.Update(planIndexStreamMsg{
		update:     daemon.StateUpdate{PlanIndex: &models.PlanIndexDelta{Revision: 9}},
		updates:    updates,
		generation: 3,
	})
	got := updated.(Model)
	if got.dataSource != "daemon live" || got.streamGeneration != 3 || got.planIndexRevision != 9 {
		t.Fatalf("buffered revision caused reconnect: source=%q generation=%d revision=%d", got.dataSource, got.streamGeneration, got.planIndexRevision)
	}
	if cmd == nil {
		t.Fatal("buffered revision must keep listening")
	}
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
		streamGeneration:  4,
	}
	old := &orchestration.Plan{Name: "old", Directory: "/plans/old"}
	updated, _ := m.Update(planListLoadCompleteMsg{
		plans:               []PlanListItem{{Name: old.Name, Plan: old}},
		portfolio:           true,
		planIndexRevision:   8,
		portfolioGeneration: 4,
	})
	got := updated.(Model)
	if len(got.plans) != 1 || got.plans[0].Name != "current" {
		t.Fatalf("stale revision replaced portfolio: %+v", got.plans)
	}

	updated, _ = m.Update(planListLoadCompleteMsg{
		plans:               []PlanListItem{{Name: old.Name, Plan: old}},
		portfolio:           true,
		planIndexRevision:   9,
		portfolioGeneration: 3,
	})
	got = updated.(Model)
	if len(got.plans) != 1 || got.plans[0].Name != "current" {
		t.Fatalf("stale stream generation replaced portfolio: %+v", got.plans)
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

	// SetHoldStatus (the ch chord) must also be refused.
	got, cmd = pressChord(t, m, "ch")
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

// baselineFakeClient serves a pre-discovery revision-0 snapshot on the first
// GetPlanIndex call and the populated baseline afterwards — the exact shape a
// freshly auto-started daemon presents while workspace discovery runs.
type baselineFakeClient struct {
	daemon.Client
	updates  <-chan daemon.StateUpdate
	baseline *models.PlanIndexSnapshot
	calls    int
}

func (f *baselineFakeClient) StreamState(context.Context) (<-chan daemon.StateUpdate, error) {
	return f.updates, nil
}

func (f *baselineFakeClient) GetPlanIndex(context.Context) (*models.PlanIndexSnapshot, error) {
	f.calls++
	if f.calls == 1 {
		return &models.PlanIndexSnapshot{Plans: []models.PlanSummary{}}, nil
	}
	return f.baseline, nil
}

func (f *baselineFakeClient) Close() error { return nil }

// TestConnectWaitsForBaselineSnapshotBeforeGoingLive: a revision-0 empty
// snapshot means "not scanned yet", not "empty portfolio". The connect
// command must hold the live transition until the daemon's first index
// publish, so reconnect never swaps retained stale rows for a pre-discovery
// empty projection.
func TestConnectWaitsForBaselineSnapshotBeforeGoingLive(t *testing.T) {
	updates := make(chan daemon.StateUpdate, 1)
	baseline := &models.PlanIndexSnapshot{
		Revision: 1,
		Plans: []models.PlanSummary{
			{PlanDir: "/plans/alpha", PlanName: "alpha", Lifecycle: "live"},
			{PlanDir: "/plans/beta", PlanName: "beta", Lifecycle: "live"},
		},
	}
	client := &baselineFakeClient{updates: updates, baseline: baseline}
	// The daemon's first publish arrives as a stream delta; it is only the
	// wake-up — the refetched snapshot is authoritative.
	updates <- daemon.StateUpdate{PlanIndex: &models.PlanIndexDelta{Revision: 1}}

	msg := connectPlanIndexCmd(func() daemon.Client { return client }, 7, "", true, true)()
	connected, ok := msg.(planIndexConnectedMsg)
	if !ok {
		t.Fatalf("expected planIndexConnectedMsg, got %T", msg)
	}
	defer connected.cancel()
	if connected.snapshot == nil || connected.snapshot.Revision != 1 || len(connected.snapshot.Plans) != 2 {
		t.Fatalf("connect did not adopt the baseline snapshot: %+v", connected.snapshot)
	}
	if client.calls != 2 {
		t.Fatalf("expected a refetch after the baseline publish, calls=%d", client.calls)
	}
	if len(connected.plans) != 2 {
		t.Fatalf("hydrated %d rows, want 2", len(connected.plans))
	}
}

// TestConnectAcceptsGenuinelyEmptyPortfolioAfterBaselineWait: a daemon that
// never publishes (nothing to index) must still complete the connection after
// the bounded wait, preserving the empty-portfolio cold-start behavior.
func TestConnectAcceptsGenuinelyEmptyPortfolioAfterBaselineWait(t *testing.T) {
	prev := baselineSnapshotWait
	baselineSnapshotWait = 50 * time.Millisecond
	defer func() { baselineSnapshotWait = prev }()

	updates := make(chan daemon.StateUpdate)
	client := &baselineFakeClient{updates: updates}

	start := time.Now()
	msg := connectPlanIndexCmd(func() daemon.Client { return client }, 3, "", true, true)()
	connected, ok := msg.(planIndexConnectedMsg)
	if !ok {
		t.Fatalf("expected planIndexConnectedMsg, got %T", msg)
	}
	defer connected.cancel()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("empty-portfolio connect took %v; the baseline wait must stay bounded", elapsed)
	}
	if len(connected.plans) != 0 {
		t.Fatalf("expected an empty portfolio, got %d rows", len(connected.plans))
	}
}

// TestDaemonPlansAreStale_DetectsRemovedPlan pins the reverse staleness check.
// The forward check catches plans on disk that the daemon has not indexed yet
// (creation lag). It had no counterpart for plans the daemon still lists whose
// directory is gone, so straight after a successful finish a running daemon
// kept the archived plan in the list until its watcher caught up — the plan
// stayed visible in the Plans view.
func TestDaemonPlansAreStale_DetectsRemovedPlan(t *testing.T) {
	plansDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(plansDir, "alive"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plansDir, "alive", ".grove-plan.yml"), []byte("name: alive\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot := []*orchestration.Plan{
		{Name: "alive", Directory: filepath.Join(plansDir, "alive")},
		{Name: "archived", Directory: filepath.Join(plansDir, "archived")},
	}
	if !daemonPlansAreStale(plansDir, snapshot) {
		t.Fatal("a snapshot listing a plan whose directory is gone must be treated as stale")
	}
}

// TestDaemonPlansAreStale_FreshSnapshotIsNotStale is the fence: a snapshot that
// matches disk must not force a full rescan on every refresh.
func TestDaemonPlansAreStale_FreshSnapshotIsNotStale(t *testing.T) {
	plansDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(plansDir, "alive"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plansDir, "alive", ".grove-plan.yml"), []byte("name: alive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := []*orchestration.Plan{{Name: "alive", Directory: filepath.Join(plansDir, "alive")}}
	if daemonPlansAreStale(plansDir, snapshot) {
		t.Fatal("a snapshot matching disk must not be reported stale")
	}
}
