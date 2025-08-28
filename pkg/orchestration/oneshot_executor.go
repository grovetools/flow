package orchestration

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	grovecontext "github.com/mattsolo1/grove-context/pkg/context"
	"github.com/mattn/go-isatty"
	"github.com/mattsolo1/grove-core/git"
	"github.com/mattsolo1/grove-gemini/pkg/gemini"
	"gopkg.in/yaml.v3"
)

// ExecutorConfig holds configuration for executors.
type ExecutorConfig struct {
	MaxPromptLength int
	Timeout         time.Duration
	RetryCount      int
	Model           string
	ModelOverride   string // Override model from CLI
	SkipInteractive bool   // Skip interactive prompts
}

// OneShotExecutor executes oneshot jobs.
type OneShotExecutor struct {
	llmClient       LLMClient
	config          *ExecutorConfig
	worktreeManager *git.WorktreeManager
	geminiRunner    *gemini.RequestRunner
}

// NewOneShotExecutor creates a new oneshot executor.
func NewOneShotExecutor(config *ExecutorConfig) *OneShotExecutor {
	var llmClient LLMClient
	if os.Getenv("GROVE_MOCK_LLM_RESPONSE_FILE") != "" {
		llmClient = NewMockLLMClient()
	} else {
		llmClient = NewCommandLLMClient()
	}

	if config == nil {
		config = &ExecutorConfig{
			MaxPromptLength: 1000000,
			Timeout:         5 * time.Minute,
			RetryCount:      3,
			Model:           "default",
		}
	}

	return &OneShotExecutor{
		llmClient:       llmClient,
		config:          config,
		worktreeManager: git.NewWorktreeManager(),
		geminiRunner:    gemini.NewRequestRunner(),
	}
}

// Name returns the executor name.
func (e *OneShotExecutor) Name() string {
	return "oneshot"
}

// Execute runs a oneshot job.
func (e *OneShotExecutor) Execute(ctx context.Context, job *Job, plan *Plan) error {
	
	// Handle chat jobs differently
	if job.Type == JobTypeChat {
		// If a chat job is part of a multi-job plan, it's an interactive step.
		if len(plan.Jobs) > 1 {
			// An already completed job wouldn't be passed here by the orchestrator,
			// but we check status to be safe.
			if job.Status == JobStatusCompleted {
				return nil
			}

			// Skip interactive prompts if configured to do so
			if e.config.SkipInteractive {
				fmt.Printf("Skipping interactive chat job '%s' (running automatically)\n", job.Title)
				return e.executeChatJob(ctx, job, plan)
			}

			// Loop until user chooses to run or complete
			for {
				fmt.Printf("\nChat job '%s' is pending... Would you like to:\n", job.Title)
				fmt.Println("  (r)un it?")
				fmt.Println("  (c) label it completed?")
				fmt.Println("  (e)dit it with $EDITOR?")
				fmt.Print("Action (r/c/e): ")

				reader := bufio.NewReader(os.Stdin)
				input, _ := reader.ReadString('\n')
				choice := strings.TrimSpace(strings.ToLower(input))

				switch choice {
				case "r", "run", "": // Default to run
					fmt.Println("\nRunning one turn of the chat...")
					return e.executeChatJob(ctx, job, plan)
				case "c", "complete":
					fmt.Println("\nMarking chat as complete.")
					job.Status = JobStatusCompleted
					job.EndTime = time.Now()
					return updateJobFile(job)
				case "e", "edit":
					editor := os.Getenv("EDITOR")
					if editor == "" {
						editor = "vim" // A common default
					}
					fmt.Printf("\nOpening %s with %s...\n", job.FilePath, editor)
					cmd := exec.Command(editor, job.FilePath)
					cmd.Stdin = os.Stdin
					cmd.Stdout = os.Stdout
					cmd.Stderr = os.Stderr
					if err := cmd.Run(); err != nil {
						return fmt.Errorf("failed to open editor: %w", err)
					}
					fmt.Println("\nEditing finished.")
					// Continue the loop to show the prompt again
					continue
				default:
					fmt.Printf("Invalid choice '%s'. Please choose 'r', 'c', or 'e'.\n", choice)
					continue
				}
			}
		}
		// This is a single-job plan (e.g. from `flow chat run`), so execute directly.
		return e.executeChatJob(ctx, job, plan)
	}

	// Notify grove-hooks about job start
	notifyJobStart(job, plan)

	// Ensure we notify completion/failure when we exit
	var execErr error
	defer func() {
		notifyJobComplete(job, execErr)
	}()

	// Update job status to running
	job.Status = JobStatusRunning
	job.StartTime = time.Now()
	if err := updateJobFile(job); err != nil {
		execErr = fmt.Errorf("updating job status: %w", err)
		return execErr
	}

	// Determine the working directory for the job
	var workDir string
	if job.Worktree != "" {
		// Prepare git worktree
		path, err := e.prepareWorktree(ctx, job, plan)
		if err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			updateJobFile(job)
			execErr = fmt.Errorf("prepare worktree: %w", err)
			return execErr
		}
		workDir = path
	} else {
		// No worktree specified, default to the git repository root.
		var err error
		workDir, err = GetGitRootSafe(plan.Directory)
		if err != nil {
			// Fallback to the plan's directory if not in a git repo
			workDir = plan.Directory
			fmt.Printf("Warning: not a git repository. Using plan directory as working directory: %s\n", workDir)
		}
	}

	// Always regenerate context to ensure oneshot has latest view
	if err := e.regenerateContextInWorktree(workDir, "oneshot"); err != nil {
		// Log warning but don't fail the job
		fmt.Printf("Warning: failed to regenerate context: %v\n", err)
	}

	// Set environment for mock testing
	os.Setenv("GROVE_CURRENT_JOB_PATH", job.FilePath)

	// Build prompt
	prompt, promptSourceFiles, contextFiles, err := e.buildPrompt(job, plan, workDir)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		updateJobFile(job)
		execErr = fmt.Errorf("building prompt: %w", err)
		return execErr
	}

	// Set environment for mock LLM if needed
	os.Setenv("GROVE_CURRENT_JOB_PATH", job.FilePath)
	defer os.Unsetenv("GROVE_CURRENT_JOB_PATH")

	// Determine the effective model to use with clear precedence
	var effectiveModel string
	
	// 1. CLI flag (highest priority)
	if e.config.ModelOverride != "" {
		effectiveModel = e.config.ModelOverride
	} else if job.Model != "" {
		// 2. Job frontmatter model
		effectiveModel = job.Model
	} else if plan.Config != nil && plan.Config.Model != "" {
		// 3. Plan config model
		effectiveModel = plan.Config.Model
	} else if plan.Orchestration != nil && plan.Orchestration.OneshotModel != "" {
		// 4. Global config model
		effectiveModel = plan.Orchestration.OneshotModel
	} else {
		// 5. Hardcoded fallback
		effectiveModel = "claude-3-5-sonnet-20241022"
	}

	// Call LLM based on model type
	var response string
	if effectiveModel == "mock" {
		// Use mock response for testing
		response = "This is a mock LLM response for testing purposes."
		err = nil
	} else if strings.HasPrefix(effectiveModel, "gemini") {
		// Use grove-gemini package for Gemini models
		opts := gemini.RequestOptions{
			Model:            effectiveModel,
			Prompt:           prompt,            // Only template and prompt body
			PromptFiles:      promptSourceFiles, // Pass resolved source file paths
			WorkDir:          workDir,
			SkipConfirmation: e.config.SkipInteractive, // Respect -y flag
			// Don't pass context files - Gemini runner finds them automatically
		}
		// Note: Job ID and Plan name are not passed here as they would need to be propagated through
		// the gemini.RequestRunner interface, which is beyond the scope of this change
		response, err = e.geminiRunner.Run(ctx, opts)
	} else {
		// Use traditional llm command for other models
		llmOpts := LLMOptions{
			Model:             effectiveModel,
			WorkingDir:        workDir,
			ContextFiles:      contextFiles,
			PromptSourceFiles: promptSourceFiles,
		}

		if job.Output.Type == "generate_jobs" {
			// Use schema for structured output
			response, err = e.completeWithSchema(ctx, job, plan, prompt, llmOpts)
		} else {
			response, err = e.llmClient.Complete(ctx, job, plan, prompt, llmOpts)
		}
	}
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		updateJobFile(job)
		execErr = fmt.Errorf("LLM completion: %w", err)
		return execErr
	}

	// Process output
	if err := e.processOutput(response, job, plan); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		updateJobFile(job)
		execErr = fmt.Errorf("processing output: %w", err)
		return execErr
	}

	// Update job status to completed
	job.Status = JobStatusCompleted
	job.EndTime = time.Now()
	if err := updateJobFile(job); err != nil {
		execErr = fmt.Errorf("updating job status to completed: %w", err)
		return execErr
	}

	return nil
}

