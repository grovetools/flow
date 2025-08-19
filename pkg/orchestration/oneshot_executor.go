package orchestration

import (
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
	"github.com/mattsolo1/grove-core/git"
	"github.com/mattsolo1/grove-flow/pkg/gemini"
	"gopkg.in/yaml.v3"
)


// ExecutorConfig holds configuration for executors.
type ExecutorConfig struct {
	MaxPromptLength int
	Timeout         time.Duration
	RetryCount      int
	Model           string
	ModelOverride   string // Override model from CLI
}

// OneShotExecutor executes oneshot jobs.
type OneShotExecutor struct {
	llmClient       LLMClient
	config          *ExecutorConfig
	worktreeManager *git.WorktreeManager
	geminiClient    *gemini.Client
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

	// Initialize Gemini client if API key is available
	var geminiClient *gemini.Client
	if os.Getenv("GEMINI_API_KEY") != "" {
		ctx := context.Background()
		if gc, err := gemini.NewClient(ctx); err == nil {
			geminiClient = gc
		}
		// Silently ignore errors - Gemini will only be used when explicitly requested
	}

	return &OneShotExecutor{
		llmClient:       llmClient,
		config:          config,
		worktreeManager: git.NewWorktreeManager(),
		geminiClient:    geminiClient,
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
	prompt, contextFiles, err := e.buildPrompt(job, plan, workDir)
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

	// Determine the effective model to use
	effectiveModel := job.Model
	if e.config.ModelOverride != "" {
		effectiveModel = e.config.ModelOverride
	} else if effectiveModel == "" {
		// No model specified in job or CLI override
		// The config default was already applied in the CLI layer
		// This is a fallback if all else fails
		effectiveModel = "claude-3-5-sonnet-20241022" // Default model
	}

	// Call LLM based on model type
	var response string
	if strings.HasPrefix(effectiveModel, "gemini") {
		// Use first-party Gemini API with caching
		response, err = e.executeWithGemini(ctx, job, plan, workDir, prompt, effectiveModel)
	} else {
		// Use traditional llm command for other models
		llmOpts := LLMOptions{
			Model:        effectiveModel,
			WorkingDir:   workDir,
			ContextFiles: contextFiles,
		}
		
		if job.Output.Type == "generate_jobs" {
			// Use schema for structured output
			response, err = e.completeWithSchema(ctx, prompt, llmOpts)
		} else {
			response, err = e.llmClient.Complete(ctx, prompt, llmOpts)
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
func (e *OneShotExecutor) completeWithSchema(ctx context.Context, prompt string, opts LLMOptions) (string, error) {
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
	return e.llmClient.Complete(ctx, prompt, opts)
}

// buildPrompt constructs the prompt from job sources and returns context file paths separately.
func (e *OneShotExecutor) buildPrompt(job *Job, plan *Plan, worktreePath string) (string, []string, error) {
	var parts []string
	
	// Check if this is a reference-based prompt (has template and prompt_source)
	if job.Template != "" && len(job.PromptSource) > 0 {
		// Reference-based prompt assembly
		
		// First, load and add the template as the system prompt
		templateManager := NewTemplateManager()
		template, err := templateManager.FindTemplate(job.Template)
		if err != nil {
			return "", nil, fmt.Errorf("resolving template %s: %w", job.Template, err)
		}
		
		// Add template content as system instructions
		parts = append(parts, fmt.Sprintf("=== System Instructions (from template: %s) ===\n%s", job.Template, template.Prompt))
		
		// If worktree is specified, add a note about the working directory
		if worktreePath != "" {
			parts = append(parts, fmt.Sprintf("\n=== Working Directory ===\nYou are working in the directory: %s", worktreePath))
		}
		
		// Add all source files with clear separators
		parts = append(parts, "\n=== Source Files ===")
		
		// Get project root for resolving paths
		projectRoot, err := GetProjectRoot()
		if err != nil {
			return "", nil, fmt.Errorf("failed to get project root: %w", err)
		}
		
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
					return "", nil, fmt.Errorf("could not find source file %s: %w", source, err)
				}
			}
			
			content, err := os.ReadFile(sourcePath)
			if err != nil {
				return "", nil, fmt.Errorf("reading source file %s: %w", sourcePath, err)
			}
			
			parts = append(parts, fmt.Sprintf("\n--- START OF %s ---\n%s\n--- END OF %s ---", source, string(content), source))
		}
		
		// Add any additional instructions from the prompt body
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
				parts = append(parts, "\n"+strings.Join(additionalInstructions, "\n"))
			}
		}
		
		// Add Grove context files for reference-based prompts
		var contextPaths []string
		
		if worktreePath != "" {
			// When using a worktree, ONLY use context from the worktree
			// Check for .grove/context in worktree
			worktreeContextPath := filepath.Join(worktreePath, ".grove", "context")
			if _, err := os.Stat(worktreeContextPath); err == nil {
				contextPaths = append(contextPaths, worktreeContextPath)
			}
			
			// Check for CLAUDE.md in worktree
			worktreeClaudePath := filepath.Join(worktreePath, "CLAUDE.md")
			if _, err := os.Stat(worktreeClaudePath); err == nil {
				contextPaths = append(contextPaths, worktreeClaudePath)
			}
		} else {
			// No worktree, use the default context search
			contextPaths = FindContextFiles(plan)
		}

		// Verify context files exist
		var validContextPaths []string
		for _, contextPath := range contextPaths {
			if _, err := os.Stat(contextPath); err == nil {
				validContextPaths = append(validContextPaths, contextPath)
			}
		}
		
		prompt := strings.Join(parts, "\n\n")

		// Check prompt length (without context files which will be passed separately)
		if e.config.MaxPromptLength > 0 && len(prompt) > e.config.MaxPromptLength {
			return "", nil, fmt.Errorf("prompt exceeds maximum length (%d > %d)", len(prompt), e.config.MaxPromptLength)
		}

		return prompt, validContextPaths, nil
	} else {
		// Traditional prompt assembly (backward compatibility)
		
		// If worktree is specified, add a note about the working directory
		if worktreePath != "" {
			parts = append(parts, fmt.Sprintf("=== Working Directory ===\nYou are working in the directory: %s\n", worktreePath))
		}

		// Add prompt sources
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
					return "", nil, fmt.Errorf("could not find prompt source %s: %w", source, err)
				}
			}
			
			content, err := os.ReadFile(sourcePath)
			if err != nil {
				return "", nil, fmt.Errorf("reading prompt source %s: %w", sourcePath, err)
			}
			parts = append(parts, fmt.Sprintf("=== Content from %s ===\n%s", source, string(content)))
		}

		// Add job prompt body
		if job.PromptBody != "" {
			parts = append(parts, "=== Job Instructions ===\n"+job.PromptBody)
		}

		// Add Grove context files
		var contextPaths []string
		
		if worktreePath != "" {
			// When using a worktree, ONLY use context from the worktree
			// Check for .grove/context in worktree
			worktreeContextPath := filepath.Join(worktreePath, ".grove", "context")
			if _, err := os.Stat(worktreeContextPath); err == nil {
				contextPaths = append(contextPaths, worktreeContextPath)
			}
			
			// Check for CLAUDE.md in worktree
			worktreeClaudePath := filepath.Join(worktreePath, "CLAUDE.md")
			if _, err := os.Stat(worktreeClaudePath); err == nil {
				contextPaths = append(contextPaths, worktreeClaudePath)
			}
		} else {
			// No worktree, use the default context search
			contextPaths = FindContextFiles(plan)
		}

		// Verify context files exist
		var validContextPaths []string
		for _, contextPath := range contextPaths {
			if _, err := os.Stat(contextPath); err == nil {
				validContextPaths = append(validContextPaths, contextPath)
			}
		}
		
		prompt := strings.Join(parts, "\n\n")

		// Check prompt length (without context files which will be passed separately)
		if e.config.MaxPromptLength > 0 && len(prompt) > e.config.MaxPromptLength {
			return "", nil, fmt.Errorf("prompt exceeds maximum length (%d > %d)", len(prompt), e.config.MaxPromptLength)
		}

		return prompt, validContextPaths, nil
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

	// Also print the output to stdout for immediate feedback
	fmt.Println("\n--- Output ---")
	fmt.Println(output)
	fmt.Println("--- End Output ---")

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
func (m *MockLLMClient) Complete(ctx context.Context, prompt string, opts LLMOptions) (string, error) {
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

	// Use the shared method to get or prepare the worktree at the git root
	return e.worktreeManager.GetOrPrepareWorktree(ctx, gitRoot, job.Worktree, "oneshot")
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
			// No rules file, but still display context info if context files exist
			return e.displayContextInfo(worktreePath)
		}
		return fmt.Errorf("checking .grove/rules: %w", err)
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
		// Use default template
		directive = &ChatDirective{
			Template: "refine-plan-generic",
		}
	}

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
	// The entire content of the plan.md file serves as conversational history
	conversationHistory := string(content)
	
	// Ensure we have a template if no action is specified
	if directive.Template == "" && directive.Action == "" {
		directive.Template = "refine-plan-generic"
	}
	
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

	// Build prompt with conversation history and template
	// Context files will be passed separately as attachments
	fullPrompt := fmt.Sprintf("%s\n\n---\n\n## System Instructions\n\n%s", 
		conversationHistory, string(templateContent))
	
	if len(validContextPaths) > 0 {
		fmt.Printf("✓ Including %d context file(s) as attachments\n", len(validContextPaths))
	} else {
		fmt.Println("⚠️  No context files included in chat prompt")
	}

	// Create LLM options - prioritize job model from frontmatter
	llmOpts := LLMOptions{
		Model:        job.Model, // Start with job model from frontmatter
		WorkingDir:   worktreePath,
		ContextFiles: validContextPaths, // Pass context file paths
	}
	
	// Allow directive to override job model (for specific turns that need different models)
	if directive.Model != "" {
		llmOpts.Model = directive.Model
	}
	
	// CLI model override has highest priority (for testing/debugging)
	if e.config.ModelOverride != "" {
		llmOpts.Model = e.config.ModelOverride
	}
	
	// Final fallback if no model specified anywhere
	if llmOpts.Model == "" {
		llmOpts.Model = "claude-3-5-sonnet-20241022" // Default
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
	fmt.Printf("[DEBUG] Calling LLM with model: %s...\n", llmOpts.Model)
	var response string
	if strings.HasPrefix(llmOpts.Model, "gemini") {
		// Use Gemini API for chat
		response, err = e.executeWithGemini(ctx, job, plan, worktreePath, fullPrompt, llmOpts.Model)
		if err != nil {
			fmt.Printf("[DEBUG] Gemini API call failed with error: %v\n", err)
			execErr = fmt.Errorf("Gemini API completion: %w", err)
			return execErr
		}
	} else {
		// Use traditional llm command
		response, err = e.llmClient.Complete(ctx, fullPrompt, llmOpts)
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

// executeWithGemini executes a job using the Gemini API with caching support
func (e *OneShotExecutor) executeWithGemini(ctx context.Context, job *Job, plan *Plan, workDir string, prompt string, model string) (string, error) {
	// Check if Gemini client is available
	if e.geminiClient == nil {
		return "", fmt.Errorf("GEMINI_API_KEY environment variable is required for Gemini models")
	}

	// Identify context files
	coldContextFile := filepath.Join(workDir, ".grove", "cached-context")
	hotContextFile := filepath.Join(workDir, ".grove", "context")

	// Initialize cache manager
	cacheManager := gemini.NewCacheManager(workDir)

	// Get or create cache for cold context (if it exists and is not empty)
	var cacheInfo *gemini.CacheInfo
	if info, err := os.Stat(coldContextFile); err == nil && info.Size() > 0 {
		// Default TTL of 1 hour for cache
		ttl := 1 * time.Hour
		var err error
		cacheInfo, err = cacheManager.GetOrCreateCache(ctx, e.geminiClient, model, coldContextFile, ttl)
		if err != nil {
			return "", fmt.Errorf("managing cache: %w", err)
		}
		// Note: cacheInfo may be nil if the file is too small for caching
	} else if err == nil && info.Size() == 0 {
		fmt.Fprintf(os.Stderr, "⚠️  Cached context file is empty, skipping cache\n")
	}

	// Prepare dynamic files (hot context)
	var dynamicFiles []string
	if _, err := os.Stat(hotContextFile); err == nil {
		dynamicFiles = append(dynamicFiles, hotContextFile)
	}

	// Determine cache ID
	var cacheID string
	if cacheInfo != nil {
		cacheID = cacheInfo.CacheID
	}

	// Call Gemini API with cache
	response, err := e.geminiClient.GenerateContentWithCache(ctx, model, prompt, cacheID, dynamicFiles)
	if err != nil {
		return "", fmt.Errorf("Gemini API call failed: %w", err)
	}

	return response, nil
}