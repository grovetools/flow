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
	"github.com/mattsolo1/grove-core/pkg/workspace"
	"github.com/mattsolo1/grove-core/state"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
)

// RunPlanInitTUI launches the interactive TUI for creating a new plan.
func RunPlanInitTUI(dir string) error {
	flowCfg, err := loadFlowConfig()
	if err != nil {
		// Don't fail if config doesn't exist.
		flowCfg = &FlowConfig{}
	}
	var plansDir string
	if flowCfg.PlansDirectory != "" {
		plansDir, err = expandPath(flowCfg.PlansDirectory)
		if err != nil {
			return err
		}
	} else {
		// If not configured, plans are relative to CWD.
		plansDir, _ = os.Getwd()
	}

	// Create a pre-populated command object from CLI flags
	initialCmd := &PlanInitCmd{
		Dir:            dir,
		Model:          planInitModel,
		Worktree:       planInitWorktree,
		Container:      planInitContainer,
		ExtractAllFrom: planInitExtractAllFrom,
		OpenSession:    planInitOpenSession,
		Recipe:         planInitRecipe,
		RecipeVars:     planInitRecipeVars,
		RecipeCmd:      planInitRecipeCmd,
		Repos:          planInitRepos,
	}

	finalCmd, err := runPlanInitTUI(plansDir, initialCmd)
	if err != nil {
		if err == ErrTUIQuit {
			// User quit the TUI, this is not an error
			return nil
		}
		return err
	}
	return RunPlanInit(finalCmd)
}

// RunPlanInit implements the plan init command.
func RunPlanInit(cmd *PlanInitCmd) error {
	result, err := executePlanInit(cmd)
	if err != nil {
		return err
	}
	fmt.Print(result)
	return nil
}

