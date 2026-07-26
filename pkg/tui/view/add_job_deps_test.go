package view

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/flow/pkg/orchestration"
)

// newMultiJobStatusHostModel boots the meta-panel into status mode over a
// three-job plan, sized so the pane Manager has real dimensions.
func newMultiJobStatusHostModel(t *testing.T) Model {
	t.Helper()
	jobs := []*orchestration.Job{
		{ID: "j1", Filename: "j1.md", Title: "job one"},
		{ID: "j2", Filename: "j2.md", Title: "job two"},
		{ID: "j3", Filename: "j3.md", Title: "job three"},
	}
	byID := make(map[string]*orchestration.Job, len(jobs))
	for _, j := range jobs {
		byID[j.ID] = j
	}
	plan := &orchestration.Plan{Name: "t", Jobs: jobs, JobsByID: byID}
	graph, err := orchestration.BuildDependencyGraph(plan)
	if err != nil {
		t.Fatalf("BuildDependencyGraph: %v", err)
	}
	m := New(Config{InitialPlan: plan, InitialGraph: graph})
	mdl, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return mdl.(Model)
}

// pressKey sends a key to the host and drains the resulting command tree back
// into it, which is what the bubbletea runtime does. The status view answers
// "A" with an embed.SwitchTabMsg command, so without draining the tab switch
// never happens.
func pressKey(t *testing.T, m Model, key tea.KeyMsg) Model {
	t.Helper()
	mdl, cmd := m.Update(key)
	m = mdl.(Model)
	return drain(t, m, cmd)
}

func drain(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	switch msg := cmd().(type) {
	case nil:
		return m
	case tea.BatchMsg:
		for _, c := range msg {
			m = drain(t, m, c)
		}
		return m
	default:
		mdl, next := m.Update(msg)
		m = mdl.(Model)
		return drain(t, m, next)
	}
}

func TestHelpSearchOwnsHostLetterShortcuts(t *testing.T) {
	m := newMultiJobStatusHostModel(t)

	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if !m.s.statusModel.IsTextEntryActive() {
		t.Fatal("help search should report active text entry to the pager host")
	}

	// Lowercase a is the host shortcut for Add Job. While help search is
	// focused it must reach the input instead of switching pages.
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.mode != modeStatus {
		t.Fatalf("typing 'a' in help search switched host mode to %v", m.mode)
	}
	if !m.s.statusModel.Help.IsTextEntryActive() {
		t.Fatal("help search lost focus after receiving a shortcut letter")
	}
}

// TestSelectedJobsSeedAddWizardDeps is the regression guard for "space a job,
// press A, the new job depends on it". A used to open an inline create-job
// form that read the multi-select; it now routes to the Add Job wizard tab, and
// the selection has to travel with it as the wizard's initial dependencies.
func TestSelectedJobsSeedAddWizardDeps(t *testing.T) {
	m := newMultiJobStatusHostModel(t)
	if m.mode != modeStatus {
		t.Fatalf("precondition: expected modeStatus, got %v", m.mode)
	}

	// Space-select the second job.
	m.s.statusModel.Cursor = 1
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeySpace})
	if got := m.s.statusModel.SelectedJobFilenames(); !reflect.DeepEqual(got, []string{"j2.md"}) {
		t.Fatalf("after space: selection = %v, want [j2.md]", got)
	}

	// "A" routes to the Add Job wizard tab.
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	if m.mode != modeAddWizard {
		t.Fatalf("after 'A': mode = %v, want modeAddWizard", m.mode)
	}

	cfg := m.addWizardConfig(m.s.statusModel.Plan)
	if !reflect.DeepEqual(cfg.InitialDeps, []string{"j2.md"}) {
		t.Errorf("add wizard InitialDeps = %v, want [j2.md] — the space-selection was dropped",
			cfg.InitialDeps)
	}
}

// TestUnselectedAddWizardHasNoDeps pins the other half: with nothing selected,
// A opens a wizard with an empty dependency picker.
func TestUnselectedAddWizardHasNoDeps(t *testing.T) {
	m := newMultiJobStatusHostModel(t)

	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	if m.mode != modeAddWizard {
		t.Fatalf("after 'A': mode = %v, want modeAddWizard", m.mode)
	}

	if cfg := m.addWizardConfig(m.s.statusModel.Plan); len(cfg.InitialDeps) != 0 {
		t.Errorf("add wizard InitialDeps = %v, want empty", cfg.InitialDeps)
	}
}
