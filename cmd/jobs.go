package cmd

import (
	"time"
	
	"github.com/spf13/cobra"
)

var jobsCmd = &cobra.Command{
	Use:   "jobs",
	Short: "Manage orchestration jobs",
	Long:  `Manage orchestration jobs for complex multi-step workflows.`,
}

var jobsInitCmd = &cobra.Command{
	Use:   "init <directory>",
	Short: "Initialize a new orchestration plan",
	Long: `Initialize a new orchestration plan in the specified directory.
If a specification file is provided with --spec-file, it will be copied to the plan directory.
Otherwise, an empty spec.md file will be created.`,
	Args: cobra.ExactArgs(1),
	RunE: runJobsInit,
}

var jobsStatusCmd = &cobra.Command{
	Use:   "status [directory]",
	Short: "Show plan status",
	Long:  `Show the status of all jobs in an orchestration plan.
If no directory is specified, uses the active job if set.`,
	Args:  cobra.MaximumNArgs(1),
	RunE:  runJobsStatus,
}

var jobsRunCmd = &cobra.Command{
	Use:   "run [job-file]",
	Short: "Run jobs",
	Long: `Run jobs in an orchestration plan. 
Without arguments, runs the next available jobs.
With a job file argument, runs that specific job.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runJobsRun,
}

var jobsAddStepCmd = &cobra.Command{
	Use:   "add-step [directory]",
	Short: "Add a new job to an existing plan",
	Long: `Add a new job to an existing orchestration plan.
Can be used interactively or with command-line arguments.
If no directory is specified, uses the active job if set.

Examples:
  # Add a job with inline prompt
  grove jobs add-step myplan -t agent --title "Implementation" -d 01-plan.md -p "Implement the user authentication feature"
  
  # Add a job with prompt from file
  grove jobs add-step myplan -t agent --title "Implementation" -d 01-plan.md -f prompt.md
  
  # Add a job with prompt from stdin
  echo "Implement feature X" | grove jobs add-step myplan -t agent --title "Implementation" -d 01-plan.md
  
  # Use active job
  grove jobs set myplan
  grove jobs add-step -t agent --title "Implementation" -d 01-plan.md -p "Implement feature"`,
	Args: cobra.MaximumNArgs(1),
	RunE: runJobsAddStep,
}

var jobsGraphCmd = &cobra.Command{
	Use:   "graph [directory]",
	Short: "Visualize job dependency graph",
	Long: `Generate a visualization of the job dependency graph.
Supports multiple output formats including Mermaid, DOT, and ASCII.
If no directory is specified, uses the active job if set.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runJobsGraph,
}

var jobsCleanupWorktreesCmd = &cobra.Command{
	Use:   "cleanup-worktrees <directory>",
	Short: "Clean up old git worktrees",
	Long: `Remove git worktrees that are no longer needed.
By default, removes worktrees older than 24 hours.`,
	Args: cobra.ExactArgs(1),
	RunE: runJobsCleanupWorktrees,
}

// Command flags
var (
	jobsInitForce          bool
	jobsInitModel          string
	jobsInitCreateInitial  bool
	jobsInitOutputType     string
	jobsInitSpecFile       string
	jobsInitTemplate       string
	jobsRunDir    string
	jobsRunAll    bool
	jobsRunNext   bool
	jobsRunModel  string
	
	// Add-step flags
	jobsAddStepTemplate    string
	jobsAddStepType        string
	jobsAddStepTitle       string
	jobsAddStepDependsOn   []string
	jobsAddStepPromptFile  string
	jobsAddStepPrompt      string
	jobsAddStepOutputType  string
	jobsAddStepInteractive bool
	jobsAddStepSourceFiles []string
	
	// Graph flags
	jobsGraphFormat string
	jobsGraphServe  bool
	jobsGraphPort   int
	jobsGraphOutput string
	
	// Cleanup worktrees flags
	jobsCleanupAge   time.Duration
	jobsCleanupForce bool
)

