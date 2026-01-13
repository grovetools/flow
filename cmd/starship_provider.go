package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattsolo1/grove-core/git"
	"github.com/mattsolo1/grove-core/pkg/workspace"
	"github.com/mattsolo1/grove-core/state"
	"github.com/mattsolo1/grove-core/tui/theme"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/muesli/termenv"
	"gopkg.in/yaml.v3"
)

// PlanStatistics holds the statistics for a plan.
type PlanStatistics struct {
	Completed int
	Running   int
	Pending   int
	Failed    int
	Todo      int
	Hold      int
	Abandoned int
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
		case orchestration.JobStatusTodo:
			stats.Todo++
		case orchestration.JobStatusHold:
			stats.Hold++
		case orchestration.JobStatusAbandoned:
			stats.Abandoned++
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
		return activePlanStr, nil
	}

	// Read the plan config
	configPath := filepath.Join(planPath, ".grove-plan.yml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		// Config file not found, just show the plan name
		return activePlanStr, nil
	}

	var config struct {
		Model    string `yaml:"model"`
		Worktree string `yaml:"worktree"`
		Status   string `yaml:"status"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		// Invalid config, just show the plan name
		return activePlanStr, nil
	}

	// If the plan is marked as finished, don't show it in the prompt
	if config.Status == "finished" {
		return "", nil
	}

	// Strip ".grove" prefix if present
	displayName := activePlanStr
	if strings.HasPrefix(displayName, ".grove") {
		displayName = strings.TrimPrefix(displayName, ".grove")
	}

	// Get current git branch to avoid redundant display
	// If plan name equals branch name, skip showing the plan name since starship already shows the branch
	cwd, _ := os.Getwd()
	_, currentBranch, _ := git.GetRepoInfo(cwd)
	showPlanName := displayName != currentBranch

	// Force color output for shell prompts
	lipgloss.SetColorProfile(termenv.TrueColor)

	var output string
	if showPlanName {
		// Color the plan name with Info style
		output = theme.DefaultTheme.Info.Render(displayName)
	}

	// Load the plan to get job statistics
	plan, err := orchestration.LoadPlan(planPath)
	if err == nil && len(plan.Jobs) > 0 {
		stats := GetPlanStatistics(plan)

		var statsParts []string
		if stats.Completed > 0 {
			statsParts = append(statsParts, theme.DefaultTheme.Success.Render(fmt.Sprintf("%s %d", theme.IconStatusCompleted, stats.Completed)))
		}
		if stats.Running > 0 {
			statsParts = append(statsParts, theme.DefaultTheme.Info.Render(fmt.Sprintf("%s %d", theme.IconStatusRunning, stats.Running)))
		}
		if stats.Pending > 0 {
			statsParts = append(statsParts, theme.DefaultTheme.Muted.Render(fmt.Sprintf("%s %d", theme.IconStatusPendingUser, stats.Pending)))
		}
		if stats.Failed > 0 {
			statsParts = append(statsParts, theme.DefaultTheme.Error.Render(fmt.Sprintf("%s %d", theme.IconStatusFailed, stats.Failed)))
		}
		if stats.Todo > 0 {
			statsParts = append(statsParts, theme.DefaultTheme.Muted.Render(fmt.Sprintf("%s %d", theme.IconStatusTodo, stats.Todo)))
		}
		if stats.Hold > 0 {
			statsParts = append(statsParts, theme.DefaultTheme.Warning.Render(fmt.Sprintf("%s %d", theme.IconStatusHold, stats.Hold)))
		}
		if stats.Abandoned > 0 {
			statsParts = append(statsParts, theme.DefaultTheme.Muted.Render(fmt.Sprintf("%s %d", theme.IconStatusAbandoned, stats.Abandoned)))
		}

		// Add worktree icon if in worktree
		if config.Worktree != "" {
			statsParts = append(statsParts, theme.DefaultTheme.Accent.Render(theme.IconWorktree))
		}

		if len(statsParts) > 0 {
			if output != "" {
				output += " "
			}
			// Determine the appropriate icon based on workspace type
			// Use ecosystem icon if within an ecosystem, otherwise repo icon
			icon := theme.IconRepo
			if wsNode, err := workspace.GetProjectByPath(cwd); err == nil && wsNode != nil {
				if wsNode.IsEcosystem() || wsNode.RootEcosystemPath != "" {
					icon = theme.IconEcosystem
				}
			}
			output += fmt.Sprintf("%s %s", icon, strings.Join(statsParts, " "))
		}
	}

	return output, nil
}
