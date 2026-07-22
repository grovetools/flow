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
