package cmd

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestBuildPlanBundle verifies the bundle includes job .md files, .grove-plan.yml
// and rules/**, derives Workspace/PlanName from the notebook layout, and EXCLUDES
// .artifacts/, .grove-lease.yml, and nested non-rules files (M2 C11).
func TestBuildPlanBundle(t *testing.T) {
	root := t.TempDir()
	// <root>/workspaces/myws/plans/myplan
	planDir := filepath.Join(root, "workspaces", "myws", "plans", "myplan")
	mkdirs(t, planDir, filepath.Join(planDir, "rules"), filepath.Join(planDir, ".artifacts"))

	write(t, filepath.Join(planDir, "01-job.md"), "job one")
	write(t, filepath.Join(planDir, "02-job.md"), "job two")
	write(t, filepath.Join(planDir, ".grove-plan.yml"), "status: active")
	write(t, filepath.Join(planDir, "rules", "01-job.md.rules"), "include: **")
	write(t, filepath.Join(planDir, ".grove-lease.yml"), "holder_origin: sat")    // excluded
	write(t, filepath.Join(planDir, ".artifacts", "big-context.txt"), "huge")     // excluded
	write(t, filepath.Join(planDir, "notes", "scratch.md"), "nested, not a rule") // excluded

	bundle, err := buildPlanBundle(planDir)
	if err != nil {
		t.Fatalf("buildPlanBundle: %v", err)
	}
	if bundle.Workspace != "myws" {
		t.Errorf("Workspace = %q, want myws", bundle.Workspace)
	}
	if bundle.PlanName != "myplan" {
		t.Errorf("PlanName = %q, want myplan", bundle.PlanName)
	}

	got := make([]string, 0, len(bundle.Files))
	for k := range bundle.Files {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"01-job.md", "02-job.md", ".grove-plan.yml", "rules/01-job.md.rules"}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("bundle keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bundle keys = %v, want %v", got, want)
		}
	}

	for _, forbidden := range []string{".grove-lease.yml", ".artifacts/big-context.txt", "notes/scratch.md"} {
		if _, ok := bundle.Files[forbidden]; ok {
			t.Errorf("bundle must not include %q", forbidden)
		}
	}
}

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
