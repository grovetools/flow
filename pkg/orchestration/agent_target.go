package orchestration

import (
	"os"

	"github.com/grovetools/core/pkg/mux"
)

// The concrete agent routing targets. "auto" is not one of them: by the time an
// agent job is launched it may be running inside groved, which inherited none of
// the submitting terminal's environment and therefore cannot answer "auto".
// Every executor refuses an empty target rather than guess (see
// interactive_agent_executor.go / isolated_agent_executor.go), so resolution has
// to happen at the submitting process's perimeter — that is what this file is.
const (
	AgentTargetTmux   = "tmux"
	AgentTargetNative = "native"
	AgentTargetTuimux = "tuimux"
)

// ResolveAgentTarget derives the routing target for jobs submitted from this
// process, from the caller's own environment. Every CLI submission path must go
// through it: `plan run`, `plan resume` and `plan retry --run` all launch the
// same agents into the same mux, and a path that skips the derivation submits a
// job the executor can only fail.
func ResolveAgentTarget() string {
	return agentTargetFor(mux.ActiveMux(), os.Getenv("GROVE_TERMINAL") != "")
}

// ResolveAgentTargetHosted is the TUI's front door to the same derivation.
// hosted is set by the terminal panel wrapper that constructed the TUI, so it is
// authoritative and deliberately consulted instead of GROVE_TERMINAL — that
// variable is exported to every process a grove terminal spawns, including ones
// that are not hosted panes, so sniffing it would claim native routing for TUIs
// that have no pane to launch into.
func ResolveAgentTargetHosted(hosted bool) string {
	if hosted {
		return AgentTargetNative
	}
	return agentTargetFor(mux.ActiveMux(), false)
}

// agentTargetFor is the precedence table itself, kept pure so it is testable
// without mutating process environment. tuimux outranks a grove terminal because
// a tuimux pane sets both markers (tuimux exports GROVE_TERMINAL for the editors
// it hosts) and the tuimux daemon is the one that actually owns the PTY. tmux is
// the fallback because it is the only target that works from a bare shell.
func agentTargetFor(active mux.MuxType, groveTerminal bool) string {
	switch {
	case active == mux.MuxTuimux:
		return AgentTargetTuimux
	case groveTerminal:
		return AgentTargetNative
	default:
		return AgentTargetTmux
	}
}

// AgentTargetForSubmission returns the target a daemon submission for this plan
// must carry. A target already injected into the plan wins — a caller that
// resolved routing more precisely than the environment can (the TUI's hosted
// flag, or the daemon replaying a submission's target into the plan it loads)
// has already recorded it there — otherwise it is derived from the environment.
func AgentTargetForSubmission(plan *Plan) string {
	if plan != nil && plan.Orchestration != nil && plan.Orchestration.AgentTarget != "" {
		return plan.Orchestration.AgentTarget
	}
	return ResolveAgentTarget()
}
