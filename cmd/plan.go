package cmd

import (
	"context"
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
If no directory is provided, an interactive TUI will be launched.

Note: if you run this from inside a git worktree, a warning will be shown.
Plans should typically be initialized from the main repository directory.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanInit,
}

var planStatusCmd = &cobra.Command{
	Use:   "status [directory]",
	Short: "Show plan status in an interactive TUI (use: flow status)",
	Long: `Show the status of all jobs in an orchestration plan in an interactive TUI.
If no directory is specified, uses the active job if set.

Plans can be referenced by slug from any directory. If the slug is globally
unique, it will be resolved automatically. Use --dir to disambiguate or to
specify the workspace context explicitly.

Examples:
  flow plan status my-feature                    # from any directory (global lookup)
  flow plan status my-feature --dir ~/Code/myapp # explicit workspace
  flow plan status my-feature --json             # JSON output`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanStatus,
}

var planRunCmd = &cobra.Command{
	Use:   "run [job-file...]",
	Short: "Run jobs (use: flow run)",
	Long: `Run jobs in an orchestration plan.
Without arguments, runs the next available jobs.
With a single job file argument, runs that specific job.
With multiple job file arguments, runs those jobs in parallel.

Satellite dispatch: '--at satellite:<name>' ships the jobs to a grove
satellite VM instead of running locally. A plan whose .grove-plan.yml carries
'satellite: <name>' ('flow plan init --satellite <name>') dispatches there BY
DEFAULT when no --at is given; any explicit --at wins, and the reserved
'--at satellite:local' forces a local run. Ship the plan worktree first with
'grove satellite worktree push <name> --plan <plan>', and fetch agent commits
back with 'grove satellite worktree pull <name> --plan <plan> [--ff]'.`,
	RunE: runPlanRun,
}

var planAddCmd = &cobra.Command{
	Use:   "add [directory]",
	Short: "Add a new job to an existing plan (use: flow add)",
	Long:  addCmdLongDesc + addCmdExamples("flow plan add"),
	Args:  cobra.MaximumNArgs(1),
	RunE:  runPlanAdd,
}

var planGraphCmd = &cobra.Command{
	Use:   "graph [directory]",
	Short: "Visualize job dependency graph (use: flow graph)",
	Long: `Generate a visualization of the job dependency graph.
Supports multiple output formats including Mermaid, DOT, and ASCII.
If no directory is specified, uses the active job if set.

Plans can be referenced by slug from any directory. Use --dir to specify the workspace context.

Examples:
  flow plan graph my-feature                     # from any directory
  flow plan graph my-feature --dir ~/Code/myapp  # explicit workspace
  flow plan graph my-feature -f dot -o graph.dot # DOT output to file`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanGraph,
}

var planStepCmd = &cobra.Command{
	Use:   "step [directory]",
	Short: "Step through plan execution interactively (use: flow step)",
	Long: `Provides an interactive wizard for executing a plan step by step.
Shows runnable jobs and allows you to run, launch, skip, or quit.
If no directory is specified, uses the current directory.

Plans can be referenced by slug from any directory. Use --dir to specify the workspace context.

Examples:
  flow plan step my-feature                     # from any directory
  flow plan step my-feature --dir ~/Code/myapp  # explicit workspace`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanStep,
}

var planOpenCmd = &cobra.Command{
	Use:   "open [directory]",
	Short: "Open a plan's worktree in a dedicated tmux session (use: flow open)",
	Long: `Switches to or creates a tmux session for the plan's worktree and opens the interactive status TUI.
This provides a one-command entry point into a plan's interactive environment.
If no directory is specified, uses the active job if set.

Plans can be referenced by slug from any directory. Use --dir to specify the workspace context.

Examples:
  flow plan open my-feature                     # from any directory
  flow plan open my-feature --dir ~/Code/myapp  # explicit workspace`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlanOpen,
}

