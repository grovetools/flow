package status

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/flow/pkg/orchestration"
)

func TestCopyPathRemainsGlobalWithDetailFocus(t *testing.T) {
	job := &orchestration.Job{
		ID:       "j1",
		Filename: "j1.md",
		Title:    "job one",
		FilePath: "/tmp/plan/j1.md",
	}
	plan := &orchestration.Plan{
		Name:     "test",
		Jobs:     []*orchestration.Job{job},
		JobsByID: map[string]*orchestration.Job{job.ID: job},
	}
	graph, err := orchestration.BuildDependencyGraph(plan)
	if err != nil {
		t.Fatalf("BuildDependencyGraph: %v", err)
	}

	oldWriteClipboard := writeClipboard
	defer func() { writeClipboard = oldWriteClipboard }()

	for _, tc := range []struct {
		name  string
		focus ViewFocus
	}{
		{name: "primary", focus: FocusDetailPrimary},
		{name: "secondary", focus: FocusDetailSecondary},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var copied string
			writeClipboard = func(path string) error {
				copied = path
				return nil
			}

			m := New(Config{Plan: plan, Graph: graph})
			m.ShowLogs = true
			m.ActiveDetailPane = LogsPaneDetail
			m.Focus = tc.focus

			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
			m = updated.(Model)

			if copied != job.FilePath {
				t.Errorf("copied path = %q, want %q", copied, job.FilePath)
			}
			if want := "Copied: " + job.FilePath; m.StatusSummary != want {
				t.Errorf("status summary = %q, want %q", m.StatusSummary, want)
			}
		})
	}
}
