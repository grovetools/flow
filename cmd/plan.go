package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Manage multi-step orchestration plans",
	Long:  `Manage multi-step orchestration plans in dedicated directories.`,
}

var planInitCmd = &cobra.Command{
	Use:   "init <directory>",
	Short: "Initialize a new plan directory, interactively or via flags",
	Long: `Initialize a new orchestration plan in the specified directory.
Creates a .grove-plan.yml file with default configuration options.
If no directory is provided, an interactive TUI will be launched.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanInit,
}

var planStatusCmd = &cobra.Command{
	Use:   "status [directory]",
	Short: "Show plan status",
	Long: `Show the status of all jobs in an orchestration plan.
If no directory is specified, uses the active job if set.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanStatus,
}

var planRunCmd = &cobra.Command{
	Use:   "run [job-file]",
	Short: "Run jobs",
	Long: `Run jobs in an orchestration plan. 
Without arguments, runs the next available jobs.
With a job file argument, runs that specific job.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanRun,
}

var planAddCmd = &cobra.Command{
	Use:   "add [directory]",
	Short: "Add a new job to an existing plan",
	Long: `Add a new job to an existing orchestration plan.
Can be used interactively or with command-line arguments.
If no directory is specified, uses the active job if set.

Examples:
  # Add a job with inline prompt
  flow plan add myplan -t agent --title "Implementation" -d 01-plan.md -p "Implement the user authentication feature"
  
  # Add a job with prompt from file
  flow plan add myplan -t agent --title "Implementation" -d 01-plan.md -f prompt.md
  
  # Add a job with prompt from stdin
  echo "Implement feature X" | flow plan add myplan -t agent --title "Implementation" -d 01-plan.md
  
  # Use active job
  flow plan set myplan
  flow plan add -t agent --title "Implementation" -d 01-plan.md -p "Implement feature"`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanAdd,
}

var planGraphCmd = &cobra.Command{
	Use:   "graph [directory]",
	Short: "Visualize job dependency graph",
	Long: `Generate a visualization of the job dependency graph.
Supports multiple output formats including Mermaid, DOT, and ASCII.
If no directory is specified, uses the active job if set.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanGraph,
}

var planCleanupWorktreesCmd = &cobra.Command{
	Use:   "cleanup-worktrees <directory>",
	Short: "Clean up old git worktrees",
	Long: `Remove git worktrees that are no longer needed.
By default, removes worktrees older than 24 hours.`,
	Args: cobra.ExactArgs(1),
	RunE: runPlanCleanupWorktrees,
}

var planLaunchCmd = &cobra.Command{
	Use:   "launch <job-file>",
	Short: "Launch an interactive agent session for a job",
	Long: `Launches a job in a new detached tmux session, pre-filling the agent prompt.
This is useful for starting long-running or interactive agent tasks that you can check on later.`,
	Args: cobra.ExactArgs(1),
	RunE: runPlanLaunch,
}

var planStepCmd = &cobra.Command{
	Use:   "step [directory]",
	Short: "Step through plan execution interactively",
	Long: `Provides an interactive wizard for executing a plan step by step.
Shows runnable jobs and allows you to run, launch, skip, or quit.
If no directory is specified, uses the current directory.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanStep,
}

var planOpenCmd = &cobra.Command{
	Use:   "open [directory]",
	Short: "Open a plan's worktree in a dedicated tmux session",
	Long: `Switches to or creates a tmux session for the plan's worktree and opens the interactive status TUI.
This provides a one-command entry point into a plan's interactive environment.
If no directory is specified, uses the active job if set.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanOpen,
}

// Command flags
var (
	planInitForce          bool
	planInitModel          string
	planInitWorktree       string
	planInitContainer      string
	planInitExtractAllFrom string
	planInitOpenSession    bool
	planInitRecipe         string
	planInitTUI            bool
	planInitRecipeVars     []string
	planRunDir             string
	planRunAll             bool
	planRunNext            bool
	planRunModel           string

	// Add flags
	planAddTemplate      string
	planAddType          string
	planAddTitle         string
	planAddDependsOn     []string
	planAddPromptFile    string
	planAddPrompt        string
	planAddOutputType    string
	planAddInteractive   bool
	planAddSourceFiles   []string
	planAddWorktree      string
	planAddAgentContinue bool

	// Graph flags
	planGraphFormat string
	planGraphServe  bool
	planGraphPort   int
	planGraphOutput string

	// Cleanup worktrees flags
	planCleanupAge   time.Duration
	planCleanupForce bool

	// Launch flags
	planLaunchHost bool
)