// executePlanInit contains the core logic for initializing a plan and returns a result string.
func executePlanInit(cmd *PlanInitCmd) (string, error) {
	// Auto-detect worktree context when running inside a sub-project of an ecosystem worktree.
	currentNode, err := workspace.GetProjectByPath(".")
	if err == nil && currentNode.Kind == workspace.KindEcosystemWorktreeSubProjectWorktree {
		// If we are in this context, the worktree for any new plan should
		// automatically be the parent ecosystem worktree.
		if cmd.Worktree == "" || cmd.Worktree == "__AUTO__" {
			parentWorktreeName := filepath.Base(currentNode.ParentEcosystemPath)
			cmd.Worktree = parentWorktreeName
		}
	}

	// Resolve the full path for the new plan directory.
	planDirArg := cmd.Dir
	planPath, err := resolvePlanPath(planDirArg)
	if err != nil {
		return "", fmt.Errorf("could not resolve plan path: %w", err)
	}

	// The canonical plan name is the base name of the directory argument.
	planName := filepath.Base(planDirArg)

	// Validate inputs with resolved path
	if err := validateInitInputs(cmd, planPath); err != nil {
		return "", err
	}

	// Create a workspace provider to discover local repositories.
	discoveryService := workspace.NewDiscoveryService(nil) // logger is optional
	discoveryResult, err := discoveryService.DiscoverAll()
	if err != nil {
		fmt.Printf("⚠️  Warning: failed to discover workspaces for go.work generation: %v\n", err)
	}
	var provider *workspace.Provider
	if discoveryResult != nil {
		provider = workspace.NewProvider(discoveryResult)
	}

	var result strings.Builder

	// NEW: Recipe-based initialization (can be combined with extraction)
	if cmd.Recipe != "" {
		// Note: runPlanInitFromRecipe prints its own messages. This part could be refactored further
		// but for now we'll call it and assume it works for the CLI context.
		// To make it TUI-friendly, it would also need to return a result string.
		// For this implementation, we assume TUI will not use recipes initially.
		return "", runPlanInitFromRecipe(cmd, planPath, planName)
	}

	// Determine worktree to set in config
	worktreeToSet := cmd.Worktree
	isInheritedWorktree := false
	if currentNode != nil && currentNode.Kind == workspace.KindEcosystemWorktreeSubProjectWorktree {
		// We're in a sub-project worktree, so the worktree is inherited from the parent ecosystem
		if worktreeToSet != "" && worktreeToSet != "__AUTO__" {
			isInheritedWorktree = true
		}
	}
	if worktreeToSet == "__AUTO__" {
		// User used --worktree without a value, use plan name
		worktreeToSet = planName
	}

	// Create the actual worktree if requested (but skip if it's inherited)
	if worktreeToSet != "" && !isInheritedWorktree {
		// Use the workspace path from currentNode to find the git root
		var workspacePath string
		if currentNode != nil {
			workspacePath = currentNode.Path
		}
		worktreePath, err := createWorktreeIfRequested(worktreeToSet, cmd.Repos, workspacePath)
		if err != nil {
			return "", err
		}

		// After creating the worktree(s), apply default context rules.
		if err := applyDefaultContextRulesToWorktree(worktreePath, cmd.Repos); err != nil {
			fmt.Printf("⚠️  Warning: could not apply default context rules: %v\n", err)
		}

		// Configure go.work file for the worktree.
		if err := configureGoWorkspace(worktreePath, cmd.Repos, provider); err != nil {
			// This is not a fatal error, but the user should be aware of it.
			fmt.Printf("⚠️  Warning: could not configure go.work file: %v\n", err)
		}
	}

	// Only use the model if explicitly provided via --model flag
	effectiveModel := cmd.Model

	// Create directory using the resolved path
	if err := createPlanDirectory(planPath, cmd.Force); err != nil {
		return "", err
	}

	// Create default .grove-plan.yml
	if err := createDefaultPlanConfig(planPath, effectiveModel, worktreeToSet, cmd.Container, cmd.NoteRef, cmd.Repos); err != nil {
		result.WriteString(fmt.Sprintf("Warning: failed to create .grove-plan.yml: %v\n", err))
	}

	// Build success message
	result.WriteString(fmt.Sprintf("Initializing orchestration plan in:\n  %s\n\n", planPath))
	result.WriteString("✓ Created plan directory\n")
	if worktreeToSet != "" {
		result.WriteString(fmt.Sprintf("✓ Created worktree: %s\n", worktreeToSet))
	}
	result.WriteString("✓ Created .grove-plan.yml with default configuration\n")

	// Set the new plan as active
	if err := state.Set("flow.active_plan", planName); err != nil {
		result.WriteString(fmt.Sprintf("Warning: failed to set active job: %v\n", err))
	} else {
		result.WriteString(fmt.Sprintf("✓ Set active plan to: %s\n", planName))
	}

	// Note: note_ref enrichment is now handled by enrichJobFrontmatter() and enrichJob()
	// in both the recipe and extraction code paths, so no post-hoc updates are needed.

	// Extraction Logic
	if cmd.ExtractAllFrom != "" {
		// 1. Load the plan we just created
		plan, err := orchestration.LoadPlan(planPath)
		if err != nil {
			return "", fmt.Errorf("failed to reload plan for extraction: %w", err)
		}

		// 2. Read the source file
		content, err := os.ReadFile(cmd.ExtractAllFrom)
		if err != nil {
			return "", fmt.Errorf("failed to read source file for extraction %s: %w", cmd.ExtractAllFrom, err)
		}

		// 3. Extract body
		_, body, err := orchestration.ParseFrontmatter(content)
		if err != nil {
			return "", fmt.Errorf("failed to parse frontmatter from source file %s: %w", cmd.ExtractAllFrom, err)
		}

		// 4. Create a new job
		jobTitle := strings.TrimSuffix(filepath.Base(cmd.ExtractAllFrom), filepath.Ext(filepath.Base(cmd.ExtractAllFrom)))
		job := &orchestration.Job{
			Title:      jobTitle,
			Type:       orchestration.JobTypeChat, // Extracts become chat jobs
			Status:     orchestration.JobStatusPending,
			ID:         orchestration.GenerateUniqueJobID(plan, jobTitle),
			PromptBody: string(body),
		}

		// Enrich the job with common fields (worktree, repository, note_ref)
		var repoName, worktreeName string
		if currentNode != nil {
			repoName = currentNode.Name
		}
		if plan.Config != nil && plan.Config.Worktree != "" {
			worktreeName = plan.Config.Worktree
		}
		enrichOpts := JobEnrichmentOptions{
			NoteRef:    cmd.NoteRef,
			Repository: repoName,
			Worktree:   worktreeName,
			IsFirstJob: true, // Extraction creates the first job
		}
		enrichJob(job, enrichOpts)

		// 5. Add the job to the plan
		filename, err := orchestration.AddJob(plan, job)
		if err != nil {
			return "", fmt.Errorf("failed to add extracted job to plan: %w", err)
		}
		result.WriteString(fmt.Sprintf("✓ Extracted content from %s to new job: %s\n", cmd.ExtractAllFrom, filename))
	}

	// Open Session Logic
	if cmd.OpenSession {
		result.WriteString("\n🚀 Launching new session...\n")

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
				result.WriteString(fmt.Sprintf("⚠️  Warning: Failed to launch tmux session: %v\n", err))
				result.WriteString("   You can launch it manually later with `flow plan open`\n")
			}
		} else {
			// Launch session without worktree (in main repo)
			if err := CreateOrSwitchToMainRepoSessionAndRunCommand(ctx, planName, commandToRun); err != nil {
				result.WriteString(fmt.Sprintf("⚠️  Warning: Failed to launch tmux session: %v\n", err))
				result.WriteString("   You can launch it manually later with `flow plan status -t`\n")
			}
		}
	} else if cmd.ExtractAllFrom != "" {
		// If we extracted but didn't open a session, give next steps.
		result.WriteString("\nNext steps:\n")
		result.WriteString("1. Open the session: flow plan launch <job-file>\n")
	} else {
		result.WriteString("\nNext steps:\n")
		result.WriteString("1. Add your first job: flow plan add\n")
		result.WriteString("2. Check status: flow plan status\n")
	}

	return result.String(), nil
}

