package cmd

import (
	"github.com/spf13/cobra"
)

// This file contains the constructors for simple top-level commands
// that are aliases for existing `plan` subcommands.

// addCmdExamples returns examples for the add command, parameterized by command prefix
func addCmdExamples(cmdPrefix string) string {
	return `
  # Add a job with inline prompt
  ` + cmdPrefix + ` myplan -t agent --title "implement-feature" --depends-on 01-plan.md -p "Implement the feature"

  # Add a job with prompt from file
  ` + cmdPrefix + ` myplan -t agent --title "implement-feature" --depends-on 01-plan.md -f prompt.md

  # Add a job with prompt from stdin
  echo "Implement feature X" | ` + cmdPrefix + ` myplan -t agent --title "implement-feature" --depends-on 01-plan.md

  # Use active job (after: flow set myplan)
  ` + cmdPrefix + ` -t agent --title "implement-feature" --depends-on 01-plan.md -p "Implement feature"

  # Specify a model for this job
  ` + cmdPrefix + ` myplan --title "analyze-codebase" --model gemini-3-pro-preview -p "Analyze the codebase"

  # Include git changes as context (useful for review/commit jobs)
  ` + cmdPrefix + ` myplan --title "review-changes" --git-changes -p "Review uncommitted changes"

  # Use a custom rules file
  ` + cmdPrefix + ` myplan --title "update-docs" --rules-file docs/.rules -p "Update documentation"

  # Use a job template
  ` + cmdPrefix + ` myplan --template code-review --title "code-review" --depends-on 01-implement.md

  # Include specific files as context
  ` + cmdPrefix + ` myplan --title "refactor-api" --include src/api.go,src/types.go -p "Refactor the API"

  # Inline dependency output into the prompt
  ` + cmdPrefix + ` myplan --title "fix-issues" --depends-on 01-review.md --inline=dependencies -p "Fix the issues"

  # Add jobs from a recipe
  ` + cmdPrefix + ` myplan --recipe standard-feature --recipe-vars "feature=auth"`
}

const addCmdLongDesc = `Add a new job to an existing orchestration plan.
Can be used interactively or with command-line arguments.
If no directory is specified, uses the active job if set.

Examples:`

