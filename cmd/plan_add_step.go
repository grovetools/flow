package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/grovetools/core/pkg/models"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/skills/pkg/skills"
	"github.com/mattn/go-isatty"

	"github.com/grovetools/flow/pkg/orchestration"
)

type PlanAddStepCmd struct {
	Dir                 string   `arg:"" help:"Plan directory"`
	Template            string   `flag:"" help:"Name of the job template to use"`
	Type                string   `flag:"t" default:"interactive_agent" help:"Job type: oneshot, chat, interactive_agent, isolated_agent, headless_agent, shell, or file"`
	Title               string   `flag:"" help:"Job title"`
	DependsOn           []string `flag:"d" help:"Dependencies (job filenames)"`
	ParentJobID         string   `flag:"" help:"Owning parent Flow job ID (lineage only; does not create a dependency)"`
	PromptFile          string   `flag:"f" help:"File containing the prompt"`
	IncludeFiles        []string `flag:"" sep:"," help:"Comma-separated list of files to include as context"`
	Prompt              string   `flag:"p" help:"Inline prompt text"`
	Interactive         bool     `flag:"i" help:"Interactive mode"`
	Worktree            string   `flag:"" help:"Explicitly set the worktree name (overrides automatic inference)"`
	Model               string   `flag:"" help:"LLM model to use for this job"`
	Responder           string   `flag:"" help:"Who authors the response turns of a chat job: oracle (default; stateless LLM API call over inlined context), agent (fresh agent session per turn), or pi-session (one persistent seeded Pi process owns the chat). Neither agent nor pi-session is dispatched to an LLM API"`
	Effort              string   `flag:"" help:"Effort level for claude agent jobs (passed to the claude CLI as --effort)"`
	Provider            string   `flag:"" help:"Agent CLI provider for agent jobs (claude/codex/opencode; defaults to flow.interactive_provider)"`
	Inline              []string `flag:"" sep:"," help:"File types to inline in prompt: dependencies, include, context, all, files, none (comma-separated)"`
	PrependDependencies bool     `flag:"" help:"[DEPRECATED] Use --inline=dependencies instead. Inline dependency content into prompt body."`
	Recipe              string   `flag:"" help:"Name of a recipe to add to the plan"`
	RecipeVars          []string `flag:"" help:"Variables for the recipe templates (e.g., key=value)"`
	SourceFile          string   `flag:"" help:"Origin file path for tracking job provenance (e.g., Claude plan file)"`
	RulesFile           string   `flag:"" help:"Path to a custom rules file for this job"`
	NoContext           bool     `flag:"" help:"Create the job with no repository context: no rules file is stamped and the job is answered from its prompt alone"`
	GitChanges          bool     `flag:"" help:"Include git changes as context for this job"`
	CoordMode           string   `flag:"" help:"Coordinator autonomy for agent jobs: manual (default) or autonomous (hand off to a successor job when context nears the threshold)"`
	HandoffFrom         string   `flag:"" help:"Predecessor Flow job ID this job continues from (coordinator handoff lineage)"`
	HandoffDepth        int      `flag:"" help:"Position of this job in its handoff chain (0 = original coordinator)"`
	HandoffMax          int      `flag:"" help:"Upper bound on chained coordinator handoffs (0 = flow.handoff_max, default 3)"`
	HandoffThreshold    int      `flag:"" help:"Context-window usage percent that arms an autonomous handoff (0 = flow.handoff_threshold, default 80)"`
	Skill               string   `flag:"" help:"Skill name to inject into the agent context"`
	SkillSequence       []string `flag:"" sep:"," help:"List of skills to execute in sequence"`
	Channels            []string `flag:"" sep:"," help:"External channels to enable (e.g., signal)"`
	Autonomous          bool     `flag:"" help:"Enable autonomous idle pinging"`
	IdleMinutes         int      `flag:"" default:"15" help:"Minutes of inactivity before idle ping"`
	JSON                bool     `flag:"" help:"Output as JSON (machine-readable {path, id, number, title})"`
	RunAfterCreate      bool     `flag:"" name:"run" help:"Create job and immediately run it"`

	// Ctx carries the cobra command context so RunPlanAddStep can honor the
	// unified `--at` target. The kong-style (*PlanAddStepCmd).Run() path leaves
	// this nil; the ctx-aware resolver is nil-safe and falls back to legacy.
	Ctx context.Context `kong:"-"`
}

func (c *PlanAddStepCmd) Run() error {
	return RunPlanAddStep(c)
}

// noContextFlagHelp is the one wording for --no-context across `flow plan add`,
// `flow add`, and `flow job add`.
const noContextFlagHelp = "Create the job with no repository context: no rules file is stamped and the job is answered from its prompt alone (mutually exclusive with --rules-file)"