// Command flags
var (
	planInitForce             bool
	planInitModel             string
	planInitWorktree          string
	planInitExtractAllFrom    string
	planInitOpenSession       bool
	planInitEnvProfile        string
	planInitRecipe            string
	planInitTUI               bool
	planInitRecipeVars        []string
	planInitRecipeCmd         string
	planInitSiblingWorkspaces []string
	planInitNoteRef           string
	planInitFromNotes         []string
	planInitNoteTargetFile    string
	planInitRunInit           bool
	planInitPlaybook          string
	planInitLayout            string
	planInitAnchor            string
	planInitSatellite         string
	planRunDir                string
	planRunAll                bool
	planRunNext               bool
	planRunModel              string
	planRunLocal              bool // Force local execution (skip daemon)
	planRunBackground         bool // Submit to daemon and exit without waiting
	planRunAppendDelta        bool // Stamp append_delta into the targeted chat turn (spec 19 D4)
	planRunRebaseContext      bool // Stamp rebase_context into the targeted chat turn (spec 19 D4)
	planRunForce              bool // Override an advisory .grove-lease.yml on the plan dir (P9 C14)

	// Add flags
	planAddTemplate            string
	planAddType                string
	planAddTitle               string
	planAddDependsOn           []string
	planAddParentJobID         string
	planAddPromptFile          string
	planAddPrompt              string
	planAddInteractive         bool
	planAddIncludeFiles        []string
	planAddWorktree            string
	planAddInline              []string
	planAddPrependDependencies bool
	planAddRecipe              string
	planAddRecipeVars          []string
	planAddSourceFile          string
	planAddModel               string
	planAddResponder           string
	planAddEffort              string
	planAddProvider            string
	planAddRulesFile           string
	planAddNoContext           bool
	planAddGitChanges          bool
	planAddSkill               string
	planAddSkillSequence       []string
	planAddCoordMode           string
	planAddHandoffFrom         string
	planAddHandoffDepth        int
	planAddHandoffMax          int
	planAddHandoffThreshold    int
	planAddJSON                bool
	planAddRun                 bool

	// Graph flags
	planGraphFormat string
	planGraphServe  bool
	planGraphPort   int
	planGraphOutput string

	// Shared --dir flag for subcommands that support workspace context override
	planContextDir string
)

