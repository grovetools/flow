package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/grovetools/core/config"
	"github.com/grovetools/core/pkg/notespace"
)

// TestBuildPlanBundle verifies the bundle includes job .md files, .grove-plan.yml
// and rules/**, derives immutable notespace identity and PlanName, and EXCLUDES
// .artifacts/, .grove-lease.yml, and nested non-rules files (M2 C11).
func TestBuildPlanBundle(t *testing.T) {
	root := t.TempDir()
	// <root>/notespaces/renamable/plans/myplan
	notespaceRoot := filepath.Join(root, "notespaces", "renamable")
	planDir := filepath.Join(notespaceRoot, "plans", "myplan")
	mkdirs(t, planDir, filepath.Join(planDir, "rules"), filepath.Join(planDir, ".artifacts"))
	const notespaceID = "01J00000000000000000000001"
	if _, err := notespace.InstallNotespace(notespaceRoot, notespace.NotespaceStamp{
		ID: notespaceID, Name: "display-name", Subject: "example.com/org/repo", Kind: "repo",
	}); err != nil {
		t.Fatal(err)
	}

	write(t, filepath.Join(planDir, "01-job.md"), "job one")
	write(t, filepath.Join(planDir, "02-job.md"), "job two")
	write(t, filepath.Join(planDir, ".grove-plan.yml"), "status: active")
	write(t, filepath.Join(planDir, "rules", "01-job.md.rules"), "include: **")
	write(t, filepath.Join(planDir, ".grove-lease.yml"), "holder_origin: sat")    // excluded
	write(t, filepath.Join(planDir, ".artifacts", "big-context.txt"), "huge")     // excluded
	write(t, filepath.Join(planDir, "notes", "scratch.md"), "nested, not a rule") // excluded

	cfg := &config.Config{Notebooks: &config.NotebooksConfig{Definitions: map[string]*config.Notebook{
		"book": {RootDir: root},
	}}}
	bundle, err := buildPlanBundleWithConfig(planDir, cfg)
	if err != nil {
		t.Fatalf("buildPlanBundle: %v", err)
	}
	if bundle.NotespaceID != notespaceID {
		t.Errorf("NotespaceID = %q, want immutable id %q", bundle.NotespaceID, notespaceID)
	}
	if bundle.NotespaceName != "display-name" {
		t.Errorf("NotespaceName = %q, want stamped display-name", bundle.NotespaceName)
	}
	if bundle.PlanName != "myplan" {
		t.Errorf("PlanName = %q, want myplan", bundle.PlanName)
	}
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["notespace_id"] != notespaceID || wire["notespace_name"] != "display-name" {
		t.Fatalf("bundle JSON identity = %s", encoded)
	}
	if _, legacy := wire["workspace"]; legacy {
		t.Fatalf("bundle JSON retained mutable workspace route: %s", encoded)
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
