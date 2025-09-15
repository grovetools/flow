package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/mattsolo1/grove-core/git"
	"github.com/mattsolo1/grove-flow/pkg/exec"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-flow/pkg/state"
)

// RunPlanInit implements the plan init command.
func RunPlanInit(cmd *PlanInitCmd) error {
	// Resolve the full path for the new plan directory.
	planDirArg := cmd.Dir
	planPath, err := resolvePlanPath(planDirArg)
	if err != nil {
		return fmt.Errorf("could not resolve plan path: %w", err)
	}

	// The canonical plan name is the base name of the directory argument.
	planName := filepath.Base(planDirArg)

	// Validate inputs with resolved path
	if err := validateInitInputs(cmd, planPath); err != nil {
		return err
	}

	// NEW: Recipe-based initialization (can be combined with extraction)
	if cmd.Recipe != "" {
		return runPlanInitFromRecipe(cmd, planPath, planName)
	}

	// Determine worktree to set in config
	worktreeToSet := cmd.Worktree
	if cmd.WithWorktree && worktreeToSet == "" {
		worktreeToSet = planName
	}

	// Only use the model if explicitly provided via --model flag
	effectiveModel := cmd.Model

	// Create directory using the resolved path
	if err := createPlanDirectory(planPath, cmd.Force); err != nil {
		return err
	}

	// Create default .grove-plan.yml
	if err := createDefaultPlanConfig(planPath, effectiveModel, worktreeToSet, cmd.Container); err != nil {
		// Log a warning but don't fail the init command
		fmt.Printf("Warning: failed to create .grove-plan.yml: %v\n", err)
	}

	// Print success message
	fmt.Printf("Initializing orchestration plan in:\n  %s\n\n", planPath)
	fmt.Println("✓ Created plan directory")
	fmt.Println("✓ Created .grove-plan.yml with default configuration")

	// Set the new plan as active
	if err := state.SetActiveJob(planName); err != nil {
		// Just warn if we can't set active job, don't fail the init
		fmt.Printf("Warning: failed to set active job: %v\n", err)
	} else {
		fmt.Printf("✓ Set active plan to: %s\n", planName)
	}

	// Extraction Logic
	if cmd.ExtractAllFrom != "" {
		// 1. Load the plan we just created
		plan, err := orchestration.LoadPlan(planPath)
		if err != nil {
			return fmt.Errorf("failed to reload plan for extraction: %w", err)
		}

		// 2. Read the source file
		content, err := os.ReadFile(cmd.ExtractAllFrom)
		if err != nil {
			return fmt.Errorf("failed to read source file for extraction %s: %w", cmd.ExtractAllFrom, err)
		}

		// 3. Extract body
		_, body, err := orchestration.ParseFrontmatter(content)
		if err != nil {
			return fmt.Errorf("failed to parse frontmatter from source file %s: %w", cmd.ExtractAllFrom, err)
		}

		// 4. Create a new job
		jobTitle := strings.TrimSuffix(filepath.Base(cmd.ExtractAllFrom), filepath.Ext(filepath.Base(cmd.ExtractAllFrom)))
		job := &orchestration.Job{
			Title:      jobTitle,
			Type:       orchestration.JobTypeChat, // Extracts become chat jobs
			Status:     orchestration.JobStatusPending,
			ID:         GenerateJobIDFromTitle(plan, jobTitle),
			PromptBody: string(body),
		}
		
		// Apply worktree from plan config if set
		if plan.Config != nil && plan.Config.Worktree != "" {
			job.Worktree = plan.Config.Worktree
		}

		// 5. Add the job to the plan
		filename, err := orchestration.AddJob(plan, job)
		if err != nil {
			return fmt.Errorf("failed to add extracted job to plan: %w", err)
		}
		fmt.Printf("✓ Extracted content from %s to new job: %s\n", cmd.ExtractAllFrom, filename)
	}

	// Open Session Logic
	if cmd.OpenSession {
		fmt.Println("\n🚀 Launching new session...")

		ctx := context.Background()
		commandToRun := []string{"flow", "plan", "status", "-t"}
		
		if worktreeToSet != "" {
			// Launch session with worktree - need to create a minimal plan object
			plan := &orchestration.Plan{
				Name:      planName,
				Directory: planPath,
			}
			if err := CreateOrSwitchToWorktreeSessionAndRunCommand(ctx, plan, worktreeToSet, commandToRun); err != nil {
				// Log the error but don't fail the init command, as the primary goal was completed
				fmt.Printf("⚠️  Warning: Failed to launch tmux session: %v\n", err)
				fmt.Printf("   You can launch it manually later with `flow plan open`\n")
			}
		} else {
			// Launch session without worktree (in main repo)
			if err := CreateOrSwitchToMainRepoSessionAndRunCommand(ctx, planName, commandToRun); err != nil {
				fmt.Printf("⚠️  Warning: Failed to launch tmux session: %v\n", err)
				fmt.Printf("   You can launch it manually later with `flow plan status -t`\n")
			}
		}
	} else if cmd.ExtractAllFrom != "" {
		// If we extracted but didn't open a session, give next steps.
		fmt.Println("\nNext steps:")
		fmt.Printf("1. Open the session: flow plan launch <job-file>\n")
		return nil
	}

	fmt.Println("\nNext steps:")
	fmt.Printf("1. Add your first job: flow plan add\n")
	fmt.Printf("2. Check status: flow plan status\n")

	return nil
}