func NewAddCmd() *cobra.Command {
	addCmd := &cobra.Command{
		Use:   "add [directory]",
		Short: "Add a new job to an existing plan",
		Long:  addCmdLongDesc + addCmdExamples("flow add"),
		Args:  cobra.MaximumNArgs(1),
		RunE:  runPlanAdd,
	}
	// Add flags from plan_add_step.go
	addCmd.Flags().StringVar(&planAddTemplate, "template", "", "Name of the job template to use")
	addCmd.Flags().StringVarP(&planAddType, "type", "t", "interactive_agent", `Job type:
   • oneshot          - Single LLM call, no tools or iteration
   • chat             - Interactive conversation requiring user input
   • shell            - Execute shell commands directly
   • headless_agent   - Autonomous agent without user interaction
   • interactive_agent - Agent with user interaction (default)
   • isolated_agent   - Background agent in isolated tmux server with TUI input
   • file             - Static file content, no execution
   • claw             - Delegate a task to the global claw daemon`)
	addCmd.Flags().StringVar(&planAddTitle, "title", "", "Job title")
	addCmd.Flags().StringSliceVar(&planAddDependsOn, "depends-on", nil, "Dependencies (job filenames)")
	addCmd.Flags().StringVar(&planAddParentJobID, "parent-job-id", "", "Owning parent Flow job ID (lineage only; does not create a dependency)")
	addCmd.Flags().StringVarP(&planAddPromptFile, "prompt-file", "f", "", "File containing the prompt")
	addCmd.Flags().StringVarP(&planAddPrompt, "prompt", "p", "", "Inline prompt text (alternative to --prompt-file)")
	addCmd.Flags().BoolVarP(&planAddInteractive, "interactive", "i", false, "Interactive mode")
	addCmd.Flags().StringSliceVar(&planAddIncludeFiles, "include", nil, "Comma-separated list of files to include as context")
	addCmd.Flags().StringVar(&planAddWorktree, "worktree", "", "Explicitly set the worktree name (overrides automatic inference)")
	addCmd.Flags().StringSliceVar(&planAddInline, "inline", nil, `File types to inline in prompt:
   • dependencies - Embed dependency output directly in prompt (vs separate files)
   • include      - Embed --include file content directly in prompt
   • context      - Embed project context (.grove/context) in prompt
   • all          - Inline all of the above
   • files        - Inline dependencies + include (not context)
   • none         - No inlining; content provided as separate files (default)`)
	addCmd.Flags().BoolVar(&planAddPrependDependencies, "prepend-dependencies", false, "[DEPRECATED] Use --inline=dependencies. Inline dependency content into prompt body")
	addCmd.Flags().StringVar(&planAddRecipe, "recipe", "", "Name of a recipe to add to the plan")
	addCmd.Flags().StringArrayVar(&planAddRecipeVars, "recipe-vars", nil, "Variables for the recipe templates (e.g., key=value)")
	addCmd.Flags().StringVar(&planAddSourceFile, "source-file", "", "Origin file path for tracking job provenance (e.g., Claude plan file)")
	addCmd.Flags().StringVarP(&planAddModel, "model", "m", "", "LLM model to use for this job (e.g., gemini-3-pro-preview, claude-sonnet-4-20250514)")
	addCmd.Flags().StringVar(&planAddResponder, "responder", "", "Who authors the response turns of a chat job: oracle (default; stateless LLM API call over inlined context) or agent (fresh agent session with file access per turn; never dispatched to an LLM API). Requires --type chat when set to agent")
	addCmd.Flags().StringVar(&planAddRulesFile, "rules-file", "", "Path to a custom rules file for this job")
	addCmd.Flags().BoolVar(&planAddGitChanges, "git-changes", false, "Include git changes (staged and unstaged) as context for this job")
	registerHandoffAddFlags(addCmd.Flags())
	addCmd.Flags().StringVar(&planAddSkill, "skill", "", "Skill name to inject into the agent context")
	addCmd.Flags().StringSliceVar(&planAddSkillSequence, "skill-sequence", nil, "Comma-separated list of skills to execute in sequence")
	return addCmd
}

func NewRunCmd() *cobra.Command {
	runCmd := &cobra.Command{
		Use:   "run [job-file...]",
		Short: "Run jobs in an orchestration plan",
		Long: `Run jobs in an orchestration plan.
Without arguments, runs the next available jobs.
With a single job file argument, runs that specific job.
With multiple job file arguments, runs those jobs in parallel.

Use the persistent --at <target> flag to run against a plan/worktree from
outside it (e.g. a headless orchestrator in the main checkout). --at accepts
a plan name, an absolute container path, or <container-id>/<name>. Positional
arguments are then treated as job filenames within the resolved plan dir.

Satellite dispatch: '--at satellite:<name>' ships the jobs to a grove
satellite VM instead of running locally. A plan whose .grove-plan.yml carries
'satellite: <name>' ('flow plan init --satellite <name>') dispatches there BY
DEFAULT when no --at is given; any explicit --at wins, and the reserved
'--at satellite:local' forces a local run. Ship the plan worktree first with
'grove satellite worktree push <name> --plan <plan>', and fetch agent commits
back with 'grove satellite worktree pull <name> --plan <plan> [--ff]'.

Examples:
  flow plan run --at my-feature                 # run next jobs in plan "my-feature"
  flow plan run --at my-feature 02-impl.md      # run a specific job by filename
  flow plan run 02-impl.md                      # implicit: from inside the worktree
  flow plan run --at satellite:mysat            # dispatch to satellite "mysat"
  flow plan run --at satellite:local            # force local despite a plan satellite`,
		RunE: runPlanRun,
	}
	runCmd.Flags().StringVarP(&planRunDir, "dir", "d", ".", "[DEPRECATED] Plan directory; use --at <target> instead")
	runCmd.Flags().BoolVarP(&planRunAll, "all", "a", false, "Run all pending jobs")
	runCmd.Flags().BoolVarP(&planRunNext, "next", "n", false, "Run next available jobs")
	runCmd.Flags().IntVarP(&planRunParallel, "parallel", "p", 3, "Max parallel jobs")
	runCmd.Flags().BoolVarP(&planRunWatch, "watch", "w", false, "Watch progress in real-time")
	runCmd.Flags().BoolVarP(&planRunYes, "yes", "y", false, "Skip confirmation prompts")
	runCmd.Flags().StringVar(&planRunModel, "model", "", "Override model for jobs (e.g., claude-sonnet-4-6, gemini-2.5-pro)")
	runCmd.Flags().BoolVar(&planRunSkipInteractive, "skip-interactive", false, "Skip interactive agent jobs (useful for CI/automation)")
	runCmd.Flags().BoolVar(&planRunLocal, "local", false, "Force local execution (bypass daemon)")
	runCmd.Flags().BoolVar(&planRunBackground, "background", false, "Submit to daemon and exit without waiting")
	runCmd.Flags().BoolVar(&planRunForce, "force", false, "Override an advisory satellite dispatch lease (.grove-lease.yml) on the plan dir")
	return runCmd
}