// completeWithSchema calls the LLM with a JSON schema for structured output.
func (e *OneShotExecutor) completeWithSchema(ctx context.Context, job *Job, plan *Plan, prompt string, opts LLMOptions) (string, error) {
	schema, err := GenerateJobCreationSchema()
	if err != nil {
		return "", fmt.Errorf("could not generate job schema: %w", err)
	}

	// Write schema to a temporary file
	schemaFile, err := os.CreateTemp("", "grove-job-schema-*.json")
	if err != nil {
		return "", fmt.Errorf("could not create temp schema file: %w", err)
	}
	defer os.Remove(schemaFile.Name())

	if _, err := schemaFile.WriteString(schema); err != nil {
		return "", fmt.Errorf("could not write to temp schema file: %w", err)
	}
	schemaFile.Close()

	// Modify llm options to include schema
	opts.SchemaPath = schemaFile.Name()
	return e.llmClient.Complete(ctx, job, plan, prompt, opts)
}

// buildPrompt constructs the prompt from job sources and returns context file paths separately.
func (e *OneShotExecutor) buildPrompt(job *Job, plan *Plan, worktreePath string) (string, []string, []string, error) {
	var parts []string
	var promptSourceFiles []string // Resolved paths for prompt source files
	var contextFiles []string      // Context files (.grove/context, CLAUDE.md)

	// Add dependency files to the prompt source list
	if len(job.Dependencies) > 0 {
		for _, dep := range job.Dependencies {
			if dep != nil && dep.FilePath != "" {
				promptSourceFiles = append(promptSourceFiles, dep.FilePath)
			}
		}
	}

	// If a template is specified, use the reference-based prompt structure
	if job.Template != "" {
		// Reference-based prompt assembly

		// First, load and add the template as the system prompt
		templateManager := NewTemplateManager()
		template, err := templateManager.FindTemplate(job.Template)
		if err != nil {
			return "", nil, nil, fmt.Errorf("resolving template %s: %w", job.Template, err)
		}

		// Start XML structure with system instructions
		parts = append(parts, fmt.Sprintf("<prompt>\n<system_instructions template=\"%s\">\n%s\n</system_instructions>", job.Template, template.Prompt))

		// If worktree is specified, add a note about the working directory
		if worktreePath != "" {
			parts = append(parts, fmt.Sprintf("\n<working_directory>%s</working_directory>", worktreePath))
		}

		// Get project root for resolving paths
		projectRoot, err := GetProjectRoot()
		if err != nil {
			return "", nil, nil, fmt.Errorf("failed to get project root: %w", err)
		}

		// Resolve prompt source file paths (without reading content)
		for _, source := range job.PromptSource {
			// Resolve the source file path
			var sourcePath string

			// If it's a relative path, make it absolute from project root
			if !filepath.IsAbs(source) {
				sourcePath = filepath.Join(projectRoot, source)
			} else {
				sourcePath = source
			}

			// Check if file exists
			if _, err := os.Stat(sourcePath); err != nil {
				// Try alternative resolution strategies
				sourcePath, err = ResolvePromptSource(source, plan)
				if err != nil {
					return "", nil, nil, fmt.Errorf("could not find source file %s: %w", source, err)
				}
			}

			// Add the resolved path to the list
			promptSourceFiles = append(promptSourceFiles, sourcePath)
		}

		// Add user's prompt/request last with clear marking
		if job.PromptBody != "" {
			// Skip the comment lines we added
			lines := strings.Split(job.PromptBody, "\n")
			var additionalInstructions []string
			skipMode := true
			for _, line := range lines {
				if skipMode && strings.HasPrefix(line, "##") {
					skipMode = false
				}
				if !skipMode && !strings.HasPrefix(line, "<!--") {
					additionalInstructions = append(additionalInstructions, line)
				}
			}
			if len(additionalInstructions) > 0 {
				parts = append(parts, fmt.Sprintf("\n<user_request priority=\"high\">\n<instruction>Please focus on addressing the following user request:</instruction>\n<content>\n%s\n</content>\n</user_request>", 
					strings.Join(additionalInstructions, "\n")))
			}
		}

		// Collect Grove context files (just paths)
		if worktreePath != "" {
			// When using a worktree, ONLY use context from the worktree
			// Check for .grove/context in worktree
			worktreeContextPath := filepath.Join(worktreePath, ".grove", "context")
			if _, err := os.Stat(worktreeContextPath); err == nil {
				contextFiles = append(contextFiles, worktreeContextPath)
			}

			// Check for CLAUDE.md in worktree
			worktreeClaudePath := filepath.Join(worktreePath, "CLAUDE.md")
			if _, err := os.Stat(worktreeClaudePath); err == nil {
				contextFiles = append(contextFiles, worktreeClaudePath)
			}
		} else {
			// No worktree, use the default context search
			for _, contextPath := range FindContextFiles(plan) {
				if _, err := os.Stat(contextPath); err == nil {
					contextFiles = append(contextFiles, contextPath)
				}
			}
		}
		
		// Close the XML prompt structure (template path)
		parts = append(parts, "</prompt>")

		prompt := strings.Join(parts, "\n")

		// Check prompt length (without context files which will be passed separately)
		if e.config.MaxPromptLength > 0 && len(prompt) > e.config.MaxPromptLength {
			return "", nil, nil, fmt.Errorf("prompt exceeds maximum length (%d > %d)", len(prompt), e.config.MaxPromptLength)
		}

		return prompt, promptSourceFiles, contextFiles, nil
	} else {
		// Traditional prompt assembly (backward compatibility)

		// If worktree is specified, add a note about the working directory
		if worktreePath != "" {
			parts = append(parts, fmt.Sprintf("=== Working Directory ===\nYou are working in the directory: %s\n", worktreePath))
		}

		// Resolve prompt source paths (without reading content)
		for _, source := range job.PromptSource {
			// First try to resolve relative to worktree if specified
			var sourcePath string
			var err error

			if worktreePath != "" && !filepath.IsAbs(source) {
				// Try worktree-relative path first
				worktreeSource := filepath.Join(worktreePath, source)
				if _, err := os.Stat(worktreeSource); err == nil {
					sourcePath = worktreeSource
				}
			}

			// If not found in worktree or no worktree, use normal resolution
			if sourcePath == "" {
				sourcePath, err = ResolvePromptSource(source, plan)
				if err != nil {
					return "", nil, nil, fmt.Errorf("could not find prompt source %s: %w", source, err)
				}
			}

			// Add the resolved path to the list
			promptSourceFiles = append(promptSourceFiles, sourcePath)
		}

		// Add prompt structure for non-template jobs
		parts = append(parts, "<prompt>")
		
		// Add job prompt body with clear marking
		if job.PromptBody != "" {
			parts = append(parts, fmt.Sprintf("<user_request priority=\"high\">\n<instruction>Please focus on addressing the following user request:</instruction>\n<content>\n%s\n</content>\n</user_request>", job.PromptBody))
		}

		// Collect Grove context files (just paths)
		if worktreePath != "" {
			// When using a worktree, ONLY use context from the worktree
			// Check for .grove/context in worktree
			worktreeContextPath := filepath.Join(worktreePath, ".grove", "context")
			if _, err := os.Stat(worktreeContextPath); err == nil {
				contextFiles = append(contextFiles, worktreeContextPath)
			}

			// Check for CLAUDE.md in worktree
			worktreeClaudePath := filepath.Join(worktreePath, "CLAUDE.md")
			if _, err := os.Stat(worktreeClaudePath); err == nil {
				contextFiles = append(contextFiles, worktreeClaudePath)
			}
		} else {
			// No worktree, use the default context search
			for _, contextPath := range FindContextFiles(plan) {
				if _, err := os.Stat(contextPath); err == nil {
					contextFiles = append(contextFiles, contextPath)
				}
			}
		}
		
		// Close the XML prompt structure (non-template path)
		parts = append(parts, "</prompt>")

		prompt := strings.Join(parts, "\n")

		// Check prompt length (without context files which will be passed separately)
		if e.config.MaxPromptLength > 0 && len(prompt) > e.config.MaxPromptLength {
			return "", nil, nil, fmt.Errorf("prompt exceeds maximum length (%d > %d)", len(prompt), e.config.MaxPromptLength)
		}

		return prompt, promptSourceFiles, contextFiles, nil
	}
}

