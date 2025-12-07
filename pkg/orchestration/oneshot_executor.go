package orchestration

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mattn/go-isatty"
	grovecontext "github.com/mattsolo1/grove-context/pkg/context"
	"github.com/mattsolo1/grove-core/git"
	grovelogging "github.com/mattsolo1/grove-core/logging"
	geminiconfig "github.com/mattsolo1/grove-gemini/pkg/config"
	"github.com/mattsolo1/grove-gemini/pkg/gemini"
	"github.com/sirupsen/logrus"
)

var (
	log       = grovelogging.NewLogger("grove-flow")
	prettyLog = grovelogging.NewPrettyLogger()
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
func NewOneShotExecutor(llmClient LLMClient, config *ExecutorConfig) *OneShotExecutor {
	if config == nil {
		config = &ExecutorConfig{
			MaxPromptLength: 0, // No limit
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
	// Get request ID from context
	requestID, _ := ctx.Value("request_id").(string)

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
				log.WithFields(logrus.Fields{
					"job_id": job.ID,
					"job_title": job.Title,
					"job_type": job.Type,
					"skip_reason": "configured",
				}).Info("Skipping interactive chat job")
				prettyLog.InfoPretty(fmt.Sprintf("Skipping interactive chat job '%s' (running automatically)", job.Title))
				return e.executeChatJob(ctx, job, plan)
			}

			// Loop until user chooses to run or complete
			for {
				prettyLog.Blank()
				prettyLog.InfoPretty(fmt.Sprintf("Chat job '%s' is pending... Would you like to:", job.Title))
				prettyLog.InfoPretty("  (r)un it?")
				prettyLog.InfoPretty("  (c) label it completed?")
				prettyLog.InfoPretty("  (e)dit it with $EDITOR?")
				prettyLog.InfoPretty("Action (r/c/e): ")

				reader := bufio.NewReader(os.Stdin)
				input, _ := reader.ReadString('\n')
				choice := strings.TrimSpace(strings.ToLower(input))

				switch choice {
				case "r", "run", "": // Default to run
					prettyLog.InfoPretty("Running one turn of the chat...")
					return e.executeChatJob(ctx, job, plan)
				case "c", "complete":
					prettyLog.InfoPretty("Marking chat as complete.")
					job.Status = JobStatusCompleted
					job.EndTime = time.Now()
					return updateJobFile(job)
				case "e", "edit":
					editor := os.Getenv("EDITOR")
					if editor == "" {
						editor = "vim" // A common default
					}
					prettyLog.InfoPretty(fmt.Sprintf("Opening %s with %s...", job.FilePath, editor))
					cmd := exec.Command(editor, job.FilePath)
					cmd.Stdin = os.Stdin
					cmd.Stdout = os.Stdout
					cmd.Stderr = os.Stderr
					if err := cmd.Run(); err != nil {
						return fmt.Errorf("failed to open editor: %w", err)
					}
					prettyLog.Success("Editing finished.")
					// Continue the loop to show the prompt again
					continue
				default:
					prettyLog.ErrorPretty(fmt.Sprintf("Invalid choice '%s'. Please choose 'r', 'c', or 'e'.", choice), nil)
					continue
				}
			}
		}
		// This is a single-job plan (e.g. from `flow chat run`), so execute directly.
		return e.executeChatJob(ctx, job, plan)
	}

	// Create lock file with the current process's PID.
	if err := CreateLockFile(job.FilePath, os.Getpid()); err != nil {
		return fmt.Errorf("failed to create lock file: %w", err)
	}
	// Ensure lock file is removed when execution finishes.
	defer RemoveLockFile(job.FilePath)

	// Update job status to running
	job.Status = JobStatusRunning
	job.StartTime = time.Now()
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}

	var execErr error

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
			log.WithFields(logrus.Fields{
				"workdir": workDir,
				"plan_dir": plan.Directory,
				"fallback": true,
			}).Warn("Not a git repository, using plan directory")
			prettyLog.WarnPretty(fmt.Sprintf("Not a git repository. Using plan directory as working directory: %s", workDir))
		}
	}

	// Always regenerate context to ensure oneshot has latest view
	if err := e.regenerateContextInWorktree(ctx, workDir, "oneshot", job, plan); err != nil {
		// Log warning but don't fail the job
		log.WithError(err).WithFields(logrus.Fields{"request_id": requestID, "job_id": job.ID}).Warn("Failed to regenerate context")
		prettyLog.WarnPretty(fmt.Sprintf("Failed to regenerate context: %v", err))
	}

	// Scope to sub-project if job.Repository is set (for ecosystem worktrees)
	// This ensures buildPrompt uses the correct context files
	workDir = ScopeToSubProject(workDir, job)

	// We need to gather context files first for BuildXMLPrompt
	_, _, contextFiles, err := e.buildPrompt(job, plan, workDir)
	if err != nil {
		// Log warning but don't fail - context files are optional
		log.WithError(err).Warn("Could not determine context files")
	}

	// Build the XML prompt and get the list of files to upload
	prompt, promptSourceFiles, err := BuildXMLPrompt(job, plan, workDir, contextFiles)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		updateJobFile(job)
		execErr = fmt.Errorf("building XML prompt: %w", err)
		return execErr
	}

	// Log the prompt content for debugging
	log.WithFields(logrus.Fields{
		"job_id":       job.ID,
		"request_id":   requestID,
		"plan_name":    plan.Name,
		"job_file":     job.FilePath,
		"prompt":       prompt,
		"prompt_chars": len(prompt),
	}).Debug("Built prompt for job")

	// Write the briefing file for auditing (no turnID for oneshot jobs)
	briefingFilePath, err := WriteBriefingFile(plan, job, prompt, "")
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{"request_id": requestID, "job_id": job.ID}).Error("Failed to write briefing file")
		prettyLog.WarnPretty(fmt.Sprintf("Warning: Failed to write briefing file: %v", err))
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to write briefing file: %w", err)
	} else if briefingFilePath != "" {
		log.WithFields(logrus.Fields{
			"job_id":             job.ID,
			"request_id":         requestID,
			"plan_name":          plan.Name,
			"job_file":           job.FilePath,
			"briefing_file_path": briefingFilePath,
			"prompt":             prompt,
			"prompt_chars":       len(prompt),
		}).Info("Briefing file created")
		prettyLog.InfoPretty(fmt.Sprintf("Briefing file created at: %s", briefingFilePath))
	}

	// Set environment for mock testing
	os.Setenv("GROVE_CURRENT_JOB_PATH", job.FilePath)

	// Set environment for mock LLM if needed
	os.Setenv("GROVE_CURRENT_JOB_PATH", job.FilePath)
	defer os.Unsetenv("GROVE_CURRENT_JOB_PATH")

	// Propagate request ID to child processes
	if requestID != "" {
		os.Setenv("GROVE_REQUEST_ID", requestID)
		defer os.Unsetenv("GROVE_REQUEST_ID")
	}

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
		// Resolve API key here where we have the correct execution context
		apiKey, geminiErr := geminiconfig.ResolveAPIKey()
		if geminiErr != nil {
			// Don't fail immediately, let the runner handle it for a more consistent error
			apiKey = ""
		}
		// Use grove-gemini package for Gemini models
		opts := gemini.RequestOptions{
			Model:            effectiveModel,
			Prompt:           prompt,            // Only template and prompt body
			PromptFiles:      promptSourceFiles, // Pass resolved source file paths
			WorkDir:          workDir,
			SkipConfirmation: e.config.SkipInteractive, // Respect -y flag
			APIKey:           apiKey, // Pass the resolved API key
			// Pass context for better logging
			Caller:   "grove-flow-oneshot",
			JobID:    job.ID,
			PlanName: plan.Name,
		}
		response, err = e.geminiRunner.Run(ctx, opts)
	} else {
		// Use traditional llm command for other models
		llmOpts := LLMOptions{
			Model:             effectiveModel,
			WorkingDir:        workDir,
			ContextFiles:      contextFiles,
			PromptSourceFiles: promptSourceFiles,
		}
		response, err = e.llmClient.Complete(ctx, job, plan, prompt, llmOpts)
	}
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		updateJobFile(job)
		log.WithError(err).WithFields(logrus.Fields{"request_id": requestID, "job_id": job.ID}).Error("LLM completion failed")
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

	// Update status to completed if we got here without errors
	job.Status = JobStatusCompleted
	job.EndTime = time.Now()
	if err := updateJobFile(job); err != nil {
		// Log but don't fail - the job executed successfully
		log.WithError(err).Warn("Failed to update job file status")
		prettyLog.WarnPretty(fmt.Sprintf("Failed to update job file status: %v", err))
	}

	return nil
}


