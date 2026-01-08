package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"
	"github.com/mattsolo1/grove-core/pkg/workspace"
	"github.com/mattsolo1/grove-core/tui/theme"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
)

type PlanAddStepCmd struct {
	Dir                 string   `arg:"" help:"Plan directory"`
	Template            string   `flag:"" help:"Name of the job template to use"`
	Type                string   `flag:"t" default:"agent" help:"Job type: oneshot, agent, chat, interactive_agent, headless_agent, or shell"`
	Title               string   `flag:"" help:"Job title"`
	DependsOn           []string `flag:"d" help:"Dependencies (job filenames)"`
	PromptFile          string   `flag:"f" help:"File containing the prompt"`
	IncludeFiles        []string `flag:"" sep:"," help:"Comma-separated list of files to include as context"`
	Prompt              string   `flag:"p" help:"Inline prompt text"`
	Interactive         bool     `flag:"i" help:"Interactive mode"`
	Worktree            string   `flag:"" help:"Explicitly set the worktree name (overrides automatic inference)"`
	Model               string   `flag:"" help:"LLM model to use for this job"`
	Inline              []string `flag:"" sep:"," help:"File types to inline in prompt: dependencies, include, context, all, files, none (comma-separated)"`
	PrependDependencies bool     `flag:"" help:"[DEPRECATED] Use --inline=dependencies instead. Inline dependency content into prompt body."`
	Recipe              string   `flag:"" help:"Name of a recipe to add to the plan"`
	RecipeVars          []string `flag:"" help:"Variables for the recipe templates (e.g., key=value)"`
}

func (c *PlanAddStepCmd) Run() error {
	return RunPlanAddStep(c)
}

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