// GetJobsCommand returns the jobs command with all subcommands configured
func GetJobsCommand() *cobra.Command {
	// Init command flags
	jobsInitCmd.Flags().BoolVarP(&jobsInitForce, "force", "f", false, "Overwrite existing directory")
	jobsInitCmd.Flags().StringVar(&jobsInitModel, "model", "", "Default model for jobs (e.g., claude-3-5-sonnet-20241022, gpt-4)")
	jobsInitCmd.Flags().BoolVar(&jobsInitCreateInitial, "create-initial-job", false, "Create an initial job file")
	jobsInitCmd.Flags().StringVar(&jobsInitOutputType, "output-type", "file", "Output type for initial job: file, commit, none, or generate_jobs")
	jobsInitCmd.Flags().StringVarP(&jobsInitSpecFile, "spec-file", "s", "", "Path to specification file (creates empty spec.md if not provided)")
	jobsInitCmd.Flags().StringVar(&jobsInitTemplate, "template", "", "Name of the job template to use for initial job")

	// Run command flags
	jobsRunCmd.Flags().StringVarP(&jobsRunDir, "dir", "d", ".", "Plan directory")
	jobsRunCmd.Flags().BoolVarP(&jobsRunAll, "all", "a", false, "Run all pending jobs")
	jobsRunCmd.Flags().BoolVarP(&jobsRunNext, "next", "n", false, "Run next available jobs")
	jobsRunCmd.Flags().IntVarP(&jobsRunParallel, "parallel", "p", 3, "Max parallel jobs")
	jobsRunCmd.Flags().BoolVarP(&jobsRunWatch, "watch", "w", false, "Watch progress in real-time")
	jobsRunCmd.Flags().BoolVarP(&jobsRunYes, "yes", "y", false, "Skip confirmation prompts")
	jobsRunCmd.Flags().StringVar(&jobsRunModel, "model", "", "Override model for jobs (e.g., claude-3-5-sonnet-20240620, gpt-4)")

	// Add-step command flags
	jobsAddStepCmd.Flags().StringVar(&jobsAddStepTemplate, "template", "", "Name of the job template to use")
	jobsAddStepCmd.Flags().StringVarP(&jobsAddStepType, "type", "t", "agent", "Job type: oneshot, agent, or shell")
	jobsAddStepCmd.Flags().StringVar(&jobsAddStepTitle, "title", "", "Job title")
	jobsAddStepCmd.Flags().StringSliceVarP(&jobsAddStepDependsOn, "depends-on", "d", nil, "Dependencies (job filenames)")
	jobsAddStepCmd.Flags().StringVarP(&jobsAddStepPromptFile, "prompt-file", "f", "", "File containing the prompt")
	jobsAddStepCmd.Flags().StringVarP(&jobsAddStepPrompt, "prompt", "p", "", "Inline prompt text (alternative to --prompt-file)")
	jobsAddStepCmd.Flags().StringVar(&jobsAddStepOutputType, "output-type", "file", "Output type: file, commit, none, or generate_jobs")
	jobsAddStepCmd.Flags().BoolVarP(&jobsAddStepInteractive, "interactive", "i", false, "Interactive mode")
	jobsAddStepCmd.Flags().StringSliceVar(&jobsAddStepSourceFiles, "source-files", nil, "Comma-separated list of source files for reference-based prompts")

	// Graph command flags
	jobsGraphCmd.Flags().StringVarP(&jobsGraphFormat, "format", "f", "mermaid", "Output format: mermaid, dot, ascii")
	jobsGraphCmd.Flags().BoolVarP(&jobsGraphServe, "serve", "s", false, "Serve interactive HTML visualization")
	jobsGraphCmd.Flags().IntVarP(&jobsGraphPort, "port", "p", 8080, "Port for web server")
	jobsGraphCmd.Flags().StringVarP(&jobsGraphOutput, "output", "o", "", "Output file (stdout if not specified)")

	// Cleanup worktrees command flags
	jobsCleanupWorktreesCmd.Flags().DurationVar(&jobsCleanupAge, "age", 24*time.Hour, "Remove worktrees older than this")
	jobsCleanupWorktreesCmd.Flags().BoolVarP(&jobsCleanupForce, "force", "f", false, "Skip confirmation prompts")

	// Run command flags
	jobsRunCmd.Flags().BoolVarP(&jobsRunAll, "all", "a", false, "Run all pending jobs")
	jobsRunCmd.Flags().BoolVar(&jobsRunNext, "next", false, "Run the next available job")
	jobsRunCmd.Flags().StringVarP(&jobsRunDir, "dir", "d", "", "Plan directory (uses active job if not specified)")
	jobsRunCmd.Flags().StringVar(&jobsRunModel, "model", "", "Override model for this run")

	// Initialize status command flags
	InitJobsStatusFlags()

	// Register templates subcommand
	jobsTemplatesCmd.AddCommand(jobsTemplatesListCmd)

	// Add subcommands
	jobsCmd.AddCommand(jobsInitCmd)
	jobsCmd.AddCommand(jobsStatusCmd)
	jobsCmd.AddCommand(jobsRunCmd)
	jobsCmd.AddCommand(jobsAddStepCmd)
	jobsCmd.AddCommand(jobsCompleteCmd)
	jobsCmd.AddCommand(jobsGraphCmd)
	jobsCmd.AddCommand(jobsCleanupWorktreesCmd)
	jobsCmd.AddCommand(jobsTemplatesCmd)
	jobsCmd.AddCommand(NewJobsSetCmd())
	jobsCmd.AddCommand(NewJobsCurrentCmd())
	jobsCmd.AddCommand(NewJobsUnsetCmd())
	jobsCmd.AddCommand(NewJobsExtractCmd())

	// Return the configured jobs command
	return jobsCmd
}

