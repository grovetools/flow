package cmd

import (
	"github.com/spf13/cobra"
)

// This file contains the constructors for simple top-level commands
// that are aliases for existing `plan` subcommands.

func NewAddCmd() *cobra.Command {
	addCmd := &cobra.Command{
		Use:   "add [directory]",
		Short: "Add a new job to an existing plan",
		Long: `Add a new job to an existing orchestration plan.
Can be used interactively or with command-line arguments.
If no directory is specified, uses the active job if set.

Examples:
  # Add a job with inline prompt
  flow add myplan -t agent --title "Implementation" -d 01-plan.md -p "Implement the user authentication feature"

  # Use active job
  flow set myplan
  flow add -t agent --title "Implementation" -d 01-plan.md -p "Implement feature"`,
		Args: cobra.MaximumNArgs(1),
		RunE: runPlanAdd,
	}
	// Add flags from plan_add_step.go
	addCmd.Flags().StringVar(&planAddTemplate, "template", "", "Name of the job template to use")
	addCmd.Flags().StringVarP(&planAddType, "type", "t", "agent", "Job type: oneshot, chat, shell, headless_agent, interactive_agent, or file (agent is an alias for interactive_agent)")
	addCmd.Flags().StringVar(&planAddTitle, "title", "", "Job title")
	addCmd.Flags().StringSliceVarP(&planAddDependsOn, "depends-on", "d", nil, "Dependencies (job filenames)")
	addCmd.Flags().StringVarP(&planAddPromptFile, "prompt-file", "f", "", "File containing the prompt")
	addCmd.Flags().StringVarP(&planAddPrompt, "prompt", "p", "", "Inline prompt text (alternative to --prompt-file)")
	addCmd.Flags().BoolVarP(&planAddInteractive, "interactive", "i", false, "Interactive mode")
	addCmd.Flags().StringSliceVar(&planAddIncludeFiles, "include", nil, "Comma-separated list of files to include as context")
	addCmd.Flags().StringVar(&planAddWorktree, "worktree", "", "Explicitly set the worktree name (overrides automatic inference)")
	addCmd.Flags().StringSliceVar(&planAddInline, "inline", nil, "File types to inline in prompt: dependencies, include, context, all, files, none")
	addCmd.Flags().BoolVar(&planAddPrependDependencies, "prepend-dependencies", false, "[DEPRECATED] Use --inline=dependencies. Inline dependency content into prompt body")
	addCmd.Flags().StringVar(&planAddRecipe, "recipe", "", "Name of a recipe to add to the plan")
	addCmd.Flags().StringArrayVar(&planAddRecipeVars, "recipe-vars", nil, "Variables for the recipe templates (e.g., key=value)")
	return addCmd
}

func NewRunCmd() *cobra.Command {
	runCmd := &cobra.Command{
		Use:   "run [job-file...]",
		Short: "Run jobs in an orchestration plan",
		Long: `Run jobs in an orchestration plan.
Without arguments, runs the next available jobs.
With a single job file argument, runs that specific job.
With multiple job file arguments, runs those jobs in parallel.`,
		RunE: runPlanRun,
	}
	runCmd.Flags().StringVarP(&planRunDir, "dir", "d", ".", "Plan directory")
	runCmd.Flags().BoolVarP(&planRunAll, "all", "a", false, "Run all pending jobs")
	runCmd.Flags().BoolVarP(&planRunNext, "next", "n", false, "Run next available jobs")
	runCmd.Flags().IntVarP(&planRunParallel, "parallel", "p", 3, "Max parallel jobs")
	runCmd.Flags().BoolVarP(&planRunWatch, "watch", "w", false, "Watch progress in real-time")
	runCmd.Flags().BoolVarP(&planRunYes, "yes", "y", false, "Skip confirmation prompts")
	runCmd.Flags().StringVar(&planRunModel, "model", "", "Override model for jobs (e.g., claude-3-5-sonnet-20240620, gpt-4)")
	runCmd.Flags().BoolVar(&planRunSkipInteractive, "skip-interactive", false, "Skip interactive agent jobs (useful for CI/automation)")
	return runCmd
}

func NewStatusCmd() *cobra.Command {
	statusCmd := &cobra.Command{
		Use:   "status [directory]",
		Short: "Show plan status in an interactive TUI",
		Long: `Show the status of all jobs in an orchestration plan within an interactive TUI.
If no directory is specified, uses the active job if set.
If no active job is set, it will launch the plan browser.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runPlanStatus,
	}
	statusCmd.Flags().BoolVarP(&statusTUI, "tui", "t", false, "Launch interactive TUI (default behavior, kept for backwards compatibility)")
	return statusCmd
}

func NewActionCmd() *cobra.Command {
	actionCmd := &cobra.Command{
		Use:   "action [action-name] [plan-name]",
		Short: "Execute or list workspace actions from a recipe",
		Long: `Execute a named workspace action defined in the recipe's workspace_init.yml,
or list all available actions.

Examples:
  # List available actions for the current plan
  flow action --list

  # List available actions for a specific plan
  flow action --list my-plan

  # Run the "start-dev" action for the current plan
  flow action start-dev

  # Run the "start-dev" action for a specific plan
  flow action start-dev my-plan

  # Run the init actions (special case)
  flow action init

Available actions are defined in the recipe's workspace_init.yml under the 'actions' key.
The special action 'init' runs the actions defined under the 'init' key.`,
		Args: cobra.RangeArgs(0, 2),
		RunE: runPlanAction,
	}
	actionCmd.Flags().BoolVar(&planActionList, "list", false, "List available actions for the plan")
	return actionCmd
}