// runPlanInitFromRecipe initializes a plan from a predefined recipe.
func runPlanInitFromRecipe(cmd *PlanInitCmd, planPath string, planName string) error {

	// Find the recipe (checks user recipes first, then built-in)
	recipe, err := orchestration.GetRecipe(cmd.Recipe)
	if err != nil {
		return err
	}

	// Create the plan directory
	if err := createPlanDirectory(planPath, cmd.Force); err != nil {
		return err
	}

	fmt.Printf("Initializing orchestration plan in:\n  %s\n\n", planPath)
	fmt.Printf("✓ Using recipe: %s\n", cmd.Recipe)

	// Prepare extracted content if provided
	var extractedBody []byte
	if cmd.ExtractAllFrom != "" {
		// Read the source file
		content, err := os.ReadFile(cmd.ExtractAllFrom)
		if err != nil {
			return fmt.Errorf("failed to read source file for extraction %s: %w", cmd.ExtractAllFrom, err)
		}

		// Extract body (remove any existing frontmatter)
		_, body, err := orchestration.ParseFrontmatter(content)
		if err != nil {
			return fmt.Errorf("failed to parse frontmatter from source file %s: %w", cmd.ExtractAllFrom, err)
		}
		
		extractedBody = body
		fmt.Printf("✓ Extracted content from %s\n", cmd.ExtractAllFrom)
	}

	// Data for templating
	templateData := struct {
		PlanName string
	}{
		PlanName: planName,
	}

	// Get sorted list of job filenames to process them in order
	var jobFiles []string
	for filename := range recipe.Jobs {
		jobFiles = append(jobFiles, filename)
	}
	sort.Strings(jobFiles)

	var firstAgentWorktree string

	// Track if this is the first job for content merging
	isFirstJob := true
	
	// Process each job file from the recipe
	for _, filename := range jobFiles {
		renderedContent, err := recipe.RenderJob(filename, templateData)
		if err != nil {
			return fmt.Errorf("rendering recipe job %s: %w", filename, err)
		}
		
		// If we have extracted content and this is the first job, merge them
		if extractedBody != nil && isFirstJob {
			// Parse the recipe job's frontmatter
			frontmatter, _, err := orchestration.ParseFrontmatter(renderedContent)
			if err != nil {
				return fmt.Errorf("parsing frontmatter from recipe job %s: %w", filename, err)
			}
			
			// Apply worktree if needed
			if cmd.WithWorktree {
				frontmatter["worktree"] = planName
			} else if cmd.Worktree != "" {
				frontmatter["worktree"] = cmd.Worktree
			}
			
			// Rebuild with recipe's frontmatter but extracted content as body
			renderedContent, err = orchestration.RebuildMarkdownWithFrontmatter(frontmatter, extractedBody)
			if err != nil {
				return fmt.Errorf("rebuilding job with extracted content: %w", err)
			}
			
			fmt.Printf("✓ Merged extracted content into job: %s\n", filename)
			isFirstJob = false
		} else {
			fmt.Printf("✓ Created job: %s\n", filename)
		}

		// Write the processed job file to the new plan directory
		destPath := filepath.Join(planPath, filename)
		if err := os.WriteFile(destPath, renderedContent, 0644); err != nil {
			return fmt.Errorf("writing recipe job file %s: %w", filename, err)
		}

		// Heuristic: find the worktree from the first agent/interactive_agent job
		// to set as the default in .grove-plan.yml
		if firstAgentWorktree == "" {
			fm, _, _ := orchestration.ParseFrontmatter(renderedContent)
			if fm != nil {
				if jobType, ok := fm["type"].(string); ok && (jobType == "agent" || jobType == "interactive_agent") {
					if worktree, ok := fm["worktree"].(string); ok {
						firstAgentWorktree = worktree
					}
				}
			}
		}
	}

	// Determine the final worktree to use in .grove-plan.yml
	finalWorktree := firstAgentWorktree
	if cmd.WithWorktree {
		// --with-worktree flag takes precedence
		finalWorktree = planName
	} else if cmd.Worktree != "" {
		// Explicit --worktree flag takes precedence
		finalWorktree = cmd.Worktree
	}
	
	// Create a default .grove-plan.yml, using the determined worktree
	if err := createDefaultPlanConfig(planPath, cmd.Model, finalWorktree, cmd.Container); err != nil {
		fmt.Printf("Warning: failed to create .grove-plan.yml: %v\n", err)
	} else {
		fmt.Println("✓ Created .grove-plan.yml")
	}

	// Set the new plan as active
	if err := state.SetActiveJob(planName); err != nil {
		fmt.Printf("Warning: failed to set active job: %v\n", err)
	} else {
		fmt.Printf("✓ Set active plan to: %s\n", planName)
	}

	// Handle --open-session for recipe flow
	if cmd.OpenSession {
		fmt.Println("\n🚀 Launching new session...")
		
		ctx := context.Background()
		commandToRun := []string{"flow", "plan", "status", "-t"}
		worktreeToSet := finalWorktree
		
		if worktreeToSet != "" {
			// Launch session with worktree - need to create a minimal plan object
			plan := &orchestration.Plan{
				Name:      planName,
				Directory: planPath,
			}
			if err := CreateOrSwitchToWorktreeSessionAndRunCommand(ctx, plan, worktreeToSet, commandToRun); err != nil {
				fmt.Printf("⚠️  Warning: Failed to launch tmux session: %v\n", err)
				fmt.Printf("   You can launch it manually later with `flow plan open`\n")
			}
		} else {
			// Launch session without worktree (in main repo)
			if err := CreateOrSwitchToMainRepoSessionAndRunCommand(ctx, planName, commandToRun); err != nil {
				fmt.Printf("⚠️  Warning: Failed to launch tmux session: %v\n", err)
				fmt.Printf("   You can launch it manually later with `flow plan status -t`\n")
			}
		}
	} else {
		fmt.Println("\nNext steps:")
		fmt.Printf("1. Review the generated job files in %s\n", planPath)
		fmt.Printf("2. Run the plan: flow plan run %s\n", planName)
	}

	return nil
}

