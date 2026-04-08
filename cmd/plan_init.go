package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"time"

	"encoding/json"

	"github.com/google/uuid"
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/env"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/state"
	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// RunPlanInitTUI launches the interactive TUI for creating a new plan.
func RunPlanInitTUI(dir string, cliCmd *PlanInitCmd) error {
	// Plans are relative to CWD (notebook locator handles workspace resolution)
	plansDir, _ := os.Getwd()

	finalCmd, err := runPlanInitTUI(plansDir, cliCmd)
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
	// Derive ExtractAllFrom and NoteRef from FromNote if provided
	// --from-note takes precedence over --extract-all-from and --note-ref
	if cmd.FromNote != "" {
		// Resolve the path to an absolute path
		fromNotePath, err := filepath.Abs(cmd.FromNote)
		if err != nil {
			return "", fmt.Errorf("failed to resolve path for --from-note file %s: %w", cmd.FromNote, err)
		}

		// Set ExtractAllFrom to the note path for content extraction
		// --from-note takes precedence
		cmd.ExtractAllFrom = fromNotePath

		// Set NoteRef to the note path for linking
		// --from-note takes precedence
		cmd.NoteRef = fromNotePath
	}

	// Auto-detect worktree context when running inside a sub-project of an ecosystem worktree.
	currentNode, err := workspace.GetProjectByPath(".")
	if err == nil && currentNode.IsWorktree() {
		// Guard: warn the user when running plan init from inside a worktree.
		// Plans should typically be initialized from the main repository, not from
		// within a worktree, as the plan will be created relative to the worktree's
		// notebook location rather than the main repo's.
		cwd, _ := os.Getwd()
		parentPath := currentNode.ParentProjectPath
		if parentPath == "" {
			parentPath = currentNode.RootEcosystemPath
		}
		fmt.Fprintf(os.Stderr, "Warning: you are inside a git worktree (%s).\n", filepath.Base(cwd))
		fmt.Fprintf(os.Stderr, "Plans are typically created from the main repository at: %s\n", parentPath)
		fmt.Fprintf(os.Stderr, "Continuing will create the plan relative to this worktree's workspace context.\n\n")
	}

	if err == nil && currentNode.Kind == workspace.KindEcosystemWorktreeSubProjectWorktree {
		// If we are in this context, the worktree for any new plan should
		// automatically be the parent ecosystem worktree.
		if cmd.Worktree == "" || cmd.Worktree == "__AUTO__" {
			parentWorktreeName := filepath.Base(currentNode.ParentEcosystemPath)
			cmd.Worktree = parentWorktreeName
		}
	}

	// Resolve the full path for the new plan directory.
	// Use resolvePlanPathInWorkspace (not resolvePlanPath) because the plan
	// doesn't exist yet — resolvePlanPath requires the directory to already exist.
	planDirArg := cmd.Dir
	planPath, err := resolvePlanPathInWorkspace(planDirArg, ".")
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
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel) // Suppress discoverer's debug output
	discoveryService := workspace.NewDiscoveryService(logger)
	discoveryResult, err := discoveryService.DiscoverAll()
	if err != nil {
		fmt.Printf("WARNING:  Warning: failed to discover workspaces for go.work generation: %v\n", err)
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
			fmt.Printf("%s  Warning: could not apply default context rules: %v\n", theme.IconWarning, err)
		}

		// Note: Skills are NOT synced to worktrees because Claude Code traverses
		// up the directory tree to find .claude/skills/. Skills should be installed
		// once at the ecosystem root using: grove-skills sync --here

		// Configure go.work file for the worktree.
		if err := configureGoWorkspace(worktreePath, cmd.Repos, provider); err != nil {
			// This is not a fatal error, but the user should be aware of it.
			fmt.Printf("%s  Warning: could not configure go.work file: %v\n", theme.IconWarning, err)
		}

		// Set the active plan inside the worktree.
		if err := setWorktreeActivePlan(worktreePath, planName); err != nil {
			// This is not a fatal error, but the user should be aware of it.
			result.WriteString(fmt.Sprintf("%s  Warning: could not set active plan in new worktree: %v\n", theme.IconWarning, err))
		} else {
			result.WriteString(fmt.Sprintf("%s Set active plan in worktree: %s\n", theme.IconSuccess, worktreeToSet))
		}
	}

	// Determine model: CLI flag takes precedence, then workspace config
	effectiveModel := cmd.Model
	if effectiveModel == "" {
		if flowCfg, err := loadFlowConfig(); err == nil && flowCfg != nil && flowCfg.OneshotModel != "" {
			effectiveModel = flowCfg.OneshotModel
		}
	}

	// Create directory using the resolved path
	if err := createPlanDirectory(planPath, cmd.Force); err != nil {
		return "", err
	}

	// Create default .grove-plan.yml
	if err := createDefaultPlanConfig(planPath, effectiveModel, worktreeToSet, cmd.NoteRef, "", cmd.Playbook, cmd.Repos); err != nil {
		result.WriteString(fmt.Sprintf("Warning: failed to create .grove-plan.yml: %v\n", err))
	}

	// Build success message
	result.WriteString(fmt.Sprintf("Initializing orchestration plan in:\n  %s\n\n", planPath))
	result.WriteString("* Created plan directory\n")
	if worktreeToSet != "" {
		result.WriteString(fmt.Sprintf("* Created worktree: %s\n", worktreeToSet))
	}
	result.WriteString("* Created .grove-plan.yml with default configuration\n")

	// Environment provisioning: if the config has an environment provider, spin it up.
	if envResult := provisionEnvironment(worktreeToSet, planPath, provider, cmd.EnvProfile); envResult != "" {
		result.WriteString(envResult)
	}

	// Set the new plan as active, but only if we are not opening a new session.
	// If a new session is opened, the active plan will be set inside that session.
	// Also skip setting the active plan in the parent if a worktree was created.
	if !cmd.OpenSession && worktreeToSet == "" {
		if err := state.Set(plan.StateKey, planName); err != nil {
			result.WriteString(fmt.Sprintf("Warning: failed to set active job: %v\n", err))
		} else {
			result.WriteString(fmt.Sprintf("* Set active plan to: %s\n", planName))
		}
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

		// 2. Read the source file after resolving its path
		extractFilePath, err := filepath.Abs(cmd.ExtractAllFrom)
		if err != nil {
			return "", fmt.Errorf("failed to resolve path for extraction file %s: %w", cmd.ExtractAllFrom, err)
		}
		content, err := os.ReadFile(extractFilePath)
		if err != nil {
			return "", fmt.Errorf("failed to read source file for extraction %s: %w", extractFilePath, err)
		}

		// 3. Extract body
		_, body, err := orchestration.ParseFrontmatter(content)
		if err != nil {
			return "", fmt.Errorf("failed to parse frontmatter from source file %s: %w", extractFilePath, err)
		}

		// 4. Create a new job
		jobTitle := strings.TrimSuffix(filepath.Base(extractFilePath), filepath.Ext(filepath.Base(extractFilePath)))
		job := &orchestration.Job{
			Title:      jobTitle,
			Type:       orchestration.JobTypeChat, // Extracts become chat jobs
			Status:     orchestration.JobStatusPendingUser,
			ID:         orchestration.GenerateUniqueJobID(plan, jobTitle),
			PromptBody: string(body),
			Model:      cmd.Model,
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
			NoteRef:      cmd.NoteRef,
			Repository:   repoName,
			Worktree:     worktreeName,
			IsNoteTarget: true, // Extraction creates the first job
		}
		enrichJob(job, enrichOpts)

		// 5. Add the job to the plan
		filename, err := orchestration.AddJob(plan, job)
		if err != nil {
			return "", fmt.Errorf("failed to add extracted job to plan: %w", err)
		}
		result.WriteString(fmt.Sprintf("* Extracted content from %s to new job: %s\n", cmd.ExtractAllFrom, filename))
	}

	// Execute on_start hook if plan was initialized from a note
	// This runs after extraction to avoid file path conflicts
	if cmd.NoteRef != "" {
		if err := executeOnStartHook(planPath, planName, cmd.NoteRef); err != nil {
			result.WriteString(fmt.Sprintf("WARNING:  Warning: on_start hook execution failed: %v\n", err))
		}
	}

	// Open Session Logic
	if cmd.OpenSession {
		result.WriteString("\n Launching new session...\n")

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
				result.WriteString(fmt.Sprintf("WARNING:  Warning: Failed to launch tmux session: %v\n", err))
				result.WriteString("   You can launch it manually later with `flow plan open`\n")
			}
		} else {
			// Launch session without worktree (in main repo)
			if err := CreateOrSwitchToMainRepoSessionAndRunCommand(ctx, planName, commandToRun); err != nil {
				result.WriteString(fmt.Sprintf("WARNING:  Warning: Failed to launch tmux session: %v\n", err))
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

	// Notify daemon to re-scan workspaces so it picks up the new plan immediately
	client := daemon.New()
	if client.IsRunning() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = client.Refresh(ctx)
		cancel()
	}
	client.Close()

	return result.String(), nil
}

