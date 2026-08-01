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

// TestResolveAgentTargetExplicit: the override exists for submitters whose own
// environment is not the mux the agent will live in — the daemon-side assistant
// supervisor, which runs flow from inside groved. An empty value must still
// derive, or every existing invocation would change meaning.
func TestResolveAgentTargetExplicit(t *testing.T) {
	// A tuimux environment, so a bug that ignored the override would return
	// tuimux and be visibly wrong for every explicit case below.
	t.Setenv(mux.EnvTuimuxPTY, "1")

	for _, tt := range []struct {
		name     string
		explicit string
		want     string
	}{
		{name: "empty derives from the environment", explicit: "", want: AgentTargetTuimux},
		{name: "blank derives too", explicit: "   ", want: AgentTargetTuimux},
		{name: "explicit native wins", explicit: AgentTargetNative, want: AgentTargetNative},
		{name: "explicit tmux wins", explicit: AgentTargetTmux, want: AgentTargetTmux},
		{name: "case and padding are forgiven", explicit: "  Native ", want: AgentTargetNative},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveAgentTargetExplicit(tt.explicit)
			if err != nil {
				t.Fatalf("ResolveAgentTargetExplicit(%q): %v", tt.explicit, err)
			}
			if got != tt.want {
				t.Errorf("ResolveAgentTargetExplicit(%q) = %q, want %q", tt.explicit, got, tt.want)
			}
		})
	}

	// An unsupported value must be refused HERE, at the CLI perimeter. Deeper
	// down it only surfaces after the job has already moved to running.
	if _, err := ResolveAgentTargetExplicit("screen"); err == nil {
		t.Error("an unsupported target must be refused at the perimeter")
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