// NewPlanCmd returns the plan command with all subcommands configured.
func NewPlanCmd() *cobra.Command {
	// Init command flags
	planInitCmd.Flags().BoolVarP(&planInitForce, "force", "f", false, "Overwrite existing directory")
	planInitCmd.Flags().StringVar(&planInitModel, "model", "", "Default model for jobs (e.g., claude-sonnet-4-6, gemini-2.5-pro)")
	planInitCmd.Flags().StringVar(&planInitWorktree, "worktree", "", "Set default worktree (uses plan name if no value provided)")
	planInitCmd.Flags().Lookup("worktree").NoOptDefVal = "__AUTO__" // Special marker for auto-naming
	planInitCmd.Flags().StringVar(&planInitExtractAllFrom, "extract-all-from", "", "Path to a markdown file to extract all content from into an initial job")
	planInitCmd.Flags().BoolVar(&planInitOpenSession, "open-session", false, "Immediately open a tmux session for the plan (uses worktree if configured, otherwise main repo)")
	planInitCmd.Flags().StringVar(&planInitEnvProfile, "env", "", "Named environment profile to use (e.g., docker, cloud)")
	planInitCmd.Flags().StringVar(&planInitRecipe, "recipe", "", "Name of a plan recipe to initialize from (e.g., standard-feature). When using --recipe-cmd, this can be omitted if the command provides only one recipe")
	planInitCmd.Flags().StringArrayVar(&planInitRecipeVars, "recipe-vars", nil, "Variables to pass to recipe templates. Can be used multiple times or comma-delimited (e.g., --recipe-vars model=gemini-2.5-pro --recipe-vars rules_file=docs.rules OR --recipe-vars \"model=gemini-2.5-pro,rules_file=docs.rules,output_dir=docs\")")
	planInitCmd.Flags().StringVar(&planInitRecipeCmd, "recipe-cmd", "", "Command that outputs JSON recipe definitions (overrides grove.yml's get_recipe_cmd)")
	planInitCmd.Flags().StringSliceVar(&planInitSiblingWorkspaces, "sibling-workspaces", nil, "Sibling workspaces to link into the ecosystem worktree. Because the bare form takes no value, the comma-list form REQUIRES = (e.g., --sibling-workspaces=grove-core,grove-flow; the space form is mis-parsed as a positional arg). Worktrees with sibling workspaces are created under the XDG data dir. A bare --sibling-workspaces (no value) links ALL direct-child git repos of the ecosystem root. If not specified, all submodules are included")
	planInitCmd.Flags().Lookup("sibling-workspaces").NoOptDefVal = "__ALL__" // Bare flag => link all discovered direct-child repos
	planInitCmd.Flags().BoolVarP(&planInitTUI, "tui", "t", false, "Launch interactive TUI to create a new plan")
	planInitCmd.Flags().StringVar(&planInitNoteRef, "note-ref", "", "Path to the source note to link to this plan")
	planInitCmd.Flags().StringArrayVar(&planInitFromNotes, "from-note", nil, "Path to a note file whose body will be used as the prompt for the first job. Repeatable: each additional note becomes its own job in the plan (a roster)")
	planInitCmd.Flags().StringVar(&planInitNoteTargetFile, "note-target-file", "", "Filename of the job within the recipe to apply the --from-note content and reference to")
	planInitCmd.Flags().BoolVar(&planInitRunInit, "init", false, "Execute init actions from the recipe's workspace_init.yml")
	planInitCmd.Flags().StringVar(&planInitPlaybook, "playbook", "", "Name of a playbook whose skills, prompts, and recipes scope this plan (e.g., gdv2). Written to .grove-plan.yml; jobs in the plan inherit $PLAYBOOK_ROOT at execution time.")
	planInitCmd.Flags().StringVar(&planInitLayout, "layout", "", "Worktree layout: 'xdg' (XDG data dir, default for ecosystems) or 'legacy' (in-repo .grove-worktrees/). Overrides GROVE_WORKTREE_LAYOUT and grove.toml [worktree] layout.")
	planInitCmd.Flags().StringVar(&planInitAnchor, "anchor", "", "Repo to anchor the worktree to (driving repo). The worktree will be placed under this repo's XDG base directory. Auto-inferred when run from inside a sub-project.")
	planInitCmd.Flags().StringVar(&planInitSatellite, "satellite", "", "Designate a grove satellite for this plan's remote work (written to .grove-plan.yml). Workflow: init --satellite <name>, then 'grove satellite worktree push <name> --plan <plan>' to ship the plan worktree to the VM; 'flow plan run' then auto-dispatches to the satellite (force a local run with --at satellite:local); fetch agent commits back with 'grove satellite worktree pull <name> --plan <plan> [--ff]'.")

	// Run command flags
	planRunCmd.Flags().StringVarP(&planRunDir, "dir", "d", ".", "Plan directory")
	planRunCmd.Flags().BoolVarP(&planRunAll, "all", "a", false, "Run all pending jobs")
	planRunCmd.Flags().BoolVarP(&planRunNext, "next", "n", false, "Run next available jobs")
	planRunCmd.Flags().IntVarP(&planRunParallel, "parallel", "p", 3, "Max parallel jobs")
	planRunCmd.Flags().BoolVarP(&planRunWatch, "watch", "w", false, "Watch progress in real-time")
	planRunCmd.Flags().BoolVarP(&planRunYes, "yes", "y", false, "Skip confirmation prompts")
	planRunCmd.Flags().StringVar(&planRunModel, "model", "", "Override model for jobs (e.g., claude-sonnet-4-6, gemini-2.5-pro)")
	planRunCmd.Flags().BoolVar(&planRunSkipInteractive, "skip-interactive", false, "Skip interactive agent jobs (useful for CI/automation)")
	planRunCmd.Flags().BoolVar(&planRunLocal, "local", false, "Force local execution (bypass daemon)")
	planRunCmd.Flags().BoolVar(&planRunBackground, "background", false, "Submit to daemon and exit without waiting")
	planRunCmd.Flags().BoolVar(&planRunAppendDelta, "append-delta", false, "Chat jobs: append a supersede-annotated context layer with files changed since the layers were frozen (cache-preserving refresh)")
	planRunCmd.Flags().BoolVar(&planRunRebaseContext, "rebase-context", false, "Chat jobs: archive all context layers and re-freeze a fresh base from the current worktree (one deliberate cold cache write)")
	planRunCmd.Flags().BoolVar(&planRunForce, "force", false, "Override an advisory satellite dispatch lease (.grove-lease.yml) on the plan dir")

	// Add-step command flags
	planAddCmd.Flags().StringVar(&planAddTemplate, "template", "", "Name of the job template to use")
	planAddCmd.Flags().StringVarP(&planAddType, "type", "t", "interactive_agent", `Job type:
   • oneshot          - Single LLM call, no tools or iteration
   • chat             - Interactive conversation requiring user input
   • shell            - Execute shell commands directly
   • headless_agent   - Autonomous agent without user interaction
   • interactive_agent - Agent with user interaction (default)
   • isolated_agent   - Background agent in isolated tmux server with TUI input
   • file             - Static file content, no execution`)
	planAddCmd.Flags().StringVar(&planAddTitle, "title", "", "Job title")
	planAddCmd.Flags().StringSliceVar(&planAddDependsOn, "depends-on", nil, "Dependencies (job filenames)")
	planAddCmd.Flags().StringVar(&planAddParentJobID, "parent-job-id", "", "Owning parent Flow job ID (lineage only; does not create a dependency)")
	planAddCmd.Flags().StringVarP(&planAddPromptFile, "prompt-file", "f", "", "File containing the prompt")
	planAddCmd.Flags().StringVarP(&planAddPrompt, "prompt", "p", "", "Inline prompt text (alternative to --prompt-file)")
	planAddCmd.Flags().BoolVarP(&planAddInteractive, "interactive", "i", false, "Interactive mode")
	planAddCmd.Flags().StringSliceVar(&planAddIncludeFiles, "include", nil, "Comma-separated list of files to include as context")
	planAddCmd.Flags().StringVar(&planAddWorktree, "worktree", "", "Explicitly set the worktree name (overrides automatic inference)")
	planAddCmd.Flags().StringSliceVar(&planAddInline, "inline", nil, `File types to inline in prompt:
   • dependencies - Embed dependency output directly in prompt (vs separate files)
   • include      - Embed --include file content directly in prompt
   • context      - Embed project context (.grove/context) in prompt
   • all          - Inline all of the above
   • files        - Inline dependencies + include (not context)
   • none         - No inlining; content provided as separate files (default)`)
	planAddCmd.Flags().BoolVar(&planAddPrependDependencies, "prepend-dependencies", false, "[DEPRECATED] Use --inline=dependencies. Inline dependency content into prompt body")
	planAddCmd.Flags().StringVar(&planAddRecipe, "recipe", "", "Name of a recipe to add to the plan")
	planAddCmd.Flags().StringArrayVar(&planAddRecipeVars, "recipe-vars", nil, "Variables for the recipe templates (e.g., key=value)")
	planAddCmd.Flags().StringVar(&planAddSourceFile, "source-file", "", "Origin file path for tracking job provenance (e.g., Claude plan file)")
	planAddCmd.Flags().StringVarP(&planAddModel, "model", "m", "", "LLM model to use for this job (e.g., gemini-3-pro-preview, claude-sonnet-4-20250514)")
	planAddCmd.Flags().StringVar(&planAddResponder, "responder", "", "Who authors the response turns of a chat job: oracle (default; stateless LLM API call over inlined context), agent (fresh agent session with file access per turn), or pi-session (one persistent seeded Pi process owns the chat). Neither agent nor pi-session is dispatched to an LLM API; both require --type chat")
	planAddCmd.Flags().StringVar(&planAddEffort, "effort", "", "Effort level for claude agent jobs (passed to the claude CLI as --effort)")
	planAddCmd.Flags().StringVar(&planAddProvider, "provider", "", "Agent CLI provider for agent jobs (claude/codex/opencode; defaults to flow.interactive_provider)")
	planAddCmd.Flags().StringVar(&planAddRulesFile, "rules-file", "", "Path to a custom rules file for this job")
	planAddCmd.Flags().BoolVar(&planAddNoContext, "no-context", false, noContextFlagHelp)
	planAddCmd.Flags().BoolVar(&planAddGitChanges, "git-changes", false, "Include git changes (staged and unstaged) as context for this job")
	registerHandoffAddFlags(planAddCmd.Flags())
	planAddCmd.Flags().StringVar(&planAddSkill, "skill", "", "Skill name to inject into the agent context")
	planAddCmd.Flags().StringSliceVar(&planAddSkillSequence, "skill-sequence", nil, "Comma-separated list of skills to execute in sequence")
	planAddCmd.Flags().BoolVar(&planAddJSON, "json", false, "Output as JSON (machine-readable {path, id, number, title})")
	planAddCmd.Flags().BoolVar(&planAddRun, "run", false, "Create job and immediately run it (equivalent to: flow plan add ... && flow plan run)")

	// Graph command flags
	planGraphCmd.Flags().StringVarP(&planGraphFormat, "format", "f", "mermaid", "Output format: mermaid, dot, ascii")
	planGraphCmd.Flags().BoolVarP(&planGraphServe, "serve", "s", false, "Serve interactive HTML visualization")
	planGraphCmd.Flags().IntVarP(&planGraphPort, "port", "p", 8080, "Port for web server")
	planGraphCmd.Flags().StringVarP(&planGraphOutput, "output", "o", "", "Output file (stdout if not specified)")
	planGraphCmd.Flags().StringVarP(&planContextDir, "dir", "d", "", "Workspace or plan directory context (defaults to current directory)")

	// Shared --dir flag on subcommands
	planStepCmd.Flags().StringVarP(&planContextDir, "dir", "d", "", "Workspace or plan directory context (defaults to current directory)")
	planOpenCmd.Flags().StringVarP(&planContextDir, "dir", "d", "", "Workspace or plan directory context (defaults to current directory)")
	planReviewCmd.Flags().StringVarP(&planContextDir, "dir", "d", "", "Workspace or plan directory context (defaults to current directory)")
	planActionCmd.Flags().StringVarP(&planContextDir, "dir", "d", "", "Workspace or plan directory context (defaults to current directory)")

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
	planCmd.AddCommand(planRetryCmd)
	planCmd.AddCommand(NewPlanWarmCmd())
	planCmd.AddCommand(NewPlanSayCmd())
	planCmd.AddCommand(NewPlanRespondCmd())
	planCmd.AddCommand(NewPlanFilesCmd())
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
	planCmd.AddCommand(planDemoteCmd)
	planCmd.AddCommand(planCommitsCmd)
	planCmd.AddCommand(planRecordingsCmd)

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
		Context:           cmd.Context(),
		Dir:               dir,
		Force:             planInitForce,
		Model:             planInitModel,
		Worktree:          planInitWorktree,
		ExtractAllFrom:    planInitExtractAllFrom,
		OpenSession:       planInitOpenSession,
		EnvProfile:        planInitEnvProfile,
		Recipe:            planInitRecipe,
		RecipeVars:        planInitRecipeVars,
		RecipeCmd:         planInitRecipeCmd,
		SiblingWorkspaces: planInitSiblingWorkspaces,
		Anchor:            planInitAnchor,
		NoteRef:           planInitNoteRef,
		FromNotes:         planInitFromNotes,
		NoteTargetFile:    planInitNoteTargetFile,
		RunInit:           planInitRunInit,
		Playbook:          planInitPlaybook,
		Layout:            planInitLayout,
		Satellite:         planInitSatellite,
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
		Dir:                 dir,
		Template:            planAddTemplate,
		Type:                planAddType,
		Title:               planAddTitle,
		DependsOn:           planAddDependsOn,
		ParentJobID:         planAddParentJobID,
		PromptFile:          planAddPromptFile,
		Prompt:              planAddPrompt,
		Interactive:         planAddInteractive,
		IncludeFiles:        planAddIncludeFiles,
		Worktree:            planAddWorktree,
		Inline:              planAddInline,
		PrependDependencies: planAddPrependDependencies,
		Recipe:              planAddRecipe,
		RecipeVars:          planAddRecipeVars,
		SourceFile:          planAddSourceFile,
		Model:               planAddModel,
		Responder:           planAddResponder,
		Effort:              planAddEffort,
		Provider:            planAddProvider,
		RulesFile:           planAddRulesFile,
		NoContext:           planAddNoContext,
		GitChanges:          planAddGitChanges,
		Skill:               planAddSkill,
		SkillSequence:       planAddSkillSequence,
		CoordMode:           planAddCoordMode,
		HandoffFrom:         planAddHandoffFrom,
		HandoffDepth:        planAddHandoffDepth,
		HandoffMax:          planAddHandoffMax,
		HandoffThreshold:    planAddHandoffThreshold,
		JSON:                planAddJSON,
		RunAfterCreate:      planAddRun,
		Ctx:                 cmd.Context(),
	}
	return RunPlanAddStep(addStepCmd)
}