func runJobsInit(cmd *cobra.Command, args []string) error {
	initCmd := &JobsInitCmd{
		Spec:          jobsInitSpecFile,
		Dir:           args[0],
		Force:         jobsInitForce,
		Model:         jobsInitModel,
		CreateInitial: jobsInitCreateInitial,
		OutputType:    jobsInitOutputType,
		Template:      jobsInitTemplate,
	}
	return RunJobsInit(initCmd)
}

func runJobsAddStep(cmd *cobra.Command, args []string) error {
	var dir string
	if len(args) > 0 {
		dir = args[0]
	}
	addStepCmd := &JobsAddStepCmd{
		Dir:         dir,
		Template:    jobsAddStepTemplate,
		Type:        jobsAddStepType,
		Title:       jobsAddStepTitle,
		DependsOn:   jobsAddStepDependsOn,
		PromptFile:  jobsAddStepPromptFile,
		Prompt:      jobsAddStepPrompt,
		OutputType:  jobsAddStepOutputType,
		Interactive: jobsAddStepInteractive,
		SourceFiles: jobsAddStepSourceFiles,
	}
	return RunJobsAddStep(addStepCmd)
}

func runJobsGraph(cmd *cobra.Command, args []string) error {
	var dir string
	if len(args) > 0 {
		dir = args[0]
	}
	graphCmd := &JobsGraphCmd{
		Dir:    dir,
		Format: jobsGraphFormat,
		Serve:  jobsGraphServe,
		Port:   jobsGraphPort,
		Output: jobsGraphOutput,
	}
	return RunJobsGraph(graphCmd)
}

func runJobsCleanupWorktrees(cmd *cobra.Command, args []string) error {
	cleanupCmd := &JobsCleanupWorktreesCmd{
		Dir:   args[0],
		Age:   jobsCleanupAge,
		Force: jobsCleanupForce,
	}
	return RunJobsCleanupWorktrees(cleanupCmd)
}

// JobsInitCmd holds the parameters for the init command.
type JobsInitCmd struct {
	Spec          string
	Dir           string
	Force         bool
	Model         string
	CreateInitial bool
	OutputType    string
	Template      string
}