// validateInitInputs validates the command inputs.
func validateInitInputs(cmd *PlanInitCmd, resolvedPath string) error {
	// Validate directory name
	if err := validateDirectoryName(cmd.Dir); err != nil {
		return err
	}

	// Check if directory exists
	if _, err := os.Stat(resolvedPath); err == nil && !cmd.Force {
		return fmt.Errorf("directory '%s' already exists (use --force to overwrite)", resolvedPath)
	}

	return nil
}

// validateDirectoryName checks if the directory name is valid.
func validateDirectoryName(name string) error {
	if name == "" {
		return fmt.Errorf("directory name cannot be empty")
	}

	// Check for illegal characters
	illegalChars := regexp.MustCompile(`[<>:"|?*\x00-\x1f]`)
	if illegalChars.MatchString(name) {
		return fmt.Errorf("invalid directory name: contains illegal characters")
	}

	return nil
}

// createPlanDirectory creates the plan directory.
func createPlanDirectory(dir string, force bool) error {
	// Remove existing directory if force is true
	if force {
		if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove existing directory: %w", err)
		}
	}

	// Create directory
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	return nil
}

// createDefaultPlanConfig creates a default .grove-plan.yml file in the plan directory.
func createDefaultPlanConfig(planPath, model, worktree, container string) error {
	var configContent strings.Builder

	configContent.WriteString("# Default model for jobs in this plan\n")
	if model != "" {
		configContent.WriteString(fmt.Sprintf("model: %s\n", model))
	} else {
		configContent.WriteString("# model: gemini-2.5-pro\n")
	}
	configContent.WriteString("\n")

	configContent.WriteString("# Default worktree for agent jobs\n")
	if worktree != "" {
		configContent.WriteString(fmt.Sprintf("worktree: %s\n", worktree))
	} else {
		configContent.WriteString("# worktree: feature-branch\n")
	}
	configContent.WriteString("\n")

	configContent.WriteString("# Default container for agent jobs\n")
	if container != "" {
		configContent.WriteString(fmt.Sprintf("target_agent_container: %s\n", container))
	} else {
		configContent.WriteString("# target_agent_container: grove-agent-ide\n")
	}

	configPath := filepath.Join(planPath, ".grove-plan.yml")
	return os.WriteFile(configPath, []byte(configContent.String()), 0644)
}

