package browser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/flow/pkg/orchestration"
)

// TestEmptyStateOffersTheRollingPlan pins the fix for the dead end a fresh repo
// used to land on: "No plans found in directory." with nothing to press. The
// empty state now names the rolling plan and takes enter.
func TestEmptyStateOffersTheRollingPlan(t *testing.T) {
	m := New(Config{PlansDir: t.TempDir()})
	m.initialLoaded = true
	m.loading = false

	view := m.View()
	if !strings.Contains(view, RollingPlanName) {
		t.Errorf("empty state does not mention the rolling plan:\n%s", view)
	}
	if !strings.Contains(view, "enter") {
		t.Errorf("empty state does not say what to press:\n%s", view)
	}
}

// TestEnterOnEmptyListCreatesTheRollingPlan walks the whole action: enter with
// no rows dispatches creation, the plan lands in THIS browser's plans directory
// (not one re-resolved from the process working directory), and a second press
// while the first is in flight does not start a duplicate.
func TestEnterOnEmptyListCreatesTheRollingPlan(t *testing.T) {
	plansDir := filepath.Join(t.TempDir(), "workspaces", "fresh", "plans")
	m := New(Config{PlansDir: plansDir})
	m.initialLoaded = true
	m.loading = false

	updated, cmd := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("enter on an empty list dispatched nothing")
	}
	if !m.rollingPending {
		t.Error("creation was not marked pending")
	}

	// A second press while pending must not start another creation.
	_, second := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	if second != nil {
		t.Error("a second enter started a duplicate creation")
	}

	msg, ok := cmd().(rollingPlanCreatedMsg)
	if !ok {
		t.Fatalf("unexpected message type %T", cmd())
	}
	if msg.err != nil {
		t.Fatalf("creating the rolling plan failed: %v", msg.err)
	}
	if !msg.created {
		t.Error("the rolling plan was reported as pre-existing")
	}
	want := filepath.Join(plansDir, RollingPlanName)
	if msg.dir != want {
		t.Errorf("rolling plan created at %q, want %q", msg.dir, want)
	}
	if _, err := os.Stat(filepath.Join(want, ".grove-plan.yml")); err != nil {
		t.Errorf("rolling plan marker missing: %v", err)
	}

	updated, _ = m.Update(msg)
	m = updated.(Model)
	if m.rollingPending {
		t.Error("pending flag survived the result")
	}
	if !strings.Contains(m.statusMessage, "rolling plan") {
		t.Errorf("status did not report the creation: %q", m.statusMessage)
	}
}

// TestEnterWithRowsStillOpensThePlan keeps the empty-state shortcut from
// stealing enter once the list has content.
func TestEnterWithRowsStillOpensThePlan(t *testing.T) {
	m := New(Config{PlansDir: t.TempDir()})
	m.initialLoaded = true
	m.loading = false
	m.plans = []PlanListItem{{Name: "alpha", Plan: &orchestration.Plan{Name: "alpha", Directory: t.TempDir()}}}

	_, cmd := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on a row dispatched nothing")
	}
	if _, ok := cmd().(BrowserPlanSelectedMsg); !ok {
		t.Errorf("enter on a row produced %T, want BrowserPlanSelectedMsg", cmd())
	}
}
