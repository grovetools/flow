package orchestration

import (
	"fmt"
	"strings"

	"github.com/grovetools/core/pkg/mux"
)

// The rule itself now lives in core/pkg/mux, next to ActiveMux and to the
// models.JobSubmitRequest field it fills, because flow is not the only submitter
// — grove.nvim's `chat` builds the same request and must reach the same answer,
// and it cannot import flow. What remains here is the flow-facing spelling, kept
// so that no caller in this repo has to know where the rule moved.

// These stay constants rather than vars: they are compared and assigned as
// untyped string constants throughout flow, and a var would both lose that and
// make the ecosystem's routing vocabulary writable at runtime.
const (
	AgentTargetTmux   = mux.AgentTargetTmux
	AgentTargetNative = mux.AgentTargetNative
	AgentTargetTuimux = mux.AgentTargetTuimux
)

// ResolveAgentTarget derives the routing target for jobs submitted from this
// process, from the caller's own environment. Every CLI submission path must go
// through it: `plan run`, `plan resume` and `plan retry --run` all launch the
// same agents into the same mux, and a path that skips the derivation submits a
// job the executor can only fail.
//
// See mux.ResolveAgentTarget for the precedence and its reasoning.
func ResolveAgentTarget() string {
	return mux.ResolveAgentTarget()
}

// ResolveAgentTargetHosted is the TUI's front door to the same derivation.
// hosted is set by the terminal panel wrapper that constructed the TUI, so it is
// authoritative and deliberately consulted instead of GROVE_TERMINAL — that
// variable is exported to every process a grove terminal spawns, including ones
// that are not hosted panes, so sniffing it would claim native routing for TUIs
// that have no pane to launch into.
func ResolveAgentTargetHosted(hosted bool) string {
	return mux.ResolveAgentTargetHosted(hosted)
}

// ResolveAgentTargetExplicit is the CLI perimeter's derivation for callers that
// may name the target outright. An empty explicit falls back to
// ResolveAgentTarget, so every existing invocation keeps deriving from its own
// environment; a named one wins and is validated here rather than deep in a
// provider, where an unsupported value surfaces as a launch failure after the
// job has already moved to running.
//
// The override exists because the environment derivation assumes the submitting
// process lives in the mux the agent will live in, and one caller structurally
// cannot: the daemon-side assistant supervisor runs `flow` from inside groved,
// which has no TMUX, no TUIMUX_PTY and no GROVE_TERMINAL, so the derivation can
// only ever answer "tmux". That answer is wrong twice over — the pi family has
// no prepared tmux resume, and a tmux-hosted session records no daemon PTY,
// which is the only thing the treemux assistant pane can attach — so the
// supervisor could never restart the chain it exists to keep alive.
//
// Deliberately a flag rather than an environment variable: an exported
// GROVE_AGENT_TARGET would be inherited by the agent the launch creates, and
// every `flow plan run` that agent itself issues for OTHER plans would silently
// adopt the supervisor's routing. GROVE_TERMINAL already taught this lesson
// (see ResolveAgentTargetHosted).
func ResolveAgentTargetExplicit(explicit string) (string, error) {
	target := strings.ToLower(strings.TrimSpace(explicit))
	if target == "" {
		return ResolveAgentTarget(), nil
	}
	switch target {
	case AgentTargetTmux, AgentTargetNative, AgentTargetTuimux:
		return target, nil
	default:
		return "", fmt.Errorf("unsupported agent target %q: expected one of %s, %s, %s",
			explicit, AgentTargetTmux, AgentTargetNative, AgentTargetTuimux)
	}
}

// AgentTargetForSubmission returns the target a daemon submission for this plan
// must carry. A target already injected into the plan wins — a caller that
// resolved routing more precisely than the environment can (the TUI's hosted
// flag, or the daemon replaying a submission's target into the plan it loads)
// has already recorded it there — otherwise it is derived from the environment.
//
// This is the part of the derivation that stays in flow: it reads a *Plan, a
// flow type that core has no business knowing about.
func AgentTargetForSubmission(plan *Plan) string {
	if plan != nil && plan.Orchestration != nil && plan.Orchestration.AgentTarget != "" {
		return plan.Orchestration.AgentTarget
	}
	return ResolveAgentTarget()
}