// createInitialPlanJob creates the first job file and returns the filename.
func createInitialPlanJob(dir string, model string, outputType string, templateName string, specContent string) (string, error) {
	// Generate job ID
	jobID := generateJobID()

	// Always include model field, use a default if not specified
	if model == "" {
		model = "claude-3-5-sonnet-20241022"
	}

	var content []byte
	var filename string

	// Use template if provided
	if templateName != "" {
		// Check if this is a chat-based template
		isChatTemplate := templateName == "chat"

		if isChatTemplate {
			// For chat templates, create a single plan.md file
			templateManager := orchestration.NewTemplateManager()
			_, err := templateManager.FindTemplate(templateName)
			if err != nil {
				return "", fmt.Errorf("could not load template '%s': %w", templateName, err)
			}

			// spec content is already provided as parameter

			// Create frontmatter for the chat job
			frontmatter := map[string]interface{}{
				"id":     jobID,
				"title":  "plan-chat",
				"status": "pending",
				"type":   "chat",
				"model":  model,
			}

			// Combine frontmatter, spec content, and template prompt
			var contentBuilder strings.Builder

			// Write frontmatter
			frontmatterBytes, err := orchestration.RebuildMarkdownWithFrontmatter(frontmatter, nil)
			if err != nil {
				return "", fmt.Errorf("create frontmatter: %w", err)
			}
			contentBuilder.Write(frontmatterBytes)

			// Add initial directive
			contentBuilder.WriteString("\n")
			contentBuilder.WriteString("<!-- grove: {\"template\": \"chat\"} -->\n")
			contentBuilder.WriteString("\n")

			// Add spec content as the initial user prompt
			contentBuilder.WriteString(specContent)
			if !strings.HasSuffix(specContent, "\n") {
				contentBuilder.WriteString("\n")
			}

			// Write to plan.md
			filename = "plan.md"
			planPath := filepath.Join(dir, filename)
			if err := os.WriteFile(planPath, []byte(contentBuilder.String()), 0644); err != nil {
				return "", fmt.Errorf("write plan.md: %w", err)
			}

			return filename, nil
		}

		// Regular template handling (non-chat)
		templateManager := orchestration.NewTemplateManager()
		_, err := templateManager.FindTemplate(templateName)
		if err != nil {
			return "", fmt.Errorf("could not load template '%s': %w", templateName, err)
		}

		// Use reference-based approach - don't inject template content
		frontmatter := make(map[string]interface{})

		// Set/override dynamic values
		frontmatter["id"] = jobID
		frontmatter["status"] = "pending"
		frontmatter["model"] = model
		frontmatter["template"] = templateName // Store template reference

		// Only set prompt_source if spec file was provided
		if specContent != "" && specContent != "# Spec\n\n" {
			// Write spec content to spec.md file
			specPath := filepath.Join(dir, "spec.md")
			if err := os.WriteFile(specPath, []byte(specContent), 0644); err != nil {
				return "", fmt.Errorf("write spec file: %w", err)
			}
			frontmatter["prompt_source"] = []string{"spec.md"}
		}

		// Set title based on template name
		words := strings.Split(templateName, "-")
		for i, word := range words {
			if len(word) > 0 {
				words[i] = strings.ToUpper(word[:1]) + word[1:]
			}
		}
		frontmatter["title"] = strings.Join(words, " ")

		// For reference-based templates, the body is empty
		promptBody := ""

		content, err = orchestration.RebuildMarkdownWithFrontmatter(frontmatter, []byte(promptBody))
		if err != nil {
			return "", fmt.Errorf("failed to rebuild job content from template: %w", err)
		}

		// Generate filename based on template name or title
		if templateName != "" {
			filename = fmt.Sprintf("01-%s.md", templateName)
		} else if title, ok := frontmatter["title"].(string); ok {
			safeTitle := strings.ToLower(title)
			safeTitle = strings.ReplaceAll(safeTitle, " ", "-")
			safeTitle = strings.ReplaceAll(safeTitle, "_", "-")
			// Remove any characters that aren't alphanumeric or hyphens
			var cleaned strings.Builder
			for _, r := range safeTitle {
				if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
					cleaned.WriteRune(r)
				}
			}
			filename = fmt.Sprintf("01-%s.md", cleaned.String())
		} else {
			filename = "01-initial-job.md"
		}
	} else if outputType == "generate_jobs" {
		// Backward compatibility: use generate-plan template for generate_jobs
		templateManager := orchestration.NewTemplateManager()
		template, err := templateManager.FindTemplate("generate-plan")
		if err != nil {
			return "", fmt.Errorf("could not load 'generate-plan' template: %w", err)
		}

		// The body of the template is now our prompt content
		// Append the spec content to the template prompt
		promptBody := template.Prompt + "\n\n" + specContent

		// Reconstruct the job file content from the template's frontmatter and body
		var frontmatter map[string]interface{}
		if template.Frontmatter != nil {
			frontmatter = template.Frontmatter
		} else {
			frontmatter = make(map[string]interface{})
		}

		// Set/override dynamic values
		frontmatter["id"] = jobID
		frontmatter["title"] = "Create High-Level Implementation Plan"
		frontmatter["status"] = "pending"
		frontmatter["model"] = model
		// Add worktree to use the plan directory name
		frontmatter["worktree"] = filepath.Base(dir)

		content, err = orchestration.RebuildMarkdownWithFrontmatter(frontmatter, []byte(promptBody))
		if err != nil {
			return "", fmt.Errorf("failed to rebuild job content from template: %w", err)
		}

		filename = "01-high-level-plan.md"
	} else {
		// Create a simpler job for other output types
		modelField := fmt.Sprintf("\nmodel: %s", model)
		worktreeField := fmt.Sprintf("\nworktree: %s", filepath.Base(dir))
		content = []byte(fmt.Sprintf(`---
id: %s
title: "Initial Job"
status: pending
type: oneshot%s%s
output:
  type: %s
---

%s
`, jobID, modelField, worktreeField, outputType, specContent))

		filename = "01-initial-job.md"
	}

	// Write job file
	jobPath := filepath.Join(dir, filename)
	if err := os.WriteFile(jobPath, content, 0644); err != nil {
		return "", fmt.Errorf("write job file: %w", err)
	}

	return filename, nil
}

