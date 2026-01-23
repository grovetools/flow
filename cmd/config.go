package cmd

import (
	"fmt"
	"github.com/grovetools/core/config"
)

//go:generate sh -c "cd .. && go run ./tools/schema-generator/"

// FlowConfig defines the structure for the 'flow' section in grove.yml.
type FlowConfig struct {
	ChatDirectory        string                  `yaml:"chat_directory"`
	OneshotModel         string                  `yaml:"oneshot_model"`
	TargetAgentContainer string                  `yaml:"target_agent_container"`
	PlansDirectory       string                  `yaml:"plans_directory"`
	MaxConsecutiveSteps  int                     `yaml:"max_consecutive_steps"`
	SummarizeOnComplete  bool                    `yaml:"summarize_on_complete"`
	SummaryModel         string                  `yaml:"summary_model"`
	SummaryPrompt        string                  `yaml:"summary_prompt"`
	SummaryMaxChars      int                     `yaml:"summary_max_chars"`
	RunInitByDefault     *bool                   `yaml:"run_init_by_default"` // Whether to run init actions by default (nil = true)
	Recipes              map[string]RecipeConfig `yaml:"recipes"`
}

// RecipeConfig defines configuration for a specific recipe.
type RecipeConfig struct {
	Vars map[string]string `yaml:"vars"`
}

// ProviderConfig holds settings for a specific agent provider.
type ProviderConfig struct {
	Args []string `yaml:"args"`
}

// AgentConfig defines the structure for the 'agent' section in grove.yml.
type AgentConfig struct {
	Args                      []string                   `yaml:"args"`
	MountWorkspaceAtHostPath  bool                       `yaml:"mount_workspace_at_host_path"`
	UseSuperprojectRoot       bool                       `yaml:"use_superproject_root"`
	InteractiveProvider       string                     `yaml:"interactive_provider,omitempty"` // "claude", "codex", or "opencode"
	Providers                 map[string]ProviderConfig  `yaml:"providers"`
}

// AppConfig wraps the core config with flow-specific extensions.
type AppConfig struct {
	Core  *config.Config
	Flow  *FlowConfig
	Agent *AgentConfig
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

// loadFullConfig loads the entire grove config including agent settings
func loadFullConfig() (*AppConfig, error) {
	coreCfg, err := config.LoadFrom(".")
	if err != nil {
		// It's okay if the config doesn't exist, we'll just use an empty one.
		coreCfg = &config.Config{}
	}

	var flowCfg FlowConfig
	coreCfg.UnmarshalExtension("flow", &flowCfg)

	var agentCfg AgentConfig
	coreCfg.UnmarshalExtension("agent", &agentCfg)

	return &AppConfig{
		Core:  coreCfg,
		Flow:  &flowCfg,
		Agent: &agentCfg,
	}, nil
}
