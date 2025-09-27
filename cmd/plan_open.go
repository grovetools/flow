package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	grovelogging "github.com/mattsolo1/grove-core/logging"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

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
		return fmt.Errorf(errorMsg.String())
	}

	// The new core logic:
	logger := grovelogging.NewPrettyLogger()
	logger.InfoPretty(fmt.Sprintf("Delegating to 'grove ws open %s'...", worktreeName))

	// Check if grove-meta is available
	if _, err := exec.LookPath("grove"); err != nil {
		return fmt.Errorf("failed to open workspace session: 'grove' command not found in PATH. Please ensure grove-meta is installed")
	}

	execCmd := exec.Command("grove", "ws", "open", worktreeName)
	execCmd.Stdin = os.Stdin
	execCmd.Stdout = os.Stdout
	execCmd.Stderr = os.Stderr

	err = execCmd.Run()
	if err != nil {
		// Provide more informative error message
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 127 {
				return fmt.Errorf("failed to open workspace session for '%s': 'grove' command not found. Please ensure grove-meta is installed and in your PATH", worktreeName)
			}
			return fmt.Errorf("failed to open workspace session for '%s': command exited with code %d. Please ensure the workspace exists. You can create it with: grove ws create %s", worktreeName, exitErr.ExitCode(), worktreeName)
		}
		return fmt.Errorf("failed to open workspace session for '%s'. Please ensure grove-meta is installed and the workspace exists. Error: %w", worktreeName, err)
	}
	return nil
}