// runPlanInitFromRecipe initializes a plan from a predefined recipe.
func runPlanInitFromRecipe(cmd *PlanInitCmd, planPath string, planName string) error {
	// Derive ExtractAllFrom and NoteRef from FromNote if provided
	// --from-note takes precedence over --extract-all-from and --note-ref
	if cmd.FromNote != "" {
		// Resolve the path to an absolute path
		fromNotePath, err := filepath.Abs(cmd.FromNote)
		if err != nil {
			return fmt.Errorf("failed to resolve path for --from-note file %s: %w", cmd.FromNote, err)
		}

		// Set ExtractAllFrom to the note path for content extraction
		// --from-note takes precedence
		cmd.ExtractAllFrom = fromNotePath

		// Set NoteRef to the note path for linking
		// --from-note takes precedence
		cmd.NoteRef = fromNotePath
	}

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
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel) // Suppress discoverer's debug output
	discoveryService := workspace.NewDiscoveryService(logger)
	discoveryResult, err := discoveryService.DiscoverAll()
	if err != nil {
		fmt.Printf("WARNING:  Warning: failed to discover workspaces for go.work generation: %v\n", err)
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
				fmt.Printf("* Auto-selected recipe: %s\n", recipeName)
			} else if cmd.Recipe == "" || cmd.Recipe == "chat-workflow" {
				// Multiple recipes available and no specific one requested
				fmt.Println("Available recipes from command:")
				for i, r := range dynamicRecipes {
					fmt.Printf("  %d. %s - %s\n", i+1, r.Name, r.Description)
				}
				// For now, we'll use the first one, but this could be made interactive
				recipeName = dynamicRecipes[0].Name
				fmt.Printf("* Using first recipe: %s (specify with --recipe to choose a different one)\n", recipeName)
			}
		}
	}

	// Find the recipe (checks project > playbook > notebook > user > dynamic > built-in)
	recipe, err := orchestration.GetRecipeWithPlaybook(recipeName, getRecipeCmd, cmd.Playbook)
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
	fmt.Printf("* Using recipe: %s %s\n", recipe.Name, recipe.Source)

	// Prepare extracted content if provided
	var extractedBody []byte
	if cmd.ExtractAllFrom != "" {
		// Resolve the extraction file path to an absolute path
		extractFilePath, err := filepath.Abs(cmd.ExtractAllFrom)
		if err != nil {
			return fmt.Errorf("failed to resolve path for extraction file %s: %w", cmd.ExtractAllFrom, err)
		}

		// Read the source file
		content, err := os.ReadFile(extractFilePath)
		if err != nil {
			return fmt.Errorf("failed to read source file for extraction %s: %w", extractFilePath, err)
		}

		// Extract body (remove any existing frontmatter)
		_, body, err := orchestration.ParseFrontmatter(content)
		if err != nil {
			return fmt.Errorf("failed to parse frontmatter from source file %s: %w", extractFilePath, err)
		}

		extractedBody = body
		fmt.Printf("* Extracted content from %s\n", extractFilePath)
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
			fmt.Printf("* Loaded default vars from grove.yml for recipe '%s'\n", cmd.Recipe)
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

	// Override model from CLI if provided, otherwise fall back to workspace config
	if cmd.Model != "" {
		recipeVars["model"] = cmd.Model
	} else if recipeVars["model"] == "" && flowCfg != nil && flowCfg.OneshotModel != "" {
		recipeVars["model"] = flowCfg.OneshotModel
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
			fmt.Printf("%s  Warning: could not apply default context rules: %v\n", theme.IconWarning, err)
		}

		// Note: Skills are NOT synced to worktrees because Claude Code traverses
		// up the directory tree to find .claude/skills/. Skills should be installed
		// once at the ecosystem root using: grove-skills sync --here

		// Configure go.work file for the worktree.
		if err := configureGoWorkspace(worktreePath, cmd.Repos, provider); err != nil {
			// This is not a fatal error, but the user should be aware of it.
			fmt.Printf("%s  Warning: could not configure go.work file: %v\n", theme.IconWarning, err)
		}

		// Set the active plan inside the worktree.
		if err := setWorktreeActivePlan(worktreePath, planName); err != nil {
			// This is not a fatal error, but the user should be aware of it.
			fmt.Printf("%s  Warning: could not set active plan in new worktree: %v\n", theme.IconWarning, err)
		} else {
			fmt.Printf("%s Set active plan in worktree: %s\n", theme.IconSuccess, worktreeOverride)
		}
	}

	// Determine the target job for note content injection
	var targetFilename string
	if cmd.NoteTargetFile != "" {
		// User specified a target file
		targetFilename = cmd.NoteTargetFile

		// Validate that the target file exists in the recipe
		found := false
		for _, f := range jobFiles {
			if f == targetFilename {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("note target file '%s' not found in recipe '%s'", targetFilename, recipe.Name)
		}
	} else if len(jobFiles) > 0 {
		// Default to the first file if no target is specified
		targetFilename = jobFiles[0]
	}

	// Map original recipe IDs to new unique IDs for dependency resolution
	recipeIDToUniqueID := make(map[string]string)

	// First pass: Generate unique IDs for all jobs and build the mapping
	filenameToUniqueID := make(map[string]string)
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

		// Map the filename to the unique ID (for jobs without an original ID)
		filenameToUniqueID[filename] = uniqueID

		// Map the original recipe ID to the new unique ID (for dependency remapping)
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

		// Set the unique ID for this job
		// If the recipe had an ID, use the remapped one; otherwise use the filename-based ID
		originalID, _ := frontmatter["id"].(string)
		if originalID != "" && recipeIDToUniqueID[originalID] != "" {
			frontmatter["id"] = recipeIDToUniqueID[originalID]
		} else {
			// Job didn't have an ID in the recipe template, so add the generated one
			frontmatter["id"] = filenameToUniqueID[filename]
		}

		// Ensure title field exists - required for job validation
		if _, hasTitle := frontmatter["title"]; !hasTitle {
			// Generate title from filename if not present in recipe
			title := strings.TrimSuffix(filename, filepath.Ext(filename))
			frontmatter["title"] = title
		}

		// Ensure status field exists - required for job validation
		if _, hasStatus := frontmatter["status"]; !hasStatus {
			frontmatter["status"] = "pending"
		}

		// Validate and potentially fix the type field
		if typeVal, hasType := frontmatter["type"]; hasType {
			typeStr, _ := typeVal.(string)
			// Fix common mistake: hyphen instead of underscore in job types
			typeStr = strings.ReplaceAll(typeStr, "-", "_")
			frontmatter["type"] = typeStr
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

		isNoteTarget := (targetFilename != "" && filename == targetFilename)

		// Enrich the job frontmatter with common fields (worktree, repository, note_ref)
		var repoName string
		if currentNode != nil {
			repoName = currentNode.Name
		}
		enrichOpts := JobEnrichmentOptions{
			NoteRef:      cmd.NoteRef,
			Repository:   repoName,
			Worktree:     worktreeOverride,
			IsNoteTarget: isNoteTarget,
		}
		enrichJobFrontmatter(frontmatter, enrichOpts)

		// Override model from CLI if provided
		if cmd.Model != "" {
			frontmatter["model"] = cmd.Model
		}

		// If we have extracted content, merge it into the target job's body
		if extractedBody != nil && isNoteTarget {
			body = extractedBody // Replace the template's body with the extracted content
			fmt.Printf("* Merged extracted content into job: %s\n", filename)
		} else {
			fmt.Printf("* Created job: %s\n", filename)
		}

		// Rebuild the markdown file with the potentially modified frontmatter and body
		finalContent, err := orchestration.RebuildMarkdownWithFrontmatter(frontmatter, body)
		if err != nil {
			return fmt.Errorf("rebuilding job content for %s: %w", filename, err)
		}

		// Write the processed job file to the new plan directory
		destPath := filepath.Join(planPath, filename)
		if err := os.WriteFile(destPath, finalContent, 0o644); err != nil {
			return fmt.Errorf("writing recipe job file %s: %w", filename, err)
		}
	}

	// The final worktree to use in .grove-plan.yml is simply the one from the CLI flag
	finalWorktree := worktreeOverride

	// Create a default .grove-plan.yml, using the determined worktree and recipe name
	// Use recipeVars["model"] which includes workspace config fallback
	effectiveModel := recipeVars["model"]
	if err := createDefaultPlanConfig(planPath, effectiveModel, finalWorktree, cmd.NoteRef, cmd.Recipe, cmd.Playbook, cmd.Repos); err != nil {
		fmt.Printf("Warning: failed to create .grove-plan.yml: %v\n", err)
	} else {
		fmt.Println("* Created .grove-plan.yml")
	}

	// Execute init actions after everything is set up (only if --init flag is set)
	if cmd.RunInit && len(recipe.InitActions) > 0 {
		fmt.Println("\n▶️  Executing initialization actions from recipe...")
		if err := executeInitActions(recipe.InitActions, worktreeOverride, finalWorktree, templateData); err != nil {
			// Log a warning but do not fail the entire plan init
			fmt.Printf("WARNING:  Warning: one or more init actions failed: %v\n", err)
		} else {
			fmt.Println("* Initialization actions completed successfully.")
		}
	} else if len(recipe.InitActions) > 0 && !cmd.RunInit {
		fmt.Println("\nTip: Tip: This recipe has initialization actions. Run them with: flow plan action init")
	}

	// Execute on_start hook if plan was initialized from a note
	if cmd.NoteRef != "" {
		if err := executeOnStartHook(planPath, planName, cmd.NoteRef); err != nil {
			fmt.Printf("Warning: on_start hook execution failed: %v\n", err)
		}
	}

	// Set the new plan as active, but only if we are not opening a new session.
	// If a new session is opened, the active plan will be set inside that session.
	// Also skip setting the active plan in the parent if a worktree was created.
	if !cmd.OpenSession && finalWorktree == "" {
		if err := state.Set(plan.StateKey, planName); err != nil {
			fmt.Printf("Warning: failed to set active job: %v\n", err)
		} else {
			fmt.Printf("* Set active plan to: %s\n", planName)
		}
	}

	// Handle --open-session for recipe flow
	if cmd.OpenSession {
		fmt.Println("\n Launching new session...")

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
				fmt.Printf("WARNING:  Warning: Failed to launch tmux session: %v\n", err)
				fmt.Printf("   You can launch it manually later with `flow plan open`\n")
			}
		} else {
			// Launch session without worktree (in main repo)
			if err := CreateOrSwitchToMainRepoSessionAndRunCommand(ctx, planName, commandToRun); err != nil {
				fmt.Printf("WARNING:  Warning: Failed to launch tmux session: %v\n", err)
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	return nil
}

// executeOnStartHook runs the on_start hook if defined in the plan's configuration.
func executeOnStartHook(planPath, planName, noteRef string) error {
	// Load the plan to get the config
	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		// Don't fail the whole operation, just log a warning
		return fmt.Errorf("could not load plan to execute on_start hook: %w", err)
	}

	if plan.Config != nil && plan.Config.Hooks != nil {
		if hookCmdStr, ok := plan.Config.Hooks["on_start"]; ok && hookCmdStr != "" {
			fmt.Println("▶️  Executing on_start hook...")

			// Prepare template data
			templateData := struct {
				PlanName string
				NoteRef  string
			}{
				PlanName: planName,
				NoteRef:  noteRef,
			}

			// Render the hook command
			tmpl, err := template.New("hook").Parse(hookCmdStr)
			if err != nil {
				return fmt.Errorf("failed to parse on_start hook template: %w", err)
			}
			var renderedCmd bytes.Buffer
			if err := tmpl.Execute(&renderedCmd, templateData); err != nil {
				return fmt.Errorf("failed to render on_start hook command: %w", err)
			}

			// Execute the command
			hookCmd := exec.Command("sh", "-c", renderedCmd.String())
			hookCmd.Stdout = os.Stdout
			hookCmd.Stderr = os.Stderr
			if err := hookCmd.Run(); err != nil {
				return fmt.Errorf("on_start hook execution failed: %w", err)
			}
			fmt.Println("* on_start hook executed successfully.")
		}
	}
	return nil
}

