package status

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/flow/pkg/orchestration"
)

// TestFullscreenOpensLogs: pressing "f" (ToggleFullscreen) with the logs pane
// closed opens the logs pane AND fullscreens it in one press — the fix for the
// bare-"f"-no-ops-when-closed bug. It remains a toggle.
func TestFullscreenOpensLogs(t *testing.T) {
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

	m := New(Config{Plan: plan, Graph: graph})
	// Give the TUI a size so the pane Manager has real dimensions.
	mdl, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = mdl.(Model)

	if m.ShowLogs {
		t.Fatalf("precondition: logs pane should start closed")
	}

	// Press "f".
	mdl, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	m = mdl.(Model)

	if !m.ShowLogs {
		t.Errorf("'f' with logs closed should open the logs pane (ShowLogs=false)")
	}
	if m.ActiveDetailPane != LogsPaneDetail {
		t.Errorf("'f' should open the LOGS pane, got ActiveDetailPane=%d", m.ActiveDetailPane)
	}
	if !m.LogPaneFullscreen {
		t.Errorf("'f' should fullscreen the freshly-opened logs pane")
	}
}
