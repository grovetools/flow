package browser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/pkg/models"
	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/pkg/worktreeregistry"
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

	m, cmd := pressChord(t, m, "vg")
	if cmd == nil {
		t.Fatal("vg did not emit a host request")
	}
	req, ok := cmd().(embed.OpenGitRequest)
	if !ok {
		t.Fatalf("vg emitted %T", cmd())
	}
	if req.Operation != embed.GitOperationInspect || req.Target.PlanDir != planDir || req.Target.ContainerPath != container {
		t.Fatalf("wrong request: %+v", req)
	}
	if m.selectedPlanKey() != planDir {
		t.Fatalf("selection changed during handoff: %q", m.selectedPlanKey())
	}
}

// TestDaemonLiveRowAssemblyResolvesQualifiedActionTarget reproduces the pilot
// F1 shape end-to-end: a plan generated for a STANDALONE repo (legacy layout)
// arrives as a daemon plan-index summary, and daemon-live row assembly must
// yield a bound row with a COMPLETE qualified action target so vg hands off to
// Git Viewer at that row's exact container — with canonical workspace
// discovery blind to the sandbox repos and without consulting process CWD.
func TestDaemonLiveRowAssemblyResolvesQualifiedActionTarget(t *testing.T) {
	t.Setenv("GROVE_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// The on-disk shape `flow plan init alpha-view --worktree --layout legacy`
	// produces for a standalone repo: owner repo, unified container holding the
	// repo checkout as a direct child, marker + synthetic grove.toml, and the
	// registry entry recording membership.
	owner := filepath.Join(root, "alpha-repo")
	container := filepath.Join(owner, ".grove-worktrees", "alpha-view")
	checkout := filepath.Join(container, "alpha-repo")
	for _, dir := range []string{filepath.Join(owner, ".git"), filepath.Join(container, ".grove"), filepath.Join(checkout, ".git")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(container, "grove.toml"), []byte("workspaces = [\"*\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := "branch: alpha-view\nplan: alpha-view\nowner: " + owner + "\necosystem: true\nrepos:\n  - alpha-repo\n"
	if err := os.WriteFile(filepath.Join(container, ".grove", "workspace"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := worktreeregistry.Save(&worktreeregistry.Entry{
		AbsPath: container, Owner: owner, Repos: []string{"alpha-repo"}, Plan: "alpha-view",
	}); err != nil {
		t.Fatal(err)
	}

	// The owner-qualified notebook plan dir — where flow plan init created the
	// plan and what the daemon plan index reports as the row's identity.
	planDir := filepath.Join(home, ".grove", "notebooks", "nb", "workspaces", "alpha-repo", "plans", "alpha-view")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, ".grove-plan.yml"), []byte("worktree: alpha-view\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	summaries := map[string]models.PlanSummary{
		planDir: {
			PlanDir: planDir, PlanName: "alpha-view", WorkspaceRoot: owner,
			PlansDir: filepath.Dir(planDir), Lifecycle: "live", Worktree: "alpha-view",
			WorktreePath: container, BindingHealth: string(coreplan.BindingValid), RegistryID: "alpha-view-fixture",
			Repositories: []string{"alpha-repo"},
		},
	}
	items, err := loadPortfolio(summaries, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one daemon-live row, got %d", len(items))
	}
	row := items[0]
	if row.Binding.Health != coreplan.BindingValid {
		t.Fatalf("generated plan is not bound: %+v", row.Binding)
	}
	if row.ActionTarget.PlanDir == "" || row.ActionTarget.ContainerPath == "" {
		t.Fatalf("daemon-live row assembled an incomplete action target: %+v", row.ActionTarget)
	}
	// The pilot's exact-container criterion: the target must carry the
	// registry binding's abs_path verbatim — not the owner repo root above it,
	// whose working tree can never show the member checkouts' changes — and
	// the member checkouts must resolve WITHIN that container.
	entry, err := worktreeregistry.Resolve(container, nil)
	if err != nil || entry == nil {
		t.Fatalf("registry entry unavailable for %q: %v", container, err)
	}
	if row.ActionTarget.ContainerPath != entry.AbsPath {
		t.Fatalf("action target names %q, want the registry entry's exact abs_path %q", row.ActionTarget.ContainerPath, entry.AbsPath)
	}
	if row.ActionTarget.ContainerPath == owner {
		t.Fatalf("action target collapsed to the owner repo root %q", owner)
	}
	if len(row.ActionTarget.Repos) != 1 || row.ActionTarget.Repos[0].Path != checkout {
		t.Fatalf("member checkouts not resolved within the container: %+v", row.ActionTarget.Repos)
	}

	m := New(Config{})
	m.plans = items
	m, cmd := pressChord(t, m, "vg")
	if cmd == nil {
		t.Fatalf("vg refused a bound daemon-live row: %s", m.statusMessage)
	}
	req, ok := cmd().(embed.OpenGitRequest)
	if !ok {
		t.Fatalf("vg emitted %T", cmd())
	}
	if req.Operation != embed.GitOperationInspect || req.Target.ContainerPath != container {
		t.Fatalf("wrong handoff request: %+v", req)
	}
}

// TestViewGitRefusesMissingContainerDistinctly pins the F2 refusal surface: a
// deleted container with retained registry metadata reaches the row as the
// distinct "missing container" health, and vg's refusal message says so instead
// of collapsing into unbound/mismatch.
func TestViewGitRefusesMissingContainerDistinctly(t *testing.T) {
	planDir := filepath.Join(t.TempDir(), "plans", "missing-view")
	m := New(Config{})
	m.plans = []PlanListItem{{
		Name: "missing-view", Key: coreplan.NewPlanKey(planDir),
		Binding: coreplan.PlanBinding{Key: coreplan.NewPlanKey(planDir), Health: coreplan.BindingMissing},
	}}

	m, cmd := pressChord(t, m, "vg")
	if cmd != nil {
		t.Fatalf("vg must refuse a missing container, emitted %T", cmd())
	}
	if !strings.Contains(m.statusMessage, "Cannot inspect git: missing container") {
		t.Fatalf("refusal not distinct: %q", m.statusMessage)
	}
}

func TestPlanMutationKeysEmitExactQualifiedPreviewRequestsAndDeduplicate(t *testing.T) {
	planDir := filepath.Join(t.TempDir(), "plans", "same")
	target := coreplan.PlanActionTarget{
		PlanDir: planDir, RegistryID: "registry/same", ContainerPath: "/containers/same",
		Repos: []coreplan.RepoTarget{{Name: "beta", Path: "/containers/same/beta"}, {Name: "alpha", Path: "/containers/same/alpha"}},
	}
	for _, tc := range []struct {
		key rune
		op  embed.GitOperation
	}{{'U', embed.GitOperationUpdateOnly}, {'M', embed.GitOperationLand}} {
		m := New(Config{})
		m.plans = []PlanListItem{{
			Name: "same", Key: coreplan.NewPlanKey(planDir),
			Binding:      coreplan.PlanBinding{Key: coreplan.NewPlanKey(planDir), Health: coreplan.BindingValid},
			ActionTarget: target,
		}}
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{tc.key}})
		m = updated.(Model)
		if cmd == nil {
			t.Fatalf("%c did not open a preview: %s", tc.key, m.statusMessage)
		}
		req, ok := cmd().(embed.OpenGitRequest)
		if !ok || req.Operation != tc.op || req.Target.PlanDir != planDir || len(req.Target.Repos) != 2 {
			t.Fatalf("%c request lost exact target: %#v", tc.key, req)
		}

		updated, duplicate := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{tc.key}})
		m = updated.(Model)
		if duplicate != nil || !strings.Contains(m.statusMessage, "already in flight") {
			t.Fatalf("%c duplicate was not refused: cmd=%v status=%q", tc.key, duplicate, m.statusMessage)
		}
		if m.refreshPlanKey != planDir {
			t.Fatalf("refresh key = %q, want exact PlanKey %q", m.refreshPlanKey, planDir)
		}
	}
}

func TestPlanMutationFocusReturnClearsPendingAndRefreshesExactPlanKey(t *testing.T) {
	firstDir := filepath.Join(t.TempDir(), "workspace-a", "plans", "same")
	secondDir := filepath.Join(t.TempDir(), "workspace-b", "plans", "same")
	m := New(Config{})
	m.plans = []PlanListItem{
		{Name: "same", Key: coreplan.NewPlanKey(firstDir), Binding: coreplan.PlanBinding{Health: coreplan.BindingValid}},
		{Name: "same", Key: coreplan.NewPlanKey(secondDir), Binding: coreplan.PlanBinding{Health: coreplan.BindingValid}},
	}
	m.cursor = 1
	m.refreshPlanKey = secondDir
	m.actionPending[secondDir] = embed.GitOperationLand
	updated, _ := m.Update(embed.FocusMsg{})
	m = updated.(Model)
	if m.selectedPlanKey() != secondDir || len(m.actionPending) != 0 || m.refreshPlanKey != "" {
		t.Fatalf("focus return did not restore/clear exact lifecycle state: key=%q pending=%v refresh=%q", m.selectedPlanKey(), m.actionPending, m.refreshPlanKey)
	}
}
