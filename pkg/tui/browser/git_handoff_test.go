package browser

import (
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/tui/embed"
)

func TestViewGitEmitsQualifiedReadOnlyTarget(t *testing.T) {
	root := t.TempDir()
	planDir := filepath.Join(root, "notebook", "plans", "same")
	container := filepath.Join(root, "containers", "workspace-a", "same")
	target := coreplan.PlanActionTarget{
		PlanDir: planDir, RegistryID: "workspace-a/same", ContainerPath: container,
		Repos: []coreplan.RepoTarget{{Name: "repo", Path: filepath.Join(container, "repo")}},
	}
	m := New(Config{})
	m.plans = []PlanListItem{{
		Name: "same", Key: coreplan.NewPlanKey(planDir),
		Binding:      coreplan.PlanBinding{Key: coreplan.NewPlanKey(planDir), Health: coreplan.BindingValid},
		ActionTarget: target,
	}}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'V'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("V did not emit a host request")
	}
	req, ok := cmd().(embed.OpenGitRequest)
	if !ok {
		t.Fatalf("V emitted %T", cmd())
	}
	if req.Operation != embed.GitOperationInspect || req.Target.PlanDir != planDir || req.Target.ContainerPath != container {
		t.Fatalf("wrong request: %+v", req)
	}
	if m.selectedPlanKey() != planDir {
		t.Fatalf("selection changed during handoff: %q", m.selectedPlanKey())
	}
}

func TestMutationBindingsRemainDisabled(t *testing.T) {
	m := New(Config{})
	if m.keys.FastForwardUpdate.Enabled() || m.keys.FastForwardMain.Enabled() {
		t.Fatal("U/M must remain disabled until the shared lifecycle service lands")
	}
}
