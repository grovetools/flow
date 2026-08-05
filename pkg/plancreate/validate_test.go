package plancreate

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "commit", "-m", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v: %s", err, out)
	}
	return dir
}

func TestValidateReportsCollisionAndManifest(t *testing.T) {
	workspace := initRepo(t)
	plans := t.TempDir()
	if err := os.Mkdir(filepath.Join(plans, "existing"), 0o755); err != nil {
		t.Fatal(err)
	}
	report, manifest := Validate(Request{TargetWorkspace: workspace, PlansDir: plans, PlanName: "existing", WorktreeName: "feature", RunInitHooks: true})
	if report.Valid() {
		t.Fatal("plan-directory collision should block creation")
	}
	if len(manifest.Steps) < 5 {
		t.Fatalf("manifest missing mutations: %+v", manifest.Steps)
	}
	if manifest.Steps[len(manifest.Steps)-1].Reversible {
		t.Fatal("init hooks must be identified as irreversible")
	}
}

func TestValidateAcceptsCleanTarget(t *testing.T) {
	workspace := initRepo(t)
	report, _ := Validate(Request{TargetWorkspace: workspace, PlansDir: t.TempDir(), PlanName: "new-plan"})
	if !report.Valid() {
		t.Fatalf("valid target rejected: %+v", report.Checks)
	}
}

// TestValidateAcceptsMissingPlansDir pins the first-plan-in-a-new-repo case: a
// workspace that has never held a plan has no plans directory, and every writer
// downstream creates it. Blocking on "missing" made the rolling plan of a fresh
// repo impossible to create from the wizard.
func TestValidateAcceptsMissingPlansDir(t *testing.T) {
	ws := initRepo(t)
	plans := filepath.Join(t.TempDir(), "workspaces", "fresh-repo", "plans")

	report, manifest := Validate(Request{TargetWorkspace: ws, PlansDir: plans, PlanName: "rolling"})
	if !report.Valid() {
		t.Fatalf("missing plans directory blocked creation: %+v", report.Checks)
	}
	if !hasStepKind(manifest, "create_plans_dir") {
		t.Errorf("manifest does not disclose that the plans directory gets created: %+v", manifest.Steps)
	}

	// An existing plans directory needs no such step.
	_, manifest = Validate(Request{TargetWorkspace: ws, PlansDir: t.TempDir(), PlanName: "rolling"})
	if hasStepKind(manifest, "create_plans_dir") {
		t.Errorf("existing plans directory listed as a mutation: %+v", manifest.Steps)
	}
}

// TestValidateRejectsUnwritablePlansDirAncestor keeps the relaxation honest: a
// plans directory that genuinely cannot be created still blocks.
func TestValidateRejectsUnwritablePlansDirAncestor(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	ws := initRepo(t)
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	report, _ := Validate(Request{TargetWorkspace: ws, PlansDir: filepath.Join(locked, "plans"), PlanName: "rolling"})
	if report.Valid() {
		t.Fatal("plans directory under a read-only parent accepted")
	}
}

func hasStepKind(manifest MutationManifest, kind string) bool {
	for _, step := range manifest.Steps {
		if step.Kind == kind {
			return true
		}
	}
	return false
}

// TestValidateRejectsPathAsWorktreeName pins the pre-flight guard for the
// phantom-worktree bug: an absolute path in the worktree field is Join'd onto
// the container base (Join concatenates rather than replacing), synthesizing a
// deep tree instead of failing. Catch it on the review screen.
func TestValidateRejectsPathAsWorktreeName(t *testing.T) {
	ws := initRepo(t)

	for _, name := range []string{
		"/Users/solair/.local/share/grove/worktrees/grove-cd22fef3/agent-testing-environments",
		"../escape",
	} {
		report, _ := Validate(Request{
			TargetWorkspace: ws, PlansDir: t.TempDir(),
			PlanName: "new-plan", WorktreeName: name,
		})
		if report.Valid() {
			t.Errorf("worktree name %q accepted, want rejected", name)
		}
	}

	// A branch-style name still passes — nesting is legitimate.
	report, _ := Validate(Request{
		TargetWorkspace: ws, PlansDir: t.TempDir(),
		PlanName: "new-plan", WorktreeName: "feature/foo",
	})
	if !report.Valid() {
		t.Fatalf("branch-style worktree name rejected: %+v", report.Checks)
	}
}
