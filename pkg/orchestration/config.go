package orchestration

import (
	"fmt"
	"strings"
)

// Config holds orchestration-specific settings, decoupled from grove-core.
type Config struct {
	OneshotModel        string
	MaxConsecutiveSteps int
	AgentTarget         string // "native" or "tmux" — resolved at submission time, never "auto"
}

// FlowConfig defines the structure for the 'flow' section in grove.yml.
//
// This is the single, canonical definition of the flow extension config. It is
// consumed by both the `cmd` package (CLI config loading) and every agent-launch
// path in this package (interactive / headless / isolated / groveterm). It lives
// in `orchestration` because `flow/cmd` imports `orchestration` (not the reverse),
// so a shared type cannot live in `cmd` without creating an import cycle.
type FlowConfig struct {
	// Job execution settings
	OneshotModel        string `yaml:"oneshot_model" jsonschema:"description=Default model for oneshot jobs" jsonschema_extras:"x-layer=global,x-priority=60,x-important=true"`
	MaxConsecutiveSteps int    `yaml:"max_consecutive_steps" jsonschema:"description=Maximum consecutive steps before requiring user input" jsonschema_extras:"x-layer=global,x-priority=85"`
	RunInitByDefault    *bool  `yaml:"run_init_by_default" jsonschema:"description=Run init actions by default (nil = true)" jsonschema_extras:"x-layer=project,x-priority=81"`

	// Agent settings (formerly in separate [agent] extension)
	InteractiveProvider string                    `yaml:"interactive_provider,omitempty" jsonschema:"description=Provider for interactive sessions: claude/codex/opencode,enum=claude,enum=codex,enum=opencode" jsonschema_extras:"x-layer=global,x-priority=55,x-important=true"`
	AgentTarget         string                    `yaml:"agent_target,omitempty" jsonschema:"description=Agent launch target: auto/native/tmux"`
	Providers           map[string]ProviderConfig `yaml:"providers" jsonschema:"description=Configuration for agent providers" jsonschema_extras:"x-layer=global,x-priority=65,x-important=true"`

	// AgentEnv holds key/value pairs injected into the environment of every
	// agent subprocess at launch, across all providers and all launch paths.
	// Used to give an agent a cloud identity (e.g. CLOUDSDK_CONFIG /
	// GOOGLE_APPLICATION_CREDENTIALS) without code changes. GROVE_FLOW_* vars
	// always take precedence over these.
	AgentEnv map[string]string `yaml:"agent_env" jsonschema:"description=Environment variables injected into all agent subprocesses" jsonschema_extras:"x-layer=global,x-priority=64"`

	// Recipe settings
	Recipes map[string]RecipeConfig `yaml:"recipes" jsonschema:"description=Recipe-specific variable overrides" jsonschema_extras:"x-layer=project,x-priority=90"`
}

// RecipeConfig defines configuration for a specific recipe.
type RecipeConfig struct {
	Vars map[string]string `yaml:"vars" jsonschema:"description=Variable overrides for this recipe" jsonschema_extras:"x-layer=project,x-priority=90"`
}

// ProviderConfig holds settings for a specific agent provider.
type ProviderConfig struct {
	Args      []string `yaml:"args" jsonschema:"description=Command-line arguments for this provider" jsonschema_extras:"x-layer=global,x-priority=65,x-important=true"`
	InputMode string   `yaml:"input_mode,omitempty" jsonschema:"description=Input mode for interactive sessions: vim or standard"`
}

// resolveProviderArgs returns the configured launch args for a provider,
// applying the claude backward-compat default.
//
// Backward-compat decision: headless and isolated claude agents historically
// launched with a hardcoded `--dangerously-skip-permissions`. That hardcode has
// been removed in favor of deriving flags from `[flow.providers.claude] args`
// uniformly across all launch paths. To preserve the behavior existing
// autonomous agents rely on, the bypass flag is the DEFAULT for the claude
// provider whenever no args are configured (the `[flow.providers.claude]` table
// is absent). To run a claude agent WITHOUT the bypass (e.g. the sandbox-profile
// validator), configure `[flow.providers.claude]` with an explicit args list
// that omits the flag. Non-claude providers never receive a default.
//
// Note: this default now also applies to the interactive path, which previously
// passed no args when unconfigured. This is the deliberate cost of unifying all
// claude launch paths through one config field; opt out via grove.toml.
func resolveProviderArgs(cfg FlowConfig, providerName string) []string {
	var args []string
	if cfg.Providers != nil {
		if pc, ok := cfg.Providers[providerName]; ok {
			args = pc.Args
		}
	}
	if providerName == "claude" && args == nil {
		args = []string{"--dangerously-skip-permissions"}
	}
	return args
}

// shellSingleQuote wraps a value in single quotes for safe inline use in a shell
// command, escaping any embedded single quotes via the '\” idiom.
func shellSingleQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// agentEnvInline renders an agent_env map as a shell fragment of `K='V' `
// assignments suitable for prefixing a single command, scoping the variables to
// the agent process. Values are single-quote escaped. Returns "" for an empty
// map. Callers must place this BEFORE the GROVE_FLOW_* assignments so the latter
// win on key collisions.
func agentEnvInline(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	var b strings.Builder
	for k, v := range env {
		fmt.Fprintf(&b, "%s=%s ", k, shellSingleQuote(v))
	}
	return b.String()
}
