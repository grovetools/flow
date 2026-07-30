package orchestration

import (
	"testing"

	"github.com/grovetools/core/pkg/mux"
)

// The precedence table and its cases live in core/pkg/mux's tests now. What is
// still worth asserting here is that flow's spelling of each entry point reaches
// core's — a wrapper pointed at the wrong function, or a constant re-exported
// with the wrong value, would otherwise only surface as misrouted agent jobs.
func TestAgentTargetReExportsDelegate(t *testing.T) {
	t.Setenv(mux.EnvTuimuxPTY, "1")
	if got := ResolveAgentTarget(); got != mux.AgentTargetTuimux {
		t.Errorf("ResolveAgentTarget() under tuimux = %q, want %q", got, mux.AgentTargetTuimux)
	}
	if got := ResolveAgentTargetHosted(true); got != mux.AgentTargetNative {
		t.Errorf("ResolveAgentTargetHosted(true) = %q, want %q", got, mux.AgentTargetNative)
	}

	for _, tt := range []struct{ flow, core string }{
		{AgentTargetTmux, mux.AgentTargetTmux},
		{AgentTargetNative, mux.AgentTargetNative},
		{AgentTargetTuimux, mux.AgentTargetTuimux},
	} {
		if tt.flow != tt.core {
			t.Errorf("re-exported target %q != core's %q", tt.flow, tt.core)
		}
	}
}

func TestAgentTargetForSubmission_PlanTargetWins(t *testing.T) {
	t.Setenv(mux.EnvTuimuxPTY, "1")

	plan := &Plan{Orchestration: &Config{AgentTarget: AgentTargetNative}}
	if got := AgentTargetForSubmission(plan); got != AgentTargetNative {
		t.Errorf("AgentTargetForSubmission() = %q, want the plan's %q", got, AgentTargetNative)
	}

	// LoadPlan leaves Orchestration nil, so submitters that only load a plan
	// (retry, resume) must still get the environment's answer.
	if got := AgentTargetForSubmission(&Plan{}); got != AgentTargetTuimux {
		t.Errorf("AgentTargetForSubmission() with no plan orchestration = %q, want %q", got, AgentTargetTuimux)
	}
}
