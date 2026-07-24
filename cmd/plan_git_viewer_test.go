package cmd

import (
	"path/filepath"
	"testing"

	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/tui/embed"
	"github.com/grovetools/flow/pkg/tui/view"
)

func TestGitViewerExecCommandCarriesExplicitQualifiedTarget(t *testing.T) {
	container := filepath.Join(t.TempDir(), "qualified", "container")
	cmd := gitViewerExecCommand(embed.OpenGitRequest{
		Target: coreplan.PlanActionTarget{
			PlanDir: "/plans/same", ContainerPath: container,
			Repos: []coreplan.RepoTarget{{Name: "repo", Path: filepath.Join(container, "repo")}},
		},
		Operation: embed.GitOperationInspect,
	})
	args := cmd.Args
	if len(args) < 6 {
		t.Fatalf("unexpected argv: %v", args)
	}
	joined := ""
	for _, arg := range args {
		joined += "\x00" + arg
	}
	for _, want := range []string{"\x00view", "\x00--dir\x00" + container, "\x00--initial-operation\x00inspect", "\x00--plan-action-target-json\x00", `"planDir":"/plans/same"`, `"repos":[`} {
		if !contains(joined, want) {
			t.Fatalf("argv %v does not contain %q", args, want)
		}
	}
}

func TestStandaloneGitViewerCloseReturnsToPreservedFlowModel(t *testing.T) {
	inner := view.New(view.Config{PlansDir: t.TempDir()})
	host := newStatusTUIHost(inner)
	before := host.model.TestState()
	updated, _ := host.Update(gitViewerClosedMsg{})
	after := updated.(statusTUIHost).model.TestState()
	if before["mode"] != after["mode"] || before["plan_count"] != after["plan_count"] {
		t.Fatalf("standalone return replaced Flow state: before=%v after=%v", before, after)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