// runPlanInitFromRecipe initializes a plan from a predefined recipe.
func runPlanInitFromRecipe(cmd *PlanInitCmd, planPath string, planName string) error {
	// Auto-detect worktree context when running inside a sub-project of an ecosystem worktree.
	currentNode, err := workspace.GetProjectByPath(".")
	if err == nil && currentNode.Kind == workspace.KindEcosystemWorktreeSubProjectWorktree {
		// If we are in this context, the worktree for any new plan should
		// automatically be the parent ecosystem worktree.
		if cmd.Worktree == "" || cmd.Worktree == "__AUTO__" {
			parentWorktreeName := filepath.Base(currentNode.ParentEcosystemPath)
			cmd.Worktree = parentWorktreeName
		}
	}

	// Create a workspace provider to discover local repositories.
	discoveryService := workspace.NewDiscoveryService(nil) // logger is optional
	discoveryResult, err := discoveryService.DiscoverAll()
	if err != nil {
		fmt.Printf("⚠️  Warning: failed to discover workspaces for go.work generation: %v\n", err)
	}
	var provider *workspace.Provider
	if discoveryResult != nil {
		provider = workspace.NewProvider(discoveryResult)
	}

	// Determine the recipe command to use
	var getRecipeCmd string
	if cmd.RecipeCmd != "" {
		// Use the command from the CLI flag if provided
		getRecipeCmd = cmd.RecipeCmd
	} else {
		// Otherwise load from flow config
		_, configRecipeCmd, err := loadFlowConfigWithDynamicRecipes()
		if err != nil {
			// Warning but don't fail
			fmt.Fprintf(os.Stderr, "Warning: could not load flow config for dynamic recipes: %v\n", err)
		}
		getRecipeCmd = configRecipeCmd
	}

	// Special handling when --recipe-cmd is provided
	recipeName := cmd.Recipe
	if cmd.RecipeCmd != "" && (cmd.Recipe == "" || cmd.Recipe == "chat-workflow") {
		// If recipe-cmd is provided but recipe is not (or is default), 
		// try to auto-select from available recipes
		dynamicRecipes, err := orchestration.ListDynamicRecipes(getRecipeCmd)
		if err == nil && len(dynamicRecipes) > 0 {
			if len(dynamicRecipes) == 1 {
				// Auto-select the only recipe
				recipeName = dynamicRecipes[0].Name
				fmt.Printf("✓ Auto-selected recipe: %s\n", recipeName)
			} else if cmd.Recipe == "" || cmd.Recipe == "chat-workflow" {
				// Multiple recipes available and no specific one requested
				fmt.Println("Available recipes from command:")
				for i, r := range dynamicRecipes {
					fmt.Printf("  %d. %s - %s\n", i+1, r.Name, r.Description)
				}
				// For now, we'll use the first one, but this could be made interactive
				recipeName = dynamicRecipes[0].Name
				fmt.Printf("✓ Using first recipe: %s (specify with --recipe to choose a different one)\n", recipeName)
			}
		}
	}

	// Find the recipe (checks user recipes first, then dynamic, then built-in)
	recipe, err := orchestration.GetRecipe(recipeName, getRecipeCmd)
	if err != nil {
		return err
	}

	// Load flow config to get default recipe vars
	flowCfg, _ := loadFlowConfig() // Ignore error, use empty config if not found

	// Create the plan directory
	if err := createPlanDirectory(planPath, cmd.Force); err != nil {
		return err
	}

	fmt.Printf("Initializing orchestration plan in:\n  %s\n\n", planPath)
	fmt.Printf("✓ Using recipe: %s %s\n", recipe.Name, recipe.Source)

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

	// Parse recipe vars into a map
	// Start with defaults from grove.yml config if present
	recipeVars := make(map[string]string)
	if flowCfg != nil && flowCfg.Recipes != nil {
		if recipeCfg, ok := flowCfg.Recipes[cmd.Recipe]; ok && recipeCfg.Vars != nil {
			// Copy default vars from config
			for k, v := range recipeCfg.Vars {
				recipeVars[k] = v
			}
			fmt.Printf("✓ Loaded default vars from grove.yml for recipe '%s'\n", cmd.Recipe)
		}
	}

	// Parse command-line recipe vars (these override config defaults)
	// Supports both:
	//   - Multiple flags: --recipe-vars key1=val1 --recipe-vars key2=val2
	//   - Comma-delimited: --recipe-vars "key1=val1,key2=val2,key3=val3"
	for _, v := range cmd.RecipeVars {
		// Split by comma to support comma-delimited format
		pairs := strings.Split(v, ",")
		for _, pair := range pairs {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			parts := strings.SplitN(pair, "=", 2)
			if len(parts) == 2 {
				recipeVars[parts[0]] = parts[1] // Overrides config default if exists
			} else {
				fmt.Printf("Warning: invalid recipe-var format '%s', expected key=value\n", pair)
			}
		}
	}

	// Data for templating
	templateData := struct {
		PlanName string
		Vars     map[string]string
	}{
		PlanName: planName,
		Vars:     recipeVars,
	}

	// Get sorted list of job filenames to process them in order
	var jobFiles []string
	for filename := range recipe.Jobs {
		jobFiles = append(jobFiles, filename)
	}
	sort.Strings(jobFiles)

	// Determine worktree from command-line flag
	var worktreeOverride string
	isInheritedWorktree := false
	if currentNode != nil && currentNode.Kind == workspace.KindEcosystemWorktreeSubProjectWorktree {
		// We're in a sub-project worktree, so the worktree is inherited from the parent ecosystem
		if cmd.Worktree != "" && cmd.Worktree != "__AUTO__" {
			isInheritedWorktree = true
		}
	}
	if cmd.Worktree == "__AUTO__" {
		worktreeOverride = planName
	} else if cmd.Worktree != "" {
		worktreeOverride = cmd.Worktree
	}

	// Create the actual worktree if requested (but skip if it's inherited)
	if worktreeOverride != "" && !isInheritedWorktree {
		// Use the workspace path from currentNode to find the git root
		var workspacePath string
		if currentNode != nil {
			workspacePath = currentNode.Path
		}
		worktreePath, err := createWorktreeIfRequested(worktreeOverride, cmd.Repos, workspacePath)
		if err != nil {
			return err
		}

		// After creating the worktree(s), apply default context rules.
		if err := applyDefaultContextRulesToWorktree(worktreePath, cmd.Repos); err != nil {
			fmt.Printf("⚠️  Warning: could not apply default context rules: %v\n", err)
		}

		// Configure go.work file for the worktree.
		if err := configureGoWorkspace(worktreePath, cmd.Repos, provider); err != nil {
			// This is not a fatal error, but the user should be aware of it.
			fmt.Printf("⚠️  Warning: could not configure go.work file: %v\n", err)
		}
	}

	// Track if this is the first job for content merging
	isFirstJob := true

	// Map original recipe IDs to new unique IDs for dependency resolution
	recipeIDToUniqueID := make(map[string]string)

	// First pass: Generate unique IDs for all jobs and build the mapping
	for _, filename := range jobFiles {
		renderedContent, err := recipe.RenderJob(filename, templateData)
		if err != nil {
			return fmt.Errorf("rendering recipe job %s: %w", filename, err)
		}

		// Parse the frontmatter to get the original ID and title
		frontmatter, _, err := orchestration.ParseFrontmatter(renderedContent)
		if err != nil {
			return fmt.Errorf("parsing frontmatter from recipe job %s: %w", filename, err)
		}

		// Get the original ID from the recipe
		originalID, _ := frontmatter["id"].(string)

		// Get the title for ID generation
		var title string
		if titleVal, ok := frontmatter["title"].(string); ok {
			title = titleVal
		} else {
			// Fallback to filename if no title
			title = strings.TrimSuffix(filename, filepath.Ext(filename))
		}

		// Generate a unique ID (pass nil for plan since we don't have it loaded yet)
		uniqueID := orchestration.GenerateUniqueJobID(nil, title)

		// Map the original recipe ID to the new unique ID
		if originalID != "" {
			recipeIDToUniqueID[originalID] = uniqueID
		}
	}

	// Second pass: Process each job file with unique IDs and remapped dependencies
	for _, filename := range jobFiles {
		renderedContent, err := recipe.RenderJob(filename, templateData)
		if err != nil {
			return fmt.Errorf("rendering recipe job %s: %w", filename, err)
		}

		// Parse the frontmatter and body from the rendered job content
		frontmatter, body, err := orchestration.ParseFrontmatter(renderedContent)
		if err != nil {
			return fmt.Errorf("parsing frontmatter from recipe job %s: %w", filename, err)
		}

		// Get the original ID and replace it with the unique ID
		originalID, _ := frontmatter["id"].(string)
		if originalID != "" && recipeIDToUniqueID[originalID] != "" {
			frontmatter["id"] = recipeIDToUniqueID[originalID]
		}

		// Remap dependencies from original recipe IDs to new unique IDs
		if depends, ok := frontmatter["depends_on"].([]interface{}); ok {
			var remappedDeps []string
			for _, dep := range depends {
				if depStr, ok := dep.(string); ok {
					// Check if this dependency is an ID that we've remapped
					if newID, found := recipeIDToUniqueID[depStr]; found {
						remappedDeps = append(remappedDeps, newID)
					} else {
						// Keep the original if not found (might be a filename)
						remappedDeps = append(remappedDeps, depStr)
					}
				}
			}
			if len(remappedDeps) > 0 {
				// Convert to []interface{} for frontmatter
				var depsInterface []interface{}
				for _, d := range remappedDeps {
					depsInterface = append(depsInterface, d)
				}
				frontmatter["depends_on"] = depsInterface
			}
		}

		// Enrich the job frontmatter with common fields (worktree, repository, note_ref)
		var repoName string
		if currentNode != nil {
			repoName = currentNode.Name
		}
		enrichOpts := JobEnrichmentOptions{
			NoteRef:    cmd.NoteRef,
			Repository: repoName,
			Worktree:   worktreeOverride,
			IsFirstJob: isFirstJob,
		}
		enrichJobFrontmatter(frontmatter, enrichOpts)

		// If we have extracted content and this is the first job, merge it into the body
		if extractedBody != nil && isFirstJob {
			body = extractedBody // Replace the template's body with the extracted content
			fmt.Printf("✓ Merged extracted content into job: %s\n", filename)
			isFirstJob = false
		} else {
			fmt.Printf("✓ Created job: %s\n", filename)
		}

		// Rebuild the markdown file with the potentially modified frontmatter and body
		finalContent, err := orchestration.RebuildMarkdownWithFrontmatter(frontmatter, body)
		if err != nil {
			return fmt.Errorf("rebuilding job content for %s: %w", filename, err)
		}

		// Write the processed job file to the new plan directory
		destPath := filepath.Join(planPath, filename)
		if err := os.WriteFile(destPath, finalContent, 0644); err != nil {
			return fmt.Errorf("writing recipe job file %s: %w", filename, err)
		}
	}

	// The final worktree to use in .grove-plan.yml is simply the one from the CLI flag
	finalWorktree := worktreeOverride
	
	// Create a default .grove-plan.yml, using the determined worktree
	if err := createDefaultPlanConfig(planPath, cmd.Model, finalWorktree, cmd.Container, cmd.NoteRef, cmd.Repos); err != nil {
		fmt.Printf("Warning: failed to create .grove-plan.yml: %v\n", err)
	} else {
		fmt.Println("✓ Created .grove-plan.yml")
	}

	// Set the new plan as active
	if err := state.Set("flow.active_plan", planName); err != nil {
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

// JobEnrichmentOptions holds context for enriching job frontmatter during plan init.
// This ensures consistent behavior across recipe-based and manual job creation.
type JobEnrichmentOptions struct {
	NoteRef    string
	Repository string
	Worktree   string
	IsFirstJob bool
}

// enrichJobFrontmatter applies common frontmatter enrichments based on plan context.
// This centralizes the logic that was previously duplicated across multiple code paths.
func enrichJobFrontmatter(frontmatter map[string]interface{}, opts JobEnrichmentOptions) {
	// Apply worktree override if specified
	if opts.Worktree != "" {
		frontmatter["worktree"] = opts.Worktree
	}

	// Add repository field from current workspace context
	if opts.Repository != "" {
		frontmatter["repository"] = opts.Repository
	}

	// Add note_ref to first job if provided
	if opts.NoteRef != "" && opts.IsFirstJob {
		frontmatter["note_ref"] = opts.NoteRef
	}
}

// enrichJob applies common field enrichments to a Job struct during plan init.
// This is the Job struct equivalent of enrichJobFrontmatter.
func enrichJob(job *orchestration.Job, opts JobEnrichmentOptions) {
	// Apply worktree if specified
	if opts.Worktree != "" {
		job.Worktree = opts.Worktree
	}

	// Add repository from current workspace context
	if opts.Repository != "" {
		job.Repository = opts.Repository
	}

	// Add note_ref to first job if provided
	if opts.NoteRef != "" && opts.IsFirstJob {
		job.NoteRef = opts.NoteRef
	}
}

// createDefaultPlanConfig creates a default .grove-plan.yml file in the plan directory.
func createDefaultPlanConfig(planPath, model, worktree, container, noteRef string, repos []string) error {
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
	configContent.WriteString("\n")

	// Add repos configuration if specified
	if len(repos) > 0 {
		configContent.WriteString("# Specific repos to include in ecosystem worktree\n")
		configContent.WriteString("repos:\n")
		for _, repo := range repos {
			configContent.WriteString(fmt.Sprintf("  - %s\n", repo))
		}
	} else {
		configContent.WriteString("# Specific repos to include in ecosystem worktree (all if not specified)\n")
		configContent.WriteString("# repos:\n")
		configContent.WriteString("#   - grove-core\n")
		configContent.WriteString("#   - grove-flow\n")
	}
	configContent.WriteString("\n")

	configContent.WriteString("# Issue tracker integration (future feature)\n")
	configContent.WriteString("# issue_tracker:\n")
	configContent.WriteString("#   provider: github # e.g., github, jira\n")
	configContent.WriteString("#   url: https://github.com/my-org/my-repo/issues/123\n")
	configContent.WriteString("\n")

	configContent.WriteString("# Hooks to run at different plan lifecycle events\n")
	if noteRef != "" {
		configContent.WriteString("hooks:\n")
		configContent.WriteString("  on_review: |\n")
		configContent.WriteString(`    nb internal update-note --path "{{.NoteRef}}" --append-content "\n\n---\n**Completed by plan:** [[plans/{{.PlanName}}]]"` + "\n")
		configContent.WriteString(`    nb move "{{.NoteRef}}" completed --force` + "\n")
	} else {
		configContent.WriteString("# hooks:\n")
		configContent.WriteString("#   on_review: |\n")
		configContent.WriteString(`#     echo "Plan {{.PlanName}} is now in review."` + "\n")
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
		// Removed default worktree assignment

		content, err = orchestration.RebuildMarkdownWithFrontmatter(frontmatter, []byte(promptBody))
		if err != nil {
			return "", fmt.Errorf("failed to rebuild job content from template: %w", err)
		}

		filename = "01-high-level-plan.md"
	} else {
		// Create a simpler job for other output types
		modelField := fmt.Sprintf("\nmodel: %s", model)
		content = []byte(fmt.Sprintf(`---
id: %s
title: "Initial Job"
status: pending
type: oneshot%s
output:
  type: %s
---

%s
`, jobID, modelField, outputType, specContent))

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

// applyDefaultContextRulesToWorktree applies default context rules to a worktree.
// It detects whether the worktree is a single-repo or ecosystem worktree and applies
// rules accordingly.
func applyDefaultContextRulesToWorktree(worktreePath string, explicitRepos []string) error {
	// Determine which repos to apply rules to
	var reposToProcess []string

	if len(explicitRepos) > 0 {
		// Use explicitly specified repos
		reposToProcess = explicitRepos
	} else {
		// Auto-detect ecosystem repos by checking for subdirectories with grove.yml
		entries, err := os.ReadDir(worktreePath)
		if err != nil {
			return fmt.Errorf("failed to read worktree directory: %w", err)
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			// Check if this directory has a grove.yml file (indicating it's a Grove repo)
			groveYmlPath := filepath.Join(worktreePath, entry.Name(), "grove.yml")
			if _, err := os.Stat(groveYmlPath); err == nil {
				reposToProcess = append(reposToProcess, entry.Name())
			}
		}
	}

	// Apply rules based on what we found
	if len(reposToProcess) > 0 {
		// Ecosystem worktree: apply rules to each sub-repo
		fmt.Println("Applying default context rules to ecosystem sub-projects...")
		for _, repoName := range reposToProcess {
			subRepoPath := filepath.Join(worktreePath, repoName)
			if err := configureDefaultContextRules(subRepoPath); err != nil {
				// Non-fatal warning for individual repos
				fmt.Printf("⚠️  Warning: could not apply default rules to '%s': %v\n", repoName, err)
			}
		}
	} else {
		// Single-repo worktree
		if err := configureDefaultContextRules(worktreePath); err != nil {
			return fmt.Errorf("could not apply default context rules: %w", err)
		}
	}

	return nil
}

// createWorktreeIfRequested creates a git worktree with the given name
func createWorktreeIfRequested(worktreeName string, repos []string, workspacePath string) (string, error) {
	// Use workspace path if provided, otherwise fall back to current directory
	searchPath := workspacePath
	if searchPath == "" {
		searchPath = "."
	}

	gitRoot, err := orchestration.GetGitRootSafe(searchPath)
	if err != nil {
		return "", fmt.Errorf("failed to find git root: %w", err)
	}

	opts := workspace.PrepareOptions{
		GitRoot:      gitRoot,
		WorktreeName: worktreeName,
		BranchName:   worktreeName,
		Repos:        repos,
	}

	worktreePath, err := workspace.Prepare(context.Background(), opts, orchestration.CopyProjectFilesToWorktree)
	if err != nil {
		return "", fmt.Errorf("failed to create worktree: %w", err)
	}

	return worktreePath, nil
}
