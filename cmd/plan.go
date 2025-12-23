package cmd

import (
	"fmt"
	"os"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Manage multi-step orchestration plans",
	Long:  `Manage multi-step orchestration plans in dedicated directories.`,
}

var planInitCmd = &cobra.Command{
	Use:   "init [directory]",
	Short: "Initialize a new plan directory, interactively or via flags",
	Long: `Initialize a new orchestration plan in the specified directory.
Creates a .grove-plan.yml file with default configuration options.
If no directory is provided, an interactive TUI will be launched.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanInit,
}

var planStatusCmd = &cobra.Command{
	Use:   "status [directory]",
	Short: "Show plan status in an interactive TUI (use: flow status)",
	Long: `Show the status of all jobs in an orchestration plan in an interactive TUI.
If no directory is specified, uses the active job if set.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanStatus,
}

var planRunCmd = &cobra.Command{
	Use:   "run [job-file...]",
	Short: "Run jobs (use: flow run)",
	Long: `Run jobs in an orchestration plan.
Without arguments, runs the next available jobs.
With a single job file argument, runs that specific job.
With multiple job file arguments, runs those jobs in parallel.`,
	RunE: runPlanRun,
}

var planAddCmd = &cobra.Command{
	Use:   "add [directory]",
	Short: "Add a new job to an existing plan (use: flow add)",
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
	Short: "Visualize job dependency graph (use: flow graph)",
	Long: `Generate a visualization of the job dependency graph.
Supports multiple output formats including Mermaid, DOT, and ASCII.
If no directory is specified, uses the active job if set.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanGraph,
}

var planStepCmd = &cobra.Command{
	Use:   "step [directory]",
	Short: "Step through plan execution interactively (use: flow step)",
	Long: `Provides an interactive wizard for executing a plan step by step.
Shows runnable jobs and allows you to run, launch, skip, or quit.
If no directory is specified, uses the current directory.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanStep,
}

var planOpenCmd = &cobra.Command{
	Use:   "open [directory]",
	Short: "Open a plan's worktree in a dedicated tmux session (use: flow open)",
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
	planInitContainer       string
	planInitExtractAllFrom  string
	planInitOpenSession     bool
	planInitRecipe          string
	planInitTUI             bool
	planInitRecipeVars     []string
	planInitRecipeCmd      string
	planInitRepos          []string
	planInitNoteRef        string
	planInitFromNote       string
	planInitNoteTargetFile string
	planInitRunInit        bool
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
	planAddInteractive       bool
	planAddSourceFiles       []string
	planAddWorktree          string
	planAddPrependDependencies bool
	planAddRecipe          string
	planAddRecipeVars      []string

	// Graph flags
	planGraphFormat string
	planGraphServe  bool
	planGraphPort   int
	planGraphOutput string
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
	planInitCmd.Flags().StringVar(&planInitRecipe, "recipe", "", "Name of a plan recipe to initialize from (e.g., standard-feature). When using --recipe-cmd, this can be omitted if the command provides only one recipe")
	planInitCmd.Flags().StringArrayVar(&planInitRecipeVars, "recipe-vars", nil, "Variables to pass to recipe templates. Can be used multiple times or comma-delimited (e.g., --recipe-vars model=gpt-4 --recipe-vars rules_file=docs.rules OR --recipe-vars \"model=gpt-4,rules_file=docs.rules,output_dir=docs\")")
	planInitCmd.Flags().StringVar(&planInitRecipeCmd, "recipe-cmd", "", "Command that outputs JSON recipe definitions (overrides grove.yml's get_recipe_cmd)")
	planInitCmd.Flags().StringSliceVar(&planInitRepos, "repos", nil, "Specific repos to include in ecosystem worktree (e.g., --repos grove-core,grove-flow). If not specified, all submodules are included")
	planInitCmd.Flags().BoolVarP(&planInitTUI, "tui", "t", false, "Launch interactive TUI to create a new plan")
	planInitCmd.Flags().StringVar(&planInitNoteRef, "note-ref", "", "Path to the source note to link to this plan")
	planInitCmd.Flags().StringVar(&planInitFromNote, "from-note", "", "Path to a note file whose body will be used as the prompt for the first job")
	planInitCmd.Flags().StringVar(&planInitNoteTargetFile, "note-target-file", "", "Filename of the job within the recipe to apply the --from-note content and reference to")
	planInitCmd.Flags().BoolVar(&planInitRunInit, "init", false, "Execute init actions from the recipe's workspace_init.yml")

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
	planAddCmd.Flags().BoolVarP(&planAddInteractive, "interactive", "i", false, "Interactive mode")
	planAddCmd.Flags().StringSliceVar(&planAddSourceFiles, "source-files", nil, "Comma-separated list of source files for reference-based prompts")
	planAddCmd.Flags().StringVar(&planAddWorktree, "worktree", "", "Explicitly set the worktree name (overrides automatic inference)")
	planAddCmd.Flags().BoolVar(&planAddPrependDependencies, "prepend-dependencies", false, "Inline dependency content into prompt body instead of uploading as separate files")
	planAddCmd.Flags().StringVar(&planAddRecipe, "recipe", "", "Name of a recipe to add to the plan")
	planAddCmd.Flags().StringArrayVar(&planAddRecipeVars, "recipe-vars", nil, "Variables for the recipe templates (e.g., key=value)")

	// Graph command flags
	planGraphCmd.Flags().StringVarP(&planGraphFormat, "format", "f", "mermaid", "Output format: mermaid, dot, ascii")
	planGraphCmd.Flags().BoolVarP(&planGraphServe, "serve", "s", false, "Serve interactive HTML visualization")
	planGraphCmd.Flags().IntVarP(&planGraphPort, "port", "p", 8080, "Port for web server")
	planGraphCmd.Flags().StringVarP(&planGraphOutput, "output", "o", "", "Output file (stdout if not specified)")

	// Initialize status command flags
	InitPlanStatusFlags()

	// Register templates subcommand
	planTemplatesListCmd.Flags().String("domain", "", "Filter templates by domain (e.g., generic, grove)")
	planTemplatesCmd.AddCommand(planTemplatesListCmd)
	planTemplatesPrintCmd.Flags().BoolVar(&planTemplatesPrintWithFrontmatter, "frontmatter", false, "Include YAML frontmatter in output")
	planTemplatesCmd.AddCommand(planTemplatesPrintCmd)

	// Add subcommands
	planCmd.AddCommand(planInitCmd)
	planCmd.AddCommand(planActionCmd)
	planCmd.AddCommand(planStatusCmd)
	planCmd.AddCommand(planTUICmd)
	planCmd.AddCommand(newPlanListCmd())
	planCmd.AddCommand(planRunCmd)
	planCmd.AddCommand(planReviewCmd)
	planCmd.AddCommand(planAddCmd)
	planCmd.AddCommand(planCompleteCmd)
	planCmd.AddCommand(planGraphCmd)
	planCmd.AddCommand(planStepCmd)
	planCmd.AddCommand(planOpenCmd)
	planCmd.AddCommand(planTemplatesCmd)
	planCmd.AddCommand(planRecipesCmd)
	planCmd.AddCommand(NewPlanSetCmd())
	planCmd.AddCommand(NewPlanCurrentCmd())
	planCmd.AddCommand(NewPlanUnsetCmd())
	planCmd.AddCommand(NewPlanExtractCmd())
	planCmd.AddCommand(NewPlanConfigCmd())
	planCmd.AddCommand(NewPlanFinishCmd())
	planCmd.AddCommand(NewPlanJobsCmd())
	planCmd.AddCommand(NewPlanContextCmd())
	planCmd.AddCommand(NewPlanHoldCmd())
	planCmd.AddCommand(NewPlanUnholdCmd())
	planCmd.AddCommand(NewPlanResumeCmd())

	// Return the configured jobs command
	return planCmd
}