// processOutput handles the job output based on configuration.
func (e *OneShotExecutor) processOutput(output string, job *Job, plan *Plan) error {
	switch job.Output.Type {
	case "file":
		return e.processFileOutput(output, job, plan)
	case "commit":
		return e.processCommitOutput(output, job, plan)
	case "generate_jobs":
		return e.processGeneratedJobs(output, job, plan)
	case "none":
		// No output processing needed
		return nil
	default:
		// Default to appending to job file
		return e.appendToJobFile(output, job)
	}
}

// processFileOutput writes output to a file.
func (e *OneShotExecutor) processFileOutput(output string, job *Job, plan *Plan) error {
	// If no path specified, append to job file
	if job.Output.Path == "" {
		return e.appendToJobFile(output, job)
	}

	// Write to specified path
	outputPath := job.Output.Path
	if !filepath.IsAbs(outputPath) {
		outputPath = filepath.Join(plan.Directory, outputPath)
	}

	// Create directory if needed
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	// Write file
	if err := os.WriteFile(outputPath, []byte(output), 0644); err != nil {
		return fmt.Errorf("writing output file: %w", err)
	}

	return nil
}

// processCommitOutput stages changes and creates a commit.
func (e *OneShotExecutor) processCommitOutput(output string, job *Job, plan *Plan) error {
	// TODO: Implement git operations
	// For now, just write to a file
	return e.processFileOutput(output, job, plan)
}

