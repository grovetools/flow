package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sort"
	
	"github.com/mattn/go-isatty"
	"github.com/mattsolo1/grove-core/git"
)

// GenerateRecipeExecutor handles generate-recipe jobs
type GenerateRecipeExecutor struct {
	llmClient       LLMClient
	config          *ExecutorConfig
	worktreeManager *git.WorktreeManager
}

// NewGenerateRecipeExecutor creates a new generate recipe executor
func NewGenerateRecipeExecutor(config *ExecutorConfig) *GenerateRecipeExecutor {
	var llmClient LLMClient
	if os.Getenv("GROVE_MOCK_LLM_RESPONSE_FILE") != "" {
		llmClient = NewMockLLMClient()
	} else {
		llmClient = NewCommandLLMClient()
	}

	if config == nil {
		config = &ExecutorConfig{
			MaxPromptLength: 1000000,
			Timeout:         30 * time.Minute,
			RetryCount:      2,
			Model:           "default",
		}
	}

	return &GenerateRecipeExecutor{
		llmClient:       llmClient,
		config:          config,
		worktreeManager: git.NewWorktreeManager(),
	}
}

// Name returns the executor name
func (e *GenerateRecipeExecutor) Name() string {
	return "generate-recipe"
}

// Execute runs a generate-recipe job
func (e *GenerateRecipeExecutor) Execute(ctx context.Context, job *Job, plan *Plan) error {
	// Determine the working directory for the job
	var workDir string
	if job.Worktree != "" {
		// Prepare git worktree
		gitRoot, err := GetGitRootSafe(plan.Directory)
		if err != nil {
			// Fallback to plan directory if not in a git repo
			gitRoot = plan.Directory
		}
		
		worktreeDir, err := e.worktreeManager.GetOrPrepareWorktree(ctx, gitRoot, job.Worktree, "main")
		if err != nil {
			return fmt.Errorf("getting worktree: %w", err)
		}
		workDir = worktreeDir
	} else {
		// No worktree specified, use git root or plan directory
		var err error
		workDir, err = GetGitRootSafe(plan.Directory)
		if err != nil {
			// Fallback to the plan's directory if not in a git repo
			workDir = plan.Directory
		}
	}

	// Set job start time and status
	job.StartTime = time.Now()
	job.Status = JobStatusRunning
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job file: %w", err)
	}

	// Validate required fields
	if job.SourcePlan == "" {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		updateJobFile(job)
		return fmt.Errorf("source_plan is required for generate-recipe jobs")
	}
	if job.RecipeName == "" {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		updateJobFile(job)
		return fmt.Errorf("recipe_name is required for generate-recipe jobs")
	}

	// Read source plan files
	sourcePlanPath := filepath.Join(workDir, job.SourcePlan)
	planFiles, err := e.readPlanFiles(sourcePlanPath)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		updateJobFile(job)
		return fmt.Errorf("reading source plan files: %w", err)
	}

	if len(planFiles) == 0 {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		updateJobFile(job)
		return fmt.Errorf("no job files found in source plan: %s", job.SourcePlan)
	}

	// Construct prompt
	prompt := e.constructPrompt(job.PromptBody, planFiles)

	// Determine the effective model to use
	effectiveModel := e.config.Model
	if e.config.ModelOverride != "" {
		effectiveModel = e.config.ModelOverride
	} else if job.Model != "" {
		effectiveModel = job.Model
	} else if plan.Config != nil && plan.Config.Model != "" {
		effectiveModel = plan.Config.Model
	}

	// Call LLM
	llmOpts := LLMOptions{
		Model:      effectiveModel,
		WorkingDir: workDir,
	}

	response, err := e.llmClient.Complete(ctx, job, plan, prompt, llmOpts)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		updateJobFile(job)
		return fmt.Errorf("LLM completion: %w", err)
	}

	// Parse LLM response and create recipe files
	recipePath, err := e.createRecipeFiles(job.RecipeName, response)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		updateJobFile(job)
		return fmt.Errorf("creating recipe files: %w", err)
	}

	// Update job output by appending to the file
	outputSection := fmt.Sprintf("\n\n---\n\n## Output\n\nSuccessfully generated recipe '%s' at: %s\n", job.RecipeName, recipePath)
	
	// Read current content
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("reading job file: %w", err)
	}
	
	// Append output
	content = append(content, []byte(outputSection)...)
	
	// Write back
	if err := os.WriteFile(job.FilePath, content, 0644); err != nil {
		return fmt.Errorf("writing job file with output: %w", err)
	}

	// Mark job as completed
	job.Status = JobStatusCompleted
	job.EndTime = time.Now()
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job status to completed: %w", err)
	}

	// Print success message if interactive
	if isatty.IsTerminal(os.Stdout.Fd()) {
		fmt.Printf("\n✓ Recipe '%s' generated successfully\n", job.RecipeName)
		fmt.Printf("  Location: %s\n", recipePath)
		fmt.Printf("  Use with: flow plan init <name> --recipe %s\n", job.RecipeName)
	}

	return nil
}