// NewPlanCmd returns the plan command with all subcommands configured.
func NewPlanCmd() *cobra.Command {
	// Init command flags
	planInitCmd.Flags().BoolVarP(&planInitForce, "force", "f", false, "Overwrite existing directory")
	planInitCmd.Flags().StringVar(&planInitModel, "model", "", "Default model for jobs (e.g., claude-3-5-sonnet-20241022, gpt-4)")
	planInitCmd.Flags().StringVar(&planInitWorktree, "worktree", "", "Set default worktree (uses plan name if no value provided)")
	planInitCmd.Flags().Lookup("worktree").NoOptDefVal = "__AUTO__" // Special marker for auto-naming
	planInitCmd.Flags().StringVar(&planInitContainer, "target-agent-container", "", "Default container for agent jobs in the plan")
	planInitCmd.Flags().StringVar(&planInitExtractAllFrom, "extract-all-from", "", "Path to a markdown file to extract all content from into an initial job")
	planInitCmd.Flags().BoolVar(&planInitOpenSession, "open-session", false, "Immediately open a tmux session for the plan (uses worktree if configured, otherwise main repo)")
	planInitCmd.Flags().StringVar(&planInitRecipe, "recipe", "chat-workflow", "Name of a plan recipe to initialize from (e.g., standard-feature)")
	planInitCmd.Flags().StringArrayVar(&planInitRecipeVars, "recipe-vars", nil, "Variables to pass to recipe templates. Can be used multiple times or comma-delimited (e.g., --recipe-vars model=gpt-4 --recipe-vars rules_file=docs.rules OR --recipe-vars \"model=gpt-4,rules_file=docs.rules,output_dir=docs\")")
	planInitCmd.Flags().BoolVarP(&planInitTUI, "tui", "t", false, "Launch interactive TUI to create a new plan")

	// Run command flags
	planRunCmd.Flags().StringVarP(&planRunDir, "dir", "d", ".", "Plan directory")
	planRunCmd.Flags().BoolVarP(&planRunAll, "all", "a", false, "Run all pending jobs")
	planRunCmd.Flags().BoolVarP(&planRunNext, "next", "n", false, "Run next available jobs")
	planRunCmd.Flags().IntVarP(&planRunParallel, "parallel", "p", 3, "Max parallel jobs")
	planRunCmd.Flags().BoolVarP(&planRunWatch, "watch", "w", false, "Watch progress in real-time")
	planRunCmd.Flags().BoolVarP(&planRunYes, "yes", "y", false, "Skip confirmation prompts")
	planRunCmd.Flags().StringVar(&planRunModel, "model", "", "Override model for jobs (e.g., claude-3-5-sonnet-20240620, gpt-4)")
	planRunCmd.Flags().BoolVar(&planRunSkipInteractive, "skip-interactive", false, "Skip interactive agent jobs (useful for CI/automation)")

	// Add-step command flags
	planAddCmd.Flags().StringVar(&planAddTemplate, "template", "", "Name of the job template to use")
	planAddCmd.Flags().StringVarP(&planAddType, "type", "t", "agent", "Job type: oneshot, chat, shell, headless_agent, or interactive_agent (agent is an alias for interactive_agent)")
	planAddCmd.Flags().StringVar(&planAddTitle, "title", "", "Job title")
	planAddCmd.Flags().StringSliceVarP(&planAddDependsOn, "depends-on", "d", nil, "Dependencies (job filenames)")
	planAddCmd.Flags().StringVarP(&planAddPromptFile, "prompt-file", "f", "", "File containing the prompt")
	planAddCmd.Flags().StringVarP(&planAddPrompt, "prompt", "p", "", "Inline prompt text (alternative to --prompt-file)")
	planAddCmd.Flags().StringVar(&planAddOutputType, "output-type", "file", "Output type: file, commit, none, or generate_jobs")
	planAddCmd.Flags().BoolVarP(&planAddInteractive, "interactive", "i", false, "Interactive mode")
	planAddCmd.Flags().StringSliceVar(&planAddSourceFiles, "source-files", nil, "Comma-separated list of source files for reference-based prompts")
	planAddCmd.Flags().StringVar(&planAddWorktree, "worktree", "", "Explicitly set the worktree name (overrides automatic inference)")
	planAddCmd.Flags().BoolVar(&planAddAgentContinue, "agent-continue", false, "Continue the last agent session (adds --continue flag)")

	// Graph command flags
	planGraphCmd.Flags().StringVarP(&planGraphFormat, "format", "f", "mermaid", "Output format: mermaid, dot, ascii")
	planGraphCmd.Flags().BoolVarP(&planGraphServe, "serve", "s", false, "Serve interactive HTML visualization")
	planGraphCmd.Flags().IntVarP(&planGraphPort, "port", "p", 8080, "Port for web server")
	planGraphCmd.Flags().StringVarP(&planGraphOutput, "output", "o", "", "Output file (stdout if not specified)")

	// Cleanup worktrees command flags
	planCleanupWorktreesCmd.Flags().DurationVar(&planCleanupAge, "age", 24*time.Hour, "Remove worktrees older than this")
	planCleanupWorktreesCmd.Flags().BoolVarP(&planCleanupForce, "force", "f", false, "Skip confirmation prompts")

	// Launch command flags
	planLaunchCmd.Flags().BoolVar(&planLaunchHost, "host", false, "Launch agent on the host in the main git repo, not in a container worktree")

	// Initialize status command flags
	InitPlanStatusFlags()

	// Register templates subcommand
	planTemplatesCmd.AddCommand(planTemplatesListCmd)

	// Add subcommands
	planCmd.AddCommand(planInitCmd)
	planCmd.AddCommand(planStatusCmd)
	planCmd.AddCommand(planTUICmd)
	planCmd.AddCommand(newPlanListCmd())
	planCmd.AddCommand(planRunCmd)
	planCmd.AddCommand(planAddCmd)
	planCmd.AddCommand(planCompleteCmd)
	planCmd.AddCommand(planGraphCmd)
	planCmd.AddCommand(planCleanupWorktreesCmd)
	planCmd.AddCommand(planLaunchCmd)
	planCmd.AddCommand(planStepCmd)
	planCmd.AddCommand(planOpenCmd)
	planCmd.AddCommand(planTemplatesCmd)
	planCmd.AddCommand(planWorktreeCmd)
	planCmd.AddCommand(planRecipesCmd)
	planCmd.AddCommand(NewPlanSetCmd())
	planCmd.AddCommand(NewPlanCurrentCmd())
	planCmd.AddCommand(NewPlanUnsetCmd())
	planCmd.AddCommand(NewPlanExtractCmd())
	planCmd.AddCommand(NewPlanConfigCmd())
	planCmd.AddCommand(NewPlanFinishCmd())
	planCmd.AddCommand(NewPlanJobsCmd())

	// Return the configured jobs command
	return planCmd
}