// processGeneratedJobs parses JSON output and creates new job files.
func (e *OneShotExecutor) processGeneratedJobs(output string, job *Job, plan *Plan) error {
	// Parse the JSON output
	var result JobGenerationSchema
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return fmt.Errorf("failed to parse LLM JSON output: %w\nRaw output:\n%s", err, output)
	}

	// Track created jobs for context update insertion and dependency resolution
	var createdJobs []struct {
		id       string
		title    string
		jobType  JobType
		filename string
	}

	// First pass: generate IDs for all jobs
	titleToID := make(map[string]string)
	titleToFilename := make(map[string]string)
	for i, jobDef := range result.Jobs {
		// Generate a unique ID for the job
		jobID := fmt.Sprintf("job-%d-%s", i+1, strings.ToLower(strings.ReplaceAll(jobDef.Title, " ", "-")))
		titleToID[jobDef.Title] = jobID
	}

	// Second pass: create job files with resolved dependencies and context updates
	for _, jobDef := range result.Jobs {
		// Skip shell jobs that are context updates (they should not be counted)
		if jobDef.Type == "shell" && strings.Contains(strings.ToLower(jobDef.Title), "context") {
			continue
		}

		// Get next job number for filename
		nextNum, err := GetNextJobNumber(plan.Directory)
		if err != nil {
			return fmt.Errorf("getting next job number: %w", err)
		}

		filename := GenerateJobFilename(nextNum, jobDef.Title)
		jobID := titleToID[jobDef.Title]

		// Store the filename for this title
		titleToFilename[jobDef.Title] = filename

		// Resolve dependencies using titleToFilename map
		var resolvedDeps []string
		for _, depTitle := range jobDef.DependsOn {
			if depFilename, ok := titleToFilename[depTitle]; ok {
				resolvedDeps = append(resolvedDeps, depFilename)
			} else {
				// If not found, assume it's already a filename
				resolvedDeps = append(resolvedDeps, depTitle)
			}
		}

		// Update jobDef with resolved dependencies
		jobDefWithResolvedDeps := jobDef
		jobDefWithResolvedDeps.DependsOn = resolvedDeps

		// Ensure all job types have the correct worktree
		// Override if not specified or if it's the example "todo-app"
		if jobDefWithResolvedDeps.Worktree == "" || jobDefWithResolvedDeps.Worktree == "todo-app" {
			jobDefWithResolvedDeps.Worktree = plan.Name
		}

		// Create job content with resolved dependencies
		jobContent := e.createJobContent(jobID, jobDefWithResolvedDeps)

		// Write job file
		jobPath := filepath.Join(plan.Directory, filename)
		if err := os.WriteFile(jobPath, []byte(jobContent), 0644); err != nil {
			return fmt.Errorf("failed to write generated job file %s: %w", filename, err)
		}
		fmt.Printf("✓ Generated new job: %s\n", filename)

		// Track the created job
		createdJobs = append(createdJobs, struct {
			id       string
			title    string
			jobType  JobType
			filename string
		}{
			id:       jobID,
			title:    jobDef.Title,
			jobType:  JobType(jobDef.Type),
			filename: filename,
		})
	}

	if len(createdJobs) == 0 {
		// Also append the output to the job file for debugging
		return e.appendToJobFile(output, job)
	}

	return nil
}

// createJobContent creates the markdown content for a job from a JobDefinition.
func (e *OneShotExecutor) createJobContent(jobID string, jobDef JobDefinition) string {
	// Build frontmatter
	fm := map[string]interface{}{
		"id":     jobID,
		"title":  jobDef.Title,
		"status": "pending",
		"type":   jobDef.Type,
	}

	// Dependencies are already resolved
	if len(jobDef.DependsOn) > 0 {
		fm["depends_on"] = jobDef.DependsOn
	}

	if jobDef.Worktree != "" {
		fm["worktree"] = jobDef.Worktree
	}

	if jobDef.OutputType != "" {
		fm["output"] = map[string]interface{}{
			"type": jobDef.OutputType,
		}
	}

	// Marshal frontmatter
	yamlBytes, _ := yaml.Marshal(fm)

	// Build complete markdown
	return fmt.Sprintf("---\n%s---\n%s\n", string(yamlBytes), jobDef.Prompt)
}

// appendToJobFile appends output to the job file.
func (e *OneShotExecutor) appendToJobFile(output string, job *Job) error {
	// Read current content
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("reading job file: %w", err)
	}

	// Append output section
	separator := "\n\n---\n\n## Output\n\n"
	newContent := string(content) + separator + output

	// Write back
	if err := os.WriteFile(job.FilePath, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("writing job file: %w", err)
	}

	return nil
}

// updateJobFile updates the job file with current status.
func updateJobFile(job *Job) error {
	// Read current content
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("reading job file: %w", err)
	}

	// Update status in frontmatter
	updates := map[string]interface{}{
		"status": string(job.Status),
	}

	newContent, err := UpdateFrontmatter(content, updates)
	if err != nil {
		return fmt.Errorf("updating frontmatter: %w", err)
	}

	// Write back
	if err := os.WriteFile(job.FilePath, newContent, 0644); err != nil {
		return fmt.Errorf("writing job file: %w", err)
	}

	return nil
}

// MockLLMClient implements a mock LLM client for testing.
type MockLLMClient struct {
	responseFile string
	outputMode   string
}

// NewMockLLMClient creates a new mock LLM client.
func NewMockLLMClient() LLMClient {
	// Check environment variables for test mode
	if file := os.Getenv("GROVE_MOCK_LLM_RESPONSE_FILE"); file != "" {
		return &MockLLMClient{
			responseFile: file,
			outputMode:   os.Getenv("GROVE_MOCK_LLM_OUTPUT_MODE"),
		}
	}
	// Return real LLM client (placeholder for now)
	return &MockLLMClient{}
}

// Complete implements the LLMClient interface for mocking.
func (m *MockLLMClient) Complete(ctx context.Context, job *Job, plan *Plan, prompt string, opts LLMOptions) (string, error) {
	// If no response file, return a simple response
	if m.responseFile == "" {
		return "Mock LLM response for: " + strings.Split(prompt, "\n")[0], nil
	}

	content, err := os.ReadFile(m.responseFile)
	if err != nil {
		return "", fmt.Errorf("read mock response: %w", err)
	}

	// For oneshot jobs that create new job files
	if m.outputMode == "split_by_frontmatter" {
		return m.splitIntoJobFiles(string(content))
	}

	return string(content), nil
}

