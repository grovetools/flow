package view

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestPlansViewportReservesPinnedHostFooter(t *testing.T) {
	plansDir := t.TempDir()
	for i := range 64 {
		dir := filepath.Join(plansDir, fmt.Sprintf("plan-%02d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".grove-plan.yml"), []byte("notes: viewport fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	m := New(Config{PlansDir: plansDir, WorkspaceDir: t.TempDir()})
	batch, ok := m.Init()().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatal("expected initial local plan load")
	}
	updated, _ := m.Update(batch[0]())
	m = updated.(Model)
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 180, Height: 46})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = updated.(Model)
	view := m.View()

	if !strings.Contains(view, "󰜴") || !strings.Contains(view, "of 64") {
		t.Fatalf("End row or range clipped by host chrome:\n%s", view)
	}
	if !strings.Contains(view, "Plan data:") || lipgloss.Height(view) != 46 {
		t.Fatalf("pinned footer/height mismatch: height=%d\n%s", lipgloss.Height(view), view)
	}
}
