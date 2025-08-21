package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-flow/pkg/state"
)

// RunPlanInit implements the plan init command.
func RunPlanInit(cmd *PlanInitCmd) error {
	// Resolve the full path for the new plan directory.
	planName := cmd.Dir
	planPath, err := resolvePlanPath(planName)
	if err != nil {
		return fmt.Errorf("could not resolve plan path: %w", err)
	}

	// Validate inputs with resolved path
	if err := validateInitInputs(cmd, planPath); err != nil {
		return err
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

	fmt.Println("\nNext steps:")
	fmt.Printf("1. Add your first job: flow plan add\n")
	fmt.Printf("2. Check status: flow plan status\n")

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