// splitIntoJobFiles parses mock response and creates job files.
func (m *MockLLMClient) splitIntoJobFiles(content string) (string, error) {
	// Split content by frontmatter markers
	// The format is: main content, then job definitions separated by ---
	parts := strings.Split(content, "\n---\n")

	if len(parts) < 2 {
		// No jobs to create
		return content, nil
	}

	// First part is the main response
	mainResponse := parts[0]

	// Process remaining parts as job definitions
	jobNum := 2 // Start numbering from 02
	planDir := filepath.Dir(os.Getenv("GROVE_CURRENT_JOB_PATH"))

	for i := 1; i < len(parts); i++ {
		part := parts[i]

		// Skip empty parts
		if strings.TrimSpace(part) == "" {
			continue
		}

		// Check if this looks like a job definition (has id: and title:)
		if strings.Contains(part, "id:") && strings.Contains(part, "title:") {
			// Split into frontmatter and body
			bodyIdx := strings.Index(part, "---\n")
			var frontmatter, body string

			if bodyIdx != -1 {
				frontmatter = part[:bodyIdx]
				body = part[bodyIdx+4:] // Skip "---\n"
			} else {
				// No body separator, entire part is frontmatter
				frontmatter = part
				body = ""
			}

			// Extract title for filename
			titleMatch := regexp.MustCompile(`title:\s*"([^"]+)"`).FindStringSubmatch(frontmatter)
			var filename string
			if len(titleMatch) > 1 {
				// Sanitize title for filename
				safeName := strings.ToLower(titleMatch[1])
				safeName = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(safeName, "-")
				safeName = strings.Trim(safeName, "-")
				filename = fmt.Sprintf("%02d-%s.md", jobNum, safeName)
			} else {
				filename = fmt.Sprintf("%02d-generated-job.md", jobNum)
			}

			// Create job file
			jobContent := fmt.Sprintf("---\n%s\n---\n%s", frontmatter, body)
			jobPath := filepath.Join(planDir, filename)

			if err := os.WriteFile(jobPath, []byte(jobContent), 0644); err != nil {
				return "", fmt.Errorf("write job file %s: %w", filename, err)
			}

			jobNum++
		}
	}

	return mainResponse, nil
}

// prepareWorktree ensures the worktree exists and is ready.
func (e *OneShotExecutor) prepareWorktree(ctx context.Context, job *Job, plan *Plan) (string, error) {
	if job.Worktree == "" {
		return "", fmt.Errorf("job %s has no worktree specified", job.ID)
	}


	// Get git root for worktree creation
	gitRoot, err := GetGitRootSafe(plan.Directory)
	if err != nil {
		// Fallback to plan directory if not in a git repo
		gitRoot = plan.Directory
	}

	// Check if we're already in the worktree
	currentDir, _ := os.Getwd()
	if currentDir != "" && (strings.HasSuffix(currentDir, "/.grove-worktrees/"+job.Worktree) || 
		strings.HasSuffix(gitRoot, "/.grove-worktrees/"+job.Worktree)) {
		// We're already in the worktree
		return currentDir, nil
	}

	// Need to find the actual git root (not a worktree)
	// If gitRoot ends with .grove-worktrees/something, go up to find real root
	realGitRoot := gitRoot
	if idx := strings.Index(gitRoot, "/.grove-worktrees/"); idx != -1 {
		realGitRoot = gitRoot[:idx]
	}

	// Use the shared method to get or prepare the worktree at the git root
	worktreePath, err := e.worktreeManager.GetOrPrepareWorktree(ctx, realGitRoot, job.Worktree, "")
	if err != nil {
		return "", err
	}


	// Check if grove-hooks is available and install hooks in the worktree
	if _, err := exec.LookPath("grove-hooks"); err == nil {
		cmd := exec.Command("grove-hooks", "install")
		cmd.Dir = worktreePath
		if output, err := cmd.CombinedOutput(); err != nil {
			fmt.Printf("Warning: grove-hooks install failed: %v (output: %s)\n", err, string(output))
		} else {
			fmt.Printf("✓ Installed grove-hooks in worktree: %s\n", worktreePath)
		}
	}

	// Automatically initialize state within the new worktree for a better UX.
	groveDir := filepath.Join(worktreePath, ".grove")
	if err := os.MkdirAll(groveDir, 0755); err != nil {
		// Log a warning but don't fail the job, as this is a convenience feature.
		fmt.Printf("Warning: failed to create .grove directory in worktree: %v\n", err)
	} else {
		planName := filepath.Base(plan.Directory)
		stateContent := fmt.Sprintf("active_plan: %s\n", planName)
		statePath := filepath.Join(groveDir, "state.yml")
		// This is a best-effort attempt; failure should not stop the job.
		_ = os.WriteFile(statePath, []byte(stateContent), 0644)
	}

	return worktreePath, nil
}

