package status

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/flow/pkg/orchestration"
)

// demoteTestModel builds a two-job model whose jobs are both demotable.
func demoteTestModel(t *testing.T) Model {
	t.Helper()
	j1 := &orchestration.Job{ID: "j1", Filename: "01-one.md", Title: "one", FilePath: "/tmp/plan/01-one.md", Status: orchestration.JobStatusPending}
	j2 := &orchestration.Job{ID: "j2", Filename: "02-two.md", Title: "two", FilePath: "/tmp/plan/02-two.md", Status: orchestration.JobStatusPending}
	plan := &orchestration.Plan{
		Name:     "test",
		Jobs:     []*orchestration.Job{j1, j2},
		JobsByID: map[string]*orchestration.Job{j1.ID: j1, j2.ID: j2},
	}
	graph, err := orchestration.BuildDependencyGraph(plan)
	if err != nil {
		t.Fatalf("BuildDependencyGraph: %v", err)
	}
	m := New(Config{Plan: plan, Graph: graph})
	m.Width, m.Height = 120, 40
	return m
}

// TestDemoteKeyOpensReasonDialog pins the demote confirmation: "D" no longer
// fires a silent background command, it opens a dialog naming the job and
// asking why it is being parked.
func TestDemoteKeyOpensReasonDialog(t *testing.T) {
	m := demoteTestModel(t)

	cursorJob := m.CurrentJob()
	if cursorJob == nil {
		t.Fatal("no job under the cursor")
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	m = updated.(Model)

	if !m.Demoting {
		t.Fatal("expected D to open the demote dialog")
	}
	if len(m.DemoteTargets) != 1 {
		t.Fatalf("expected exactly the cursor job as the target, got %d", len(m.DemoteTargets))
	}
	if got := m.DemoteTargets[0].Filename; got != cursorJob.Filename {
		t.Fatalf("expected the cursor job as the target, got %q", got)
	}
	view := m.View()
	if !strings.Contains(view, cursorJob.Filename) || !strings.Contains(view, "Reason") {
		t.Errorf("dialog should name the job and ask for a reason, got:\n%s", view)
	}

	// Esc backs out without demoting anything.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.Demoting || len(m.DemoteTargets) != 0 {
		t.Errorf("esc should cancel the demote dialog")
	}
}

// TestDemoteTargetsFollowSelection verifies a space-selected batch is what a
// demote applies to — the "park this plan's pending jobs" pass — with the
// cursor row as the fallback.
func TestDemoteTargetsFollowSelection(t *testing.T) {
	m := demoteTestModel(t)
	m.Selected["j1"] = true
	m.Selected["j2"] = true

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	m = updated.(Model)

	if len(m.DemoteTargets) != 2 {
		t.Fatalf("expected both selected jobs as targets, got %d", len(m.DemoteTargets))
	}
	if view := m.View(); !strings.Contains(view, "Demote 2 jobs") {
		t.Errorf("dialog should say how many jobs are being parked, got:\n%s", view)
	}
}

// TestDemoteResultIsVisibleAndSurvivesRefresh is the original complaint in
// test form: pressing D looked like it did nothing and never said where the
// note went. The result must render in the footer AND survive the plan refresh
// that immediately follows the demote.
func TestDemoteResultIsVisibleAndSurvivesRefresh(t *testing.T) {
	m := demoteTestModel(t)

	updated, _ := m.Update(DemoteJobMsg{NotePath: "/nb/ws/inbox/20260809-one.md"})
	m = updated.(Model)

	if !strings.Contains(m.StatusSummary, "/nb/ws/inbox/20260809-one.md") {
		t.Fatalf("demote result should name the note path, got %q", m.StatusSummary)
	}
	if !strings.Contains(m.View(), "20260809-one.md") {
		t.Errorf("demote result should be rendered in the footer, view:\n%s", m.View())
	}

	// A refresh tick reloads the plan; it must not blank the message.
	updated, _ = m.Update(RefreshMsg{})
	m = updated.(Model)
	if !strings.Contains(m.StatusSummary, "20260809-one.md") {
		t.Errorf("refresh cleared the demote result: %q", m.StatusSummary)
	}
}

// TestStatusSummaryExpires verifies the footer message is transient: it is
// stamped on the first tick and swept once it outlives its TTL, so a stale
// result never masquerades as live state.
func TestStatusSummaryExpires(t *testing.T) {
	m := demoteTestModel(t)
	m.StatusSummary = "Demoted to note: /nb/ws/inbox/x.md"

	updated, _ := m.Update(RefreshTickMsg(time.Now()))
	m = updated.(Model)
	if m.StatusSummaryAt.IsZero() {
		t.Fatal("first tick should stamp an unstamped message")
	}
	if m.StatusSummary == "" {
		t.Fatal("first tick must not clear a fresh message")
	}

	m.StatusSummaryAt = time.Now().Add(-2 * statusSummaryTTL)
	updated, _ = m.Update(RefreshTickMsg(time.Now()))
	m = updated.(Model)
	if m.StatusSummary != "" {
		t.Errorf("expired message should be swept, got %q", m.StatusSummary)
	}
}
