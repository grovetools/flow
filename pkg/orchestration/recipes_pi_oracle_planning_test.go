package orchestration

import (
	"strings"
	"testing"
)

// renderOraclePlanningJob renders the recipe's single job with the given vars,
// the same way cmd/plan_init.go does.
func renderOraclePlanningJob(t *testing.T, vars map[string]string) (map[string]interface{}, string) {
	t.Helper()
	recipe, err := GetBuiltinRecipe("grove/pi-oracle-planning")
	if err != nil {
		t.Fatalf("GetBuiltinRecipe: %v", err)
	}
	if recipe.DefaultNoteTarget != "01-plan.md" {
		t.Fatalf("DefaultNoteTarget = %q, want 01-plan.md", recipe.DefaultNoteTarget)
	}
	data := struct {
		PlanName string
		Vars     map[string]string
	}{PlanName: "oracle-planning-test", Vars: vars}
	rendered, err := recipe.RenderJob("01-plan.md", data)
	if err != nil {
		t.Fatalf("RenderJob: %v", err)
	}
	frontmatter, body, err := ParseFrontmatter(rendered)
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	return frontmatter, string(body)
}

func TestPiOraclePlanningRecipeBootstrapsPlanningCoordinator(t *testing.T) {
	frontmatter, body := renderOraclePlanningJob(t, map[string]string{})

	if got := frontmatter["type"]; got != "interactive_agent" {
		t.Fatalf("type = %#v, want interactive_agent", got)
	}
	// The planner drives a Pi session and calls flow_subjob/flow_handoff, both
	// of which are grove-pi extensions: a non-Pi provider has neither tool.
	if got := frontmatter["provider"]; got != "pi" {
		t.Fatalf("provider = %#v, want pi", got)
	}
	if got := frontmatter["skill"]; got != "grove-pi-oracle-planner" {
		t.Fatalf("skill = %#v, want grove-pi-oracle-planner", got)
	}
	// Step 5 of the SOP is a planned handoff, so the job must not need an
	// operator /handoff to reach its own last step.
	if got := frontmatter["coord_mode"]; got != "autonomous" {
		t.Fatalf("coord_mode = %#v, want autonomous", got)
	}
	// The coordinator is a Pi CLI session; a model: here would be read as an
	// API model for a job that never dispatches to one.
	if _, ok := frontmatter["model"]; ok {
		t.Fatal("planning coordinator recipe must not set a model")
	}
	// A parent_job_id on the coordinator itself would make it a subjob child,
	// and handoff.ts refuses a handoff from a child that owes a parent report.
	if _, ok := frontmatter["parent_job_id"]; ok {
		t.Fatal("planning coordinator must be a root job so it can hand off")
	}

	for _, want := range []string{
		"responder: pi-session",    // the oracle is a chat job, not a subjob child
		"--parent-job-id",          // …carrying the vertical lineage anyway
		"flow plan say",            // turn delivery through the sanctioned writer
		"flow_subjob",              // the verifier really is a subjob child
		"not gateable",             // the verification pass is mandatory
		"pending and unlaunched",   // decomposition is reviewed before it runs
		"grove-pi-oracle-executor", // the successor carries the executor skill
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered body is missing %q:\n%s", want, body)
		}
	}
}

func TestPiOraclePlanningRecipeVars(t *testing.T) {
	_, defaults := renderOraclePlanningJob(t, map[string]string{})
	if !strings.Contains(defaults, "the ticket or brief the operator gives you") {
		t.Errorf("no feature var should ask the operator for the brief:\n%s", defaults)
	}
	if !strings.Contains(defaults, "Pick a big-window model") {
		t.Errorf("no oracle_model var should leave the choice to the planner:\n%s", defaults)
	}

	_, chosen := renderOraclePlanningJob(t, map[string]string{
		"feature":      "add a --dry-run flag to flow plan add",
		"oracle_model": "gpt-5.6-sol",
	})
	if !strings.Contains(chosen, "Design target: add a --dry-run flag to flow plan add") {
		t.Errorf("feature var did not render:\n%s", chosen)
	}
	if !strings.Contains(chosen, "--model gpt-5.6-sol") {
		t.Errorf("oracle_model var did not render:\n%s", chosen)
	}
	if strings.Contains(chosen, "Pick a big-window model") {
		t.Errorf("oracle_model var should replace the choose-one-yourself branch:\n%s", chosen)
	}
}
