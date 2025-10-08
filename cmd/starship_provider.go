package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-core/state"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"gopkg.in/yaml.v3"
)

// PlanStatistics holds the statistics for a plan.
type PlanStatistics struct {
	Completed int
	Running   int
	Pending   int
	Failed    int
	Total     int
}

// GetPlanStatistics calculates statistics for a plan.
// This is extracted from the plan status command to be reused by the starship provider.
func GetPlanStatistics(plan *orchestration.Plan) PlanStatistics {
	stats := PlanStatistics{
		Total: len(plan.Jobs),
	}

	for _, job := range plan.Jobs {
		switch job.Status {
		case orchestration.JobStatusCompleted:
			stats.Completed++
		case orchestration.JobStatusRunning:
			stats.Running++
		case orchestration.JobStatusPending, orchestration.JobStatusPendingUser, orchestration.JobStatusPendingLLM:
			stats.Pending++
		case orchestration.JobStatusFailed:
			stats.Failed++
		}
	}

	return stats
}

// FlowStatusProvider is the status provider for grove-flow.
// It generates the starship prompt status string based on the active plan.
func FlowStatusProvider(s state.State) (string, error) {
	// Get the active plan from state (with backwards compatibility)
	// Try new namespaced key first, then fall back to old key
	activePlan, ok := s["flow.active_plan"]
	if !ok {
		// Fall back to old key for backwards compatibility
		activePlan, ok = s["active_plan"]
		if !ok {
			return "", nil // No active plan
		}
	}

	activePlanStr, ok := activePlan.(string)
	if !ok || activePlanStr == "" {
		return "", nil
	}

	// Resolve the plan path
	planPath, err := resolvePlanPathWithActiveJob(activePlanStr)
	if err != nil {
		// Can't resolve path, just show the plan name
		return fmt.Sprintf("📈 Plan: %s", activePlanStr), nil
	}

	// Read the plan config
	configPath := filepath.Join(planPath, ".grove-plan.yml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		// Config file not found, just show the plan name
		return fmt.Sprintf("📈 Plan: %s", activePlanStr), nil
	}

	var config struct {
		Model    string `yaml:"model"`
		Worktree string `yaml:"worktree"`
		Status   string `yaml:"status"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		// Invalid config, just show the plan name
		return fmt.Sprintf("📈 Plan: %s", activePlanStr), nil
	}

	// If the plan is marked as finished, don't show it in the prompt
	if config.Status == "finished" {
		return "", nil
	}

	// Format: 📈 Plan: plan-name (stats) 🤖 model-name 🌲 in worktree (or worktree name)
	output := fmt.Sprintf("📈 Plan: %s", activePlanStr)

	// Load the plan to get job statistics
	plan, err := orchestration.LoadPlan(planPath)
	if err == nil && len(plan.Jobs) > 0 {
		stats := GetPlanStatistics(plan)

		// Add job statistics with symbols matching flow plan status TUI
		var statsParts []string
		if stats.Completed > 0 {
			statsParts = append(statsParts, fmt.Sprintf("●%d", stats.Completed))
		}
		if stats.Running > 0 {
			statsParts = append(statsParts, fmt.Sprintf("◐%d", stats.Running))
		}
		if stats.Pending > 0 {
			statsParts = append(statsParts, fmt.Sprintf("○%d", stats.Pending))
		}
		if stats.Failed > 0 {
			statsParts = append(statsParts, fmt.Sprintf("✗%d", stats.Failed))
		}
		if len(statsParts) > 0 {
			output += fmt.Sprintf(" (%s)", strings.Join(statsParts, " "))
		}
	}

	if config.Model != "" {
		output += fmt.Sprintf(" 🤖 %s", config.Model)
	}

	if config.Worktree != "" {
		if config.Worktree == activePlanStr {
			output += " 🌲 in worktree"
		} else {
			output += fmt.Sprintf(" 🌲 %s", config.Worktree)
		}
	}

	return output, nil
}
