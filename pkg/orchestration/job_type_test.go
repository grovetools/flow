package orchestration

import "testing"

// TestJobTypeInheritsPlanModel locks the invariant that the plan-level default
// model (typically a gemini-* chat model from .grove-plan.yml) is inherited ONLY
// by oneshot/chat jobs — never by agent jobs, which select their own model via
// the CLI agent and have it backfilled at launch. Every job-creation site (CLI
// add, TUI add-job wizard, recipe expansion, plan extract) gates on this method;
// a regression here is what made headless agents advertise gemini-3.5-flash.
func TestJobTypeInheritsPlanModel(t *testing.T) {
	cases := map[JobType]bool{
		JobTypeOneshot:          true,
		JobTypeChat:             true,
		JobTypeHeadlessAgent:    false,
		JobTypeInteractiveAgent: false,
		JobTypeIsolatedAgent:    false,
		JobTypeAgent:            false,
		JobTypeShell:            false,
		JobTypeGenerateRecipe:   false,
		JobTypeFile:             false,
	}
	for jt, want := range cases {
		if got := jt.InheritsPlanModel(); got != want {
			t.Errorf("JobType(%q).InheritsPlanModel() = %v, want %v", jt, got, want)
		}
	}
}