func runPlanInit(cmd *cobra.Command, args []string) error {
	var dir string
	if len(args) > 0 {
		dir = args[0]
	}

	// This is the command object built from CLI flags.
	// It will be used for both direct CLI execution and to pre-populate the TUI.
	cliCmd := &PlanInitCmd{
		Dir:            dir,
		Force:          planInitForce,
		Model:          planInitModel,
		Worktree:       planInitWorktree,
		Container:       planInitContainer,
		ExtractAllFrom:  planInitExtractAllFrom,
		OpenSession:     planInitOpenSession,
		Recipe:          planInitRecipe,
		RecipeVars:      planInitRecipeVars,
		RecipeCmd:      planInitRecipeCmd,
		Repos:          planInitRepos,
		NoteRef:        planInitNoteRef,
		FromNote:       planInitFromNote,
		NoteTargetFile: planInitNoteTargetFile,
		RunInit:        planInitRunInit,
	}

	// Launch TUI if no directory is provided and we are in a TTY, or if --tui is explicitly set.
	isTTY := isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
	if (dir == "" && isTTY) || planInitTUI {
		// This logic is now in cmd/plan_init.go
		return RunPlanInitTUI(dir, cliCmd)
	}

	// Non-interactive path
	if dir == "" {
		return cmd.Help() // Show help if no directory is given and not in TUI mode
	}

	result, err := executePlanInit(cliCmd)
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
	var dir string
	if len(args) > 0 {
		dir = args[0]
	}
	addStepCmd := &PlanAddStepCmd{
		Dir:                  dir,
		Template:             planAddTemplate,
		Type:                 planAddType,
		Title:                planAddTitle,
		DependsOn:            planAddDependsOn,
		PromptFile:           planAddPromptFile,
		Prompt:               planAddPrompt,
		Interactive:          planAddInteractive,
		SourceFiles:          planAddSourceFiles,
		Worktree:             planAddWorktree,
		PrependDependencies:  planAddPrependDependencies,
		Recipe:               planAddRecipe,
		RecipeVars:           planAddRecipeVars,
	}
	return RunPlanAddStep(addStepCmd)
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

// PlanInitCmd holds the parameters for the init command.
type PlanInitCmd struct {
	Dir            string
	Force          bool
	Model          string
	Worktree       string
	Container       string
	ExtractAllFrom  string
	OpenSession     bool
	Recipe          string
	RecipeVars      []string
	RecipeCmd      string
	Repos          []string // List of repos to include in ecosystem worktree
	NoteRef        string
	FromNote       string
	NoteTargetFile string
	RunInit        bool     // Run init actions from workspace_init.yml
}