func runPlanGraph(cmd *cobra.Command, args []string) error {
	graphCmd := &PlanGraphCmd{
		Format:     planGraphFormat,
		Serve:      planGraphServe,
		Port:       planGraphPort,
		Output:     planGraphOutput,
		ContextDir: planContextDir,
		Ctx:        cmd.Context(),
	}
	if len(args) > 0 {
		graphCmd.Directory = args[0]
	}
	return RunPlanGraph(graphCmd)
}

// PlanInitCmd holds the parameters for the init command.
type PlanInitCmd struct {
	Context           context.Context
	Dir               string
	Force             bool
	Model             string
	Worktree          string
	ExtractAllFrom    string
	OpenSession       bool
	EnvProfile        string // Named environment profile (--env flag)
	Recipe            string
	RecipeVars        []string
	RecipeCmd         string
	SiblingWorkspaces []string // Sibling workspaces to link into the ecosystem worktree
	NoteRef           string
	FromNotes         []string // Repeatable --from-note; the first drives ExtractAllFrom/NoteRef, each additional note becomes its own job
	NoteTargetFile    string
	RunInit           bool // Run init actions from workspace_init.yml
	Playbook          string
	Satellite         string // Satellite the plan's remote work is designated to (written to .grove-plan.yml)
	Layout            string // Worktree layout: "xdg" or "legacy" (empty = resolver default)
	Anchor            string // Repo name to anchor the worktree to (driving repo)
}

// firstFromNote returns the first --from-note path, or "" when none given.
func (c *PlanInitCmd) firstFromNote() string {
	if len(c.FromNotes) == 0 {
		return ""
	}
	return c.FromNotes[0]
}
