package cmd

import (
	"fmt"
	"github.com/grovetools/core/config"
)

//go:generate sh -c "cd .. && go run ./tools/schema-generator/"

// FlowConfig defines the structure for the 'flow' section in grove.yml.
type FlowConfig struct {
	// Job execution settings
	OneshotModel         string                  `yaml:"oneshot_model" jsonschema:"description=Default model for oneshot jobs" jsonschema_extras:"x-layer=global,x-priority=60,x-important=true"`
	MaxConsecutiveSteps  int                     `yaml:"max_consecutive_steps" jsonschema:"description=Maximum consecutive steps before requiring user input" jsonschema_extras:"x-layer=global,x-priority=85"`
	TargetAgentContainer string                  `yaml:"target_agent_container" jsonschema:"description=Docker container name for agent execution" jsonschema_extras:"x-layer=project,x-priority=80"`
	RunInitByDefault     *bool                   `yaml:"run_init_by_default" jsonschema:"description=Run init actions by default (nil = true)" jsonschema_extras:"x-layer=project,x-priority=81"`

	// Summary settings
	SummarizeOnComplete bool   `yaml:"summarize_on_complete" jsonschema:"description=Generate summary when job completes" jsonschema_extras:"x-layer=global,x-priority=70"`
	SummaryModel        string `yaml:"summary_model" jsonschema:"description=Model to use for generating summaries" jsonschema_extras:"x-layer=global,x-priority=71"`
	SummaryPrompt       string `yaml:"summary_prompt" jsonschema:"description=Custom prompt for summary generation" jsonschema_extras:"x-layer=global,x-priority=72"`
	SummaryMaxChars     int    `yaml:"summary_max_chars" jsonschema:"description=Maximum characters in generated summary" jsonschema_extras:"x-layer=global,x-priority=73"`

	// Agent settings (formerly in separate [agent] extension)
	AgentArgs           []string                  `yaml:"agent_args" jsonschema:"description=Default command-line arguments for agents" jsonschema_extras:"x-layer=global,x-priority=50"`
	InteractiveProvider string                    `yaml:"interactive_provider,omitempty" jsonschema:"description=Provider for interactive sessions: claude/codex/opencode,enum=claude,enum=codex,enum=opencode" jsonschema_extras:"x-layer=global,x-priority=55,x-important=true"`
	Providers           map[string]ProviderConfig `yaml:"providers" jsonschema:"description=Configuration for agent providers" jsonschema_extras:"x-layer=global,x-priority=65,x-important=true"`

	// Recipe settings
	Recipes map[string]RecipeConfig `yaml:"recipes" jsonschema:"description=Recipe-specific variable overrides" jsonschema_extras:"x-layer=project,x-priority=90"`

	// Deprecated fields
	ChatDirectory  string `yaml:"chat_directory" jsonschema:"description=DEPRECATED: Configure notebook.root_dir instead" jsonschema_extras:"x-layer=global,x-priority=200,x-status=deprecated,x-status-message=Chats are now stored in notebook workspaces,x-status-since=v0.6.0,x-status-target=v1.0,x-status-replaced-by=notebook.root_dir"`
	PlansDirectory string `yaml:"plans_directory" jsonschema:"description=DEPRECATED: Configure notebook.root_dir instead" jsonschema_extras:"x-layer=global,x-priority=200,x-status=deprecated,x-status-message=Plans are now stored in notebook workspaces,x-status-since=v0.6.0,x-status-target=v1.0,x-status-replaced-by=notebook.root_dir"`
}

// RecipeConfig defines configuration for a specific recipe.
type RecipeConfig struct {
	Vars map[string]string `yaml:"vars" jsonschema:"description=Variable overrides for this recipe" jsonschema_extras:"x-layer=project,x-priority=90"`
}

// ProviderConfig holds settings for a specific agent provider.
type ProviderConfig struct {
	Args []string `yaml:"args" jsonschema:"description=Command-line arguments for this provider" jsonschema_extras:"x-layer=global,x-priority=65,x-important=true"`
}

// AppConfig wraps the core config with flow-specific extensions.
type AppConfig struct {
	Core *config.Config
	Flow *FlowConfig
}

// loadFlowConfig loads the core grove config and unmarshals the 'flow' extension.
func loadFlowConfig() (*FlowConfig, error) {
	// Load the config using LoadFrom to get the full hierarchy (global -> project -> override)
	coreCfg, err := config.LoadFrom(".")
	if err != nil {
		// It's okay if the core config doesn't exist, we'll just use an empty one.
		coreCfg = &config.Config{}
	}

	var flowCfg FlowConfig
	if err := coreCfg.UnmarshalExtension("flow", &flowCfg); err != nil {
		return nil, fmt.Errorf("failed to parse 'flow' configuration from grove.yml: %w", err)
	}

	return &flowCfg, nil
}

// loadFullConfig loads the entire grove config including flow settings
func loadFullConfig() (*AppConfig, error) {
	coreCfg, err := config.LoadFrom(".")
	if err != nil {
		// It's okay if the config doesn't exist, we'll just use an empty one.
		coreCfg = &config.Config{}
	}

	var flowCfg FlowConfig
	coreCfg.UnmarshalExtension("flow", &flowCfg)

	return &AppConfig{
		Core: coreCfg,
		Flow: &flowCfg,
	}, nil
}
