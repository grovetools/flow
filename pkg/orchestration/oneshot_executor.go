package orchestration

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mattn/go-isatty"
	grovecontext "github.com/mattsolo1/grove-context/pkg/context"
	"github.com/mattsolo1/grove-anthropic/pkg/anthropic"
	anthropicconfig "github.com/mattsolo1/grove-anthropic/pkg/config"
	anthropicmodels "github.com/mattsolo1/grove-anthropic/pkg/models"
	"github.com/mattsolo1/grove-core/git"
	grovelogging "github.com/mattsolo1/grove-core/logging"
	"github.com/mattsolo1/grove-core/tui/theme"
	geminiconfig "github.com/mattsolo1/grove-gemini/pkg/config"
	"github.com/mattsolo1/grove-gemini/pkg/gemini"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

var (
	log       = grovelogging.NewLogger("grove-flow")
	prettyLog = grovelogging.NewPrettyLogger()
)

// resolveModelAlias expands a model alias to its full API ID, or returns the input unchanged.
// Checks Anthropic aliases first, then could be extended for other providers.
func resolveModelAlias(model string) string {
	// Try Anthropic aliases
	if resolved := anthropicmodels.ResolveAlias(model); resolved != model {
		return resolved
	}
	// Gemini models don't have aliases (IDs are already short)
	return model
}

// isTUIMode checks if we're running in TUI mode
func isTUIMode() bool {
	return os.Getenv("GROVE_FLOW_TUI_MODE") == "true"
}

// printfUnlessTUI prints to stdout unless in TUI mode
func printfUnlessTUI(format string, args ...interface{}) {
	if !isTUIMode() {
		fmt.Printf(format, args...)
	}
}

// printlnUnlessTUI prints to stdout unless in TUI mode
func printlnUnlessTUI(args ...interface{}) {
	if !isTUIMode() {
		fmt.Println(args...)
	}
}

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
	anthropicRunner *anthropic.RequestRunner
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
		anthropicRunner: anthropic.NewRequestRunner(),
	}
}

// Name returns the executor name.
func (e *OneShotExecutor) Name() string {
	return "oneshot"
}