// parseInlineFlag converts CLI --inline flag values to an InlineConfig.
func parseInlineFlag(values []string) orchestration.InlineConfig {
	if len(values) == 0 {
		return orchestration.InlineConfig{}
	}

	// Check for shorthand values
	if len(values) == 1 {
		switch strings.ToLower(values[0]) {
		case "none", "":
			return orchestration.InlineConfig{}
		case "all":
			return orchestration.InlineConfig{
				Categories: []orchestration.InlineCategory{
					orchestration.InlineDependencies,
					orchestration.InlineInclude,
					orchestration.InlineContext,
				},
			}
		case "files":
			// Shorthand for dependencies + include (excludes context)
			return orchestration.InlineConfig{
				Categories: []orchestration.InlineCategory{
					orchestration.InlineDependencies,
					orchestration.InlineInclude,
				},
			}
		}
	}

	// Parse as array of categories
	var categories []orchestration.InlineCategory
	for _, v := range values {
		v = strings.TrimSpace(strings.ToLower(v))
		if v != "" && v != "none" {
			categories = append(categories, orchestration.InlineCategory(v))
		}
	}
	return orchestration.InlineConfig{Categories: categories}
}

// validateResponderFlag checks the --responder flag value against the job
// type. Accepted values are empty (default), "oracle", "agent", and
// "pi-session". "oracle" is explicitly valid and treated identically to an
// absent field (a stateless LLM API call over inlined context); "agent" marks
// an agent-responded chat whose response turns are authored by a fresh agent
// session per turn; "pi-session" marks a chat owned by one persistent,
// context-seeded Pi process. Neither of the latter two is ever dispatched to an
// LLM API, and both only make sense on chat jobs.
func validateResponderFlag(responder, jobType string) error {
	switch responder {
	case "", orchestration.ResponderOracle:
		return nil
	case orchestration.ResponderAgent:
		if jobType != "chat" {
			return fmt.Errorf("--responder agent requires --type chat (agent-responded chats are turn-based dialogues answered by a fresh agent session per turn); got --type %s", jobType)
		}
		return nil
	case orchestration.ResponderPiSession:
		if jobType != "chat" {
			return fmt.Errorf("--responder pi-session requires --type chat (a pi-session chat is a turn-based dialogue answered by one persistent seeded Pi process); got --type %s", jobType)
		}
		return nil
	default:
		return fmt.Errorf("invalid --responder value %q: must be oracle, agent, or pi-session", responder)
	}
}

// inheritSkillSequence sets job.SkillSequence from the skill's frontmatter
// when a skill is set but no explicit skill_sequence was provided.
func inheritSkillSequence(job *orchestration.Job, workDir string) {
	if job.Skill == "" || len(job.SkillSequence) > 0 {
		return
	}
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	loaded, err := skills.LoadSkillBypassingAccess(workDir, job.Skill)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not inherit skill_sequence from %q (workDir=%s): %v\n", job.Skill, workDir, err)
		return
	}
	content, ok := loaded.Files["SKILL.md"]
	if !ok {
		return
	}
	meta, err := skills.ParseSkillFrontmatter(content)
	if err != nil {
		return
	}
	if len(meta.SkillSequence) > 0 {
		job.SkillSequence = meta.SkillSequence
	}
}

