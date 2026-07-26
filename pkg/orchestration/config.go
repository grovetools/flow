package orchestration

import (
	"fmt"
	"strings"

	"github.com/grovetools/core/config"
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

// FlowConfigFrom decodes the 'flow' extension from an already-loaded core
// config into a FlowConfig. It is the single unmarshal+error-wrap helper shared
// by every flow-config load path. UnmarshalExtension returns nil (leaving the
// target zero-valued) when the 'flow' section is absent, so a non-nil error here
// only ever means a genuinely malformed config — worth surfacing.
func FlowConfigFrom(cfg *config.Config) (*FlowConfig, error) {
	var flowCfg FlowConfig
	if err := cfg.UnmarshalExtension("flow", &flowCfg); err != nil {
		return nil, fmt.Errorf("failed to parse 'flow' configuration from grove.yml: %w", err)
	}
	return &flowCfg, nil
}

// LoadFlowConfig loads the core grove config from the current directory
// (hierarchical: global -> project -> override) and decodes its 'flow'
// extension. A failure to load the core config is tolerated (an empty config is
// used); only a malformed 'flow' section produces an error.
func LoadFlowConfig() (*FlowConfig, error) {
	coreCfg, err := config.LoadFrom(".")
	if err != nil {
		coreCfg = &config.Config{}
	}
	return FlowConfigFrom(coreCfg)
}

// LoadFlowConfigDefault is like LoadFlowConfig but loads the core config via
// config.LoadDefault() (global-anchored discovery) rather than from the current
// directory.
func LoadFlowConfigDefault() (*FlowConfig, error) {
	coreCfg, err := config.LoadDefault()
	if err != nil {
		coreCfg = &config.Config{}
	}
	return FlowConfigFrom(coreCfg)
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

// resolveProviderArgs returns the configured launch args for a provider. No
// provider gets an implicit default — claude is never auto-given
// --dangerously-skip-permissions; the bypass is opt-in via
// [flow.providers.claude] args only.
func resolveProviderArgs(cfg FlowConfig, providerName string) []string {
	var args []string
	if cfg.Providers != nil {
		if pc, ok := cfg.Providers[providerName]; ok {
			args = pc.Args
		}
	}
	// Always hand back a copy. Callers append to the result — the Pi providers
	// append a per-job --session-dir — and appending to the config's own slice
	// lets two jobs launching concurrently write into the same backing array
	// and cross-wire each other's arguments. appendProviderJobArgs copies for
	// the same reason, but only on the path where a model or effort is set, so
	// the guarantee has to live here to cover every caller.
	if len(args) == 0 {
		return nil
	}
	out := make([]string, len(args))
	copy(out, args)
	return out
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
