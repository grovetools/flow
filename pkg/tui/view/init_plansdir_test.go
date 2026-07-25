package view

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	groveplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/tui/embed"
	planinit "github.com/grovetools/flow/pkg/tui/wizards/init"
)

// ecosystemFixture reproduces the shape the pilot hit: a root ecosystem that
// owns a sub-workspace, with a centralized notebook whose plans directories are
// per-workspace. HOME/XDG_CONFIG_HOME are redirected into the fixture so
// core's config loader cannot reach the developer's real notebook.
type ecosystemFixture struct {
	eco         string
	sub         string
	ecoPlansDir string
	subPlansDir string
	notebook    string
}

func newEcosystemFixture(t *testing.T) ecosystemFixture {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdgconfig"))

	fx := ecosystemFixture{
		eco:      filepath.Join(root, "eco"),
		sub:      filepath.Join(root, "eco", "workspaces", "sub"),
		notebook: filepath.Join(root, "nb"),
	}
	fx.ecoPlansDir = filepath.Join(fx.notebook, "workspaces", "eco", "plans")
	fx.subPlansDir = filepath.Join(fx.notebook, "workspaces", "sub", "plans")
	for _, dir := range []string{fx.sub, fx.ecoPlansDir, fx.subPlansDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ecoToml := "workspaces = [\"workspaces/*\"]\n\n" +
		"[notebooks.definitions.test]\nroot_dir = \"" + fx.notebook + "\"\n\n" +
		"[notebooks.rules]\ndefault = \"test\"\n"
	if err := os.WriteFile(filepath.Join(fx.eco, "grove.toml"), []byte(ecoToml), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fx.sub, "grove.toml"), []byte("[project]\nname = \"sub\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Guard the fixture itself: if these two stop diverging the tests below
	// become vacuous.
	want := groveplan.ResolvePlansDir(planinit.ResolveTargetWorkspace(fx.sub))
	if want != fx.ecoPlansDir {
		t.Fatalf("fixture did not resolve the ecosystem plans dir: got %q, want %q", want, fx.ecoPlansDir)
	}
	if fx.ecoPlansDir == fx.subPlansDir {
		t.Fatal("fixture does not reproduce the two-anchor divergence")
	}
	return fx
}

// T1. The directory the review screen validates, the directory the live-output
// log is written to, and the directory the subprocess will actually create the
// plan in must all be one directory.
func TestInitValidatesAndWritesTheSamePlansDirectory(t *testing.T) {
	fx := newEcosystemFixture(t)
	want := groveplan.ResolvePlansDir(planinit.ResolveTargetWorkspace(fx.sub))

	m := New(Config{PlansDir: fx.subPlansDir, WorkspaceDir: fx.sub})
	m.mode = modeInitWizard
	updated, _ := m.Update(embed.DoneMsg{Result: &planinit.Request{Dir: "probe"}})
	m = updated.(Model)

	if got := filepath.Dir(m.s.initOutputPath); got != want {
		t.Errorf("live-output log anchor = %q, want %q (the directory the child writes into)", got, want)
	}
	if got := m.initPlansDir(); got != want {
		t.Errorf("validation/journal anchor = %q, want %q", got, want)
	}
}

// initPlansDir must never retarget a host that supplied a plans dir for a
// workspace it cannot resolve — including the bare unit-test shape used
// throughout this package.
func TestInitPlansDirFallsBackToTheHostConfiguration(t *testing.T) {
	plansDir := t.TempDir()
	if got := New(Config{PlansDir: plansDir}).initPlansDir(); got != plansDir {
		t.Fatalf("initPlansDir() with no WorkspaceDir = %q, want the host value %q", got, plansDir)
	}
	// A workspace that resolves to nothing (no grove.toml anywhere above it)
	// must not be re-anchored either: ResolveTargetWorkspace hands the
	// directory straight back.
	bare := t.TempDir()
	if got := New(Config{PlansDir: plansDir, WorkspaceDir: bare}).initPlansDir(); got != plansDir {
		t.Fatalf("initPlansDir() for an unresolvable workspace = %q, want the host value %q", got, plansDir)
	}
}

// T2. End-to-end: the child resolves its plans directory from its OWN cwd
// (cmd/plan_init.go:112 → resolvePlanPathInWorkspace(arg, ".")), never from an
// argument. The report the app records must name the directory that actually
// came into existence.
func TestExecuteInitSubprocessReportsTheDirectoryTheChildActuallyCreates(t *testing.T) {
	fx := newEcosystemFixture(t)

	m := New(Config{PlansDir: fx.subPlansDir, WorkspaceDir: fx.sub})
	m.mode = modeInitWizard
	updated, _ := m.Update(embed.DoneMsg{Result: &planinit.Request{Dir: "probe"}})
	m = updated.(Model)

	// Exactly the two values model.go hands to runInitSubprocess.
	plansDir := filepath.Dir(m.s.initOutputPath)
	workspaceDir := planinit.ResolveTargetWorkspace(m.s.cfg.WorkspaceDir)

	// Stand-in for `flow plan init`: derives its target from its own working
	// directory plus notebook configuration, and ignores the parent's idea of
	// where plans live.
	script := `set -e
target="$FLOW_TEST_NOTEBOOK/workspaces/$(basename "$(pwd)")/plans/$1"
mkdir -p "$target"
printf 'Initializing orchestration plan in:\n  %s\n' "$target"`
	deps := testInitDeps(func(_ string, _ ...string) *exec.Cmd {
		cmd := exec.Command("sh", "-c", script, "sh", "probe")
		cmd.Env = append(os.Environ(), "FLOW_TEST_NOTEBOOK="+fx.notebook)
		return cmd
	}, "attempt-anchor")

	report := executeInitSubprocess(&planinit.Request{Dir: "probe", RunInit: true}, plansDir, workspaceDir, deps)
	if report.Err != nil {
		t.Fatalf("stub subprocess failed: %v (stderr=%q)", report.Err, report.Stderr)
	}
	info, err := os.Stat(report.PlanDir)
	if err != nil || !info.IsDir() {
		t.Fatalf("recorded plan dir %q does not exist (err=%v); the child created something else:\n%s",
			report.PlanDir, err, report.Stdout)
	}
	if want := filepath.Join(report.PlanDir, ".init-journal.json"); report.JournalPath != want {
		t.Errorf("journal was not finalized into the plan directory: got %q, want %q", report.JournalPath, want)
	}
	if !strings.Contains(report.Stdout, report.PlanDir) {
		t.Errorf("the app recorded %q but the subprocess reported:\n%s", report.PlanDir, report.Stdout)
	}
}

// T3. The canary for this whole defect class: a plan that was created where the
// child put it must load, instead of falling through to the "created but could
// not be loaded" terminal frame.
func TestInitSuccessLoadsThePlanFromTheEcosystemPlansDir(t *testing.T) {
	fx := newEcosystemFixture(t)
	planDir := filepath.Join(fx.ecoPlansDir, "probe")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, ".grove-plan.yml"), []byte("notes: created by the child\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := New(Config{PlansDir: fx.subPlansDir, WorkspaceDir: fx.sub})
	m.mode = modeInitWizard
	m.pendingInitPlanName = "probe"
	m.lastInitRequest = &planinit.Request{Dir: "probe", Worktree: "probe"}

	updated, _ := m.Update(initCompletedMsg{report: InitExecutionReport{}})
	got := updated.(Model)
	if got.mode != modeStatus {
		t.Fatalf("successful creation left mode=%v, want status (the new plan was not found)", got.mode)
	}
	if strings.Contains(got.finishTransient, "plan created, but its workspace could not be loaded") {
		t.Fatalf("terminal frame still reports an unloadable plan: %q", got.finishTransient)
	}
}

// B3. A host workspace switch must re-anchor the plans directory, or every
// subsequent Add Plan validates one directory and writes to another again.
func TestSetWorkspaceMsgUpdatesViewPlansDir(t *testing.T) {
	fx := newEcosystemFixture(t)
	m := New(Config{PlansDir: t.TempDir(), WorkspaceDir: t.TempDir()})

	updated, _ := m.Update(embed.SetWorkspaceMsg{Node: &workspace.WorkspaceNode{Path: fx.sub}})
	m = updated.(Model)

	if m.s.cfg.WorkspaceDir != fx.sub {
		t.Fatalf("workspace was not switched: %q", m.s.cfg.WorkspaceDir)
	}
	if m.s.cfg.PlansDir != fx.subPlansDir {
		t.Fatalf("plans dir did not follow the workspace switch: got %q, want %q", m.s.cfg.PlansDir, fx.subPlansDir)
	}
}
