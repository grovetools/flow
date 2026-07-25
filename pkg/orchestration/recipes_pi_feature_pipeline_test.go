package orchestration

import (
	"strings"
	"testing"
)

func TestPiFeaturePipelineRecipeBootstrapsPiCoordinator(t *testing.T) {
	recipe, err := GetBuiltinRecipe("grove/pi-feature-pipeline")
	if err != nil {
		t.Fatalf("GetBuiltinRecipe: %v", err)
	}
	if recipe.DefaultNoteTarget != "01-coordinate.md" {
		t.Fatalf("DefaultNoteTarget = %q, want 01-coordinate.md", recipe.DefaultNoteTarget)
	}

	data := struct {
		PlanName string
		Vars     map[string]string
	}{PlanName: "pipeline-test", Vars: map[string]string{"flavor": "quick-fix"}}
	rendered, err := recipe.RenderJob("01-coordinate.md", data)
	if err != nil {
		t.Fatalf("RenderJob: %v", err)
	}
	frontmatter, body, err := ParseFrontmatter(rendered)
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	if got := frontmatter["type"]; got != "interactive_agent" {
		t.Fatalf("type = %#v, want interactive_agent", got)
	}
	if got := frontmatter["provider"]; got != "pi" {
		t.Fatalf("provider = %#v, want pi", got)
	}
	if got := frontmatter["skill"]; got != "grove-feature-pipeline" {
		t.Fatalf("skill = %#v, want grove-feature-pipeline", got)
	}
	if strings.Contains(string(rendered), "model:") {
		t.Fatal("Pi coordinator recipe must not set a model")
	}
	if !strings.Contains(string(body), "Use pipeline flavor `quick-fix`") {
		t.Fatalf("rendered body did not select quick-fix:\n%s", body)
	}
	if !strings.Contains(string(body), "executable input to `flow_pipeline`") {
		t.Fatalf("rendered body does not state machine-consumption contract:\n%s", body)
	}
}
