package orchestration

import (
	"testing"

	"github.com/grovetools/core/pkg/mux"
)

func TestAgentTargetFor(t *testing.T) {
	tests := []struct {
		name          string
		active        mux.MuxType
		groveTerminal bool
		want          string
	}{
		{"tuimux pane", mux.MuxTuimux, false, AgentTargetTuimux},
		{"tuimux pane also exporting GROVE_TERMINAL", mux.MuxTuimux, true, AgentTargetTuimux},
		{"grove terminal pane", mux.MuxNone, true, AgentTargetNative},
		{"tmux session", mux.MuxTmux, false, AgentTargetTmux},
		{"bare shell", mux.MuxNone, false, AgentTargetTmux},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := agentTargetFor(tt.active, tt.groveTerminal); got != tt.want {
				t.Errorf("agentTargetFor(%q, %v) = %q, want %q", tt.active, tt.groveTerminal, got, tt.want)
			}
		})
	}
}

func TestResolveAgentTarget_FromEnvironment(t *testing.T) {
	t.Setenv(mux.EnvTuimuxPTY, "1")
	if got := ResolveAgentTarget(); got != AgentTargetTuimux {
		t.Errorf("ResolveAgentTarget() under tuimux = %q, want %q", got, AgentTargetTuimux)
	}

	t.Setenv(mux.EnvTuimuxPTY, "")
	t.Setenv("GROVE_TERMINAL", "1")
	if got := ResolveAgentTarget(); got != AgentTargetNative {
		t.Errorf("ResolveAgentTarget() under a grove terminal = %q, want %q", got, AgentTargetNative)
	}
}

// The TUI's hosted flag comes from the panel wrapper that constructed it and is
// trusted over the environment, which cannot distinguish a hosted pane from any
// other process a grove terminal spawned.
func TestResolveAgentTargetHosted(t *testing.T) {
	t.Setenv(mux.EnvTuimuxPTY, "1")
	if got := ResolveAgentTargetHosted(true); got != AgentTargetNative {
		t.Errorf("ResolveAgentTargetHosted(true) = %q, want %q", got, AgentTargetNative)
	}
	if got := ResolveAgentTargetHosted(false); got != AgentTargetTuimux {
		t.Errorf("ResolveAgentTargetHosted(false) under tuimux = %q, want %q", got, AgentTargetTuimux)
	}

	t.Setenv(mux.EnvTuimuxPTY, "")
	t.Setenv("GROVE_TERMINAL", "1")
	if got := ResolveAgentTargetHosted(false); got != AgentTargetTmux {
		t.Errorf("ResolveAgentTargetHosted(false) = %q, want %q: an unhosted TUI must not claim a native pane", got, AgentTargetTmux)
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