// JobEnrichmentOptions holds context for enriching job frontmatter during plan init.
// This ensures consistent behavior across recipe-based and manual job creation.
type JobEnrichmentOptions struct {
	NoteRef      string
	Repository   string
	Worktree     string
	IsNoteTarget bool
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
	if opts.NoteRef != "" && opts.IsNoteTarget {
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
	if opts.NoteRef != "" && opts.IsNoteTarget {
		job.NoteRef = opts.NoteRef
	}
}

// createDefaultPlanConfig creates a default .grove-plan.yml file in the plan directory.
func createDefaultPlanConfig(planPath, model, worktree, noteRef, recipe, playbook string, repos []string) error {
	var configContent strings.Builder

	// Recipe field (if applicable)
	if recipe != "" {
		configContent.WriteString("# Recipe used to create this plan\n")
		configContent.WriteString(fmt.Sprintf("recipe: %s\n", recipe))
		configContent.WriteString("\n")
	}

	// Playbook scoping (if applicable)
	if playbook != "" {
		configContent.WriteString("# Playbook scoping: jobs in this plan inherit $PLAYBOOK_ROOT\n")
		configContent.WriteString(fmt.Sprintf("playbook: %s\n", playbook))
		configContent.WriteString("\n")
	}

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
		configContent.WriteString("  on_start: |\n")
		configContent.WriteString(`    OLD_PATH="{{.NoteRef}}"` + "\n")
		configContent.WriteString(`    nb internal update-frontmatter --path "$OLD_PATH" --field plan_ref --value "plans/{{.PlanName}}"` + "\n")
		configContent.WriteString(`    NEW_PATH=$(nb move "$OLD_PATH" in_progress --force | grep "To:" | awk '{print $2}')` + "\n")
		configContent.WriteString(`    flow plan update-note-ref "{{.PlanName}}" "$NEW_PATH"` + "\n")
		configContent.WriteString("  on_review: |\n")
		configContent.WriteString(`    OLD_PATH="{{.NoteRef}}"` + "\n")
		configContent.WriteString(`    nb internal update-note --path "$OLD_PATH" --append-content "\n\n---\n**Completed by plan:** [[plans/{{.PlanName}}]]"` + "\n")
		configContent.WriteString(`    NEW_PATH=$(nb move "$OLD_PATH" review --force | grep "To:" | awk '{print $2}')` + "\n")
		configContent.WriteString(`    flow plan update-note-ref "{{.PlanName}}" "$NEW_PATH"` + "\n")
		configContent.WriteString("  on_finish: |\n")
		configContent.WriteString(`    OLD_PATH="{{.NoteRef}}"` + "\n")
		configContent.WriteString(`    nb move "$OLD_PATH" completed --force` + "\n")
	} else {
		configContent.WriteString("# hooks:\n")
		configContent.WriteString("#   on_start: |\n")
		configContent.WriteString(`#     echo "Plan {{.PlanName}} is starting..."` + "\n")
		configContent.WriteString("#   on_review: |\n")
		configContent.WriteString(`#     echo "Plan {{.PlanName}} is now in review."` + "\n")
		configContent.WriteString("#   on_finish: |\n")
		configContent.WriteString(`#     echo "Plan {{.PlanName}} is finished."` + "\n")
	}

	configPath := filepath.Join(planPath, ".grove-plan.yml")
	return os.WriteFile(configPath, []byte(configContent.String()), 0o644)
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
				fmt.Printf("WARNING:  Warning: could not apply default rules to '%s': %v\n", repoName, err)
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

// executeInitActions orchestrates the execution of actions defined in a recipe's workspace_init.yml
func executeInitActions(actions []orchestration.InitAction, worktreeOverride, finalWorktree string, templateData interface{}) error {
	var errors []string

	// Determine the base worktree path
	var worktreePath string
	if finalWorktree != "" {
		// Get the worktree path
		gitRoot, err := orchestration.GetGitRootSafe(".")
		if err != nil {
			return fmt.Errorf("failed to find git root: %w", err)
		}

		// Defensive check: prevent creating worktrees in notebook repos
		if workspace.IsNotebookRepo(gitRoot) {
			return fmt.Errorf("cannot create worktree: running from within a notebook git repository at %s. Please run this command from your project directory", gitRoot)
		}

		opts := workspace.PrepareOptions{
			GitRoot:      gitRoot,
			WorktreeName: finalWorktree,
			BranchName:   finalWorktree,
		}
		worktreePath, err = workspace.Prepare(context.Background(), opts)
		if err != nil {
			return fmt.Errorf("failed to get worktree path: %w", err)
		}
	} else {
		// Use current directory if no worktree
		var err error
		worktreePath, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current directory: %w", err)
		}
	}

	for _, action := range actions {
		fmt.Printf("  - Running action: %s\n", action.Description)

		// Determine working directory
		workDir := worktreePath
		if action.Repo != "" {
			workDir = filepath.Join(worktreePath, action.Repo)
		}

		var err error
		switch action.Type {
		case "shell":
			err = executeShellAction(action, workDir, templateData)
		default:
			err = fmt.Errorf("unknown action type: %s", action.Type)
		}

		if err != nil {
			errorMsg := fmt.Sprintf("failed to execute action '%s': %v", action.Description, err)
			fmt.Printf("    %s\n", errorMsg)
			errors = append(errors, errorMsg)
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf(strings.Join(errors, "\n"))
	}
	return nil
}

// executeShellAction handles the 'shell' init action type
func executeShellAction(action orchestration.InitAction, workDir string, templateData interface{}) error {
	// Render the command as a Go template
	tmpl, err := template.New("shell-command").Parse(action.Command)
	if err != nil {
		return fmt.Errorf("parsing command template: %w", err)
	}

	var renderedCmd bytes.Buffer
	if err := tmpl.Execute(&renderedCmd, templateData); err != nil {
		return fmt.Errorf("rendering command template: %w", err)
	}

	commandStr := renderedCmd.String()
	fmt.Printf("    $ %s\n", commandStr)

	cmd := exec.Command("sh", "-c", commandStr)
	cmd.Dir = workDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
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

	// Defensive check: prevent creating worktrees in notebook repos
	if workspace.IsNotebookRepo(gitRoot) {
		return "", fmt.Errorf("cannot create worktree: running from within a notebook git repository at %s. Please run this command from your project directory", gitRoot)
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

// setWorktreeActivePlan writes a state file within a worktree to set the active plan.
func setWorktreeActivePlan(worktreePath, planName string) error {
	groveDir := filepath.Join(worktreePath, ".grove")
	if err := os.MkdirAll(groveDir, 0755); err != nil {
		return fmt.Errorf("failed to create .grove directory in worktree: %w", err)
	}

	// Use a flat map with the key plan.StateKey to match how state.Set works.
	stateData := map[string]string{
		plan.StateKey: planName,
	}

	yamlBytes, err := yaml.Marshal(stateData)
	if err != nil {
		return fmt.Errorf("failed to marshal state data: %w", err)
	}

	statePath := filepath.Join(groveDir, "state.yml")
	if err := os.WriteFile(statePath, yamlBytes, 0644); err != nil {
		return fmt.Errorf("failed to write state file in worktree: %w", err)
	}

	return nil
}

// provisionEnvironment checks the layered config for an environment provider and
// provisions it if configured. Returns a string with status messages to append to the result.
func provisionEnvironment(worktreeName, planPath string, wsProvider *workspace.Provider, envProfile string) string {
	// Determine the config load path: worktree if available, otherwise CWD
	var loadPath string
	if worktreeName != "" {
		cwd, _ := os.Getwd()
		gitRoot, err := orchestration.GetGitRootSafe(cwd)
		if err != nil {
			return ""
		}
		loadPath = filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
	} else {
		loadPath, _ = os.Getwd()
	}

	layeredCfg, err := config.LoadLayered(loadPath)
	if err != nil || layeredCfg.Final == nil {
		return ""
	}

	// Determine active environment profile: --env flag > sticky state > default
	activeProfile := envProfile
	if activeProfile == "" {
		activeProfile, _ = state.GetString("environment")
	}

	// Validate that the named profile exists before resolving
	if activeProfile != "" {
		if layeredCfg.Final.Environments == nil {
			return fmt.Sprintf("%s  Error: environment profile %q not found (no environments defined)\n", theme.IconWarning, activeProfile)
		}
		if _, exists := layeredCfg.Final.Environments[activeProfile]; !exists {
			return fmt.Sprintf("%s  Error: environment profile %q not found\n", theme.IconWarning, activeProfile)
		}
	}

	// Resolve the environment profile (merges default + named overlay)
	envCfg, resolveErr := config.ResolveEnvironment(layeredCfg.Final, activeProfile)
	if resolveErr != nil {
		return fmt.Sprintf("%s  Warning: %v\n", theme.IconWarning, resolveErr)
	}
	if envCfg.Provider == "" {
		return ""
	}

	var result strings.Builder

	// Determine the plan slug for ManagedBy tracking
	planSlug := filepath.Base(planPath)

	// Check if an environment is already running at the worktree level
	worktreeStateDir := filepath.Join(loadPath, ".grove", "env")
	worktreeStatePath := filepath.Join(worktreeStateDir, "state.json")
	if existingData, err := os.ReadFile(worktreeStatePath); err == nil {
		var existingState env.EnvStateFile
		if err := json.Unmarshal(existingData, &existingState); err == nil {
			// Environment already exists — attach to it instead of provisioning
			result.WriteString(fmt.Sprintf("* Attaching to existing %s environment (managed by: %s)\n",
				existingState.Provider, existingState.ManagedBy))

			// Write .env.local from existing state if it has env vars
			if len(existingState.EnvVars) > 0 {
				keys := make([]string, 0, len(existingState.EnvVars))
				for k := range existingState.EnvVars {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				var envContent strings.Builder
				for _, k := range keys {
					envContent.WriteString(fmt.Sprintf("%s=%s\n", k, existingState.EnvVars[k]))
				}
				envPath := filepath.Join(loadPath, ".env.local")
				if err := os.WriteFile(envPath, []byte(envContent.String()), 0644); err != nil {
					result.WriteString(fmt.Sprintf("%s  Warning: could not write .env.local: %v\n", theme.IconWarning, err))
				}
			}

			// Write legacy .env_state.json to plan dir for backward compat
			stateBytes, _ := json.MarshalIndent(existingState, "", "  ")
			os.WriteFile(filepath.Join(planPath, ".env_state.json"), stateBytes, 0644)

			return result.String()
		}
	}

	if activeProfile != "" {
		result.WriteString(fmt.Sprintf("* Provisioning environment %q via provider: %s\n", activeProfile, envCfg.Provider))
	} else {
		result.WriteString(fmt.Sprintf("* Provisioning environment via provider: %s\n", envCfg.Provider))
	}

	// Create state directory
	if err := os.MkdirAll(worktreeStateDir, 0755); err != nil {
		result.WriteString(fmt.Sprintf("%s  Warning: could not create .grove/env directory: %v\n", theme.IconWarning, err))
	}

	// Build request
	managedBy := fmt.Sprintf("plan:%s", planSlug)
	req := env.EnvRequest{
		Provider:  envCfg.Provider,
		PlanDir:   planPath,
		StateDir:  worktreeStateDir,
		Config:    envCfg.Config,
		ManagedBy: managedBy,
	}

	// Attach workspace node context if available
	if wsProvider != nil {
		if node := wsProvider.FindByPath(loadPath); node != nil {
			req.Workspace = node
		}
	}
	if req.Workspace == nil {
		// Fall back to lookup
		if node, err := workspace.GetProjectByPath(loadPath); err == nil {
			req.Workspace = node
		}
	}

	// Resolve the provider. Built-in providers (native/docker/terraform) need a daemon client.
	var client env.DaemonEnvClient
	if envCfg.Provider == "native" || envCfg.Provider == "docker" || envCfg.Provider == "terraform" {
		client = daemon.New()
	}
	provider := env.ResolveProvider(envCfg.Provider, client, envCfg.Command)

	resp, err := provider.Up(context.Background(), req)
	if err != nil {
		result.WriteString(fmt.Sprintf("%s  Warning: Environment provision failed: %v\n", theme.IconWarning, err))
		return result.String()
	}

	if resp == nil {
		return result.String()
	}

	// Write .env.local to worktree (sorted keys for deterministic output)
	if len(resp.EnvVars) > 0 {
		keys := make([]string, 0, len(resp.EnvVars))
		for k := range resp.EnvVars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var envContent strings.Builder
		for _, k := range keys {
			envContent.WriteString(fmt.Sprintf("%s=%s\n", k, resp.EnvVars[k]))
		}
		envPath := filepath.Join(loadPath, ".env.local")
		if err := os.WriteFile(envPath, []byte(envContent.String()), 0644); err != nil {
			result.WriteString(fmt.Sprintf("%s  Warning: could not write .env.local: %v\n", theme.IconWarning, err))
		} else {
			result.WriteString("* Wrote environment variables to .env.local\n")
		}
	}

	// Build state file with new fields
	stateFile := env.EnvStateFile{
		Provider:    envCfg.Provider,
		Command:     envCfg.Command,
		Environment: activeProfile,
		ManagedBy:   managedBy,
		EnvVars:     resp.EnvVars,
		State:       resp.State,
	}
	stateBytes, _ := json.MarshalIndent(stateFile, "", "  ")

	// Write to .grove/env/state.json (primary location)
	if err := os.WriteFile(worktreeStatePath, stateBytes, 0644); err != nil {
		result.WriteString(fmt.Sprintf("%s  Warning: could not write .grove/env/state.json: %v\n", theme.IconWarning, err))
	}

	// Write legacy .env_state.json to plan directory for backward compat
	if err := os.WriteFile(filepath.Join(planPath, ".env_state.json"), stateBytes, 0644); err != nil {
		result.WriteString(fmt.Sprintf("%s  Warning: could not write .env_state.json: %v\n", theme.IconWarning, err))
	}

	// Display endpoints
	if len(resp.Endpoints) > 0 {
		result.WriteString("  Endpoints:\n")
		for _, ep := range resp.Endpoints {
			result.WriteString(fmt.Sprintf("    - %s\n", ep))
		}
	}

	return result.String()
}