func runPlanInit(cmd *cobra.Command, args []string) error {
	var dir string
	if len(args) > 0 {
		dir = args[0]
	}

	// Launch TUI if no directory is provided and we are in a TTY, or if --tui is explicitly set.
	isTTY := isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
	if (dir == "" && isTTY) || planInitTUI {
		// This logic is now in cmd/plan_init.go
		return RunPlanInitTUI(dir)
	}

	// Non-interactive path
	if dir == "" {
		return cmd.Help() // Show help if no directory is given and not in TUI mode
	}

	// This is the direct CLI execution path
	directCmd := &PlanInitCmd{
		Dir:            dir,
		Force:          planInitForce,
		Model:          planInitModel,
		Worktree:       planInitWorktree,
		Container:      planInitContainer,
		ExtractAllFrom: planInitExtractAllFrom,
		OpenSession:    planInitOpenSession,
		Recipe:         planInitRecipe,
		RecipeVars:     planInitRecipeVars,
	}
	result, err := executePlanInit(directCmd)
	if err != nil {
		return err
	}
	fmt.Print(result)
	return nil
}

func runPlanStatus(cmd *cobra.Command, args []string) error {
	return RunPlanStatus(cmd, args)
}

func runPlanAdd(cmd *cobra.Command, args []string) error {
	return runPlanAddStep(cmd, args)
}

func runPlanGraph(cmd *cobra.Command, args []string) error {
	graphCmd := &PlanGraphCmd{
		Format: planGraphFormat,
		Serve:  planGraphServe,
		Port:   planGraphPort,
		Output: planGraphOutput,
	}
	if len(args) > 0 {
		graphCmd.Directory = args[0]
	}
	return RunPlanGraph(graphCmd)
}

func runPlanCleanupWorktrees(cmd *cobra.Command, args []string) error {
	cleanupCmd := &PlanCleanupWorktreesCmd{
		Directory: args[0],
		Age:       planCleanupAge,
		Force:     planCleanupForce,
	}
	return RunPlanCleanupWorktrees(cleanupCmd)
}

func runPlanLaunch(cmd *cobra.Command, args []string) error {
	return RunPlanLaunch(cmd, args[0])
}

func runPlanAddStep(cmd *cobra.Command, args []string) error {
	var dir string
	if len(args) > 0 {
		dir = args[0]
	}
	addStepCmd := &PlanAddStepCmd{
		Dir:           dir,
		Template:      planAddTemplate,
		Type:          planAddType,
		Title:         planAddTitle,
		DependsOn:     planAddDependsOn,
		PromptFile:    planAddPromptFile,
		Prompt:        planAddPrompt,
		OutputType:    planAddOutputType,
		Interactive:   planAddInteractive,
		SourceFiles:   planAddSourceFiles,
		Worktree:      planAddWorktree,
		AgentContinue: planAddAgentContinue,
	}
	return RunPlanAddStep(addStepCmd)
}

// PlanInitCmd holds the parameters for the init command.
type PlanInitCmd struct {
	Dir            string
	Force          bool
	Model          string
	Worktree       string
	Container      string
	ExtractAllFrom string
	OpenSession    bool
	Recipe         string
	RecipeVars     []string
}
