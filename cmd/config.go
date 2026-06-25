package cmd

import (
	"fmt"

	"github.com/grovetools/core/config"
	"github.com/grovetools/flow/pkg/orchestration"
)

//go:generate sh -c "cd .. && go run ./tools/schema-generator/"

// FlowConfig, ProviderConfig and RecipeConfig are now defined canonically in the
// orchestration package (flow/pkg/orchestration/config.go) so the agent-launch
// executors and the CLI share a single definition. They are aliased here to keep
// existing cmd-package references compiling without an orchestration. prefix.
type (
	FlowConfig     = orchestration.FlowConfig
	ProviderConfig = orchestration.ProviderConfig
	RecipeConfig   = orchestration.RecipeConfig
)

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
	_ = coreCfg.UnmarshalExtension("flow", &flowCfg)

	return &AppConfig{
		Core: coreCfg,
		Flow: &flowCfg,
	}, nil
}