// regenerateContextInWorktree regenerates the context within a worktree.
func (e *OneShotExecutor) regenerateContextInWorktree(worktreePath string, jobType string) error {
	fmt.Printf("Checking context in worktree for %s job...\n", jobType)

	// Create context manager for the worktree
	ctxMgr := grovecontext.NewManager(worktreePath)

	// Check if .grove/rules exists
	rulesPath := filepath.Join(worktreePath, ".grove", "rules")
	if _, err := os.Stat(rulesPath); err != nil {
		if os.IsNotExist(err) {
			// Check if we should skip interactive prompts
			if e.config.SkipInteractive {
				fmt.Println("\n⚠️  No .grove/rules file found in worktree.")
				fmt.Printf("Skipping interactive prompt and proceeding without context for %s job.\n", jobType)
				return e.displayContextInfo(worktreePath)
			}

			// Check if we have a TTY before prompting
			if !isatty.IsTerminal(os.Stdin.Fd()) && !isatty.IsCygwinTerminal(os.Stdin.Fd()) {
				fmt.Println("\n⚠️  No .grove/rules file found in worktree.")
				fmt.Printf("No TTY available, proceeding without context for %s job.\n", jobType)
				return e.displayContextInfo(worktreePath)
			}

			// Prompt user when rules file is missing
			fmt.Println("\n⚠️  No .grove/rules file found in worktree.")
			fmt.Printf("Without a rules file, context cannot be generated for this %s job.\n", jobType)
			
			// Interactive prompt loop
			for {
				fmt.Println("\nOptions:")
				fmt.Println("  [E]dit - Create and edit a rules file (default)")
				fmt.Println("  [P]roceed - Continue without context")
				fmt.Println("  [C]ancel - Cancel the job")
				fmt.Print("\nYour choice [E/p/c]: ")
				
				reader := bufio.NewReader(os.Stdin)
				input, _ := reader.ReadString('\n')
				choice := strings.TrimSpace(strings.ToLower(input))
				
				switch choice {
				case "e", "edit", "":
					// Find cx or grove-context binary
					var cxBinary string
					if _, err := exec.LookPath("cx"); err == nil {
						cxBinary = "cx"
					} else if _, err := exec.LookPath("grove-context"); err == nil {
						cxBinary = "grove-context"
					} else {
						fmt.Println("\n❌ Error: Neither 'cx' nor 'grove-context' found in PATH.")
						fmt.Println("Please install grove-context to use this feature.")
						continue
					}
					
					// Run cx edit in the worktree
					fmt.Printf("\nOpening rules editor with '%s edit'...\n", cxBinary)
					cmd := exec.Command(cxBinary, "edit")
					cmd.Dir = worktreePath
					cmd.Stdin = os.Stdin
					cmd.Stdout = os.Stdout
					cmd.Stderr = os.Stderr
					
					if err := cmd.Run(); err != nil {
						fmt.Printf("\n❌ Error running %s edit: %v\n", cxBinary, err)
						fmt.Println("Please try again or choose a different option.")
						continue
					}
					
					// After edit completes, check if rules file now exists
					if _, err := os.Stat(rulesPath); err == nil {
						fmt.Println("\n✓ Rules file created successfully.")
						// Break out of the prompt loop and continue with regeneration
						break
					} else {
						fmt.Println("\n⚠️  Rules file still not found. Please try again.")
						continue
					}
					
				case "p", "proceed":
					fmt.Println("\n⚠️  Proceeding without context from rules.")
					fmt.Println("💡 To add context for future runs, open a new terminal, navigate to the worktree, and run 'cx edit'.")
					return e.displayContextInfo(worktreePath)
					
				case "c", "cancel":
					return fmt.Errorf("job canceled by user: .grove/rules file not found")
					
				default:
					fmt.Printf("\n❌ Invalid choice '%s'. Please choose E, P, or C.\n", choice)
					continue
				}
				
				// If we reach here from the edit case, break the loop
				break
			}
		} else {
			return fmt.Errorf("checking .grove/rules: %w", err)
		}
	}

	// Display absolute path of rules file being used
	absRulesPath, _ := filepath.Abs(rulesPath)
	fmt.Printf("Found context rules file, regenerating context in worktree...\n")
	fmt.Printf("  Rules File: %s\n", absRulesPath)

	// Update context from rules
	if err := ctxMgr.UpdateFromRules(); err != nil {
		return fmt.Errorf("update context from rules: %w", err)
	}

	// Generate context file
	if err := ctxMgr.GenerateContext(true); err != nil {
		return fmt.Errorf("generate context: %w", err)
	}

	// Get and display context statistics
	// Read the files list that was just generated
	files, _ := ctxMgr.ReadFilesList(grovecontext.FilesListFile)
	stats, err := ctxMgr.GetStats("oneshot", files, 10) // Show top 10 files
	if err != nil {
		fmt.Printf("Warning: failed to get context stats: %v\n", err)
	} else {
		// Display summary statistics
		fmt.Println("\n=== Context Summary ===")
		fmt.Printf("Total files: %d\n", stats.TotalFiles)
		fmt.Printf("Total tokens: %s\n", grovecontext.FormatTokenCount(stats.TotalTokens))
		fmt.Printf("Total size: %s\n", grovecontext.FormatBytes(int(stats.TotalSize)))

		// Check token limit
		if stats.TotalTokens > 500000 {
			return fmt.Errorf("context size exceeds limit: %d tokens (max 500,000 tokens)", stats.TotalTokens)
		}

		// Show language distribution if there are files
		if stats.TotalFiles > 0 {
			fmt.Println("\nLanguage Distribution:")

			// Sort languages by token count
			var languages []grovecontext.LanguageStats
			for _, lang := range stats.Languages {
				languages = append(languages, *lang)
			}
			sort.Slice(languages, func(i, j int) bool {
				return languages[i].TotalTokens > languages[j].TotalTokens
			})

			// Show top 5 languages
			shown := 0
			for _, lang := range languages {
				if shown >= 5 {
					break
				}
				fmt.Printf("  %-12s %5.1f%%  (%s tokens, %d files)\n",
					lang.Name,
					lang.Percentage,
					grovecontext.FormatTokenCount(lang.TotalTokens),
					lang.FileCount,
				)
				shown++
			}

			fmt.Printf("\n✓ Context available for %s job.\n", jobType)
		}
	}

	return nil
}

// displayContextInfo displays information about available context files
func (e *OneShotExecutor) displayContextInfo(worktreePath string) error {
	var contextFiles []string
	var totalSize int64

	// Check for .grove/context
	groveContextPath := filepath.Join(worktreePath, ".grove", "context")
	if info, err := os.Stat(groveContextPath); err == nil && !info.IsDir() {
		contextFiles = append(contextFiles, groveContextPath)
		totalSize += info.Size()
	}

	// Check for CLAUDE.md
	claudePath := filepath.Join(worktreePath, "CLAUDE.md")
	if info, err := os.Stat(claudePath); err == nil && !info.IsDir() {
		contextFiles = append(contextFiles, claudePath)
		totalSize += info.Size()
	}

	if len(contextFiles) == 0 {
		fmt.Println("No context files found (.grove/context or CLAUDE.md)")
		return nil
	}

	fmt.Println("\n=== Context Files Available ===")
	for _, file := range contextFiles {
		relPath, _ := filepath.Rel(worktreePath, file)
		fmt.Printf("  • %s\n", relPath)
	}
	fmt.Printf("\nTotal context size: %s\n", grovecontext.FormatBytes(int(totalSize)))

	return nil
}

