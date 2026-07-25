package browser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/grovetools/core/pkg/models"
	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/tui/embed"
)

// newSwitchTargetWorkspace builds a workspace whose plans directory resolves
// through the same locator the CLI uses at launch, with HOME/XDG redirected so
// the developer's real notebook is never consulted.
func newSwitchTargetWorkspace(t *testing.T) (wsDir, plansDir string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdgconfig"))

	wsDir = filepath.Join(root, "eco")
	notebook := filepath.Join(root, "nb")
	plansDir = filepath.Join(notebook, "workspaces", "eco", "plans")
	for _, dir := range []string{wsDir, plansDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	toml := "workspaces = [\"workspaces/*\"]\n\n" +
		"[notebooks.definitions.test]\nroot_dir = \"" + notebook + "\"\n\n" +
		"[notebooks.rules]\ndefault = \"test\"\n"
	if err := os.WriteFile(filepath.Join(wsDir, "grove.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := coreplan.ResolvePlansDir(wsDir); got != plansDir {
		t.Fatalf("fixture plans dir = %q, want %q", got, plansDir)
	}
	return wsDir, plansDir
}

func switchWorkspace(t *testing.T, wsDir string) Model {
	t.Helper()
	m := New(Config{EmbedMode: true})
	updated, _ := m.Update(embed.SetWorkspaceMsg{Node: &workspace.WorkspaceNode{Path: wsDir}})
	got, ok := updated.(Model)
	if !ok {
		t.Fatalf("workspace switch returned %T, want browser.Model", updated)
	}
	return got
}

// A host workspace switch must scope the browser at the new workspace's plans
// directory, not at its parent. ResolvePlanDir(dir, "") returns the plans
// directory itself, so taking filepath.Dir of it lands one level too high.
func TestSetWorkspaceMsgResolvesPlansDirectory(t *testing.T) {
	wsDir, plansDir := newSwitchTargetWorkspace(t)
	m := switchWorkspace(t, wsDir)
	if m.plansDirectory != plansDir {
		t.Fatalf("browser scope after workspace switch = %q, want %q", m.plansDirectory, plansDir)
	}
}

// The consequence assertion: with the scope one directory too high, every
// daemon summary is filtered out and the Plans panel renders empty.
func TestSetWorkspaceMsgKeepsScopeMatchingSummaries(t *testing.T) {
	wsDir, plansDir := newSwitchTargetWorkspace(t)
	m := switchWorkspace(t, wsDir)

	summaries := map[string]models.PlanSummary{
		"probe": {PlanName: "probe", PlansDir: plansDir, PlanDir: filepath.Join(plansDir, "probe")},
	}
	scoped := scopedPlanSummaries(summaries, m.plansDirectory)
	if len(scoped) != 1 {
		t.Fatalf("workspace switch scoped away every plan: %d of %d summaries survived (scope=%q)",
			len(scoped), len(summaries), m.plansDirectory)
	}
}
