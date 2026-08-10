package browser

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/core/pkg/models"
)

// writePlanDir materializes the minimum a plans directory needs for a plan to
// be recognized: the marker file and one job.
func writePlanDir(t *testing.T, plansDir, name string) {
	t.Helper()
	dir := filepath.Join(plansDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".grove-plan.yml"), []byte("name: "+name+"\n"), 0o600); err != nil {
		t.Fatalf("write plan marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "01-job.md"), []byte("---\ntitle: job\n---\n"), 0o600); err != nil {
		t.Fatalf("write job: %v", err)
	}
}

// TestIndexMissesDiskDiagnosesOnlyABlindDaemon pins both directions of the
// probe that reports the observed regression: a daemon whose flow watcher had
// lost its watch set kept publishing real, non-zero revisions carrying zero
// plans, so the browser rendered "No plans found in directory." over a
// workspace holding two dozen of them.
//
// The negatives are the load-bearing half. The daemon stays the source of plan
// rows, so this probe exists only to diagnose — and it must neither cost a
// syscall on the healthy path nor cry wolf over the two empty lists that are
// not faults: a genuinely empty workspace, and the pre-discovery revision 0
// that means "not scanned yet" rather than "scanned, nothing here".
func TestIndexMissesDiskDiagnosesOnlyABlindDaemon(t *testing.T) {
	plansDir := t.TempDir()
	writePlanDir(t, plansDir, "misc-fixes")

	if !indexMissesDisk(nil, plansDir, 9) {
		t.Fatal("an empty index over a populated directory was not diagnosed")
	}

	summaries := map[string]models.PlanSummary{
		"/plans/alpha": {PlanDir: "/plans/alpha", PlanName: "alpha", Lifecycle: "live"},
	}
	if indexMissesDisk(summaries, plansDir, 9) {
		t.Error("a populated index was diagnosed as blind")
	}
	if indexMissesDisk(nil, t.TempDir(), 9) {
		t.Error("a genuinely empty workspace was diagnosed as blind")
	}
	if indexMissesDisk(nil, "", 9) {
		t.Error("an unscoped browser was diagnosed as blind")
	}
	if indexMissesDisk(nil, plansDir, 0) {
		t.Error("a pre-discovery revision-0 index was diagnosed as blind")
	}
}

// TestBlindIndexRefusesTheRollingPlanShortcut: enter on an empty list normally
// creates the rolling plan. Over a directory that already holds plans that is a
// write taken on the strength of a list the daemon got wrong.
func TestBlindIndexRefusesTheRollingPlanShortcut(t *testing.T) {
	plansDir := t.TempDir()
	writePlanDir(t, plansDir, "misc-fixes")

	m := New(Config{PlansDir: plansDir})
	m.initialLoaded = true
	m.loading = false
	m.indexMissesDisk = true

	updated, cmd := m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("enter created a plan while the daemon index was known to be wrong")
	}
	if got := updated.(Model); got.rollingPending {
		t.Error("creation was marked pending anyway")
	}
	if _, err := os.Stat(filepath.Join(plansDir, RollingPlanName)); err == nil {
		t.Error("a rolling plan was written into a directory that already had plans")
	}
}
