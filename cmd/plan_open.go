package cmd

import (
	"fmt"
	"strings"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

// NewOpenCmd creates the top-level `open` command.
func NewOpenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "open [directory]",
		Short: "Open a plan's worktree in a dedicated tmux session",
		Long: `Switches to or creates a tmux session for the plan's worktree and opens the interactive status TUI.
This provides a one-command entry point into a plan's interactive environment.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runPlanOpen,
	}
}

// runPlanOpen implements the open command.
func runPlanOpen(cmd *cobra.Command, args []string) error {
	var planDir string
	if len(args) > 0 {
		planDir = args[0]
	}

	// Resolve the plan path
	resolvedPath, err := resolvePlanPathWithActiveJob(planDir)
	if err != nil {
		return fmt.Errorf("could not resolve plan path: %w", err)
	}

	// Load the plan
	plan, err := orchestration.LoadPlan(resolvedPath)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}

	// --- Determine the authoritative worktree for the plan ---
	var worktreeName string

	// 1. Prioritize worktree from the plan's config file
	if plan.Config != nil && plan.Config.Worktree != "" {
		worktreeName = plan.Config.Worktree
	} else {
		// 2. If not in config, analyze jobs for a single, unique worktree
		worktrees := make(map[string]bool)
		hasMainRepoJobs := false
		for _, job := range plan.Jobs {
			if job.Worktree == "" {
				hasMainRepoJobs = true
			} else {
				worktrees[job.Worktree] = true
			}
		}

		// Only proceed if there's exactly one worktree and no main repo jobs
		if len(worktrees) == 1 && !hasMainRepoJobs {
			for wt := range worktrees {
				worktreeName = wt
				break
			}
		}
	}

	// 3. If no single worktree could be determined, return an error
	if worktreeName == "" {
		var errorMsg strings.Builder
		errorMsg.WriteString(fmt.Sprintf("could not determine a single worktree for plan '%s'.\n", plan.Name))
		errorMsg.WriteString("Please either:\n")
		errorMsg.WriteString(fmt.Sprintf("  a) Set a default worktree in '%s/.grove-plan.yml' with 'flow plan config --set worktree=<name>'\n", plan.Directory))
		errorMsg.WriteString("  b) Ensure all jobs in the plan use the same worktree name.")
		return fmt.Errorf("%s", errorMsg.String())
	}

	// Create a minimal plan object for the session helper.
	planForSession := &orchestration.Plan{
		Name:      plan.Name,
		Directory: plan.Directory,
	}

	// The command to run after setting the active plan in the new session.
	// This provides a good user experience by showing the plan's status.
	commandToRun := []string{"flow", "plan", "status", "-t"}

	// Call the session helper.
	err = CreateOrSwitchToWorktreeSessionAndRunCommand(
		cmd.Context(),
		planForSession,
		worktreeName,
		commandToRun,
	)
	if err != nil {
		return fmt.Errorf("failed to open workspace session for '%s': %w", worktreeName, err)
	}

	return nil
}