// generateJobID creates a unique job ID.
func generateJobID() string {
	// Use UUID for uniqueness
	id := uuid.New().String()
	// Take first 8 characters for brevity
	return "job-" + id[:8]
}

// copyFile copies a file from source to destination.
func copyFile(src, dst string) error {
	// Read source file
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read source file: %w", err)
	}

	// Create destination directory if needed
	dstDir := filepath.Dir(dst)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}

	// Write destination file
	if err := os.WriteFile(dst, data, 0644); err != nil {
		return fmt.Errorf("write destination file: %w", err)
	}

	return nil
}

// launchWorktreeSession is a helper to launch a tmux session for a worktree with container support, adapted from plan_launch and chat_launch.
// This is kept for backward compatibility with agent launch commands that require containers.
func launchWorktreeSession(ctx context.Context, worktreeName string, agentCommand string) error {
	// Load configuration
	flowCfg, err := loadFlowConfig()
	if err != nil {
		return err
	}
	fullCfg, err := loadFullConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	container := flowCfg.TargetAgentContainer
	if container == "" {
		return fmt.Errorf("'flow.target_agent_container' is not set in your grove.yml")
	}

	// Get git root
	gitRoot, err := git.GetGitRoot(".")
	if err != nil {
		return fmt.Errorf("could not find git root: %w", err)
	}

	// Prepare the worktree
	wm := git.NewWorktreeManager()
	worktreePath, err := wm.GetOrPrepareWorktree(ctx, gitRoot, worktreeName, "interactive")
	if err != nil {
		return fmt.Errorf("failed to prepare worktree: %w", err)
	}

	// Set up Go workspace and Canopy hooks
	_ = orchestration.SetupGoWorkspaceForWorktree(worktreePath, gitRoot)
	_ = configureCanopyHooks(worktreePath)

	// Prepare launch parameters
	repoName := filepath.Base(gitRoot)
	sessionTitle := SanitizeForTmuxSession(worktreeName)
	params := LaunchParameters{
		SessionName:      fmt.Sprintf("%s__%s", repoName, sessionTitle),
		Container:        container,
		HostWorktreePath: worktreePath,
		AgentCommand:     agentCommand,
	}

	// Calculate container work directory
	relPath, err := filepath.Rel(gitRoot, worktreePath)
	if err != nil {
		return fmt.Errorf("failed to calculate relative path: %w", err)
	}
	if fullCfg.Agent.MountWorkspaceAtHostPath {
		params.ContainerWorkDir = filepath.Join(gitRoot, relPath)
	} else {
		params.ContainerWorkDir = filepath.Join("/workspace", repoName, relPath)
	}

	executor := &exec.RealCommandExecutor{}
	return LaunchTmuxSession(executor, params)
}