// Execute runs a oneshot job.
func (e *OneShotExecutor) Execute(ctx context.Context, job *Job, plan *Plan) error {
	// Get the output writer from context
	output := grovelogging.GetWriter(ctx)

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

			// Skip interactive prompts if configured to do so or if not in a TTY
			isTTY := isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
			if e.config.SkipInteractive || !isTTY {
				log.WithFields(logrus.Fields{
					"job_id":      job.ID,
					"job_title":   job.Title,
					"job_type":    job.Type,
					"skip_reason": "configured or no TTY",
				}).Info("Skipping interactive chat job")
				prettyLog.InfoPretty(fmt.Sprintf("Skipping interactive chat job '%s' (running automatically)", job.Title))
				return e.executeChatJob(ctx, job, plan, output)
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
					return e.executeChatJob(ctx, job, plan, output)
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
		return e.executeChatJob(ctx, job, plan, output)
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
		// No worktree specified, default to the project git repository root (notebook-aware).
		var err error
		workDir, err = GetProjectGitRoot(plan.Directory)
		if err != nil {
			// Fallback to the plan's directory if not in a git repo
			workDir = plan.Directory
			log.WithFields(logrus.Fields{
				"workdir": workDir,
				"plan_dir": plan.Directory,
				"fallback": true,
			}).Warn("Not a git repository, using plan directory")
			prettyLog.WarnPrettyCtx(ctx, fmt.Sprintf("Not a git repository. Using plan directory as working directory: %s", workDir))
		}
	}

	// Always regenerate context to ensure oneshot has latest view
	if err := e.regenerateContextInWorktree(ctx, workDir, "oneshot", job, plan); err != nil {
		// Log warning but don't fail the job
		log.WithError(err).WithFields(logrus.Fields{"request_id": requestID, "job_id": job.ID}).Warn("Failed to regenerate context")
		prettyLog.WarnPrettyCtx(ctx, fmt.Sprintf("Failed to regenerate context: %v", err))
	}

	// --- Concept Gathering Logic ---
	if job.GatherConceptNotes || job.GatherConceptPlans {
		conceptContextFile, err := gatherConcepts(ctx, job, plan, workDir)
		if err != nil {
			log.WithError(err).WithFields(logrus.Fields{"request_id": requestID, "job_id": job.ID}).Error("Failed to gather concepts")
			prettyLog.WarnPrettyCtx(ctx, fmt.Sprintf("Warning: Failed to gather concepts: %v", err))
		} else if conceptContextFile != "" {
			// Add the aggregated concepts file to the list of files to upload
			// We handle this here before building the prompt.
		}
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
		prettyLog.WarnPrettyCtx(ctx, fmt.Sprintf("Warning: Failed to write briefing file: %v", err))
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to write briefing file: %w", err)
	}

	// --- Concept Gathering Logic ---
	if job.GatherConceptNotes || job.GatherConceptPlans {
		conceptContextFile, err := gatherConcepts(ctx, job, plan, workDir)
		if err != nil {
			log.WithError(err).WithFields(logrus.Fields{"request_id": requestID, "job_id": job.ID}).Error("Failed to gather concepts")
			prettyLog.WarnPrettyCtx(ctx, fmt.Sprintf("Warning: Failed to gather concepts: %v", err))
			// Don't fail the job, just log warning
		} else if conceptContextFile != "" {
			// Add the aggregated concepts file to the list of files to upload
			promptSourceFiles = append(promptSourceFiles, conceptContextFile)
			log.WithFields(logrus.Fields{
				"job_id":               job.ID,
				"request_id":           requestID,
				"concept_context_file": conceptContextFile,
			}).Info("Added aggregated concepts to context")
			prettyLog.InfoPrettyCtx(ctx, fmt.Sprintf("Added aggregated concepts to context: %s", conceptContextFile))
		}
	}

	if briefingFilePath != "" {
		log.WithFields(logrus.Fields{
			"job_id":             job.ID,
			"request_id":         requestID,
			"plan_name":          plan.Name,
			"job_file":           job.FilePath,
			"briefing_file_path": briefingFilePath,
			"prompt":             prompt,
			"prompt_chars":       len(prompt),
		}).Info("Briefing file created")
		if isTUIMode() {
			fmt.Fprintf(output, "\n%s  Briefing file created at: %s\n\n", theme.IconCode, briefingFilePath)
		} else {
			fmt.Fprintln(output)
			prettyLog.InfoPretty(fmt.Sprintf("%s  Briefing file created at: %s", theme.IconCode, briefingFilePath))
			fmt.Fprintln(output)
		}
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
	var modelSource string

	// 1. CLI flag (highest priority)
	if e.config.ModelOverride != "" {
		effectiveModel = e.config.ModelOverride
		modelSource = "CLI override"
	} else if job.Model != "" {
		// 2. Job frontmatter model
		effectiveModel = job.Model
		modelSource = "job frontmatter"
	} else if plan.Config != nil && plan.Config.Model != "" {
		// 3. Plan config model
		effectiveModel = plan.Config.Model
		modelSource = "plan config"
	} else if plan.Orchestration != nil && plan.Orchestration.OneshotModel != "" {
		// 4. Global config model
		effectiveModel = plan.Orchestration.OneshotModel
		modelSource = "global config"
	} else {
		// 5. Hardcoded fallback - use Anthropic default
		effectiveModel = anthropicmodels.DefaultModel
		modelSource = "default fallback"
	}

	// Resolve model aliases (e.g., "claude-sonnet-4-5" -> "claude-sonnet-4-5-20250929")
	effectiveModel = resolveModelAlias(effectiveModel)

	logrus.WithFields(logrus.Fields{
		"job_id":       job.ID,
		"model":        effectiveModel,
		"model_source": modelSource,
	}).Debug("Resolved model for job execution")

	// Call LLM based on model type
	var response string
	if effectiveModel == "mock" {
		// Use mock response for testing
		response = "This is a mock LLM response for testing purposes."
		err = nil
	} else if os.Getenv("GROVE_MOCK_LLM_RESPONSE_FILE") != "" {
		// Check if mocking is enabled - if so, always use llmClient regardless of model
		// Use traditional llm command which is mocked
		llmOpts := LLMOptions{
			Model:             effectiveModel,
			WorkingDir:        workDir,
			ContextFiles:      contextFiles,
			PromptSourceFiles: promptSourceFiles,
		}
		response, err = e.llmClient.Complete(ctx, job, plan, prompt, llmOpts, output)
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
	} else if strings.HasPrefix(effectiveModel, "claude") {
		// Resolve API key here where we have the correct execution context
		apiKey, anthropicErr := anthropicconfig.ResolveAPIKey()
		if anthropicErr != nil {
			err = fmt.Errorf("resolving Anthropic API key: %w", anthropicErr)
		} else {
			// Use grove-anthropic package for Claude models
			opts := anthropic.RequestOptions{
				Model:        effectiveModel,
				Prompt:       prompt,
				ContextFiles: append(promptSourceFiles, contextFiles...),
				WorkDir:      workDir,
				APIKey:       apiKey,
				MaxTokens:    64000,
				Caller:       "grove-flow-oneshot",
				JobID:        job.ID,
				PlanName:     plan.Name,
			}
			if isTUIMode() {
				fmt.Fprintf(output, "\n%s Calling Anthropic API with model: %s\n\n", theme.IconRobot, effectiveModel)
			}
			response, err = e.anthropicRunner.Run(ctx, opts)
		}
	} else {
		// Use traditional llm command for other models
		llmOpts := LLMOptions{
			Model:             effectiveModel,
			WorkingDir:        workDir,
			ContextFiles:      contextFiles,
			PromptSourceFiles: promptSourceFiles,
		}
		if isTUIMode() {
			fmt.Fprintf(output, "\n󰚩 Calling Gemini API with model: %s\n\n", effectiveModel)
		}
		response, err = e.llmClient.Complete(ctx, job, plan, prompt, llmOpts, output)
	}
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		updateJobFile(job)
		log.WithError(err).WithFields(logrus.Fields{"request_id": requestID, "job_id": job.ID}).Error("LLM completion failed")
		execErr = fmt.Errorf("LLM completion: %w", err)
		return execErr
	}

	// Append output to job file
	if err := e.appendToJobFile(response, job); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		updateJobFile(job)
		execErr = fmt.Errorf("appending output to job file: %w", err)
		return execErr
	}

	// Update status to completed if we got here without errors
	job.Status = JobStatusCompleted
	job.EndTime = time.Now()
	if err := updateJobFile(job); err != nil {
		// Log but don't fail - the job executed successfully
		log.WithError(err).Warn("Failed to update job file status")
		prettyLog.WarnPrettyCtx(ctx, fmt.Sprintf("Failed to update job file status: %v", err))
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
		printlnUnlessTUI("🔗 prepend_dependencies enabled - inlining dependency content into prompt body")
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

			printfUnlessTUI("   Prepending %d dependenc%s to prompt:\n", len(sortedDeps), func() string {
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
					printfUnlessTUI("     • %s (inlined, not uploaded as file)\n", dep.Filename)
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
			printfUnlessTUI("📎 Adding %d dependenc%s as separate file%s:\n", len(job.Dependencies), func() string {
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
					printfUnlessTUI("     • %s (uploaded as file attachment)\n", dep.Filename)
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
}

// NewMockLLMClient creates a new mock LLM client.
func NewMockLLMClient() LLMClient {
	// Check environment variables for test mode
	if file := os.Getenv("GROVE_MOCK_LLM_RESPONSE_FILE"); file != "" {
		return &MockLLMClient{
			responseFile: file,
		}
	}
	// Return real LLM client (placeholder for now)
	return &MockLLMClient{}
}

// Complete implements the LLMClient interface for mocking.
func (m *MockLLMClient) Complete(ctx context.Context, job *Job, plan *Plan, prompt string, opts LLMOptions, output io.Writer) (string, error) {
	// If no response file, return a simple response
	if m.responseFile == "" {
		return "Mock LLM response for: " + strings.Split(prompt, "\n")[0], nil
	}

	content, err := os.ReadFile(m.responseFile)
	if err != nil {
		return "", fmt.Errorf("read mock response: %w", err)
	}

	return string(content), nil
}

// prepareWorktree ensures the worktree exists and is ready.
func (e *OneShotExecutor) prepareWorktree(ctx context.Context, job *Job, plan *Plan) (string, error) {
	if job.Worktree == "" {
		return "", fmt.Errorf("job %s has no worktree specified", job.ID)
	}

	// Get project git root for worktree creation (notebook-aware)
	gitRoot, err := GetProjectGitRoot(plan.Directory)
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
	worktreePath, _, err := e.worktreeManager.GetOrPrepareWorktree(ctx, realGitRoot, job.Worktree, "")
	if err != nil {
		return "", err
	}

	// Automatically initialize state within the new worktree for a better UX.
	groveDir := filepath.Join(worktreePath, ".grove")
	if err := os.MkdirAll(groveDir, 0o755); err != nil {
		// Log a warning but don't fail the job, as this is a convenience feature.
		log.WithError(err).Warn("Failed to create .grove directory in worktree")
		prettyLog.WarnPrettyCtx(ctx, fmt.Sprintf("Failed to create .grove directory in worktree: %v", err))
	} else {
		planName := filepath.Base(plan.Directory)
		// Use a flat map with the key "flow.active_plan" to match how state.Set works.
		stateData := map[string]string{
			"flow.active_plan": planName,
		}
		yamlBytes, err := yaml.Marshal(stateData)
		if err == nil {
			statePath := filepath.Join(groveDir, "state.yml")
			// This is a best-effort attempt; failure should not stop the job.
			_ = os.WriteFile(statePath, yamlBytes, 0o644)
		}
	}

	return worktreePath, nil
}

// regenerateContextInWorktree regenerates the context within a worktree.
func (e *OneShotExecutor) regenerateContextInWorktree(ctx context.Context, worktreePath string, jobType string, job *Job, plan *Plan) error {
	writer := grovelogging.GetWriter(ctx)
	log.WithField("job_type", jobType).Info("Checking context in worktree")

	// Scope to sub-project if job.Repository is set (for ecosystem worktrees)
	contextDir := ScopeToSubProject(worktreePath, job)
	if contextDir != worktreePath {
		log.WithField("context_dir", contextDir).Info("Scoping context generation to sub-project")
		fmt.Fprintf(writer, "Scoping context to sub-project: %s\n", job.Repository)
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
		
		// 3. Try relative to project git root (notebook-aware)
		if !foundPath {
			gitRoot, err := GetProjectGitRoot(plan.Directory)
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
		fmt.Fprintf(writer, "Using job-specific context from: %s\n", rulesFilePath)

		// Generate context using the custom rules file
		if err := ctxMgr.GenerateContextFromRulesFile(rulesFilePath, true); err != nil {
			return fmt.Errorf("failed to generate job-specific context: %w", err)
		}

		return e.displayContextInfo(ctx, contextDir)
	}

	// Check if .grove/rules exists for default context generation
	rulesPath := filepath.Join(contextDir, ".grove", "rules")
	if _, err := os.Stat(rulesPath); err != nil {
		if os.IsNotExist(err) {
			// Try to create default rules file using cx reset
			fmt.Fprintf(writer, "No .grove/rules file found. Creating default rules file...\n")
			
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
					fmt.Fprintf(writer, "✓ Created default .grove/rules file\n")
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
					fmt.Fprintf(writer, "Warning: Could not create .grove/rules file.\n")
					fmt.Fprintf(writer, "Skipping interactive prompt and proceeding without context for %s job\n", jobType)
					log.WithField("job_type", jobType).Info(fmt.Sprintf("Skipping interactive prompt and proceeding without context for %s job", jobType))
					return e.displayContextInfo(ctx, contextDir)
				}

				// Check if we have a TTY before prompting
				if !isatty.IsTerminal(os.Stdin.Fd()) && !isatty.IsCygwinTerminal(os.Stdin.Fd()) {
					fmt.Fprintf(writer, "Warning: Could not create .grove/rules file.\n")
					log.WithField("job_type", jobType).Info("No TTY available, proceeding without context")
					return e.displayContextInfo(ctx, contextDir)
				}

				// Prompt user when rules file is missing
				fmt.Fprintf(writer, "Warning: Could not create .grove/rules file.\n")
				fmt.Fprintf(writer, "Without a rules file, context cannot be generated for this %s job.\n", jobType)

				// Interactive prompt loop
				for {
					fmt.Fprintf(writer, "\n")
					fmt.Fprintf(writer, "Options:\n")
					fmt.Fprintf(writer, "  [E]dit - Create and edit a rules file (default)\n")
					fmt.Fprintf(writer, "  [P]roceed - Continue without context\n")
					fmt.Fprintf(writer, "  [C]ancel - Cancel the job\n")
					fmt.Fprintf(writer, "Your choice [E/p/c]: ")

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
							fmt.Fprintf(writer, "Error: Neither 'cx' nor 'grove-context' found in PATH.\n")
							fmt.Fprintf(writer, "Please install grove-context to use this feature.\n")
							continue
						}

						// Run cx edit in the context directory
						fmt.Fprintf(writer, "Opening rules editor with '%s edit'...\n", cxBinary)
						cmd := exec.Command("grove", "cx", "edit")
						cmd.Dir = contextDir
						cmd.Stdin = os.Stdin
						cmd.Stdout = os.Stdout
						cmd.Stderr = os.Stderr

						if err := cmd.Run(); err != nil {
							fmt.Fprintf(writer, "Error running %s edit: %v\n", cxBinary, err)
							fmt.Fprintf(writer, "Please try again or choose a different option.\n")
							continue
						}

						// After edit completes, check if rules file now exists
						if _, err := os.Stat(rulesPath); err == nil {
							fmt.Fprintf(writer, "✓ Rules file created successfully.\n")
							// Break out of the prompt loop and continue with regeneration
							break
						} else {
							fmt.Fprintf(writer, "Warning: Rules file still not found. Please try again.\n")
							continue
						}

					case "p", "proceed":
						fmt.Fprintf(writer, "Warning: Proceeding without context from rules.\n")
						fmt.Fprintf(writer, "💡 To add context for future runs, open a new terminal, navigate to the context directory, and run 'cx edit'.\n")
						return e.displayContextInfo(ctx, contextDir)

					case "c", "cancel":
						return fmt.Errorf("job canceled by user: .grove/rules file not found")

					default:
						fmt.Fprintf(writer, "Error: Invalid choice '%s'. Please choose E, P, or C.\n", choice)
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

		// Display summary statistics
		fmt.Fprintf(writer, "%s Context Summary\n", theme.IconFileTree)
		prettyLog.FieldCtx(ctx, "Total Files", stats.TotalFiles)
		prettyLog.FieldCtx(ctx, "Total Tokens", grovecontext.FormatTokenCount(stats.TotalTokens))
		prettyLog.FieldCtx(ctx, "Total Size", grovecontext.FormatBytes(int(stats.TotalSize)))

		// Token limit check removed - no longer enforcing limits

		// Show language distribution if there are files
		if stats.TotalFiles > 0 {
			prettyLog.BlankCtx(ctx)
			fmt.Fprintf(writer, "%s Language Distribution:\n", theme.IconProject)

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
				fmt.Fprintf(writer, "%s: %5.1f%% (%s tokens, %d files)\n",
					lang.Name,
					lang.Percentage,
					grovecontext.FormatTokenCount(lang.TotalTokens),
					lang.FileCount,
				)
				shown++
			}

		}
	}

	return nil
}

// displayContextInfo displays information about available context files
func (e *OneShotExecutor) displayContextInfo(ctx context.Context, worktreePath string) error {
	writer := grovelogging.GetWriter(ctx)
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
		fmt.Fprintln(writer, "No context files found (.grove/context or CLAUDE.md)")
		return nil
	}

	fmt.Fprintln(writer, strings.Repeat("─", 60))
	fmt.Fprintln(writer, "Context Files Available")
	for _, file := range contextFiles {
		relPath, _ := filepath.Rel(worktreePath, file)
		fmt.Fprintf(writer, "File: %s\n", relPath)
	}
	fmt.Fprintln(writer)
	fmt.Fprintf(writer, "Total context size: %s\n", grovecontext.FormatBytes(int(totalSize)))
	fmt.Fprintln(writer, strings.Repeat("─", 60))

	return nil
}

// executeChatJob handles the conversational logic for chat-type jobs
func (e *OneShotExecutor) executeChatJob(ctx context.Context, job *Job, plan *Plan, output io.Writer) error {
	// Generate a unique request ID for tracing this turn
	requestID := "req-" + uuid.New().String()[:8]
	ctx = context.WithValue(ctx, "request_id", requestID)
	log.WithFields(logrus.Fields{
		"job_id":     job.ID,
		"request_id": requestID,
		"plan_name":  plan.Name,
	}).Info("Executing chat turn")

	// --- Pre-flight Check ---
	// Read the job file content to check state before creating locks or changing status.
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return fmt.Errorf("reading chat file: %w", err)
	}

	// Parse the chat file to determine runnability
	turns, err := ParseChatFile(content)
	if err != nil {
		return fmt.Errorf("parsing chat file: %w", err)
	}

	if len(turns) == 0 {
		return fmt.Errorf("chat file has no turns")
	}

	lastTurn := turns[len(turns)-1]

	// If the last turn is from the LLM, or if it's an empty prompt from the user,
	// the job is not ready to run. Return successfully without changing state.
	if lastTurn.Speaker != "user" || strings.TrimSpace(lastTurn.Content) == "" {
		log.WithField("job", job.Title).Info("Chat job is waiting for user input, skipping execution.")
		prettyLog.InfoPretty(fmt.Sprintf("Chat job '%s' is waiting for user input.", job.Title))
		// Ensure status is correctly set to pending_user and return.
		if job.Status != JobStatusPendingUser {
			job.Status = JobStatusPendingUser
			updateJobFile(job)
		}
		return nil
	}

	// --- Execution ---
	// Pre-flight check passed, proceed with execution.
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

	// Process the active directive
	// Note: We already parsed and validated turns in the pre-flight check
	lastTurn = turns[len(turns)-1]
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
		// No worktree specified, default to the project git repository root (notebook-aware).
		var err error
		worktreePath, err = GetProjectGitRoot(plan.Directory)
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

	// --- Concept Gathering Logic ---
	if job.GatherConceptNotes || job.GatherConceptPlans {
		conceptContextFile, err := gatherConcepts(ctx, job, plan, worktreePath)
		if err != nil {
			log.WithError(err).WithFields(logrus.Fields{"request_id": requestID, "job_id": job.ID}).Error("Failed to gather concepts")
			prettyLog.WarnPrettyCtx(ctx, fmt.Sprintf("Warning: Failed to gather concepts: %v", err))
		} else if conceptContextFile != "" {
			// The file will be picked up by the context gathering logic below
		}
	}

	// Scope to sub-project if job.Repository is set (for ecosystem worktrees)
	// This ensures chat uses the correct context files
	worktreePath = ScopeToSubProject(worktreePath, job)

	// Build the prompt
	// Format conversation history as structured XML using parsed turns
	formattedConversation := FormatConversationXML(turns)

	// Handle dependencies - either prepend to prompt or collect for upload
	var dependencyFilePaths []string
	var prependedDependencies []struct {
		Filename string
		Content  string
	}
	if job.PrependDependencies && len(job.Dependencies) > 0 {
		// Prepend mode: read dependency content for inlining
		printlnUnlessTUI("🔗 prepend_dependencies enabled - inlining dependency content into prompt")
		// Sort dependencies by filename for consistent order
		sortedDeps := make([]*Job, len(job.Dependencies))
		copy(sortedDeps, job.Dependencies)
		sort.Slice(sortedDeps, func(i, j int) bool {
			if sortedDeps[i] == nil || sortedDeps[j] == nil {
				return false
			}
			return sortedDeps[i].Filename < sortedDeps[j].Filename
		})

		printfUnlessTUI("   Prepending %d dependenc%s to prompt:\n", len(sortedDeps), func() string {
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
				printfUnlessTUI("     • %s (inlined, not uploaded as file)\n", dep.Filename)
				_, depBody, _ := ParseFrontmatter(depContent)
				prependedDependencies = append(prependedDependencies, struct {
					Filename string
					Content  string
				}{dep.Filename, string(depBody)})
			}
		}
	} else if len(job.Dependencies) > 0 {
		// Upload mode: collect dependency file paths for upload/attachment to LLM
		printfUnlessTUI("📎 Collecting %d dependenc%s for upload:\n", len(job.Dependencies), func() string {
			if len(job.Dependencies) == 1 {
				return "y"
			}
			return "ies"
		}())
		for _, dep := range job.Dependencies {
			if dep != nil && dep.FilePath != "" {
				dependencyFilePaths = append(dependencyFilePaths, dep.FilePath)
				printfUnlessTUI("     • %s (will be uploaded as file attachment)\n", dep.Filename)
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
			printfUnlessTUI("  Context file not found: %s (error: %v)\n", claudePath, err)
		}
	} else {
		// No worktree, use the default context search
		printlnUnlessTUI("  No worktree path, using default context search")
		contextPaths = FindContextFiles(plan)
	}

	// Verify context files exist and collect valid paths
	var validContextPaths []string
	for _, contextPath := range contextPaths {
		if info, err := os.Stat(contextPath); err == nil {
			printfUnlessTUI("  Successfully found context file: %s (%d bytes)\n", contextPath, info.Size())
			validContextPaths = append(validContextPaths, contextPath)
		} else {
			printfUnlessTUI("  Failed to access context file: %s (error: %v)\n", contextPath, err)
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

	// Add chat-specific context explanation (simplified since conversation is now structured XML)
	promptBuilder.WriteString(`
<conversation_note>
This is a multi-turn conversation. Focus on the turn marked status="awaiting_response" -
that is the message requiring your reply. The respond_as attribute indicates what persona
to use. Previous responses may have been generated by different prompts or personas;
interpret and continue through YOUR current system instructions.
</conversation_note>
`)

	// Add context section if we have dependencies or context files
	if len(prependedDependencies) > 0 || len(dependencyFilePaths) > 0 || len(validContextPaths) > 0 {
		promptBuilder.WriteString("\n<context>\n")

		// Add prepended dependencies (inlined content from upstream jobs)
		for _, dep := range prependedDependencies {
			promptBuilder.WriteString(fmt.Sprintf("    <prepended_dependency file=\"%s\">\n", dep.Filename))
			promptBuilder.WriteString(dep.Content)
			promptBuilder.WriteString("\n    </prepended_dependency>\n")
		}

		// Add uploaded dependencies (references to files uploaded separately)
		for _, depPath := range dependencyFilePaths {
			promptBuilder.WriteString(fmt.Sprintf("    <uploaded_context_file file=\"%s\" type=\"dependency\" importance=\"high\" description=\"Context from upstream jobs in this LLM pipeline.\"/>\n", filepath.Base(depPath)))
		}

		// Add context files (concatenated project/source code)
		for _, ctxPath := range validContextPaths {
			promptBuilder.WriteString(fmt.Sprintf("    <uploaded_context_file file=\"%s\" type=\"repository\" importance=\"medium\" description=\"Concatenated project/source code files from the current repository.\"/>\n", filepath.Base(ctxPath)))
		}

		promptBuilder.WriteString("</context>\n")
	}

	// Add the structured conversation XML
	promptBuilder.WriteString("\n")
	promptBuilder.WriteString(formattedConversation)
	promptBuilder.WriteString("\n")
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
		printfUnlessTUI("✓ Including %d context file(s) as attachments\n", len(validContextPaths))
	} else {
		printlnUnlessTUI("⚠️  No context files included in chat prompt")
	}

	// Determine effective model with clear precedence
	var effectiveModel string
	var modelSource string

	// 1. CLI flag (highest priority)
	if e.config.ModelOverride != "" {
		effectiveModel = e.config.ModelOverride
		modelSource = "CLI override"
	} else if directive.Model != "" {
		// 2. Chat directive model (for specific turns)
		effectiveModel = directive.Model
		modelSource = "chat directive"
	} else if job.Model != "" {
		// 3. Job frontmatter model
		effectiveModel = job.Model
		modelSource = "job frontmatter"
	} else if plan.Config != nil && plan.Config.Model != "" {
		// 4. Plan config model
		effectiveModel = plan.Config.Model
		modelSource = "plan config"
	} else if plan.Orchestration != nil && plan.Orchestration.OneshotModel != "" {
		// 5. Global config model
		effectiveModel = plan.Orchestration.OneshotModel
		modelSource = "global config"
	} else {
		// 6. Hardcoded fallback - use Anthropic default
		effectiveModel = anthropicmodels.DefaultModel
		modelSource = "default fallback"
	}

	// Resolve model aliases (e.g., "claude-sonnet-4-5" -> "claude-sonnet-4-5-20250929")
	effectiveModel = resolveModelAlias(effectiveModel)

	logrus.WithFields(logrus.Fields{
		"job_id":       job.ID,
		"model":        effectiveModel,
		"model_source": modelSource,
	}).Debug("Resolved model for chat job execution")

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
	if os.Getenv("GROVE_MOCK_LLM_RESPONSE_FILE") != "" {
		// Check if mocking is enabled - if so, always use llmClient regardless of model
		response, err = e.llmClient.Complete(ctx, job, plan, fullPrompt, llmOpts, output)
	} else if strings.HasPrefix(effectiveModel, "gemini") {
		// Resolve API key here where we have the correct execution context
		apiKey, geminiErr = geminiconfig.ResolveAPIKey()
		if geminiErr != nil {
			// Don't fail immediately, let the runner handle it for a more consistent error
			apiKey = ""
		}
		// Use grove-gemini package for Gemini models
		allFilesToUpload := append(dependencyFilePaths, validContextPaths...)
		opts := gemini.RequestOptions{
			Model:            llmOpts.Model,
			Prompt:           fullPrompt,
			APIKey:           apiKey, // Pass the resolved API key
			PromptFiles:      allFilesToUpload, // Pass all dependency and context files to be uploaded
			WorkDir:          contextDir,
			SkipConfirmation: e.config.SkipInteractive, // Respect -y flag
			// Pass context for better logging
			Caller:   "grove-flow-chat",
			JobID:    job.ID,
			PlanName: plan.Name,
		}
		if isTUIMode() {
			fmt.Fprintf(output, "\n󰚩 Calling Gemini API with model: %s\n\n", effectiveModel)
		}
		response, err = e.geminiRunner.Run(ctx, opts)
		if err != nil {
			printfUnlessTUI("[DEBUG] Gemini API call failed with error: %v\n", err)
			execErr = fmt.Errorf("Gemini API completion: %w", err)
			return execErr
		}
	} else if strings.HasPrefix(effectiveModel, "claude") {
		// Resolve API key here where we have the correct execution context
		apiKey, anthropicErr := anthropicconfig.ResolveAPIKey()
		if anthropicErr != nil {
			err = fmt.Errorf("resolving Anthropic API key: %w", anthropicErr)
		} else {
			// Use grove-anthropic package for Claude models
			opts := anthropic.RequestOptions{
				Model:        effectiveModel,
				Prompt:       fullPrompt,
				ContextFiles: append(dependencyFilePaths, validContextPaths...),
				WorkDir:      contextDir,
				APIKey:       apiKey,
				MaxTokens:    64000,
				Caller:       "grove-flow-chat",
				JobID:        job.ID,
				PlanName:     plan.Name,
			}
			if isTUIMode() {
				fmt.Fprintf(output, "\n%s Calling Anthropic API with model: %s\n\n", theme.IconRobot, effectiveModel)
			}
			response, err = e.anthropicRunner.Run(ctx, opts)
		}
		if err != nil {
			printfUnlessTUI("[DEBUG] Anthropic API call failed with error: %v\n", err)
			execErr = fmt.Errorf("Anthropic API completion: %w", err)
			return execErr
		}
	} else {
		if isTUIMode() {
			fmt.Fprintf(output, "\n󰚩 Calling LLM API with model: %s\n\n", effectiveModel)
		}
		// Use traditional llm command
		response, err = e.llmClient.Complete(ctx, job, plan, fullPrompt, llmOpts, output)
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
	// Use the directive's template (which respects frontmatter > inline directive > default "chat")
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	newCell := fmt.Sprintf("\n<!-- grove: {\"id\": \"%s\"} -->\n## LLM Response (%s)\n\n%s\n\n<!-- grove: {\"template\": \"%s\"} -->\n", turnID, timestamp, response, directive.Template)

	// Append atomically
	if err := os.WriteFile(job.FilePath, append(content, []byte(newCell)...), 0o644); err != nil {
		execErr = fmt.Errorf("appending LLM response: %w", err)
		return execErr
	}

	printfUnlessTUI("✓ Added LLM response to chat: %s\n", job.FilePath)
	printfUnlessTUI("✓ Chat job is now waiting for user input\n")

	// Update job status - chat jobs always go to pending_user (not completed)
	job.Status = JobStatusPendingUser
	job.EndTime = time.Now()
	updateJobFile(job)

	return nil
}
