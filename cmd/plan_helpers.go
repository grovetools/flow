package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/mattsolo1/grove-core/config"
	"github.com/mattsolo1/grove-core/util/pathutil"
	"github.com/mattsolo1/grove-flow/pkg/state"
)

// resolvePlanPath determines the absolute path for a plan directory.
func resolvePlanPath(planName string) (string, error) {
	flowCfg, err := loadFlowConfig()
	if err != nil {
		// It's okay if config doesn't exist, we just won't use PlansDirectory.
		flowCfg = &FlowConfig{}
	}

	if flowCfg.PlansDirectory == "" {
		// No custom directory configured, use the provided name as-is.
		return filepath.Abs(planName)
	}

	// A custom plans directory is configured.
	expandedBasePath, err := pathutil.Expand(flowCfg.PlansDirectory)
	if err != nil {
		return "", fmt.Errorf("could not expand plans_directory path: %w", err)
	}

	fullPath := filepath.Join(expandedBasePath, planName)
	return filepath.Abs(fullPath)
}

// resolvePlanPathWithActiveJob resolves a plan path, using the active job if no path is provided.
func resolvePlanPathWithActiveJob(planName string) (string, error) {
	// If no plan name provided, try to use active job
	if planName == "" {
		activeJob, err := state.GetActiveJob()
		if err != nil {
			return "", fmt.Errorf("get active job: %w", err)
		}
		if activeJob == "" {
			return "", fmt.Errorf("no plan directory specified and no active job set (use 'flow plan set <plan-directory>' to set one)")
		}
		planName = activeJob
	}

	return resolvePlanPath(planName)
}

// loadFlowConfigWithDynamicRecipes is a helper to load flow config and extract the get_recipe_cmd.
func loadFlowConfigWithDynamicRecipes() (*FlowConfig, string, error) {
	coreCfg, err := config.LoadFrom(".")
	if err != nil {
		coreCfg = &config.Config{}
	}

	// Load the flow section as a generic map to find get_recipe_cmd
	var rawFlowConfig map[string]interface{}
	if err := coreCfg.UnmarshalExtension("flow", &rawFlowConfig); err != nil {
		return nil, "", fmt.Errorf("failed to parse 'flow' configuration: %w", err)
	}

	var getRecipeCmd string
	if recipes, ok := rawFlowConfig["recipes"].(map[string]interface{}); ok {
		if cmd, ok := recipes["get_recipe_cmd"].(string); ok {
			getRecipeCmd = cmd
			// Remove the key so it doesn't interfere with unmarshalling into FlowConfig
			delete(recipes, "get_recipe_cmd")
		}
	}
	
	// Now unmarshal into the typed FlowConfig struct
	var flowCfg FlowConfig
	if err := coreCfg.UnmarshalExtension("flow", &flowCfg); err != nil {
		return nil, "", fmt.Errorf("failed to parse 'flow' configuration into struct: %w", err)
	}
	
	return &flowCfg, getRecipeCmd, nil
}