func RunPlanAddStep(cmd *PlanAddStepCmd) error {
	// --no-context declares a job with no repository context at all; --rules-file
	// names the context it wants. Asking for both is a contradiction, and one of
	// them would have to be silently dropped at run time.
	if cmd.NoContext && cmd.RulesFile != "" {
		return fmt.Errorf("--no-context and --rules-file are mutually exclusive: --no-context means the job is answered from its prompt alone")
	}

	// Capture CWD early before any directory changes (needed for skill resolution)
	startingDir, _ := os.Getwd()

	// Resolve the plan path with active job support, honoring a unified `--at`
	// target when present on the context (nil-safe: falls back to legacy).
	planPath, err := resolvePlanPathWithActiveJobCtx(cmd.Ctx, cmd.Dir, ".")
	if err != nil {
		return fmt.Errorf("could not resolve plan path: %w", err)
	}

	// For absolute paths, use them directly (important for tests) — but only
	// when no `--at` target is set, so `--at` wins over a bare absolute Dir.
	if _, hasAtTarget := TargetFromContext(cmd.Ctx); !hasAtTarget && filepath.IsAbs(cmd.Dir) {
		planPath = cmd.Dir
	}

	// Create plan directory if it doesn't exist
	if _, err := os.Stat(planPath); os.IsNotExist(err) {
		if err := os.MkdirAll(planPath, 0o755); err != nil {
			return fmt.Errorf("failed to create plan directory: %w", err)
		}
	}

	// Load existing plan
	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	// Handle adding jobs from a recipe
	if cmd.Recipe != "" {
		// 1. Load the recipe
		// We don't have a recipe command here, so we pass an empty string
		recipe, err := orchestration.GetRecipe(cmd.Recipe, "")
		if err != nil {
			return err
		}

		// 2. Parse recipe variables from CLI flags
		recipeVars := make(map[string]string)
		for _, v := range cmd.RecipeVars {
			pairs := strings.Split(v, ",")
			for _, pair := range pairs {
				pair = strings.TrimSpace(pair)
				if pair == "" {
					continue
				}
				parts := strings.SplitN(pair, "=", 2)
				if len(parts) == 2 {
					recipeVars[parts[0]] = parts[1]
				}
			}
		}
		templateData := struct {
			PlanName string
			Vars     map[string]string
		}{
			PlanName: plan.Name,
			Vars:     recipeVars,
		}

		// 3. Call the core orchestration function
		newFiles, err := orchestration.AddJobsFromRecipe(plan, recipe, cmd.DependsOn, templateData)
		if err != nil {
			return fmt.Errorf("failed to add jobs from recipe: %w", err)
		}

		// 4. Print success message
		fmt.Println(theme.DefaultTheme.Success.Render("*") + " Added " + fmt.Sprintf("%d jobs from recipe '%s':", len(newFiles), cmd.Recipe))
		for _, file := range newFiles {
			fmt.Println("  - " + file)
		}
		return nil
	}

	// Use explicit worktree from command line flag only
	worktreeToUse := cmd.Worktree

	// An agent-responded chat defaults to the chat-agent template, which
	// carries the respondent contract as its base prompt. An explicit
	// --template always wins.
	if cmd.Responder == "agent" && cmd.Template == "" {
		cmd.Template = "chat-agent"
	}

	var job *orchestration.Job

	if cmd.Template != "" {
		// New logic for handling templates
		templateManager := orchestration.NewTemplateManager()
		template, err := templateManager.FindTemplate(cmd.Template)
		if err != nil {
			return err
		}
		var jobErr error
		job, jobErr = collectJobDetailsFromTemplate(cmd, plan, template, worktreeToUse)
		if jobErr != nil {
			return jobErr
		}
	} else {
		// Existing logic
		var jobErr error
		job, jobErr = collectJobDetails(cmd, plan, worktreeToUse)
		if jobErr != nil {
			return jobErr
		}
	}

	if job == nil {
		return fmt.Errorf("failed to create job: no job details collected")
	}

	// parent_job_id is ownership lineage, not dependency lineage. Validate it
	// against the exact loaded plan and stamp it only after all template/flag
	// merging has completed.
	if cmd.ParentJobID != "" {
		if _, found := plan.GetJobByID(cmd.ParentJobID); !found {
			return fmt.Errorf("parent job ID not found in this plan: %s", cmd.ParentJobID)
		}
		job.ParentJobID = cmd.ParentJobID
	}

	// Coordinator handoff frontmatter. Stamped after template/flag merging for
	// the same reason parent_job_id is, and validated against the loaded plan
	// so a successor can never point at a job outside it.
	if err := applyHandoffFields(cmd, plan, job); err != nil {
		return err
	}

	// Add-time lint warnings for agent-responded chats (warn, never error):
	// agent responders read files from disk, so oracle-only context knobs are
	// wasted or meaningless on them.
	if job.IsAgentResponded() {
		if len(cmd.Inline) > 0 {
			fmt.Fprintln(os.Stderr, "Warning: --inline with --responder agent: agent responders read dependencies from disk; inlining content into the prompt only wastes tokens")
		}
		if job.StripComments != nil {
			fmt.Fprintln(os.Stderr, "Warning: strip_comments is a no-op with responder: agent — agents read raw files; comment stripping only applies to oracle context inlining")
		}
	}

	// pi-session chats are the opposite case on both knobs: strip_comments DOES
	// apply (it selects the bytes frozen into the layers the seed embeds), while
	// a rules file is not optional — the whole point of the responder is that
	// the session wakes up already holding the curated bundle.
	if job.IsPiSessionResponded() {
		if len(cmd.Inline) > 0 {
			fmt.Fprintln(os.Stderr, "Warning: --inline with --responder pi-session: the seeded session reads dependencies from disk; inlining content into the prompt only wastes tokens")
		}
		if job.RulesFile == "" {
			fmt.Fprintln(os.Stderr, "Warning: responder: pi-session without a rules_file — the session would launch with no seeded context. Add one with --rules-file before running it (the launch preflight refuses an empty resolution).")
		}
		// The Pi CLI is the engine for this responder; any other agent provider
		// cannot open the synthesized session file.
		if job.Provider == "" {
			job.Provider = orchestration.DefaultPiSessionProvider
		}
	}

	// Oracle chats (responder oracle/absent) with dependencies need those
	// dependencies inlined into the prompt: the stateless, tool-less LLM can't
	// read a "<dep>.md" file off disk, so an un-inlined dep silently never
	// reaches the model. Default inline: dependencies ON for them unless the
	// user gave an explicit --inline value (which always wins) or a template
	// already set inline. responder: agent and responder: pi-session chats are
	// the opposite case — they read files from disk and keep the lint warnings
	// above.
	if job.Type == orchestration.JobTypeChat && !job.IsAPIDispatchVetoed() &&
		len(job.DependsOn) > 0 && len(cmd.Inline) == 0 && len(job.Inline.Categories) == 0 {
		job.Inline = orchestration.InlineConfig{
			Categories: []orchestration.InlineCategory{orchestration.InlineDependencies},
		}
		fmt.Fprintln(os.Stderr, "Note: defaulting inline: dependencies for oracle chat with dependencies (pass --inline to override)")
	}

	// Validate the provider (job frontmatter / --provider) early: an unknown
	// provider must fail at submit time, not at launch.
	if err := orchestration.ValidateAgentProviderName(job.Provider); err != nil {
		return fmt.Errorf("invalid --provider: %w", err)
	}

	// Validate the model for this job type against the job's EFFECTIVE
	// provider (job provider > flow.interactive_provider > claude): reject
	// unknown models and provider mismatches (e.g. gemini model on a claude
	// agent job) early.
	if err := orchestration.ValidateModelForJob(job.Model, job.Type, orchestration.ResolveJobProviderNameFromConfig(job)); err != nil {
		return fmt.Errorf("invalid --model: %w", err)
	}

	// If a skill is set but no skill_sequence, inherit from the skill's frontmatter.
	// Use the CWD captured at function entry (before flow changes directories)
	inheritSkillSequence(job, startingDir)

	// Lineage-overlap advisory (oracle-plays K2): if a completed sibling chat
	// already froze layers this chat's rules re-resolve, `-d`-ing it would
	// inherit them warm instead of paying a fresh cold base write. Advisory only
	// — printed to stderr (so it never pollutes --json stdout), never blocks the
	// add, all errors swallowed to nil advice.
	if advice, _ := orchestration.AdviseLineageOverlapAtAdd(plan, job); advice != nil {
		fmt.Fprintln(os.Stderr, "Note: "+advice.FormatAdvice())
	}

	// Generate job file
	filename, err := orchestration.AddJob(plan, job)
	if err != nil {
		return fmt.Errorf("failed to add job: %w", err)
	}

	// Handle JSON output or text output
	if cmd.JSON {
		// Extract job number from filename (format: "02-title.md")
		numStr := strings.Split(filename, "-")[0]
		jobNumber := 0
		if n, err := strconv.Atoi(numStr); err == nil {
			jobNumber = n
		}

		// Create JSON output
		result := map[string]interface{}{
			"path":   filepath.Join(planPath, filename),
			"id":     job.ID,
			"number": jobNumber,
			"title":  job.Title,
		}
		jsonBytes, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		// Output ONLY the JSON to stdout
		fmt.Println(string(jsonBytes))
	} else {
		// Display success with human-readable output
		fmt.Println(theme.DefaultTheme.Success.Render("*") + " Created " + filename)
		fmt.Println("\nNext steps:")
		fmt.Println("- Review the job file")
		fmt.Printf("- Run with: flow plan run --at %s %s\n", plan.Name, filename)
	}

	// Handle --run flag: create then run the job
	if cmd.RunAfterCreate {
		// Invoke the equivalent of: flow plan run <plan-dir>/<job-file>
		// We print the command for the user to run since we can't directly invoke the run command here
		jobFile := filepath.Join(planPath, filename)

		// For now, we'll just suggest the command to run
		// The implementation of actually running it will be done by printing to stderr
		// so it doesn't interfere with --json output
		if !cmd.JSON {
			fmt.Fprintf(os.Stderr, "\nRunning: flow plan run %s\n", jobFile)
		}

		// Execute the equivalent: flow plan run <jobFile>
		// We use exec to replace the current process
		runCmd := exec.Command("flow", "plan", "run", jobFile, "--yes")
		runCmd.Stdin = os.Stdin
		runCmd.Stdout = os.Stdout
		runCmd.Stderr = os.Stderr
		if err := runCmd.Run(); err != nil {
			return fmt.Errorf("failed to run job: %w", err)
		}
	}

	return nil
}