func RunPlanAddStep(cmd *PlanAddStepCmd) error {
	// Resolve the plan path with active job support
	planPath, err := resolvePlanPathWithActiveJob(cmd.Dir)
	if err != nil {
		return fmt.Errorf("could not resolve plan path: %w", err)
	}

	// For absolute paths, use them directly (important for tests)
	if filepath.IsAbs(cmd.Dir) {
		planPath = cmd.Dir
	}

	// Create plan directory if it doesn't exist
	if _, err := os.Stat(planPath); os.IsNotExist(err) {
		if err := os.MkdirAll(planPath, 0755); err != nil {
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
		fmt.Println(theme.DefaultTheme.Success.Render("✓") + " Added " + fmt.Sprintf("%d jobs from recipe '%s':", len(newFiles), cmd.Recipe))
		for _, file := range newFiles {
			fmt.Println("  - " + file)
		}
		return nil
	}

	// Use explicit worktree from command line flag only
	worktreeToUse := cmd.Worktree

	var job *orchestration.Job

	if cmd.Template != "" {
		// New logic for handling templates
		templateManager := orchestration.NewTemplateManager()
		template, err := templateManager.FindTemplate(cmd.Template)
		if err != nil {
			return err
		}
		job, err = collectJobDetailsFromTemplate(cmd, plan, template, worktreeToUse)
	} else {
		// Existing logic
		job, err = collectJobDetails(cmd, plan, worktreeToUse)
	}

	if err != nil {
		return err
	}

	if job == nil {
		return fmt.Errorf("failed to create job: no job details collected")
	}

	// Generate job file
	filename, err := orchestration.AddJob(plan, job)
	if err != nil {
		return fmt.Errorf("failed to add job: %w", err)
	}

	// Display success
	fmt.Println(theme.DefaultTheme.Success.Render("✓") + " Created " + filename)
	fmt.Println("\nNext steps:")
	fmt.Println("- Review the job file")
	fmt.Printf("- Run with: flow plan run %s/%s\n", cmd.Dir, filename)

	return nil
}

func collectJobDetails(cmd *PlanAddStepCmd, plan *orchestration.Plan, worktreeToUse string) (*orchestration.Job, error) {
	// Auto-detect worktree context if not explicitly provided
	if worktreeToUse == "" {
		currentNode, err := workspace.GetProjectByPath(".")
		if err == nil && currentNode != nil && currentNode.IsWorktree() && currentNode.RootEcosystemPath != "" {
			// This is an ecosystem worktree context. Find the name of the ecosystem worktree.
			// This is typically the base name of the ParentEcosystemPath for a sub-project worktree.
			if currentNode.ParentEcosystemPath != "" && strings.Contains(currentNode.ParentEcosystemPath, ".grove-worktrees") {
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

	if cmd.Type != "oneshot" && cmd.Type != "agent" && cmd.Type != "chat" && cmd.Type != "shell" && cmd.Type != "interactive_agent" && cmd.Type != "headless_agent" {
		return nil, fmt.Errorf("invalid job type: must be oneshot, agent, chat, shell, interactive_agent, or headless_agent")
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
		}

		// Parse inline config from CLI flags
		inlineConfig := parseInlineFlag(cmd.Inline)

		job := &orchestration.Job{
			ID:                  jobID,
			Title:               cmd.Title,
			Type:                orchestration.JobType(cmd.Type),
			Status:              status,
			DependsOn:           cmd.DependsOn,
			Include:             relativeIncludeFiles,
			Model:               cmd.Model,
			Inline:              inlineConfig,
			PrependDependencies: cmd.PrependDependencies, // Keep for backwards compat
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

		// Apply plan-level defaults if not set
		if job.Model == "" && plan.Config != nil && plan.Config.Model != "" {
			job.Model = plan.Config.Model
		}
		if job.Worktree == "" && plan.Config != nil && plan.Config.Worktree != "" {
			job.Worktree = plan.Config.Worktree
		}
		// Apply inline defaults from plan config if not explicitly set via flag
		if job.Inline.IsEmpty() && plan.Config != nil && !plan.Config.Inline.IsEmpty() {
			job.Inline = plan.Config.Inline
		}
		// Backwards compat: apply prepend_dependencies default if not explicitly set
		if !cmd.PrependDependencies && job.Inline.IsEmpty() && plan.Config != nil && plan.Config.PrependDependencies {
			job.PrependDependencies = true
		}

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

	// Require a prompt if no template was used
	if prompt == "" && cmd.Template == "" {
		return nil, fmt.Errorf("either a prompt or template is required")
	}

	// Generate job ID
	jobID := orchestration.GenerateUniqueJobID(plan, cmd.Title)

	// Build job structure
	status := orchestration.JobStatusPending
	if cmd.Type == "chat" {
		status = orchestration.JobStatusPendingUser
	}

	// Parse inline config from CLI flags
	inlineConfig := parseInlineFlag(cmd.Inline)

	job := &orchestration.Job{
		ID:                  jobID,
		Title:               cmd.Title,
		Type:                orchestration.JobType(cmd.Type),
		Status:              status,
		DependsOn:           cmd.DependsOn,
		PromptBody:          strings.TrimSpace(prompt),
		Model:               cmd.Model,
		Inline:              inlineConfig,
		PrependDependencies: cmd.PrependDependencies, // Keep for backwards compat
	}

	// Set worktree only if explicitly provided
	if worktreeToUse != "" {
		job.Worktree = worktreeToUse
	}

	// Apply plan-level defaults if not set
	if job.Model == "" && plan.Config != nil && plan.Config.Model != "" {
		job.Model = plan.Config.Model
	}
	if job.Worktree == "" && plan.Config != nil && plan.Config.Worktree != "" {
		job.Worktree = plan.Config.Worktree
	}
	// Apply inline defaults from plan config if not explicitly set via flag
	if job.Inline.IsEmpty() && plan.Config != nil && !plan.Config.Inline.IsEmpty() {
		job.Inline = plan.Config.Inline
	}
	// Backwards compat: apply prepend_dependencies default if not explicitly set
	if !cmd.PrependDependencies && job.Inline.IsEmpty() && plan.Config != nil && plan.Config.PrependDependencies {
		job.PrependDependencies = true
	}

	return job, nil
}

func interactiveJobCreation(plan *orchestration.Plan, cmd *PlanAddStepCmd) (*orchestration.Job, error) {
	// Create the initial TUI model
	model := initialModel(plan, cmd.DependsOn)

	// Note: worktree is no longer configurable in the TUI
	// The explicitWorktree parameter is now part of the cmd struct and is ignored.

	// Run the TUI
	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("error running TUI for job creation: %w", err)
	}

	// Cast the final model and check if the user quit
	finalTUIModel := finalModel.(tuiModel)
	if finalTUIModel.quitting && finalTUIModel.jobTitle == "" {
		return nil, fmt.Errorf("job creation cancelled")
	}

	// Convert the final TUI state into a Job object
	job := finalTUIModel.toJob(plan)

	// Validate the job
	if job.Title == "" {
		return nil, fmt.Errorf("title cannot be empty")
	}

	// Apply plan defaults if not set by user
	if job.Model == "" && plan.Config != nil && plan.Config.Model != "" {
		job.Model = plan.Config.Model
	}
	if job.Worktree == "" && plan.Config != nil && plan.Config.Worktree != "" {
		job.Worktree = plan.Config.Worktree
	}
	// Apply inline defaults from plan config
	if job.Inline.IsEmpty() && plan.Config != nil && !plan.Config.Inline.IsEmpty() {
		job.Inline = plan.Config.Inline
	}
	// Backwards compat: apply prepend_dependencies default from plan config
	if !job.PrependDependencies && job.Inline.IsEmpty() && plan.Config != nil && plan.Config.PrependDependencies {
		job.PrependDependencies = true
	}

	return job, nil
}

func selectDependencies(plan *orchestration.Plan, reader *bufio.Reader) ([]string, error) {
	if len(plan.Jobs) == 0 {
		return nil, nil
	}

	fmt.Println("\nDependencies (enter job numbers separated by commas, or press enter for none):")

	// List available jobs
	jobMap := make(map[int]*orchestration.Job)
	i := 1
	for _, job := range plan.Jobs {
		fmt.Printf("%d. %s (%s)\n", i, job.Title, job.Filename)
		jobMap[i] = job
		i++
	}

	fmt.Print("Selection: ")
	selection, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read selection: %w", err)
	}
	selection = strings.TrimSpace(selection)

	if selection == "" {
		return nil, nil
	}

	// Parse selections
	var deps []string
	parts := strings.Split(selection, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		var num int
		if _, err := fmt.Sscanf(part, "%d", &num); err == nil {
			if job, ok := jobMap[num]; ok {
				deps = append(deps, job.Filename)
			}
		}
	}

	return deps, nil
}

func collectJobDetailsFromTemplate(cmd *PlanAddStepCmd, plan *orchestration.Plan, template *orchestration.JobTemplate, worktreeToUse string) (*orchestration.Job, error) {
	// Auto-detect worktree context if not explicitly provided
	if worktreeToUse == "" {
		currentNode, err := workspace.GetProjectByPath(".")
		if err == nil && currentNode != nil && currentNode.IsWorktree() && currentNode.RootEcosystemPath != "" {
			// This is an ecosystem worktree context. Find the name of the ecosystem worktree.
			// This is typically the base name of the ParentEcosystemPath for a sub-project worktree.
			if currentNode.ParentEcosystemPath != "" && strings.Contains(currentNode.ParentEcosystemPath, ".grove-worktrees") {
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
		Title:  cmd.Title,
		Status: orchestration.JobStatusPending,
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
	if genPlan, ok := template.Frontmatter["generate_plan_from"].(bool); ok {
		job.GeneratePlanFrom = genPlan
	}
	if prependDeps, ok := template.Frontmatter["prepend_dependencies"].(bool); ok {
		job.PrependDependencies = prependDeps
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
	if cmd.Type != "" && cmd.Type != "agent" { // "agent" is the default, so only override if explicitly set
		job.Type = orchestration.JobType(cmd.Type)
	}
	if len(cmd.DependsOn) > 0 {
		job.DependsOn = cmd.DependsOn
	}
	if cmd.Model != "" {
		job.Model = cmd.Model
	}
	// New inline flag overrides template and plan defaults
	if len(cmd.Inline) > 0 {
		job.Inline = parseInlineFlag(cmd.Inline)
	}
	// Backwards compat: prepend_dependencies flag
	if cmd.PrependDependencies {
		job.PrependDependencies = true
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
		var includeFiles []string

		// Only use include files, not prompt files
		includeFiles = cmd.IncludeFiles

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

	// Apply plan-level defaults if not set (CLI > Template > Plan config)
	if job.Model == "" && plan.Config != nil && plan.Config.Model != "" {
		job.Model = plan.Config.Model
	}
	if job.Worktree == "" && plan.Config != nil && plan.Config.Worktree != "" {
		job.Worktree = plan.Config.Worktree
	}
	// Apply inline defaults from plan config if not explicitly set via flag or template
	if job.Inline.IsEmpty() && plan.Config != nil && !plan.Config.Inline.IsEmpty() {
		job.Inline = plan.Config.Inline
	}
	// Backwards compat: apply prepend_dependencies default if not explicitly set via flag
	if !cmd.PrependDependencies && job.Inline.IsEmpty() && plan.Config != nil && plan.Config.PrependDependencies {
		job.PrependDependencies = true
	}

	return job, nil
}