// buildPrompt constructs the prompt from job sources and returns context file paths separately.
func (e *OneShotExecutor) buildPrompt(job *Job, plan *Plan, worktreePath string) (string, []string, []string, error) {
	var parts []string
	var promptSourceFiles []string // Resolved paths for prompt source files
	var contextFiles []string      // Context files (.grove/context, CLAUDE.md)
	var finalPromptBody string

	// Handle dependencies based on prepend_dependencies flag
	if job.PrependDependencies {
		// Prepend dependency content directly to the prompt body
		fmt.Println("🔗 prepend_dependencies enabled - inlining dependency content into prompt body")
		var dependencyContentBuilder strings.Builder
		if len(job.Dependencies) > 0 {
			// Sort dependencies by filename for consistent order
			sortedDeps := make([]*Job, len(job.Dependencies))
			copy(sortedDeps, job.Dependencies)
			sort.Slice(sortedDeps, func(i, j int) bool {
				if sortedDeps[i] == nil || sortedDeps[j] == nil {
					return false
				}
				return sortedDeps[i].Filename < sortedDeps[j].Filename
			})

			fmt.Printf("   Prepending %d dependenc%s to prompt:\n", len(sortedDeps), func() string {
				if len(sortedDeps) == 1 {
					return "y"
				}
				return "ies"
			}())
			for _, dep := range sortedDeps {
				if dep != nil && dep.FilePath != "" {
					depContent, err := os.ReadFile(dep.FilePath)
					if err != nil {
						return "", nil, nil, fmt.Errorf("reading dependency file %s: %w", dep.FilePath, err)
					}
					fmt.Printf("     • %s (inlined, not uploaded as file)\n", dep.Filename)
					dependencyContentBuilder.WriteString(fmt.Sprintf("\n\n---\n## Context from %s\n\n", dep.Filename))
					_, depBody, _ := ParseFrontmatter(depContent)
					dependencyContentBuilder.Write(depBody)
				}
			}
			dependencyContentBuilder.WriteString("\n\n---\n\n")
		}
		finalPromptBody = dependencyContentBuilder.String() + job.PromptBody
	} else {
		// Original logic: add dependencies to promptSourceFiles
		if len(job.Dependencies) > 0 {
			fmt.Printf("📎 Adding %d dependenc%s as separate file%s:\n", len(job.Dependencies), func() string {
				if len(job.Dependencies) == 1 {
					return "y"
				}
				return "ies"
			}(), func() string {
				if len(job.Dependencies) == 1 {
					return ""
				}
				return "s"
			}())
			for _, dep := range job.Dependencies {
				if dep != nil && dep.FilePath != "" {
					fmt.Printf("     • %s (uploaded as file attachment)\n", dep.Filename)
					promptSourceFiles = append(promptSourceFiles, dep.FilePath)
				}
			}
		}
		finalPromptBody = job.PromptBody
	}

	// Handle source_block reference if present
	if job.SourceBlock != "" {
		extractedContent, err := resolveSourceBlock(job.SourceBlock, plan)
		if err != nil {
			return "", nil, nil, fmt.Errorf("resolving source_block: %w", err)
		}
		// Prepend extracted content to the prompt body
		if finalPromptBody != "" {
			finalPromptBody = extractedContent + "\n\n" + finalPromptBody
		} else {
			finalPromptBody = extractedContent
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

		// Get project root for resolving paths - use worktreePath if available, fallback to workspace discovery
		var projectRoot string
		if worktreePath != "" {
			projectRoot = worktreePath
		} else {
			projectRoot = GetProjectRootSafe(".")
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
		if strings.TrimSpace(finalPromptBody) != "" {
			parts = append(parts, fmt.Sprintf("\n<user_request priority=\"high\">\n<instruction>Please focus on addressing the following user request:</instruction>\n<content>\n%s\n</content>\n</user_request>",
				strings.TrimSpace(finalPromptBody)))
		}

		// Collect Grove context files (just paths)
		// Scope to sub-project if job.Repository is set (for ecosystem worktrees)
		contextDir := ScopeToSubProject(worktreePath, job)

		if contextDir != "" {
			// When using a worktree/context dir, ONLY use context from that directory
			contextPath := filepath.Join(contextDir, ".grove", "context")
			if _, err := os.Stat(contextPath); err == nil {
				contextFiles = append(contextFiles, contextPath)
			}

			claudePath := filepath.Join(contextDir, "CLAUDE.md")
			if _, err := os.Stat(claudePath); err == nil {
				contextFiles = append(contextFiles, claudePath)
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
		// Prompt length check removed - no longer enforcing limits

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
		if finalPromptBody != "" {
			parts = append(parts, fmt.Sprintf("<user_request priority=\"high\">\n<instruction>Please focus on addressing the following user request:</instruction>\n<content>\n%s\n</content>\n</user_request>", finalPromptBody))
		}

		// Collect Grove context files (just paths)
		// Scope to sub-project if job.Repository is set (for ecosystem worktrees)
		contextDir := ScopeToSubProject(worktreePath, job)

		if contextDir != "" {
			// When using a worktree/context dir, ONLY use context from that directory
			contextPath := filepath.Join(contextDir, ".grove", "context")
			if _, err := os.Stat(contextPath); err == nil {
				contextFiles = append(contextFiles, contextPath)
			}

			claudePath := filepath.Join(contextDir, "CLAUDE.md")
			if _, err := os.Stat(claudePath); err == nil {
				contextFiles = append(contextFiles, claudePath)
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
		// Prompt length check removed - no longer enforcing limits

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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	// Write file
	if err := os.WriteFile(outputPath, []byte(output), 0o644); err != nil {
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
	if err := os.WriteFile(job.FilePath, []byte(newContent), 0o644); err != nil {
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
	if err := os.WriteFile(job.FilePath, newContent, 0o644); err != nil {
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

			if err := os.WriteFile(jobPath, []byte(jobContent), 0o644); err != nil {
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

	// Automatically initialize state within the new worktree for a better UX.
	groveDir := filepath.Join(worktreePath, ".grove")
	if err := os.MkdirAll(groveDir, 0o755); err != nil {
		// Log a warning but don't fail the job, as this is a convenience feature.
		log.WithError(err).Warn("Failed to create .grove directory in worktree")
		prettyLog.WarnPretty(fmt.Sprintf("Failed to create .grove directory in worktree: %v", err))
	} else {
		planName := filepath.Base(plan.Directory)
		stateContent := fmt.Sprintf("active_plan: %s\n", planName)
		statePath := filepath.Join(groveDir, "state.yml")
		// This is a best-effort attempt; failure should not stop the job.
		_ = os.WriteFile(statePath, []byte(stateContent), 0o644)
	}

	return worktreePath, nil
}

// regenerateContextInWorktree regenerates the context within a worktree.
func (e *OneShotExecutor) regenerateContextInWorktree(ctx context.Context, worktreePath string, jobType string, job *Job, plan *Plan) error {
	log.WithField("job_type", jobType).Info("Checking context in worktree")
	prettyLog.InfoPretty(fmt.Sprintf("Checking context in worktree for %s job...", jobType))

	// Scope to sub-project if job.Repository is set (for ecosystem worktrees)
	contextDir := ScopeToSubProject(worktreePath, job)
	if contextDir != worktreePath {
		log.WithField("context_dir", contextDir).Info("Scoping context generation to sub-project")
		prettyLog.InfoPretty(fmt.Sprintf("Scoping context to sub-project: %s", job.Repository))
	}

	// Create context manager for the worktree (or sub-project)
	ctxMgr := grovecontext.NewManager(contextDir)

	// Check if job has a custom rules file specified
	if job != nil && job.RulesFile != "" {
		// Try multiple locations for the rules file:
		// 1. Relative to plan directory (original behavior)
		// 2. Relative to current working directory
		// 3. Relative to git root
		
		var rulesFilePath string
		var foundPath bool
		
		// 1. Try relative to plan directory (original/primary location)
		candidatePath := filepath.Join(plan.Directory, job.RulesFile)
		if _, err := os.Stat(candidatePath); err == nil {
			rulesFilePath = candidatePath
			foundPath = true
		}
		
		// 2. Try relative to current working directory
		if !foundPath {
			cwd, err := os.Getwd()
			if err == nil {
				candidatePath = filepath.Join(cwd, job.RulesFile)
				if _, err := os.Stat(candidatePath); err == nil {
					rulesFilePath = candidatePath
					foundPath = true
				}
			}
		}
		
		// 3. Try relative to git root
		if !foundPath {
			gitRoot, err := GetGitRootSafe(plan.Directory)
			if err == nil {
				candidatePath = filepath.Join(gitRoot, job.RulesFile)
				if _, err := os.Stat(candidatePath); err == nil {
					rulesFilePath = candidatePath
					foundPath = true
				}
			}
		}
		
		// 4. Try as an absolute path
		if !foundPath {
			if filepath.IsAbs(job.RulesFile) {
				if _, err := os.Stat(job.RulesFile); err == nil {
					rulesFilePath = job.RulesFile
					foundPath = true
				}
			}
		}
		
		if !foundPath {
			return fmt.Errorf("rules file '%s' not found in plan directory, current directory, or git root", job.RulesFile)
		}
		
		log.WithField("rules_file", rulesFilePath).Info("Using job-specific context")
		prettyLog.InfoPretty(fmt.Sprintf("Using job-specific context from: %s", rulesFilePath))

		// Generate context using the custom rules file
		if err := ctxMgr.GenerateContextFromRulesFile(rulesFilePath, true); err != nil {
			return fmt.Errorf("failed to generate job-specific context: %w", err)
		}

		return e.displayContextInfo(contextDir)
	}

	// Check if .grove/rules exists for default context generation
	rulesPath := filepath.Join(contextDir, ".grove", "rules")
	if _, err := os.Stat(rulesPath); err != nil {
		if os.IsNotExist(err) {
			// Try to create default rules file using cx reset
			prettyLog.InfoPretty("No .grove/rules file found. Creating default rules file...")
			
			// Try cx reset to create default rules
			var resetCmd *exec.Cmd
			var resetErr error
			
			// Try grove cx reset first
			resetCmd = exec.Command("grove", "cx", "reset")
			resetCmd.Dir = contextDir
			resetCmd.Stdout = os.Stdout
			resetCmd.Stderr = os.Stderr
			resetErr = resetCmd.Run()

			if resetErr != nil {
				// Try cx reset directly as fallback
				// Fallback removed - always use grove cx for workspace awareness
				resetCmd.Dir = contextDir
				resetCmd.Stdout = os.Stdout
				resetCmd.Stderr = os.Stderr
				resetErr = resetCmd.Run()
			}
			
			// Check if cx reset succeeded in creating the rules file
			if resetErr == nil {
				if _, err := os.Stat(rulesPath); err == nil {
					prettyLog.Success("Created default .grove/rules file")
					// Continue with the normal flow - the rules file now exists
					// Fall through to the code below that handles existing rules files
				} else {
					// cx reset ran but didn't create the file
					log.Warn("cx reset completed but .grove/rules was not created")
					resetErr = fmt.Errorf("rules file not created")
				}
			}
			
			// If cx reset failed or didn't create the file, handle as before
			if resetErr != nil {
				// Check if we should skip interactive prompts
				if e.config.SkipInteractive {
					prettyLog.WarnPretty("Could not create .grove/rules file.")
					prettyLog.InfoPretty(fmt.Sprintf("Skipping interactive prompt and proceeding without context for %s job", jobType))
					log.WithField("job_type", jobType).Info(fmt.Sprintf("Skipping interactive prompt and proceeding without context for %s job", jobType))
					return e.displayContextInfo(contextDir)
				}

				// Check if we have a TTY before prompting
				if !isatty.IsTerminal(os.Stdin.Fd()) && !isatty.IsCygwinTerminal(os.Stdin.Fd()) {
					prettyLog.WarnPretty("Could not create .grove/rules file.")
					log.WithField("job_type", jobType).Info("No TTY available, proceeding without context")
					return e.displayContextInfo(contextDir)
				}

				// Prompt user when rules file is missing
				prettyLog.WarnPretty("Could not create .grove/rules file.")
				prettyLog.WarnPretty(fmt.Sprintf("Without a rules file, context cannot be generated for this %s job.", jobType))

				// Interactive prompt loop
				for {
					prettyLog.Blank()
					prettyLog.InfoPretty("Options:")
					prettyLog.InfoPretty("  [E]dit - Create and edit a rules file (default)")
					prettyLog.InfoPretty("  [P]roceed - Continue without context")
					prettyLog.InfoPretty("  [C]ancel - Cancel the job")
					prettyLog.InfoPretty("Your choice [E/p/c]: ")

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
							prettyLog.ErrorPretty("Neither 'cx' nor 'grove-context' found in PATH.", nil)
							prettyLog.ErrorPretty("Please install grove-context to use this feature.", nil)
							continue
						}

						// Run cx edit in the context directory
						prettyLog.InfoPretty(fmt.Sprintf("Opening rules editor with '%s edit'...", cxBinary))
						cmd := exec.Command("grove", "cx", "edit")
						cmd.Dir = contextDir
						cmd.Stdin = os.Stdin
						cmd.Stdout = os.Stdout
						cmd.Stderr = os.Stderr

						if err := cmd.Run(); err != nil {
							prettyLog.ErrorPretty(fmt.Sprintf("Error running %s edit", cxBinary), err)
							prettyLog.ErrorPretty("Please try again or choose a different option.", nil)
							continue
						}

						// After edit completes, check if rules file now exists
						if _, err := os.Stat(rulesPath); err == nil {
							prettyLog.Success("Rules file created successfully.")
							// Break out of the prompt loop and continue with regeneration
							break
						} else {
							prettyLog.WarnPretty("Rules file still not found. Please try again.")
							continue
						}

					case "p", "proceed":
						prettyLog.WarnPretty("Proceeding without context from rules.")
						prettyLog.InfoPretty("💡 To add context for future runs, open a new terminal, navigate to the context directory, and run 'cx edit'.")
						return e.displayContextInfo(contextDir)

					case "c", "cancel":
						return fmt.Errorf("job canceled by user: .grove/rules file not found")

					default:
						prettyLog.ErrorPretty(fmt.Sprintf("Invalid choice '%s'. Please choose E, P, or C.", choice), nil)
						continue
					}

					// If we reach here from the edit case, break the loop
					break
				}
			}
		} else {
			return fmt.Errorf("checking .grove/rules: %w", err)
		}
	}

	// Display absolute path of rules file being used
	absRulesPath, _ := filepath.Abs(rulesPath)
	log.WithField("rules_file", absRulesPath).Info("Found context rules file, regenerating context")

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
		log.WithError(err).Warn("Failed to get context stats")
	} else {
		// Display summary statistics for structured logs
		requestID, _ := ctx.Value("request_id").(string)
		log.WithFields(logrus.Fields{
			"request_id":   requestID,
			"job_id":       job.ID,
			"total_files":  stats.TotalFiles,
			"total_tokens": stats.TotalTokens,
			"total_size":   stats.TotalSize,
		}).Info("Context summary generated")

		// Display summary statistics for pretty console
		prettyLog.Divider()
		prettyLog.InfoPretty("Context Summary")
		prettyLog.Field("Total Files", fmt.Sprintf("%d", stats.TotalFiles))
		prettyLog.Field("Total Tokens", grovecontext.FormatTokenCount(stats.TotalTokens))
		prettyLog.Field("Total Size", grovecontext.FormatBytes(int(stats.TotalSize)))

		// Token limit check removed - no longer enforcing limits

		// Show language distribution if there are files
		if stats.TotalFiles > 0 {
			prettyLog.Blank()
			prettyLog.InfoPretty("Language Distribution:")

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
				// Add to structured logs
				log.WithFields(logrus.Fields{
					fmt.Sprintf("lang_%s_tokens", lang.Name): lang.TotalTokens,
					fmt.Sprintf("lang_%s_files", lang.Name):  lang.FileCount,
					fmt.Sprintf("lang_%s_pct", lang.Name):    lang.Percentage,
				}).Debug("Language stats")
				
				// Display for pretty console
				prettyLog.Field(
					lang.Name,
					fmt.Sprintf("%5.1f%% (%s tokens, %d files)",
						lang.Percentage,
						grovecontext.FormatTokenCount(lang.TotalTokens),
						lang.FileCount,
					),
				)
				shown++
			}

			prettyLog.Blank()
			prettyLog.Success(fmt.Sprintf("Context available for %s job.", jobType))
			prettyLog.Divider()
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
		prettyLog.InfoPretty("No context files found (.grove/context or CLAUDE.md)")
		return nil
	}

	prettyLog.Divider()
	prettyLog.InfoPretty("Context Files Available")
	for _, file := range contextFiles {
		relPath, _ := filepath.Rel(worktreePath, file)
		prettyLog.Field("File", relPath)
	}
	prettyLog.Blank()
	prettyLog.Field("Total context size", grovecontext.FormatBytes(int(totalSize)))
	prettyLog.Divider()

	return nil
}

// executeChatJob handles the conversational logic for chat-type jobs
func (e *OneShotExecutor) executeChatJob(ctx context.Context, job *Job, plan *Plan) error {
	// Generate a unique request ID for tracing this turn
	requestID := "req-" + uuid.New().String()[:8]
	ctx = context.WithValue(ctx, "request_id", requestID)
	log.WithFields(logrus.Fields{
		"job_id":     job.ID,
		"request_id": requestID,
		"plan_name":  plan.Name,
	}).Info("Executing chat turn")

	// Create lock file with the current process's PID.
	if err := CreateLockFile(job.FilePath, os.Getpid()); err != nil {
		return fmt.Errorf("failed to create lock file: %w", err)
	}
	// Ensure lock file is removed when execution finishes.
	defer RemoveLockFile(job.FilePath)

	// Update job status to running FIRST
	job.Status = JobStatusRunning
	job.StartTime = time.Now()
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}

	var execErr error

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
		if err := os.WriteFile(job.FilePath, newContent, 0o644); err != nil {
			execErr = fmt.Errorf("writing updated chat file: %w", err)
			return execErr
		}

		// Update the job object with the new template
		job.Template = "chat"

		// Update the content variable for subsequent processing
		content = newContent

		log.Info("Added 'template: chat' to job frontmatter")
		prettyLog.Success("Added 'template: chat' to job frontmatter")
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
		log.WithField("job", job.Title).Info("Chat job is waiting for user input")
		job.Status = JobStatusPendingUser
		updateJobFile(job)
		log.Debug("Early return: job waiting for user input")
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
		log.WithField("job", job.Title).Info("Completing chat job")
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
		if err := e.regenerateContextInWorktree(ctx, worktreePath, "chat", job, plan); err != nil {
			// Log warning but don't fail the job
			log.WithError(err).Warn("Failed to regenerate context in worktree")
		}
	} else {
		// No worktree specified, default to the git repository root.
		var err error
		worktreePath, err = GetGitRootSafe(plan.Directory)
		if err != nil {
			// Fallback to the plan's directory if not in a git repo
			worktreePath = plan.Directory
			log.WithField("workdir", worktreePath).Warn("Not a git repository, using plan directory as working directory")
		}

		// Also regenerate context for non-worktree case if .grove/rules exists
		if err := e.regenerateContextInWorktree(ctx, worktreePath, "chat", job, plan); err != nil {
			// Log warning but don't fail the job
			log.WithError(err).Warn("Failed to regenerate context")
		}
	}

	// Scope to sub-project if job.Repository is set (for ecosystem worktrees)
	// This ensures chat uses the correct context files
	worktreePath = ScopeToSubProject(worktreePath, job)

	// Build the prompt
	// Extract only the body content (without frontmatter) as conversation history
	_, bodyContent, err := ParseFrontmatter(content)
	if err != nil {
		execErr = fmt.Errorf("parsing frontmatter: %w", err)
		return execErr
	}
	conversationHistory := string(bodyContent)

	// Handle dependencies - either prepend to conversation or collect for upload
	var dependencyFilePaths []string
	if job.PrependDependencies {
		fmt.Println("🔗 prepend_dependencies enabled - inlining dependency content into chat history")
		var dependencyContentBuilder strings.Builder
		if len(job.Dependencies) > 0 {
			// Sort dependencies by filename for consistent order
			sortedDeps := make([]*Job, len(job.Dependencies))
			copy(sortedDeps, job.Dependencies)
			sort.Slice(sortedDeps, func(i, j int) bool {
				if sortedDeps[i] == nil || sortedDeps[j] == nil {
					return false
				}
				return sortedDeps[i].Filename < sortedDeps[j].Filename
			})

			fmt.Printf("   Prepending %d dependenc%s to chat:\n", len(sortedDeps), func() string {
				if len(sortedDeps) == 1 {
					return "y"
				}
				return "ies"
			}())
			for _, dep := range sortedDeps {
				if dep != nil && dep.FilePath != "" {
					depContent, err := os.ReadFile(dep.FilePath)
					if err != nil {
						execErr = fmt.Errorf("reading dependency file %s: %w", dep.FilePath, err)
						return execErr
					}
					fmt.Printf("     • %s (inlined, not uploaded as file)\n", dep.Filename)
					dependencyContentBuilder.WriteString(fmt.Sprintf("\n\n---\n## Context from %s\n\n", dep.Filename))
					_, depBody, _ := ParseFrontmatter(depContent)
					dependencyContentBuilder.Write(depBody)
				}
			}
			dependencyContentBuilder.WriteString("\n\n---\n\n")
		}
		conversationHistory = dependencyContentBuilder.String() + conversationHistory
	} else if len(job.Dependencies) > 0 {
		// Collect dependency file paths for upload/attachment to LLM
		fmt.Printf("📎 Collecting %d dependenc%s for upload:\n", len(job.Dependencies), func() string {
			if len(job.Dependencies) == 1 {
				return "y"
			}
			return "ies"
		}())
		for _, dep := range job.Dependencies {
			if dep != nil && dep.FilePath != "" {
				dependencyFilePaths = append(dependencyFilePaths, dep.FilePath)
				fmt.Printf("     • %s (will be uploaded as file attachment)\n", dep.Filename)
			}
		}
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

	log.WithField("worktree", worktreePath).Debug("Worktree path for chat job")

	// Scope to sub-project if job.Repository is set (for ecosystem worktrees)
	contextDir := ScopeToSubProject(worktreePath, job)

	if contextDir != "" {
		// When using a worktree/context dir, ONLY use context from that directory
		// Check for .grove/context
		contextPath := filepath.Join(contextDir, ".grove", "context")
		if _, err := os.Stat(contextPath); err == nil {
			contextPaths = append(contextPaths, contextPath)
			log.WithField("file", contextPath).Debug("Found context file")
		} else {
			log.WithFields(logrus.Fields{
				"file":  contextPath,
				"error": err,
			}).Debug("Context file not found")
		}

		// Check for CLAUDE.md
		claudePath := filepath.Join(contextDir, "CLAUDE.md")
		if _, err := os.Stat(claudePath); err == nil {
			contextPaths = append(contextPaths, claudePath)
			log.WithField("file", claudePath).Debug("Found context file")
		} else {
			fmt.Printf("  Context file not found: %s (error: %v)\n", claudePath, err)
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
	// Generate a unique ID for this chat turn (used for both briefing filename and response directive)
	bytes := make([]byte, 3)
	if _, err := rand.Read(bytes); err != nil {
		execErr = fmt.Errorf("generate turn ID: %w", err)
		return execErr
	}
	turnID := hex.EncodeToString(bytes)

	// Build the briefing XML with context section if there are dependencies or context files
	var promptBuilder strings.Builder
	promptBuilder.WriteString("<prompt>\n<system_instructions>\n")
	promptBuilder.WriteString(string(templateContent))
	promptBuilder.WriteString("\n</system_instructions>\n")

	// Add context section if we have dependencies or context files
	if len(dependencyFilePaths) > 0 || len(validContextPaths) > 0 {
		promptBuilder.WriteString("\n    <context>\n")

		// Add dependencies
		for _, depPath := range dependencyFilePaths {
			promptBuilder.WriteString(fmt.Sprintf("        <inlined_dependency file=\"%s\" description=\"This file's content is provided elsewhere in the prompt context.\"/>\n", filepath.Base(depPath)))
		}

		// Add context files
		for _, ctxPath := range validContextPaths {
			promptBuilder.WriteString(fmt.Sprintf("        <inlined_context_file file=\"%s\" description=\"Project context file provided elsewhere in the prompt.\"/>\n", filepath.Base(ctxPath)))
		}

		promptBuilder.WriteString("    </context>\n")
	}

	promptBuilder.WriteString("\n<user_request priority=\"high\">\n")
	promptBuilder.WriteString("<instruction>Please focus on addressing the following user request:</instruction>\n")
	promptBuilder.WriteString("<content>\n")
	promptBuilder.WriteString(conversationHistory)
	promptBuilder.WriteString("\n</content>\n")
	promptBuilder.WriteString("</user_request>\n")
	promptBuilder.WriteString("</prompt>\n")

	fullPrompt := promptBuilder.String()

	// Log the prompt content for debugging
	log.WithFields(logrus.Fields{
		"job_id":       job.ID,
		"request_id":   requestID,
		"plan_name":    plan.Name,
		"job_file":     job.FilePath,
		"turn_id":      turnID,
		"prompt":       fullPrompt,
		"prompt_chars": len(fullPrompt),
	}).Debug("Built prompt for chat turn")

	// Write the full prompt to a briefing file for observability using the turn UUID
	if briefingFilePath, err := WriteBriefingFile(plan, job, fullPrompt, turnID); err != nil {
		log.WithError(err).Warn("Failed to write chat briefing file")
	} else {
		log.WithFields(logrus.Fields{
			"job_id":             job.ID,
			"request_id":         requestID,
			"plan_name":          plan.Name,
			"job_file":           job.FilePath,
			"turn_id":            turnID,
			"briefing_file_path": briefingFilePath,
			"prompt":             fullPrompt,
			"prompt_chars":       len(fullPrompt),
		}).Info("Chat briefing file created")
		prettyLog.InfoPretty(fmt.Sprintf("Chat briefing file created at: %s", briefingFilePath))
	}

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
		Model:             effectiveModel,
		WorkingDir:        contextDir,
		ContextFiles:      validContextPaths,      // Pass context file paths
		PromptSourceFiles: dependencyFilePaths,    // Pass dependency file paths
	}

	// Log memory usage before LLM call
	log.Debug("About to call LLM")
	log.WithField("prompt_length_bytes", len(fullPrompt)).Debug("Prompt length")
	log.WithField("context_files_count", len(llmOpts.ContextFiles)).Debug("Context files")
	for i, cf := range llmOpts.ContextFiles {
		log.WithField("file", cf).Debug(fmt.Sprintf("Context file %d", i+1))
	}

	// Only run cx generate if we don't have a custom rules file
	// Custom rules files have already generated the correct context via regenerateContextInWorktree
	// Running cx generate would overwrite it with the wrong rules
	if job.RulesFile == "" {
		// For jobs without custom rules, cx generate ensures we have the latest context
		prettyLog.InfoPretty("Running cx generate before submission...")

		// Create a log file for the cx generate output
		logDir := ResolveLogDirectory(plan, job)
		logFileName := fmt.Sprintf("%s-%s-cx.log", job.ID, time.Now().Format("150405"))
		logFilePath := filepath.Join(logDir, logFileName)
		logFile, logErr := os.Create(logFilePath)
		if logErr == nil {
			defer logFile.Close()
			prettyLog.InfoPretty(fmt.Sprintf("`cx generate` output is being logged to: %s", logFilePath))
		}

		// Try grove cx generate first
		cxCmd := exec.CommandContext(ctx, "grove", "cx", "generate")
		cxCmd.Dir = contextDir
		if logFile != nil {
			cxCmd.Stdout = logFile
			cxCmd.Stderr = logFile
		} else {
			cxCmd.Stdout = os.Stdout
			cxCmd.Stderr = os.Stderr
		}
		groveErr := cxCmd.Run()

		if groveErr != nil {
			// Try cx generate directly as fallback
			// Fallback removed - always use grove cx for workspace awareness
			cxCmd.Dir = contextDir
			if logFile != nil {
				cxCmd.Stdout = logFile
				cxCmd.Stderr = logFile
			} else {
				cxCmd.Stdout = os.Stdout
				cxCmd.Stderr = os.Stderr
			}
			if err := cxCmd.Run(); err != nil {
				prettyLog.WarnPretty(fmt.Sprintf("Failed to run cx generate: %v", err))
			}
		}
	} else {
		log.WithField("rules_file", job.RulesFile).Info("Skipping cx generate (using custom rules file)")
		prettyLog.InfoPretty(fmt.Sprintf("Skipping cx generate (using custom rules file: %s)", job.RulesFile))
	}

	// Call LLM based on model type
	log.WithField("model", effectiveModel).Debug("Calling LLM")
	var response string
	var apiKey string
	var geminiErr error
	if strings.HasPrefix(effectiveModel, "gemini") {
		// Resolve API key here where we have the correct execution context
		apiKey, geminiErr = geminiconfig.ResolveAPIKey()
		if geminiErr != nil {
			// Don't fail immediately, let the runner handle it for a more consistent error
			apiKey = ""
		}
		// Use grove-gemini package for Gemini models
		opts := gemini.RequestOptions{
			Model:            llmOpts.Model,
			Prompt:           fullPrompt,
			APIKey:           apiKey, // Pass the resolved API key
			PromptFiles:      []string{}, // Don't include the chat file as it's already in the prompt
			WorkDir:          contextDir,
			SkipConfirmation: e.config.SkipInteractive, // Respect -y flag
			// Pass context for better logging
			Caller:   "grove-flow-chat",
			JobID:    job.ID,
			PlanName: plan.Name,
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
			log.WithError(err).Debug("LLM call failed")
			execErr = fmt.Errorf("LLM completion: %w", err)
			return execErr
		}
	}
	log.WithField("response_length_bytes", len(response)).Debug("LLM call succeeded")

	// Use the same turnID that was generated earlier for the briefing file
	// This creates a 1:1 correspondence between briefing files and chat turns
	// (turnID was already generated before the LLM call)

	// Append the response to the chat file
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	newCell := fmt.Sprintf("\n---\n\n<!-- grove: {\"id\": \"%s\"} -->\n## LLM Response (%s)\n\n%s\n\n<!-- grove: {\"template\": \"chat\"} -->\n", turnID, timestamp, response)

	// Append atomically
	if err := os.WriteFile(job.FilePath, append(content, []byte(newCell)...), 0o644); err != nil {
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
