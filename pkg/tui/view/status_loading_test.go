package view

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grovetools/flow/pkg/tui/browser"
)

func TestPlanSelectionShowsLoadingUntilJobsAreHydrated(t *testing.T) {
	planDir := filepath.Join(t.TempDir(), "selected-plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, ".grove-plan.yml"), []byte("notes: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	job := "---\nid: job-1\ntitle: Loaded Job\ntype: oneshot\nstatus: pending\n---\n\nDo it.\n"
	if err := os.WriteFile(filepath.Join(planDir, "01-loaded.md"), []byte(job), 0o600); err != nil {
		t.Fatal(err)
	}

	m := New(Config{PlansDir: filepath.Dir(planDir)})
	updated, cmd := m.Update(browser.BrowserPlanSelectedMsg{
		PlanName: "selected-plan", PlanPath: planDir,
	})
	m = updated.(Model)
	if !m.s.statusLoading || cmd == nil {
		t.Fatal("selection did not enter asynchronous status loading")
	}
	if got := m.View(); !strings.Contains(got, "Loading jobs") {
		t.Fatalf("loading placeholder missing:\n%s", got)
	}

	updated, _ = m.Update(cmd())
	m = updated.(Model)
	if m.s.statusLoading || m.s.statusModel == nil {
		t.Fatal("hydrated status model was not installed")
	}
	if got := m.View(); !strings.Contains(got, "01-loaded.md") {
		t.Fatalf("hydrated job missing:\n%s", got)
	}
}