// executeChatJob handles the conversational logic for chat-type jobs
func (e *OneShotExecutor) executeChatJob(ctx context.Context, job *Job, plan *Plan) error {
	
	// Update job status to running FIRST
	job.Status = JobStatusRunning
	job.StartTime = time.Now()
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}

	// Notify grove-hooks about job start AFTER status is set
	notifyJobStart(job, plan)

	// Ensure we notify completion/failure when we exit
	var execErr error
	defer func() {
		notifyJobComplete(job, execErr)
	}()

	// Read the job file content
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		execErr = fmt.Errorf("reading chat file: %w", err)
		return execErr
	}

	// Check if job has a template, if not, add template: chat to frontmatter
	if job.Template == "" {
		// Add template: chat to the frontmatter
		updates := map[string]interface{}{
			"template": "chat",
		}
		newContent, err := UpdateFrontmatter(content, updates)
		if err != nil {
			execErr = fmt.Errorf("updating frontmatter with template: %w", err)
			return execErr
		}
		
		// Write the updated content back to the file
		if err := os.WriteFile(job.FilePath, newContent, 0644); err != nil {
			execErr = fmt.Errorf("writing updated chat file: %w", err)
			return execErr
		}
		
		// Update the job object with the new template
		job.Template = "chat"
		
		// Update the content variable for subsequent processing
		content = newContent
		
		fmt.Println("✓ Added 'template: chat' to job frontmatter")
	}

	// Parse the chat file
	turns, err := ParseChatFile(content)
	if err != nil {
		execErr = fmt.Errorf("parsing chat file: %w", err)
		return execErr
	}

	// Determine if the job is runnable
	if len(turns) == 0 {
		execErr = fmt.Errorf("chat file has no turns")
		return execErr
	}

	lastTurn := turns[len(turns)-1]
	if lastTurn.Speaker == "llm" {
		// Job is waiting for user input
		fmt.Printf("Chat job '%s' is waiting for user input.\n", job.Title)
		job.Status = JobStatusPendingUser
		updateJobFile(job)
		fmt.Printf("[DEBUG] Early return: job waiting for user input\n")
		return nil
	}

	if lastTurn.Speaker != "user" {
		execErr = fmt.Errorf("unexpected last speaker: %s", lastTurn.Speaker)
		return execErr
	}

	// Process the active directive
	var directive *ChatDirective
	if lastTurn.Directive != nil {
		directive = lastTurn.Directive
	} else {
		// No directive in the last turn, create a new one.
		directive = &ChatDirective{}
	}

	// --- FIX STARTS HERE ---
	// Prioritize template from the turn's directive, then job's frontmatter.
	if directive.Template == "" && job.Template != "" {
		directive.Template = job.Template
	}

	// Fallback if still no template
	if directive.Template == "" && directive.Action == "" {
		directive.Template = "chat" // Default template for chat jobs
	}
	// --- FIX ENDS HERE ---

	// Check for special actions
	if directive.Action == "complete" {
		// Mark the chat as completed
		fmt.Printf("✓ Completing chat job: %s\n", job.Title)
		job.Status = JobStatusCompleted
		job.EndTime = time.Now()
		updateJobFile(job)
		return nil
	}

	// Determine the working directory for the job
	var worktreePath string
	if job.Worktree != "" {
		// Prepare git worktree only if explicitly specified
		path, err := e.prepareWorktree(ctx, job, plan)
		if err != nil {
			execErr = fmt.Errorf("prepare worktree: %w", err)
			return execErr
		}
		worktreePath = path

		// Regenerate context in the worktree to ensure chat has latest view
		if err := e.regenerateContextInWorktree(worktreePath, "chat"); err != nil {
			// Log warning but don't fail the job
			fmt.Printf("Warning: failed to regenerate context in worktree: %v\n", err)
		}
	} else {
		// No worktree specified, default to the git repository root.
		var err error
		worktreePath, err = GetGitRootSafe(plan.Directory)
		if err != nil {
			// Fallback to the plan's directory if not in a git repo
			worktreePath = plan.Directory
			fmt.Printf("Warning: not a git repository. Using plan directory as working directory: %s\n", worktreePath)
		}

		// Also regenerate context for non-worktree case if .grove/rules exists
		if err := e.regenerateContextInWorktree(worktreePath, "chat"); err != nil {
			// Log warning but don't fail the job
			fmt.Printf("Warning: failed to regenerate context: %v\n", err)
		}
	}

	// Build the prompt
	// Extract only the body content (without frontmatter) as conversation history
	_, bodyContent, err := ParseFrontmatter(content)
	if err != nil {
		execErr = fmt.Errorf("parsing frontmatter: %w", err)
		return execErr
	}
	conversationHistory := string(bodyContent)


	// Load the template using TemplateManager
	templateManager := NewTemplateManager()
	template, err := templateManager.FindTemplate(directive.Template)
	if err != nil {
		execErr = fmt.Errorf("resolving template %s: %w", directive.Template, err)
		return execErr
	}

	templateContent := []byte(template.Prompt)

	// Add Grove context files
	var contextPaths []string

	fmt.Printf("DEBUG: Worktree path for chat job: %s\n", worktreePath)

	if worktreePath != "" {
		// When using a worktree, ONLY use context from the worktree
		// Check for .grove/context in worktree
		worktreeContextPath := filepath.Join(worktreePath, ".grove", "context")
		if _, err := os.Stat(worktreeContextPath); err == nil {
			contextPaths = append(contextPaths, worktreeContextPath)
			fmt.Printf("  Found context file: %s\n", worktreeContextPath)
		} else {
			fmt.Printf("  Context file not found: %s (error: %v)\n", worktreeContextPath, err)
		}

		// Check for CLAUDE.md in worktree
		worktreeClaudePath := filepath.Join(worktreePath, "CLAUDE.md")
		if _, err := os.Stat(worktreeClaudePath); err == nil {
			contextPaths = append(contextPaths, worktreeClaudePath)
			fmt.Printf("  Found context file: %s\n", worktreeClaudePath)
		} else {
			fmt.Printf("  Context file not found: %s (error: %v)\n", worktreeClaudePath, err)
		}
	} else {
		// No worktree, use the default context search
		fmt.Println("  No worktree path, using default context search")
		contextPaths = FindContextFiles(plan)
	}

	// Verify context files exist and collect valid paths
	var validContextPaths []string
	for _, contextPath := range contextPaths {
		if info, err := os.Stat(contextPath); err == nil {
			fmt.Printf("  Successfully found context file: %s (%d bytes)\n", contextPath, info.Size())
			validContextPaths = append(validContextPaths, contextPath)
		} else {
			fmt.Printf("  Failed to access context file: %s (error: %v)\n", contextPath, err)
		}
	}

	// Build prompt with XML structure for better LLM parsing
	// XML provides clearer boundaries and structure for the model
	fullPrompt := fmt.Sprintf(`<prompt>
<system_instructions>
%s
</system_instructions>

<user_request priority="high">
<instruction>Please focus on addressing the following user request:</instruction>
<content>
%s
</content>
</user_request>
</prompt>`,
		string(templateContent), conversationHistory)

	if len(validContextPaths) > 0 {
		fmt.Printf("✓ Including %d context file(s) as attachments\n", len(validContextPaths))
	} else {
		fmt.Println("⚠️  No context files included in chat prompt")
	}

	// Determine effective model with clear precedence
	var effectiveModel string
	
	// 1. CLI flag (highest priority)
	if e.config.ModelOverride != "" {
		effectiveModel = e.config.ModelOverride
	} else if directive.Model != "" {
		// 2. Chat directive model (for specific turns)
		effectiveModel = directive.Model
	} else if job.Model != "" {
		// 3. Job frontmatter model
		effectiveModel = job.Model
	} else if plan.Config != nil && plan.Config.Model != "" {
		// 4. Plan config model
		effectiveModel = plan.Config.Model
	} else if plan.Orchestration != nil && plan.Orchestration.OneshotModel != "" {
		// 5. Global config model
		effectiveModel = plan.Orchestration.OneshotModel
	} else {
		// 6. Hardcoded fallback
		effectiveModel = "claude-3-5-sonnet-20241022"
	}
	
	// Create LLM options with determined model
	llmOpts := LLMOptions{
		Model:        effectiveModel,
		WorkingDir:   worktreePath,
		ContextFiles: validContextPaths, // Pass context file paths
	}

	// Log memory usage before LLM call
	fmt.Printf("[DEBUG] About to call LLM with:\n")
	fmt.Printf("[DEBUG]   - Prompt length: %d bytes\n", len(fullPrompt))
	fmt.Printf("[DEBUG]   - Context files: %d\n", len(llmOpts.ContextFiles))
	for i, cf := range llmOpts.ContextFiles {
		fmt.Printf("[DEBUG]   - Context file %d: %s\n", i+1, cf)
	}

	// Run cx generate before LLM submission
	fmt.Printf("Running cx generate before submission...\n")

	// Try grove cx generate first (capture stderr to suppress error if fallback works)
	var stderrBuf strings.Builder
	cxCmd := exec.CommandContext(ctx, "grove", "cx", "generate")
	cxCmd.Dir = worktreePath
	cxCmd.Stdout = os.Stdout
	cxCmd.Stderr = &stderrBuf
	groveErr := cxCmd.Run()

	if groveErr != nil {
		// Try cx generate directly as fallback
		cxCmd = exec.CommandContext(ctx, "cx", "generate")
		cxCmd.Dir = worktreePath
		cxCmd.Stdout = os.Stdout
		cxCmd.Stderr = os.Stderr
		if err := cxCmd.Run(); err != nil {
			// Both commands failed, show the errors
			if stderrBuf.Len() > 0 {
				fmt.Fprintf(os.Stderr, "%s", stderrBuf.String())
			}
			fmt.Printf("Warning: failed to run cx generate: %v\n", err)
		}
		// If cx generate succeeded, we don't show the grove cx error
	}

	// Call LLM based on model type
	fmt.Printf("[DEBUG] Calling LLM with model: %s...\n", effectiveModel)
	var response string
	if strings.HasPrefix(effectiveModel, "gemini") {
		// Use grove-gemini package for Gemini models
		opts := gemini.RequestOptions{
			Model:            llmOpts.Model,
			Prompt:           fullPrompt,
			PromptFiles:      []string{}, // Don't include the chat file as it's already in the prompt
			WorkDir:          worktreePath,
			SkipConfirmation: e.config.SkipInteractive,  // Respect -y flag
			// Don't pass context files - Gemini runner finds them automatically
		}
		response, err = e.geminiRunner.Run(ctx, opts)
		if err != nil {
			fmt.Printf("[DEBUG] Gemini API call failed with error: %v\n", err)
			execErr = fmt.Errorf("Gemini API completion: %w", err)
			return execErr
		}
	} else {
		// Use traditional llm command
		response, err = e.llmClient.Complete(ctx, job, plan, fullPrompt, llmOpts)
		if err != nil {
			fmt.Printf("[DEBUG] LLM call failed with error: %v\n", err)
			execErr = fmt.Errorf("LLM completion: %w", err)
			return execErr
		}
	}
	fmt.Printf("[DEBUG] LLM call succeeded, response length: %d bytes\n", len(response))

	// Generate a unique ID for this response
	bytes := make([]byte, 3)
	if _, err := rand.Read(bytes); err != nil {
		execErr = fmt.Errorf("generate block ID: %w", err)
		return execErr
	}
	blockID := hex.EncodeToString(bytes)

	// Append the response to the chat file
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	newCell := fmt.Sprintf("\n---\n\n<!-- grove: {\"id\": \"%s\"} -->\n## LLM Response (%s)\n\n%s\n\n<!-- grove: {\"template\": \"chat\"} -->\n", blockID, timestamp, response)

	// Append atomically
	if err := os.WriteFile(job.FilePath, append(content, []byte(newCell)...), 0644); err != nil {
		execErr = fmt.Errorf("appending LLM response: %w", err)
		return execErr
	}

	fmt.Printf("✓ Added LLM response to chat: %s\n", job.FilePath)
	fmt.Printf("✓ Chat job is now waiting for user input\n")

	// Update job status - chat jobs always go to pending_user (not completed)
	job.Status = JobStatusPendingUser
	job.EndTime = time.Now()
	updateJobFile(job)

	return nil
}


