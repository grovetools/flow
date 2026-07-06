package view

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/tui/status"
)

// newStatusHostModel builds a view.Model booted straight into status mode over
// a one-job plan, sized so the pane Manager has real dimensions. This is the
// host layer the status model runs under — the layer whose esc handler decides
// status↔browser transitions.
func newStatusHostModel(t *testing.T) Model {
	t.Helper()
	job := &orchestration.Job{ID: "j1", Filename: "j1.md", Title: "job one"}
	plan := &orchestration.Plan{
		Name:     "t",
		Jobs:     []*orchestration.Job{job},
		JobsByID: map[string]*orchestration.Job{job.ID: job},
	}
	graph, err := orchestration.BuildDependencyGraph(plan)
	if err != nil {
		t.Fatalf("BuildDependencyGraph: %v", err)
	}
	m := New(Config{InitialPlan: plan, InitialGraph: graph})
	mdl, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return mdl.(Model)
}

// TestEscDismissingWhichKeyDoesNotEscapeStatusTUI is the regression guard for
// jobs 43/46: pressing a namespace prefix ("v") arms the which-key popup, and
// the esc pressed to dismiss it must be delegated INTO the status model (where
// the seam consumes it via SequenceCancel) rather than read by the host as
// "pop back to the plan browser". The bug this pins: the host esc handler
// ignored chord-pending state, so v-then-esc exited the whole status TUI.
func TestEscDismissingWhichKeyDoesNotEscapeStatusTUI(t *testing.T) {
	m := newStatusHostModel(t)
	if m.mode != modeStatus {
		t.Fatalf("precondition: expected modeStatus, got %v", m.mode)
	}

	// Arm the View ("v") namespace prefix — the which-key popup is now pending.
	mdl, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	m = mdl.(Model)
	if m.s.statusModel == nil || !m.s.statusModel.IsChordPending() {
		t.Fatalf("expected an armed chord after pressing 'v' (IsChordPending), got pending=%v",
			m.s.statusModel != nil && m.s.statusModel.IsChordPending())
	}

	// Press esc to dismiss the popup.
	mdl, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = mdl.(Model)

	// The status TUI must still be up (NOT bounced to the browser), and the
	// chord must be cleared.
	if m.mode != modeStatus {
		t.Errorf("esc-to-dismiss-which-key escaped the status TUI: mode=%v, want modeStatus", m.mode)
	}
	if m.s.statusModel == nil {
		t.Fatal("status model was torn down by esc-to-dismiss-which-key")
	}
	if m.s.statusModel.IsChordPending() {
		t.Errorf("esc should have cancelled the armed chord, but IsChordPending still true")
	}
}

// TestChordContinuationNotStolenByHostShortcut is the regression guard for the
// "va should open the agent pane but opens the flow-plan-add dialog" bug: the
// host intercepts flat "a" (→ add-job wizard) before delegating to the status
// model, which stole the "a" that completes the "va" chord (preview agent
// pane — what flat "p" did before the rebind). With the chord-pending guard the
// host stands down, so "v" then "a" completes the chord and never opens the
// add-job wizard.
func TestChordContinuationNotStolenByHostShortcut(t *testing.T) {
	m := newStatusHostModel(t)

	// Arm the View ("v") prefix.
	mdl, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	m = mdl.(Model)
	if !m.s.statusModel.IsChordPending() {
		t.Fatalf("expected an armed chord after 'v'")
	}

	// Press "a" to complete "va". The host must NOT hijack it into the add-job
	// wizard.
	mdl, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = mdl.(Model)

	if m.mode == modeAddWizard {
		t.Errorf("'va' chord was hijacked into the add-job wizard (mode=modeAddWizard)")
	}
	if m.mode != modeStatus {
		t.Errorf("mode after 'va' = %v, want modeStatus", m.mode)
	}
	if m.s.statusModel != nil && m.s.statusModel.IsChordPending() {
		t.Errorf("'va' chord did not resolve — still pending after the 'a'")
	}
}

// TestBareAStillOpensAddJob pins that with no chord armed, flat "a" still opens
// the add-job wizard (the host shortcut is only suppressed mid-chord).
func TestBareAStillOpensAddJob(t *testing.T) {
	m := newStatusHostModel(t)
	if m.s.statusModel.IsChordPending() {
		t.Fatalf("precondition: no chord should be armed")
	}
	mdl, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = mdl.(Model)
	if m.mode != modeAddWizard {
		t.Errorf("bare 'a' should open the add-job wizard: mode=%v, want modeAddWizard", m.mode)
	}
}

// TestBareEscStillEscapesStatusTUI pins the intended behavior: with no chord
// armed and nothing open, a bare esc still pops back to the plan browser.
func TestBareEscStillEscapesStatusTUI(t *testing.T) {
	m := newStatusHostModel(t)
	if m.s.statusModel.IsChordPending() {
		t.Fatalf("precondition: no chord should be armed")
	}
	if m.s.statusModel.ActiveDetailPane != status.NoPane {
		t.Fatalf("precondition: no detail pane should be open")
	}

	mdl, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = mdl.(Model)

	if m.mode != modeBrowser {
		t.Errorf("bare esc with nothing open should return to the browser: mode=%v, want modeBrowser", m.mode)
	}
}
