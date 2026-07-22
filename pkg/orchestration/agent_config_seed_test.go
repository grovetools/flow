package orchestration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grovetools/eval/pkg/record"
)

func TestWriteAgentConfigArtifactIsPerJobAndHashOnly(t *testing.T) {
	planDir := t.TempDir()
	workDir := t.TempDir()
	ctxFile := filepath.Join(workDir, "context.md")
	if err := os.WriteFile(ctxFile, []byte("context"), 0o600); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("a", 64)
	v := record.ConfigVector{
		Model: "provider/model", Provider: "pi", WorktreeCommit: strings.Repeat("b", 40),
		Components: map[string]string{"knowledge_tools": hash, "judge_prompt": strings.Repeat("c", 64)},
	}
	if err := WriteAgentConfigArtifact(planDir, "arm-1", v, workDir, []string{ctxFile}); err != nil {
		t.Fatal(err)
	}
	path := AgentConfigArtifactPath(planDir, "arm-1")
	if strings.HasPrefix(path, workDir+string(filepath.Separator)) {
		t.Fatalf("config leaked into shared worktree: %s", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	var got agentConfigArtifact
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || got.Config["judge_prompt"] != strings.Repeat("c", 64) || got.Model != v.Model {
		t.Fatalf("artifact = %#v", got)
	}
	if len(got.Config["knowledge_tools"]) != 64 || got.BundleFiles[0] != "context.md" {
		t.Fatalf("artifact did not preserve hashes/bundle: %#v", got)
	}
}

func TestBuildHeadlessEnvPointsAtPerJobAgentConfig(t *testing.T) {
	plan := &Plan{Name: "p", Directory: t.TempDir()}
	job := &Job{ID: "arm-2", FilePath: "arm-2.md", Title: "arm"}
	env := buildHeadlessEnv(job, plan, "pi", t.TempDir(), nil)
	want := "GROVE_CONFIG_FILE=" + AgentConfigArtifactPath(plan.Directory, job.ID)
	for _, item := range env {
		if item == want {
			return
		}
	}
	t.Fatalf("%q not found in launch env", want)
}
