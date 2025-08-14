package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
)

type PlanAddStepCmd struct {
	Dir         string   `arg:"" help:"Plan directory"`
	Template    string   `flag:"" help:"Name of the job template to use"`
	Type        string   `flag:"t" default:"agent" help:"Job type: oneshot, agent, interactive_agent, or shell"`
	Title       string   `flag:"" help:"Job title"`
	DependsOn   []string `flag:"d" help:"Dependencies (job filenames)"`
	PromptFile  string   `flag:"f" help:"File containing the prompt (DEPRECATED: use --source-files)"`
	SourceFiles []string `flag:"" sep:"," help:"Comma-separated list of source files for reference-based prompts"`
	Prompt      string   `flag:"p" help:"Inline prompt text"`
	OutputType  string   `flag:"" default:"file" help:"Output type: file, commit, none, or generate_jobs"`
	Interactive bool     `flag:"i" help:"Interactive mode"`
}

func (c *PlanAddStepCmd) Run() error {
	return RunPlanAddStep(c)
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

	// Smart worktree inheritance logic
	var inheritedWorktree string
	if len(cmd.DependsOn) > 0 {
		// Check worktrees of dependencies
		worktrees := make(map[string]bool)
		for _, depFilename := range cmd.DependsOn {
			// Find the dependency job
			for _, depJob := range plan.Jobs {
				if depJob.Filename == depFilename && depJob.Worktree != "" {
					worktrees[depJob.Worktree] = true
				}
			}
		}
		
		// If all dependencies share the same worktree, inherit it
		if len(worktrees) == 1 {
			for wt := range worktrees {
				inheritedWorktree = wt
			}
		} else if len(worktrees) > 1 {
			// Multiple different worktrees found
			fmt.Printf("⚠️  Warning: Dependencies have conflicting worktrees. Please specify --worktree manually.\n")
		}
	}

	var job *orchestration.Job

	if cmd.Template != "" {
		// New logic for handling templates
		templateManager := orchestration.NewTemplateManager()
		template, err := templateManager.FindTemplate(cmd.Template)
		if err != nil {
			return err
		}
		job, err = collectJobDetailsFromTemplate(cmd, plan, template, inheritedWorktree)
	} else {
		// Existing logic
		job, err = collectJobDetails(cmd, plan, inheritedWorktree)
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
	successStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	fmt.Println(successStyle.Render("✓") + " Created " + filename)
	fmt.Println("\nNext steps:")
	fmt.Println("- Review the job file")
	fmt.Printf("- Run with: flow plan run %s/%s\n", cmd.Dir, filename)

	return nil
}

func collectJobDetails(cmd *PlanAddStepCmd, plan *orchestration.Plan, inheritedWorktree string) (*orchestration.Job, error) {
	if cmd.Interactive {
		return interactiveJobCreation(plan)
	}

	// Validate non-interactive inputs
	if cmd.Title == "" {
		return nil, fmt.Errorf("title is required (use --title or -i for interactive mode)")
	}

	if cmd.Type != "oneshot" && cmd.Type != "agent" && cmd.Type != "shell" && cmd.Type != "interactive_agent" {
		return nil, fmt.Errorf("invalid job type: must be oneshot, agent, shell, or interactive_agent")
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

	// Check if we should use reference-based prompts (even without a template)
	if len(cmd.SourceFiles) > 0 {
		// Reference-based prompt handling
		projectRoot, err := orchestration.GetProjectRoot()
		if err != nil {
			return nil, fmt.Errorf("failed to get project root: %w", err)
		}
		
		// Convert to relative paths
		var relativeSourceFiles []string
		for _, file := range cmd.SourceFiles {
			// Resolve the file path
			resolvedPath, err := orchestration.ResolvePromptSource(file, plan)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve source file %s: %w", file, err)
			}
			
			// If the file is in the plan directory, use just the relative path from plan
			if strings.HasPrefix(resolvedPath, plan.Directory+string(filepath.Separator)) {
				relPath, _ := filepath.Rel(plan.Directory, resolvedPath)
				relativeSourceFiles = append(relativeSourceFiles, relPath)
			} else {
				// Otherwise, make it relative to project root
				relPath, err := filepath.Rel(projectRoot, resolvedPath)
				if err != nil {
					// If we can't make it relative, use the absolute path
					relPath = resolvedPath
				}
				relativeSourceFiles = append(relativeSourceFiles, relPath)
			}
		}
		
		// Generate job ID
		jobID := generateJobIDFromTitle(plan, cmd.Title)
		
		// Build job structure
		job := &orchestration.Job{
			ID:           jobID,
			Title:        cmd.Title,
			Type:         orchestration.JobType(cmd.Type),
			Status:       "pending",
			DependsOn:    cmd.DependsOn,
			PromptSource: relativeSourceFiles,
			Output: orchestration.OutputConfig{
				Type: cmd.OutputType,
			},
		}
		
		// Initialize empty prompt body - no comments needed since info is in frontmatter
		job.PromptBody = ""
		
		// Add user-provided prompt if any
		if cmd.Prompt != "" {
			if job.PromptBody == "" {
				job.PromptBody = cmd.Prompt
			} else {
				job.PromptBody = job.PromptBody + "\n\n## Additional Instructions\n\n" + cmd.Prompt
			}
		} else if len(relativeSourceFiles) == 0 {
			// If no source files and no prompt, this is an error
			return nil, fmt.Errorf("prompt is required when no source files are provided")
		}
		
		// Set worktree: use inherited worktree if available, otherwise default to plan name
		if job.Worktree == "" {
			if inheritedWorktree != "" {
				job.Worktree = inheritedWorktree
			} else {
				job.Worktree = filepath.Base(plan.Directory)
			}
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

	if prompt == "" {
		return nil, fmt.Errorf("prompt is required (use --prompt, --prompt-file, stdin, or -i for interactive mode)")
	}

	// Generate job ID
	jobID := generateJobIDFromTitle(plan, cmd.Title)

	// Build job structure
	job := &orchestration.Job{
		ID:        jobID,
		Title:     cmd.Title,
		Type:      orchestration.JobType(cmd.Type),
		Status:    "pending",
		DependsOn: cmd.DependsOn,
		PromptBody: strings.TrimSpace(prompt),
		Output: orchestration.OutputConfig{
			Type: cmd.OutputType,
		},
	}

	// Set worktree: use inherited worktree if available, otherwise default to plan name
	// The plan name is the base name of the plan directory
	if job.Worktree == "" {
		if inheritedWorktree != "" {
			job.Worktree = inheritedWorktree
		} else {
			job.Worktree = filepath.Base(plan.Directory)
		}
	}

	return job, nil
}

func interactiveJobCreation(plan *orchestration.Plan) (*orchestration.Job, error) {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("\n=== Add New Job ===")

	// Get title
	fmt.Print("Title: ")
	title, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read title: %w", err)
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("title cannot be empty")
	}

	// Select job type
	fmt.Println("\nJob Type:")
	fmt.Println("1. oneshot - Single LLM call")
	fmt.Println("2. agent - Long-running agent")
	fmt.Println("3. interactive_agent - Interactive agent session")
	fmt.Print("Choice [1-3]: ")
	
	choice, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read choice: %w", err)
	}
	choice = strings.TrimSpace(choice)
	
	jobType := "oneshot"
	switch choice {
	case "2":
		jobType = "agent"
	case "3":
		jobType = "interactive_agent"
	}

	// Select dependencies
	deps, err := selectDependencies(plan, reader)
	if err != nil {
		return nil, fmt.Errorf("failed to select dependencies: %w", err)
	}

	// Get worktree name (optional)
	fmt.Print("\nWorktree name (optional): ")
	worktree, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read worktree: %w", err)
	}
	worktree = strings.TrimSpace(worktree)

	// Select output type
	fmt.Println("\nOutput type:")
	fmt.Println("1. file - Save to file")
	fmt.Println("2. commit - Create git commit")
	fmt.Println("3. none - No output")
	fmt.Println("4. generate_jobs - Generate new job files")
	fmt.Print("Choice [1-4]: ")
	
	choice, err = reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read choice: %w", err)
	}
	choice = strings.TrimSpace(choice)
	
	outputType := "file"
	switch choice {
	case "2":
		outputType = "commit"
	case "3":
		outputType = "none"
	case "4":
		outputType = "generate_jobs"
	}

	// Get prompt
	fmt.Println("\nEnter prompt (Ctrl+D to finish):")
	promptBytes, err := io.ReadAll(reader)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("failed to read prompt: %w", err)
	}
	prompt := strings.TrimSpace(string(promptBytes))
	if prompt == "" {
		return nil, fmt.Errorf("prompt cannot be empty")
	}

	// Generate job ID
	jobID := generateJobIDFromTitle(plan, title)

	// Build job
	job := &orchestration.Job{
		ID:         jobID,
		Title:      title,
		Type:       orchestration.JobType(jobType),
		Status:     "pending",
		DependsOn:  deps,
		PromptBody: prompt,
		Worktree:   worktree,
		Output: orchestration.OutputConfig{
			Type: outputType,
		},
	}
	
	// Set worktree to the plan name if not provided
	if job.Worktree == "" {
		job.Worktree = filepath.Base(plan.Directory)
	}

	// Show summary
	fmt.Println("\n--- Job Summary ---")
	fmt.Printf("Title: %s\n", job.Title)
	fmt.Printf("Type: %s\n", job.Type)
	if len(job.DependsOn) > 0 {
		fmt.Printf("Dependencies: %s\n", strings.Join(job.DependsOn, ", "))
	}
	if job.Worktree != "" {
		fmt.Printf("Worktree: %s\n", job.Worktree)
	}
	fmt.Printf("Output: %s\n", job.Output.Type)

	// Confirm
	fmt.Print("\nCreate job? [Y/n]: ")
	confirm, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read confirmation: %w", err)
	}
	confirm = strings.TrimSpace(strings.ToLower(confirm))
	if confirm != "" && confirm != "y" && confirm != "yes" {
		return nil, fmt.Errorf("cancelled by user")
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

func collectJobDetailsFromTemplate(cmd *PlanAddStepCmd, plan *orchestration.Plan, template *orchestration.JobTemplate, inheritedWorktree string) (*orchestration.Job, error) {
	// Title is required even with template
	if cmd.Title == "" {
		return nil, fmt.Errorf("title is required (use --title)")
	}

	// Apply template defaults
	job := &orchestration.Job{
		Title:  cmd.Title,
		Status: "pending",
	}
	
	// Use reflection or a helper to merge template.Frontmatter into the job struct
	// For simplicity, let's do it manually for key fields:
	if typ, ok := template.Frontmatter["type"].(string); ok {
		job.Type = orchestration.JobType(typ)
	}
	if output, ok := template.Frontmatter["output"].(map[string]interface{}); ok {
		if outputType, ok := output["type"].(string); ok {
			job.Output.Type = outputType
		}
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
	if promptSource, ok := template.Frontmatter["prompt_source"].([]interface{}); ok {
		for _, src := range promptSource {
			if srcStr, ok := src.(string); ok {
				job.PromptSource = append(job.PromptSource, srcStr)
			}
		}
	}

	// CLI flags override template defaults
	if cmd.Type != "" && cmd.Type != "agent" { // "agent" is the default, so only override if explicitly set
		job.Type = orchestration.JobType(cmd.Type)
	}
	if cmd.OutputType != "" && cmd.OutputType != "file" { // "file" is the default
		job.Output.Type = cmd.OutputType
	}
	if len(cmd.DependsOn) > 0 {
		job.DependsOn = cmd.DependsOn
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
	if true { // Always use reference-based prompts with templates
		// Reference-based prompt handling
		var sourceFiles []string
		
		// Only use source files, not prompt files
		sourceFiles = cmd.SourceFiles
		
		// Get project root
		projectRoot, err := orchestration.GetProjectRoot()
		if err != nil {
			return nil, fmt.Errorf("failed to get project root: %w", err)
		}
		
		// Convert to relative paths
		var relativeSourceFiles []string
		for _, file := range sourceFiles {
			// Resolve the file path
			resolvedPath, err := orchestration.ResolvePromptSource(file, plan)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve source file %s: %w", file, err)
			}
			
			// If the file is in the plan directory, use just the relative path from plan
			if strings.HasPrefix(resolvedPath, plan.Directory+string(filepath.Separator)) {
				relPath, _ := filepath.Rel(plan.Directory, resolvedPath)
				relativeSourceFiles = append(relativeSourceFiles, relPath)
			} else {
				// Otherwise, make it relative to project root
				relPath, err := filepath.Rel(projectRoot, resolvedPath)
				if err != nil {
					// If we can't make it relative, use the absolute path
					relPath = resolvedPath
				}
				relativeSourceFiles = append(relativeSourceFiles, relPath)
			}
		}
		
		// Store template name and source files as metadata
		if len(relativeSourceFiles) > 0 {
			job.PromptSource = relativeSourceFiles
		}
		job.Template = template.Name
		
		// Initialize empty prompt body - no comments needed since info is in frontmatter
		job.PromptBody = ""
		
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
	job.ID = generateJobIDFromTitle(plan, job.Title)

	// Set worktree: use inherited worktree if available, otherwise default to plan name
	// The plan name is the base name of the plan directory
	if job.Worktree == "" {
		if inheritedWorktree != "" {
			job.Worktree = inheritedWorktree
		} else {
			job.Worktree = filepath.Base(plan.Directory)
		}
	}

	return job, nil
}

func generateJobIDFromTitle(plan *orchestration.Plan, title string) string {
	// Convert title to ID format
	base := strings.ToLower(title)
	base = strings.ReplaceAll(base, " ", "-")
	base = strings.ReplaceAll(base, "_", "-")
	
	// Remove non-alphanumeric characters
	var cleaned strings.Builder
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			cleaned.WriteRune(r)
		}
	}
	
	id := cleaned.String()
	
	// Ensure uniqueness
	exists := false
	for _, job := range plan.Jobs {
		if job.ID == id {
			exists = true
			break
		}
	}
	
	if exists {
		// Add number suffix
		for i := 2; ; i++ {
			testID := fmt.Sprintf("%s-%d", id, i)
			found := false
			for _, job := range plan.Jobs {
				if job.ID == testID {
					found = true
					break
				}
			}
			if !found {
				return testID
			}
		}
	}
	
	return id
}