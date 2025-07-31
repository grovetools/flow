package orchestration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	
	grovecontext "github.com/mattsolo1/grove-context/pkg/context"
	"github.com/mattsolo1/grove-core/git"
	"github.com/mattsolo1/grove-core/util/sanitize"
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

	// Update job status to running
	job.Status = JobStatusRunning
	job.StartTime = time.Now()
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job status: %w", err)
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
			return fmt.Errorf("prepare worktree: %w", err)
		}
		workDir = path
		
		// Regenerate context in the worktree to ensure oneshot has latest view
		if err := e.regenerateContextInWorktree(workDir, "oneshot"); err != nil {
			// Log warning but don't fail the job
			fmt.Printf("Warning: failed to regenerate context in worktree: %v\n", err)
		}
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

	// Set environment for mock testing
	os.Setenv("GROVE_CURRENT_JOB_PATH", job.FilePath)

	// Build prompt
	prompt, err := e.buildPrompt(job, plan, workDir)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		updateJobFile(job)
		return fmt.Errorf("building prompt: %w", err)
	}

	// Set environment for mock LLM if needed
	os.Setenv("GROVE_CURRENT_JOB_PATH", job.FilePath)
	defer os.Unsetenv("GROVE_CURRENT_JOB_PATH")

	// Create LLM options from job
	llmOpts := LLMOptions{
		Model:      job.Model,
		WorkingDir: workDir, // Set working directory to worktree if available
	}
	
	// Apply model override from CLI if specified
	if e.config.ModelOverride != "" {
		llmOpts.Model = e.config.ModelOverride
	} else if llmOpts.Model == "" {
		// No model specified in job or CLI override
		// The config default was already applied in the CLI layer
		// This is a fallback if all else fails
		llmOpts.Model = "claude-3-5-sonnet-20241022" // Default model
	}

	// Call LLM
	var response string
	if job.Output.Type == "generate_jobs" {
		// Use schema for structured output
		response, err = e.completeWithSchema(ctx, prompt, llmOpts)
	} else {
		response, err = e.llmClient.Complete(ctx, prompt, llmOpts)
	}
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		updateJobFile(job)
		return fmt.Errorf("LLM completion: %w", err)
	}

	// Process output
	if err := e.processOutput(response, job, plan); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		updateJobFile(job)
		return fmt.Errorf("processing output: %w", err)
	}

	// Update job status to completed
	job.Status = JobStatusCompleted
	job.EndTime = time.Now()
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job status to completed: %w", err)
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

// buildPrompt constructs the prompt from job sources.
func (e *OneShotExecutor) buildPrompt(job *Job, plan *Plan, worktreePath string) (string, error) {
	var parts []string
	
	// Check if this is a reference-based prompt (has template and prompt_source)
	if job.Template != "" && len(job.PromptSource) > 0 {
		// Reference-based prompt assembly
		
		// First, load and add the template as the system prompt
		templateManager := NewTemplateManager()
		template, err := templateManager.FindTemplate(job.Template)
		if err != nil {
			return "", fmt.Errorf("resolving template %s: %w", job.Template, err)
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
			return "", fmt.Errorf("failed to get project root: %w", err)
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
					return "", fmt.Errorf("could not find source file %s: %w", source, err)
				}
			}
			
			content, err := os.ReadFile(sourcePath)
			if err != nil {
				return "", fmt.Errorf("reading source file %s: %w", sourcePath, err)
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
					return "", fmt.Errorf("could not find prompt source %s: %w", source, err)
				}
			}
			
			content, err := os.ReadFile(sourcePath)
			if err != nil {
				return "", fmt.Errorf("reading prompt source %s: %w", sourcePath, err)
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

		for _, contextPath := range contextPaths {
			if content, err := os.ReadFile(contextPath); err == nil {
				// Sanitize UTF-8 to prevent encoding errors in LLM client
				sanitizedContent := sanitize.UTF8(content)
				parts = append(parts, fmt.Sprintf("=== Grove Context from %s ===\n%s", 
					contextPath, sanitizedContent))
			}
		}
	}

	prompt := strings.Join(parts, "\n\n")

	// Check prompt length
	if e.config.MaxPromptLength > 0 && len(prompt) > e.config.MaxPromptLength {
		return "", fmt.Errorf("prompt exceeds maximum length (%d > %d)", len(prompt), e.config.MaxPromptLength)
	}

	// The generate_jobs instructions are now included in the template itself,
	// so we don't need special logic here anymore

	return prompt, nil
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

	// For demos and isolated projects, create worktrees in the current directory
	// This allows demos to be self-contained
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("could not get working directory: %w", err)
	}

	// Use the shared method to get or prepare the worktree
	return e.worktreeManager.GetOrPrepareWorktree(ctx, cwd, job.Worktree, "oneshot")
}