func collectJobDetails(cmd *PlanAddStepCmd, plan *orchestration.Plan, worktreeToUse string) (*orchestration.Job, error) {
	// Auto-detect worktree context if not explicitly provided
	if worktreeToUse == "" {
		currentNode, err := workspace.GetProjectByPath(".")
		if err == nil && currentNode != nil && currentNode.IsWorktree() && currentNode.RootEcosystemPath != "" {
			// This is an ecosystem worktree context. Find the name of the ecosystem worktree.
			// This is typically the base name of the ParentEcosystemPath for a sub-project worktree.
			if currentNode.ParentEcosystemPath != "" && workspace.IsWorktreePath(currentNode.ParentEcosystemPath) {
				worktreeToUse = filepath.Base(currentNode.ParentEcosystemPath)
			} else if currentNode.IsEcosystem() {
				// This is the ecosystem worktree itself
				worktreeToUse = currentNode.Name
			}
		}
	}

	if cmd.Interactive {
		// Check if we're in a TTY before launching interactive mode
		if !isatty.IsTerminal(os.Stdout.Fd()) && !isatty.IsCygwinTerminal(os.Stdout.Fd()) {
			return nil, fmt.Errorf("interactive mode requires a terminal (TTY)")
		}
		return interactiveJobCreation(plan, cmd)
	}

	// Validate non-interactive inputs
	if cmd.Title == "" {
		return nil, fmt.Errorf("title is required (use --title or -i for interactive mode)")
	}

	if cmd.Type != "oneshot" && cmd.Type != "chat" && cmd.Type != "shell" && cmd.Type != "interactive_agent" && cmd.Type != "headless_agent" && cmd.Type != "file" {
		return nil, fmt.Errorf("invalid job type: must be oneshot, chat, shell, interactive_agent, headless_agent, or file")
	}

	if err := validateResponderFlag(cmd.Responder, cmd.Type); err != nil {
		return nil, err
	}

	// Validate dependencies
	for _, dep := range cmd.DependsOn {
		found := false
		for _, job := range plan.Jobs {
			if job.Filename == dep {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("dependency not found: %s", dep)
		}
	}

	// Check if we should use include files (even without a template)
	if len(cmd.IncludeFiles) > 0 {
		// Include file handling
		projectRoot, err := orchestration.GetProjectRoot()
		if err != nil {
			return nil, fmt.Errorf("failed to get project root: %w", err)
		}

		// Convert to relative paths
		var relativeIncludeFiles []string
		for _, file := range cmd.IncludeFiles {
			// Resolve the file path
			resolvedPath, err := orchestration.ResolvePromptSource(file, plan)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve include file %s: %w", file, err)
			}

			// If the file is in the plan directory, use just the relative path from plan
			if strings.HasPrefix(resolvedPath, plan.Directory+string(filepath.Separator)) {
				relPath, _ := filepath.Rel(plan.Directory, resolvedPath)
				relativeIncludeFiles = append(relativeIncludeFiles, relPath)
			} else {
				// Otherwise, make it relative to project root
				relPath, err := filepath.Rel(projectRoot, resolvedPath)
				if err != nil {
					// If we can't make it relative, use the absolute path
					relPath = resolvedPath
				}
				relativeIncludeFiles = append(relativeIncludeFiles, relPath)
			}
		}

		// Generate job ID
		jobID := orchestration.GenerateUniqueJobID(plan, cmd.Title)

		// Build job structure
		status := orchestration.JobStatusPending
		if cmd.Type == "chat" {
			status = orchestration.JobStatusPendingUser
		} else if cmd.Type == "file" {
			status = orchestration.JobStatusCompleted
		}

		// Parse inline config from CLI flags
		inlineConfig := parseInlineFlag(cmd.Inline)

		job := &orchestration.Job{
			ID:                  jobID,
			Title:               cmd.Title,
			Type:                orchestration.JobType(cmd.Type),
			Responder:           cmd.Responder,
			Status:              status,
			DependsOn:           cmd.DependsOn,
			Include:             relativeIncludeFiles,
			Model:               cmd.Model,
			Effort:              cmd.Effort,
			Provider:            cmd.Provider,
			Inline:              inlineConfig,
			PrependDependencies: cmd.PrependDependencies, // Keep for backwards compat
			SourceFile:          cmd.SourceFile,
			RulesFile:           cmd.RulesFile,
			NoContext:           cmd.NoContext,
			GitChanges:          cmd.GitChanges,
			Skill:               cmd.Skill,
			SkillSequence:       cmd.SkillSequence,
			Channels:            cmd.Channels,
		}
		if cmd.Autonomous {
			job.Autonomous = &models.AutonomousConfig{
				Enabled:     true,
				IdleMinutes: cmd.IdleMinutes,
			}
		}

		// Initialize empty prompt body - no comments needed since info is in frontmatter
		job.PromptBody = ""

		// Add user-provided prompt if any
		userPrompt := ""
		if cmd.Prompt != "" {
			userPrompt = cmd.Prompt
		} else if cmd.PromptFile != "" {
			resolvedPath, err := orchestration.ResolvePromptSource(cmd.PromptFile, plan)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve prompt file %s: %w", cmd.PromptFile, err)
			}
			content, err := os.ReadFile(resolvedPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read prompt file '%s': %w", resolvedPath, err)
			}
			userPrompt = string(content)
		} else {
			stat, _ := os.Stdin.Stat()
			if (stat.Mode() & os.ModeCharDevice) == 0 {
				content, err := io.ReadAll(os.Stdin)
				if err != nil {
					return nil, fmt.Errorf("failed to read prompt from stdin: %w", err)
				}
				userPrompt = string(content)
			}
		}
		if userPrompt != "" {
			if job.PromptBody == "" {
				job.PromptBody = userPrompt
			} else {
				job.PromptBody = job.PromptBody + "\n\n## Additional Instructions\n\n" + userPrompt
			}
		}

		// Set worktree only if explicitly provided
		if worktreeToUse != "" {
			job.Worktree = worktreeToUse
		}

		// Apply plan-level defaults (model gated to oneshot/chat) for anything
		// not explicitly set above.
		orchestration.ApplyPlanDefaults(plan, job)

		return job, nil
	}

	// Traditional prompt handling (non-reference based)
	prompt := ""
	if cmd.Prompt != "" {
		// Use inline prompt if provided
		prompt = cmd.Prompt
	} else if cmd.PromptFile != "" {
		// Resolve the prompt file path using the same logic as source files
		resolvedPath, err := orchestration.ResolvePromptSource(cmd.PromptFile, plan)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve prompt file %s: %w", cmd.PromptFile, err)
		}

		// Read from resolved file
		content, err := os.ReadFile(resolvedPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read prompt file '%s': %w", resolvedPath, err)
		}
		prompt = string(content)
	} else {
		// Read from stdin if available
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			content, err := io.ReadAll(os.Stdin)
			if err != nil {
				return nil, fmt.Errorf("failed to read prompt from stdin: %w", err)
			}
			prompt = string(content)
		}
	}

	// Require a prompt if no template was used (file jobs can be empty)
	if prompt == "" && cmd.Template == "" && cmd.Type != "file" {
		return nil, fmt.Errorf("either a prompt or template is required")
	}

	// Generate job ID
	jobID := orchestration.GenerateUniqueJobID(plan, cmd.Title)

	// Build job structure
	status := orchestration.JobStatusPending
	if cmd.Type == "chat" {
		status = orchestration.JobStatusPendingUser
	} else if cmd.Type == "file" {
		status = orchestration.JobStatusCompleted
	}

	// Parse inline config from CLI flags
	inlineConfig := parseInlineFlag(cmd.Inline)

	job := &orchestration.Job{
		ID:                  jobID,
		Title:               cmd.Title,
		Type:                orchestration.JobType(cmd.Type),
		Responder:           cmd.Responder,
		Status:              status,
		DependsOn:           cmd.DependsOn,
		PromptBody:          strings.TrimSpace(prompt),
		Model:               cmd.Model,
		Effort:              cmd.Effort,
		Provider:            cmd.Provider,
		Inline:              inlineConfig,
		PrependDependencies: cmd.PrependDependencies, // Keep for backwards compat
		SourceFile:          cmd.SourceFile,
		RulesFile:           cmd.RulesFile,
		NoContext:           cmd.NoContext,
		GitChanges:          cmd.GitChanges,
		Skill:               cmd.Skill,
		SkillSequence:       cmd.SkillSequence,
	}

	// Set worktree only if explicitly provided
	if worktreeToUse != "" {
		job.Worktree = worktreeToUse
	}

	// Apply plan-level defaults (model gated to oneshot/chat) for anything
	// not explicitly set above.
	orchestration.ApplyPlanDefaults(plan, job)

	return job, nil
}