// readPlanFiles reads all .md files from a plan directory
func (e *GenerateRecipeExecutor) readPlanFiles(planPath string) (map[string]string, error) {
	files := make(map[string]string)
	
	entries, err := os.ReadDir(planPath)
	if err != nil {
		return nil, fmt.Errorf("reading directory %s: %w", planPath, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		content, err := os.ReadFile(filepath.Join(planPath, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading file %s: %w", entry.Name(), err)
		}

		files[entry.Name()] = string(content)
	}

	return files, nil
}

// constructPrompt builds the full prompt for the LLM
func (e *GenerateRecipeExecutor) constructPrompt(userInstructions string, planFiles map[string]string) string {
	var promptBuilder strings.Builder

	// System prompt
	promptBuilder.WriteString("You are an expert grove-flow user. Your task is to create a reusable plan recipe from the provided example plan files.\n")
	promptBuilder.WriteString("Generalize the content by identifying placeholder variables and replacing specific names/values with Go template syntax (e.g., {{ .VariableName }}).\n\n")

	// User instructions
	promptBuilder.WriteString("User Instructions:\n")
	promptBuilder.WriteString(userInstructions)
	promptBuilder.WriteString("\n\n")

	// Source files
	promptBuilder.WriteString("Source Plan Files:\n\n")
	
	// Sort filenames for consistent output
	var filenames []string
	for filename := range planFiles {
		filenames = append(filenames, filename)
	}
	sort.Strings(filenames)
	
	for _, filename := range filenames {
		promptBuilder.WriteString(fmt.Sprintf("=== START: %s ===\n", filename))
		promptBuilder.WriteString(planFiles[filename])
		promptBuilder.WriteString(fmt.Sprintf("\n=== END: %s ===\n\n", filename))
	}

	// Output format instructions
	promptBuilder.WriteString("Output Format:\n")
	promptBuilder.WriteString("Generate the recipe files as a single markdown document.\n")
	promptBuilder.WriteString("Delimit each file with '--- [filename.md] ---' on its own line.\n")
	promptBuilder.WriteString("Include the full frontmatter and content for each file.\n")
	promptBuilder.WriteString("Use Go template syntax ({{ .VariableName }}) for all variables.\n")
	promptBuilder.WriteString("\nIMPORTANT: The response should contain ONLY the recipe files, nothing else.\n")

	return promptBuilder.String()
}

// createRecipeFiles parses the LLM response and creates recipe files
func (e *GenerateRecipeExecutor) createRecipeFiles(recipeName string, response string) (string, error) {
	// Determine recipe directory path
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}
	
	recipePath := filepath.Join(homeDir, ".config", "grove", "recipes", recipeName)
	
	// Create recipe directory
	if err := os.MkdirAll(recipePath, 0755); err != nil {
		return "", fmt.Errorf("creating recipe directory: %w", err)
	}

	// Parse response into individual files
	files := e.parseRecipeFiles(response)
	if len(files) == 0 {
		return "", fmt.Errorf("no files found in LLM response")
	}

	// Write each file
	for filename, content := range files {
		filePath := filepath.Join(recipePath, filename)
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			return "", fmt.Errorf("writing file %s: %w", filename, err)
		}
	}

	return recipePath, nil
}

// parseRecipeFiles extracts individual files from the LLM response
func (e *GenerateRecipeExecutor) parseRecipeFiles(response string) map[string]string {
	files := make(map[string]string)
	
	// Split by file delimiter
	parts := strings.Split(response, "--- [")
	
	for i := 1; i < len(parts); i++ {
		part := parts[i]
		
		// Extract filename
		endIdx := strings.Index(part, "] ---")
		if endIdx == -1 {
			continue
		}
		
		filename := part[:endIdx]
		content := strings.TrimSpace(part[endIdx+5:])
		
		// Remove any trailing delimiter if it exists
		if idx := strings.Index(content, "--- ["); idx != -1 {
			content = strings.TrimSpace(content[:idx])
		}
		
		files[filename] = content
	}
	
	return files
}