// regenerateContextInWorktree regenerates the context within a worktree.
func (e *OneShotExecutor) regenerateContextInWorktree(worktreePath string, jobType string) error {
	fmt.Printf("Checking context in worktree for %s job...\n", jobType)
	
	// Create context manager for the worktree
	ctxMgr := grovecontext.NewManager(worktreePath)
	
	// Check if .grovectx exists
	groveCtxPath := filepath.Join(worktreePath, ".grovectx")
	if _, err := os.Stat(groveCtxPath); err != nil {
		if os.IsNotExist(err) {
			// No .grovectx file, but still display context info if context files exist
			return e.displayContextInfo(worktreePath)
		}
		return fmt.Errorf("checking .grovectx: %w", err)
	}
	
	// Update context from rules
	if err := ctxMgr.UpdateFromRules(); err != nil {
		return fmt.Errorf("update context from rules: %w", err)
	}
	
	// Generate context file
	if err := ctxMgr.GenerateContext(true); err != nil {
		return fmt.Errorf("generate context: %w", err)
	}
	
	// Get and display context statistics
	stats, err := ctxMgr.GetStats(10) // Show top 10 files
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
	// Read the job file content
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("reading chat file: %w", err)
	}

	// Parse the chat file
	turns, err := ParseChatFile(content)
	if err != nil {
		return fmt.Errorf("parsing chat file: %w", err)
	}

	// Determine if the job is runnable
	if len(turns) == 0 {
		return fmt.Errorf("chat file has no turns")
	}

	lastTurn := turns[len(turns)-1]
	if lastTurn.Speaker == "llm" {
		// Job is waiting for user input
		fmt.Printf("Chat job '%s' is waiting for user input.\n", job.Title)
		job.Status = JobStatusPendingUser
		updateJobFile(job)
		return nil
	}

	if lastTurn.Speaker != "user" {
		return fmt.Errorf("unexpected last speaker: %s", lastTurn.Speaker)
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
			return fmt.Errorf("prepare worktree: %w", err)
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
		
		// Also regenerate context for non-worktree case if .grovectx exists
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
		return fmt.Errorf("resolving template %s: %w", directive.Template, err)
	}

	templateContent := []byte(template.Prompt)

	// Add Grove context files
	var contextParts []string
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

	for _, contextPath := range contextPaths {
		if content, err := os.ReadFile(contextPath); err == nil {
			contextParts = append(contextParts, fmt.Sprintf("=== Grove Context from %s ===\n%s", 
				contextPath, string(content)))
			fmt.Printf("  Successfully read context from: %s (%d bytes)\n", contextPath, len(content))
		} else {
			fmt.Printf("  Failed to read context from: %s (error: %v)\n", contextPath, err)
		}
	}

	// Combine conversation history, context, and template as the full prompt
	var fullPrompt string
	if len(contextParts) > 0 {
		contextSection := strings.Join(contextParts, "\n\n")
		fullPrompt = fmt.Sprintf("%s\n\n---\n\n%s\n\n---\n\n## System Instructions\n\n%s", 
			conversationHistory, contextSection, string(templateContent))
		fmt.Printf("✓ Including %d context file(s) in chat prompt\n", len(contextParts))
	} else {
		fullPrompt = fmt.Sprintf("%s\n\n---\n\n## System Instructions\n\n%s", 
			conversationHistory, string(templateContent))
		fmt.Println("⚠️  No context files included in chat prompt")
	}

	// Create LLM options - prioritize job model from frontmatter
	llmOpts := LLMOptions{
		Model:      job.Model, // Start with job model from frontmatter
		WorkingDir: worktreePath,
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

	// Call LLM
	response, err := e.llmClient.Complete(ctx, fullPrompt, llmOpts)
	if err != nil {
		return fmt.Errorf("LLM completion: %w", err)
	}

	// Generate a unique ID for this response
	bytes := make([]byte, 3)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Errorf("generate block ID: %w", err)
	}
	blockID := hex.EncodeToString(bytes)
	
	// Append the response to the chat file
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	newCell := fmt.Sprintf("\n---\n\n<!-- grove: {\"id\": \"%s\"} -->\n## LLM Response (%s)\n\n%s\n\n<!-- grove: {\"template\": \"chat\"} -->\n", blockID, timestamp, response)
	
	// Append atomically
	if err := os.WriteFile(job.FilePath, append(content, []byte(newCell)...), 0644); err != nil {
		return fmt.Errorf("appending LLM response: %w", err)
	}

	fmt.Printf("✓ Added LLM response to chat: %s\n", job.FilePath)
	fmt.Printf("✓ Chat job is now waiting for user input\n")
	
	// Update job status - chat jobs always go to pending_user (not completed)
	job.Status = JobStatusPendingUser
	job.EndTime = time.Now()
	updateJobFile(job)
	
	return nil
}