func interactiveJobCreation(plan *orchestration.Plan, cmd *PlanAddStepCmd) (*orchestration.Job, error) {
	// Run the add-job wizard via its embeddable package. Note that
	// worktree is no longer configurable in the TUI — cmd.Worktree is
	// passed through via the outer code path only.
	job, err := runAddJobWizard(plan, cmd.DependsOn)
	if err != nil {
		return nil, err
	}

	// Apply plan-level defaults (model gated to oneshot/chat) for anything the
	// wizard did not set.
	orchestration.ApplyPlanDefaults(plan, job)

	return job, nil
}

func collectJobDetailsFromTemplate(cmd *PlanAddStepCmd, plan *orchestration.Plan, template *orchestration.JobTemplate, worktreeToUse string) (*orchestration.Job, error) {
	// Auto-detect worktree context if not explicitly provided
	if worktreeToUse == "" {
		currentNode, err := workspace.GetProjectByPath(".")
		if err == nil && currentNode != nil && currentNode.IsWorktree() && currentNode.RootEcosystemPath != "" {
			// This is an ecosystem worktree context. Find the name of the ecosystem worktree.
			// This is typically the base name of the ParentEcosystemPath for a sub-project worktree.
			if currentNode.ParentEcosystemPath != "" && workspace.IsWorktreePath(currentNode.ParentEcosystemPath) {
				worktreeToUse = filepath.Base(currentNode.ParentEcosystemPath)
			} else if currentNode.IsEcosystem() {
				// This is the ecosystem worktree itself
				worktreeToUse = currentNode.Name
			}
		}
	}

	// Title is required even with template
	if cmd.Title == "" {
		return nil, fmt.Errorf("title is required (use --title)")
	}

	// Apply template defaults
	job := &orchestration.Job{
		Title:      cmd.Title,
		Status:     orchestration.JobStatusPending,
		SourceFile: cmd.SourceFile,
		RulesFile:  cmd.RulesFile,
		NoContext:  cmd.NoContext,
		GitChanges: cmd.GitChanges,
	}

	// Use reflection or a helper to merge template.Frontmatter into the job struct
	// For simplicity, let's do it manually for key fields:
	if typ, ok := template.Frontmatter["type"].(string); ok {
		job.Type = orchestration.JobType(typ)
	}
	if deps, ok := template.Frontmatter["depends_on"].([]interface{}); ok {
		for _, dep := range deps {
			if depStr, ok := dep.(string); ok {
				job.DependsOn = append(job.DependsOn, depStr)
			}
		}
	}
	if worktree, ok := template.Frontmatter["worktree"].(string); ok {
		job.Worktree = worktree
	}
	if include, ok := template.Frontmatter["include"].([]interface{}); ok {
		for _, src := range include {
			if srcStr, ok := src.(string); ok {
				job.Include = append(job.Include, srcStr)
			}
		}
	}
	if model, ok := template.Frontmatter["model"].(string); ok {
		job.Model = model
	}
	if effort, ok := template.Frontmatter["effort"].(string); ok {
		job.Effort = effort
	}
	if provider, ok := template.Frontmatter["provider"].(string); ok {
		job.Provider = provider
	}
	if genPlan, ok := template.Frontmatter["generate_plan_from"].(bool); ok {
		job.GeneratePlanFrom = genPlan
	}
	if prependDeps, ok := template.Frontmatter["prepend_dependencies"].(bool); ok {
		job.PrependDependencies = prependDeps
	}
	if noContext, ok := template.Frontmatter["no_context"].(bool); ok && !job.NoContext {
		job.NoContext = noContext
	}
	// An explicit --no-context beats a template's default rules file: the flag
	// is the caller saying this job carries its own context.
	if rulesFile, ok := template.Frontmatter["rules_file"].(string); ok && job.RulesFile == "" && !job.NoContext {
		job.RulesFile = rulesFile
	}
	if gitChanges, ok := template.Frontmatter["git_changes"].(bool); ok && !job.GitChanges {
		job.GitChanges = gitChanges
	}
	if skill, ok := template.Frontmatter["skill"].(string); ok {
		job.Skill = skill
	}
	if seq, ok := template.Frontmatter["skill_sequence"].([]interface{}); ok {
		for _, s := range seq {
			if str, ok := s.(string); ok {
				job.SkillSequence = append(job.SkillSequence, str)
			}
		}
	}
	// Handle inline field from template (can be string or array)
	if inlineVal, ok := template.Frontmatter["inline"]; ok {
		switch v := inlineVal.(type) {
		case string:
			// Single value like "all", "none", or "dependencies"
			ic := orchestration.InlineConfig{}
			if err := ic.UnmarshalYAML(func(out interface{}) error {
				*(out.(*string)) = v
				return nil
			}); err == nil {
				job.Inline = ic
			}
		case []interface{}:
			// Array of categories
			var categories []orchestration.InlineCategory
			for _, cat := range v {
				if catStr, ok := cat.(string); ok {
					categories = append(categories, orchestration.InlineCategory(catStr))
				}
			}
			job.Inline = orchestration.InlineConfig{Categories: categories}
		}
	}

	// CLI flags override template defaults
	if cmd.Type != "" && cmd.Type != "interactive_agent" { // "interactive_agent" is the default, so only override if explicitly set
		job.Type = orchestration.JobType(cmd.Type)
	}
	if len(cmd.DependsOn) > 0 {
		job.DependsOn = cmd.DependsOn
	}
	if cmd.Model != "" {
		job.Model = cmd.Model
	}
	if cmd.Effort != "" {
		job.Effort = cmd.Effort
	}
	if cmd.Responder != "" {
		// Validate against the resolved type (template frontmatter may have
		// set it, with the CLI flag taking precedence above).
		if err := validateResponderFlag(cmd.Responder, string(job.Type)); err != nil {
			return nil, err
		}
		job.Responder = cmd.Responder
	}
	if cmd.Provider != "" {
		job.Provider = cmd.Provider
	}
	// New inline flag overrides template and plan defaults
	if len(cmd.Inline) > 0 {
		job.Inline = parseInlineFlag(cmd.Inline)
	}
	// Backwards compat: prepend_dependencies flag
	if cmd.PrependDependencies {
		job.PrependDependencies = true
	}
	if cmd.Skill != "" {
		job.Skill = cmd.Skill
	}
	if len(cmd.SkillSequence) > 0 {
		job.SkillSequence = cmd.SkillSequence
	}

	// If the job type is chat, set the status to pending_user
	if job.Type == "chat" {
		job.Status = orchestration.JobStatusPendingUser
	}

	// Validate dependencies
	for _, dep := range job.DependsOn {
		found := false
		for _, existingJob := range plan.Jobs {
			if existingJob.Filename == dep {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("dependency not found: %s", dep)
		}
	}

	// When using a template, ALWAYS use reference-based approach
	if true { // Always use include files with templates
		// Include file handling
		// Only use include files, not prompt files
		includeFiles := cmd.IncludeFiles

		// Get project root
		projectRoot, err := orchestration.GetProjectRoot()
		if err != nil {
			return nil, fmt.Errorf("failed to get project root: %w", err)
		}

		// Convert to relative paths
		var relativeIncludeFiles []string
		for _, file := range includeFiles {
			// Resolve the file path
			resolvedPath, err := orchestration.ResolvePromptSource(file, plan)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve include file %s: %w", file, err)
			}

			// If the file is in the plan directory, use just the relative path from plan
			if strings.HasPrefix(resolvedPath, plan.Directory+string(filepath.Separator)) {
				relPath, _ := filepath.Rel(plan.Directory, resolvedPath)
				relativeIncludeFiles = append(relativeIncludeFiles, relPath)
			} else {
				// Otherwise, make it relative to project root
				relPath, err := filepath.Rel(projectRoot, resolvedPath)
				if err != nil {
					// If we can't make it relative, use the absolute path
					relPath = resolvedPath
				}
				relativeIncludeFiles = append(relativeIncludeFiles, relPath)
			}
		}

		// Store template name and include files as metadata
		if len(relativeIncludeFiles) > 0 {
			job.Include = relativeIncludeFiles
		}
		job.Template = template.Name

		// Initialize prompt body with the template's prompt content
		job.PromptBody = strings.TrimSpace(template.Prompt)

		// Add user-provided prompt if any
		userPrompt := ""
		if cmd.Prompt != "" {
			userPrompt = cmd.Prompt
		} else if cmd.PromptFile != "" {
			// Resolve the prompt file path using the same logic as source files
			resolvedPath, err := orchestration.ResolvePromptSource(cmd.PromptFile, plan)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve prompt file %s: %w", cmd.PromptFile, err)
			}

			// Read prompt from resolved file
			content, err := os.ReadFile(resolvedPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read prompt file: %w", err)
			}
			userPrompt = string(content)
		} else {
			// Read from stdin if available
			stat, _ := os.Stdin.Stat()
			if (stat.Mode() & os.ModeCharDevice) == 0 {
				content, err := io.ReadAll(os.Stdin)
				if err != nil {
					return nil, fmt.Errorf("failed to read prompt from stdin: %w", err)
				}
				userPrompt = string(content)
			}
		}

		if userPrompt != "" {
			if job.PromptBody == "" {
				job.PromptBody = strings.TrimSpace(userPrompt)
			} else {
				job.PromptBody = job.PromptBody + "\n\n## Additional Instructions\n\n" + strings.TrimSpace(userPrompt)
			}
		}
	} else {
		// Traditional template rendering approach

		// Render the prompt
		promptData := map[string]string{
			"Title": cmd.Title,
		}
		renderedPrompt, err := template.Render(promptData)
		if err != nil {
			return nil, fmt.Errorf("failed to render template: %w", err)
		}
		job.PromptBody = strings.TrimSpace(renderedPrompt)

		// Append user-provided prompt to template prompt if provided
		if cmd.Prompt != "" {
			job.PromptBody = job.PromptBody + "\n\n" + cmd.Prompt
		}
	}

	// Generate job ID
	job.ID = orchestration.GenerateUniqueJobID(plan, job.Title)

	// Command line --worktree flag overrides template worktree
	if worktreeToUse != "" {
		job.Worktree = worktreeToUse
	}

	// Apply plan-level defaults (model gated to oneshot/chat) for anything not
	// set by flag or template (CLI > Template > Plan config).
	orchestration.ApplyPlanDefaults(plan, job)

	return job, nil
}