func NewStatusCmd() *cobra.Command {
	statusCmd := &cobra.Command{
		Use:   "status [directory]",
		Short: "Show plan status in an interactive TUI",
		Long: `Show the status of all jobs in an orchestration plan within an interactive TUI.
If no directory is specified, uses the active job if set.
If no active job is set, it will launch the plan browser.

Plans can be referenced by slug from any directory. If the slug is globally
unique, it will be resolved automatically. Prefer the persistent --at <target>
flag (plan name, container path, or <container-id>/<name>) to operate on a
plan/worktree from outside it. --dir is deprecated in favor of --at.

Examples:
  flow status --at my-feature               # registry-backed target (recommended)
  flow status my-feature                    # from any directory (global lookup)
  flow status --at my-feature --json        # JSON output`,
		Args: cobra.MaximumNArgs(1),
		RunE: runPlanStatus,
	}
	statusCmd.Flags().BoolVarP(&statusTUI, "tui", "t", false, "Launch interactive TUI (default behavior, kept for backwards compatibility)")
	statusCmd.Flags().StringVarP(&planStatusDir, "dir", "d", "", "[DEPRECATED] Workspace or plan directory context; use --at <target> instead")
	return statusCmd
}

func NewJobCmd() *cobra.Command {
	jobCmd := &cobra.Command{
		Use:   "job",
		Short: "Manage jobs in orchestration plans",
		Long:  `Commands for managing jobs in orchestration plans.`,
	}

	// Create the add subcommand
	jobAddCmd := &cobra.Command{
		Use:   "add [directory]",
		Short: "Add a new job to an existing plan",
		Long:  addCmdLongDesc + addCmdExamples("flow job add"),
		Args:  cobra.MaximumNArgs(1),
		RunE:  runPlanAdd,
	}

	// Register all flags (same as plan add)
	jobAddCmd.Flags().StringVar(&planAddTemplate, "template", "", "Name of the job template to use")
	jobAddCmd.Flags().StringVarP(&planAddType, "type", "t", "interactive_agent", `Job type:
   • oneshot          - Single LLM call, no tools or iteration
   • chat             - Interactive conversation requiring user input
   • shell            - Execute shell commands directly
   • headless_agent   - Autonomous agent without user interaction
   • interactive_agent - Agent with user interaction (default)
   • isolated_agent   - Background agent in isolated tmux server with TUI input
   • file             - Static file content, no execution
   • claw             - Delegate a task to the global claw daemon`)
	jobAddCmd.Flags().StringVar(&planAddTitle, "title", "", "Job title")
	jobAddCmd.Flags().StringSliceVar(&planAddDependsOn, "depends-on", nil, "Dependencies (job filenames)")
	jobAddCmd.Flags().StringVar(&planAddParentJobID, "parent-job-id", "", "Owning parent Flow job ID (lineage only; does not create a dependency)")
	jobAddCmd.Flags().StringVarP(&planAddPromptFile, "prompt-file", "f", "", "File containing the prompt")
	jobAddCmd.Flags().StringVarP(&planAddPrompt, "prompt", "p", "", "Inline prompt text (alternative to --prompt-file)")
	jobAddCmd.Flags().BoolVarP(&planAddInteractive, "interactive", "i", false, "Interactive mode")
	jobAddCmd.Flags().StringSliceVar(&planAddIncludeFiles, "include", nil, "Comma-separated list of files to include as context")
	jobAddCmd.Flags().StringVar(&planAddWorktree, "worktree", "", "Explicitly set the worktree name (overrides automatic inference)")
	jobAddCmd.Flags().StringSliceVar(&planAddInline, "inline", nil, `File types to inline in prompt:
   • dependencies - Embed dependency output directly in prompt (vs separate files)
   • include      - Embed --include file content directly in prompt
   • context      - Embed project context (.grove/context) in prompt
   • all          - Inline all of the above
   • files        - Inline dependencies + include (not context)
   • none         - No inlining; content provided as separate files (default)`)
	jobAddCmd.Flags().BoolVar(&planAddPrependDependencies, "prepend-dependencies", false, "[DEPRECATED] Use --inline=dependencies. Inline dependency content into prompt body")
	jobAddCmd.Flags().StringVar(&planAddRecipe, "recipe", "", "Name of a recipe to add to the plan")
	jobAddCmd.Flags().StringArrayVar(&planAddRecipeVars, "recipe-vars", nil, "Variables for the recipe templates (e.g., key=value)")
	jobAddCmd.Flags().StringVar(&planAddSourceFile, "source-file", "", "Origin file path for tracking job provenance (e.g., Claude plan file)")
	jobAddCmd.Flags().StringVarP(&planAddModel, "model", "m", "", "LLM model to use for this job (e.g., gemini-3-pro-preview, claude-sonnet-4-20250514)")
	jobAddCmd.Flags().StringVar(&planAddResponder, "responder", "", "Who authors the response turns of a chat job: oracle (default; stateless LLM API call over inlined context) or agent (fresh agent session with file access per turn; never dispatched to an LLM API). Requires --type chat when set to agent")
	jobAddCmd.Flags().StringVar(&planAddRulesFile, "rules-file", "", "Path to a custom rules file for this job")
	jobAddCmd.Flags().BoolVar(&planAddGitChanges, "git-changes", false, "Include git changes (staged and unstaged) as context for this job")
	registerHandoffAddFlags(jobAddCmd.Flags())
	jobAddCmd.Flags().StringVar(&planAddSkill, "skill", "", "Skill name to inject into the agent context")
	jobAddCmd.Flags().StringSliceVar(&planAddSkillSequence, "skill-sequence", nil, "Comma-separated list of skills to execute in sequence")

	jobCmd.AddCommand(jobAddCmd)
	return jobCmd
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
The special action 'init' runs the actions defined under the 'init' key.

Plans can be referenced by slug from any directory. Prefer the persistent
--at <target> flag (plan name, container path, or <container-id>/<name>) to
operate on a plan/worktree from outside it. --dir is deprecated in favor of --at.

  # Run an action against a plan from any directory with --at
  flow action start-dev --at my-plan`,
		Args: cobra.RangeArgs(0, 2),
		RunE: runPlanAction,
	}
	actionCmd.Flags().BoolVar(&planActionList, "list", false, "List available actions for the plan")
	actionCmd.Flags().StringVarP(&planContextDir, "dir", "d", "", "[DEPRECATED] Workspace or plan directory context; use --at <target> instead")
	return actionCmd
}
