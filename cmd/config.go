package cmd

import (
	"fmt"
	"github.com/mattsolo1/grove-core/config"
)

// FlowConfig defines the structure for the 'flow' section in grove.yml.
type FlowConfig struct {
	ChatDirectory        string `yaml:"chat_directory"`
	OneshotModel         string `yaml:"oneshot_model"`
	TargetAgentContainer string `yaml:"target_agent_container"`
	PlansDirectory       string `yaml:"plans_directory"`
	MaxConsecutiveSteps  int    `yaml:"max_consecutive_steps"`
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
func loadFullConfig() (*config.Config, error) {
	coreCfg, err := config.LoadFrom(".")
	if err != nil {
		// It's okay if the core config doesn't exist, we'll just use an empty one.
		coreCfg = &config.Config{}
	}
	return coreCfg, nil
}
