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
	"github.com/grovetools/core/git"
	grovelogging "github.com/grovetools/core/logging"
	groveplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/core/util/delegation"
	grovecontext "github.com/grovetools/cx/pkg/context"
	"github.com/grovetools/grove-anthropic/pkg/anthropic"
	anthropicconfig "github.com/grovetools/grove-anthropic/pkg/config"
	anthropicmodels "github.com/grovetools/grove-anthropic/pkg/models"
	geminiconfig "github.com/grovetools/grove-gemini/pkg/config"
	"github.com/grovetools/grove-gemini/pkg/gemini"
	openrouterconfig "github.com/grovetools/grove-openrouter/pkg/config"
	openroutermodels "github.com/grovetools/grove-openrouter/pkg/models"
	openrouter "github.com/grovetools/grove-openrouter/pkg/openrouter"

	modelpkg "github.com/grovetools/flow/pkg/model"
	"github.com/mattn/go-isatty"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

var (
	log  = grovelogging.NewLogger("grove-flow")
	ulog = grovelogging.NewUnifiedLogger("grove-flow")
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

// ExecutorConfig holds configuration for executors.
type ExecutorConfig struct {
	MaxPromptLength int
	Timeout         time.Duration
	RetryCount      int
	Model           string
	ModelOverride   string // Override model from CLI
	SkipInteractive bool   // Skip interactive prompts

	// DaemonJobPersist, when true, makes the headless agent executor write a
	// minimal daemon JobInfo record (with the agent PID) to the daemon jobs
	// directory after launching the agent. It must be set ONLY for the
	// daemon-less `flow plan run` fallback path: in that mode no daemon owns
	// the job, so persisting a JobInfo lets a later daemon adopt and reconcile
	// the detached agent. When the daemon itself runs the executor in-process,
	// this stays false to avoid clobbering the daemon's own lifecycle-managed
	// jobs/<id>.json record.
	DaemonJobPersist bool
}

// OneShotExecutor executes oneshot jobs.
type OneShotExecutor struct {
	llmClient        LLMClient
	config           *ExecutorConfig
	worktreeManager  *git.WorktreeManager
	geminiRunner     *gemini.RequestRunner
	anthropicRunner  *anthropic.RequestRunner
	openrouterRunner *openrouter.RequestRunner
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
		llmClient:        llmClient,
		config:           config,
		worktreeManager:  git.NewWorktreeManager(),
		geminiRunner:     gemini.NewRequestRunner(),
		anthropicRunner:  anthropic.NewRequestRunner(),
		openrouterRunner: openrouter.NewRequestRunner(),
	}
}

// Name returns the executor name.
func (e *OneShotExecutor) Name() string {
	return "oneshot"
}

// usesMockLLM reports whether this executor was constructed with a mock LLM
// client. Tests inject a *MockLLMClient to run offline; production always
// injects a *CommandLLMClient (or a provider runner is used), so this returns
// false for real runs and leaves model routing unchanged.
func (e *OneShotExecutor) usesMockLLM() bool {
	_, ok := e.llmClient.(*MockLLMClient)
	return ok
}

// Execute runs a oneshot job.
func (e *OneShotExecutor) Execute(ctx context.Context, job *Job, plan *Plan) error {
	// Get the output writer from context
	output := grovelogging.GetWriter(ctx)

	// Get request ID from context
	requestID, _ := ctx.Value(contextKey("request_id")).(string)

	// Handle chat jobs - execute directly without confirmation
	// Plan-level confirmations already guard against unintended execution
	if job.Type == JobTypeChat {
		if job.Status == JobStatusCompleted {
			return nil
		}
		return e.executeChatJob(ctx, job, plan, output)
	}

	// Create lock file with the current process's PID.
	if err := CreateLockFile(job.FilePath, os.Getpid()); err != nil {
		return fmt.Errorf("failed to create lock file: %w", err)
	}
	// Ensure lock file is removed when execution finishes.
	defer func() { _ = RemoveLockFile(job.FilePath) }()

	// Update job status to running
	job.Status = JobStatusRunning
	job.StartTime = time.Now()
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}

	var execErr error

	// Reject the removed pinned_context key (spec 19 D5) after the running
	// flip so the failure lands as status: failed + last_error, not a silent
	// skip of files the author believed were pinned.
	if pinErr := job.PinnedContextRemovedError(); pinErr != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		_ = updateJobFile(job)
		ulog.Error("pinned_context is no longer supported").
			Err(pinErr).
			Field("job_id", job.ID).
			Pretty(theme.IconError + " " + pinErr.Error()).
			Log(ctx)
		execErr = pinErr
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
			_ = updateJobFile(job)
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
			ulog.Warn("Not a git repository, using plan directory").
				Field("workdir", workDir).
				Field("plan_dir", plan.Directory).
				Field("fallback", true).
				Log(ctx)
		}
	}

	// Always regenerate context to ensure oneshot has latest view
	usedRulesPath, jobCtx, err := e.regenerateContextInWorktree(ctx, workDir, "oneshot", job, plan)
	if err != nil {
		// Hard-fail: a regen error means we could not build this job's stripped,
		// job-scoped context. Proceeding would either upload no repo context or
		// (via the runner's WorkDir fallback) the shared, unstripped context —
		// both wrong. Fail loudly with the underlying error instead.
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		_ = updateJobFile(job)
		ulog.Error("Failed to regenerate context").
			Err(err).
			Field("request_id", requestID).
			Field("job_id", job.ID).
			Pretty(theme.IconError + " Failed to regenerate context: " + err.Error()).
			Log(ctx)
		execErr = fmt.Errorf("regenerating context: %w", err)
		return execErr
	} else if usedRulesPath != "" {
		if archiveErr := ArchiveContextRules(job, plan, usedRulesPath); archiveErr != nil {
			ulog.Warn("Failed to archive context rules").Err(archiveErr).Log(ctx)
		}
	}

	// --- Concept Gathering Logic ---
	if job.GatherConceptNotes || job.GatherConceptPlans {
		_, err := gatherConcepts(ctx, job, plan, workDir)
		if err != nil {
			ulog.Warn("Failed to gather concepts").
				Err(err).
				Field("request_id", requestID).
				Field("job_id", job.ID).
				Log(ctx)
		}
		// The concept file (if any) is picked up by context gathering logic below.
	}

	// Scope to sub-project if job.Repository is set (for ecosystem worktrees)
	// This ensures buildPrompt uses the correct context files
	workDir = ScopeToSubProject(workDir, job)

	// We need to gather context files first for BuildXMLPrompt
	_, _, contextFiles, err := e.buildPrompt(job, plan, workDir, jobCtx)
	if err != nil {
		// Log warning but don't fail - context files are optional
		log.WithError(err).Warn("Could not determine context files")
	}

	// Resolve the job-scoped context paths to hand explicitly to the LLM
	// runners. The grove-gemini/grove-anthropic runners otherwise re-resolve
	// context from WorkDir, which is plan-scoped and shared across concurrent
	// jobs — passing these makes each job upload its OWN context. Empty when
	// generation produced nothing, in which case the runners fall back to
	// their default WorkDir resolution.
	hotCtxFile, coldCtxFile := jobCtx.existingPaths()

	// Query memory database for related memories (bounded; see memoryPrefetchTimeout)
	memories := FetchRelatedMemoriesBounded(ctx, job)

	// Build the XML prompt and get the list of files to upload
	prompt, promptSourceFiles, err := BuildXMLPrompt(job, plan, workDir, contextFiles, memories)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		_ = updateJobFile(job)
		ulog.Error("Failed to build prompt for job").
			Field("job_id", job.ID).
			Field("job_file", job.FilePath).
			Err(err).
			Pretty(theme.IconError + " " + err.Error()).
			Log(ctx)
		execErr = fmt.Errorf("building XML prompt: %w", err)
		return execErr
	}

	// Log the prompt content for debugging
	ulog.Debug("Built prompt for job").
		Field("job_id", job.ID).
		Field("request_id", requestID).
		Field("plan_name", plan.Name).
		Field("job_file", job.FilePath).
		Field("prompt", prompt).
		Field("prompt_chars", len(prompt)).
		Log(ctx)

	// Write the briefing file for auditing (no turnID for oneshot jobs)
	briefingFilePath, err := WriteBriefingFile(plan, job, prompt, "")
	if err != nil {
		ulog.Error("Failed to write briefing file").
			Err(err).
			Field("request_id", requestID).
			Field("job_id", job.ID).
			Log(ctx)
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to write briefing file: %w", err)
	}

	// Stamp the config vector alongside the briefing. contextFiles is nil for
	// this family: jobCtx carries the generated cold/hot bundle instead.
	stampJobConfigVector(ctx, job, plan, e.config, workDir, jobCtx, nil, briefingFilePath)

	// --- Concept Gathering Logic ---
	if job.GatherConceptNotes || job.GatherConceptPlans {
		conceptContextFile, err := gatherConcepts(ctx, job, plan, workDir)
		if err != nil {
			ulog.Warn("Failed to gather concepts").
				Err(err).
				Field("request_id", requestID).
				Field("job_id", job.ID).
				Log(ctx)
			// Don't fail the job, just log warning
		} else if conceptContextFile != "" {
			// Add the aggregated concepts file to the list of files to upload
			promptSourceFiles = append(promptSourceFiles, conceptContextFile)
			ulog.Info("Added aggregated concepts to context").
				Field("job_id", job.ID).
				Field("request_id", requestID).
				Field("concept_context_file", conceptContextFile).
				Pretty(theme.IconSuccess + " Added aggregated concepts: " + theme.DefaultTheme.Italic.Render(conceptContextFile)).
				Log(ctx)
		}
	}

	if briefingFilePath != "" {
		ulog.Success("Briefing file created").
			Field("job_id", job.ID).
			Field("request_id", requestID).
			Field("plan_name", plan.Name).
			Field("job_file", job.FilePath).
			Field("briefing_file_path", briefingFilePath).
			Field("prompt_chars", len(prompt)).
			Pretty(theme.IconCode + "  Briefing file created at: " + theme.DefaultTheme.Accent.Render(briefingFilePath)).
			Log(ctx)
	}

	// Enforce the prompt length limit when one is configured. A zero limit
	// means "no limit" (the production default for the plain constructor). The
	// orchestrator sets a very high ceiling, so this is effectively dormant for
	// real runs and only guards against pathologically large prompts; tests set
	// a small limit to exercise this path.
	if e.config != nil && e.config.MaxPromptLength > 0 && len(prompt) > e.config.MaxPromptLength {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		_ = updateJobFile(job)
		return fmt.Errorf("prompt exceeds maximum length: %d characters (limit %d)", len(prompt), e.config.MaxPromptLength)
	}

	// NOTE: GROVE_CURRENT_JOB_PATH is intentionally NOT set here. os.Setenv is
	// process-global, and runJobsConcurrently dispatches jobs as goroutines in
	// one process, so concurrent jobs would clobber each other's value — a data
	// race. Nothing in the production LLM integrations reads it, so it is safe
	// to drop; per-job identity flows through the Job/Plan values and context.

	// Propagate request ID to child processes
	if requestID != "" {
		os.Setenv("GROVE_REQUEST_ID", requestID)
		defer os.Unsetenv("GROVE_REQUEST_ID")
	}

	// Determine the effective model to use with clear precedence:
	// CLI override > job frontmatter > plan config > global config > default,
	// then alias resolution. The chain lives in resolveEffectiveModel
	// (config_vector.go) so the config-vector stamp records exactly the model
	// this call site uses — duplicating it would let the two drift apart.
	effectiveModel, modelSource := resolveEffectiveModel(e.config, job, plan)

	logrus.WithFields(logrus.Fields{
		"job_id":       job.ID,
		"model":        effectiveModel,
		"model_source": modelSource,
	}).Debug("Resolved model for job execution")

	// Call LLM based on model type, with automatic retry for transient failures
	var response string
	// apiUsage carries the in-process token/cost usage from the anthropic runner
	// (claude branch only) so we can accumulate it into the job's token-usage
	// artifact after a successful call. Gemini/mock/other providers leave it nil.
	var apiUsage *anthropic.UsageResult
	if effectiveModel == "mock" {
		// Use mock response for testing
		response = "This is a mock LLM response for testing purposes."
		err = nil
	} else if os.Getenv("GROVE_MOCK_LLM_RESPONSE_FILE") != "" || e.usesMockLLM() {
		// Route through the injected llmClient whenever mocking is active — either
		// via the env-file mock or because a test injected a *MockLLMClient. This
		// is the hermetic test seam: production always injects a CommandLLMClient,
		// so usesMockLLM() is false and the provider-specific branches below
		// (gemini/claude) run unchanged.
		err = e.executeWithRetry(ctx, job, func() error {
			llmOpts := LLMOptions{
				Model:        effectiveModel,
				WorkingDir:   workDir,
				ContextFiles: contextFiles,
				IncludeFiles: promptSourceFiles,
			}
			var innerErr error
			response, innerErr = e.llmClient.Complete(ctx, job, plan, prompt, llmOpts, output)
			return innerErr
		})
	} else if strings.HasPrefix(effectiveModel, "gemini") {
		// Resolve API key here where we have the correct execution context
		apiKey, geminiErr := geminiconfig.ResolveAPIKey()
		if geminiErr != nil {
			// Don't fail immediately, let the runner handle it for a more consistent error
			apiKey = ""
		}
		// Use grove-gemini package for Gemini models with retry
		err = e.executeWithRetry(ctx, job, func() error {
			opts := gemini.RequestOptions{
				Model:            effectiveModel,
				Prompt:           prompt,            // Only template and prompt body
				PromptFiles:      promptSourceFiles, // Pass resolved source file paths
				WorkDir:          workDir,
				HotContextFile:   hotCtxFile,               // Job-scoped, avoids cross-job race
				ColdContextFile:  coldCtxFile,              // Job-scoped, avoids cross-job race
				SkipConfirmation: e.config.SkipInteractive, // Respect -y flag
				APIKey:           apiKey,                   // Pass the resolved API key
				NoCache:          job.NoCache,              // Frontmatter no_cache: true opts out of prompt caching
				// Pass context for better logging
				Caller:   "grove-flow-oneshot",
				JobID:    job.ID,
				PlanName: plan.Name,
			}
			var innerErr error
			response, innerErr = e.geminiRunner.Run(ctx, opts)
			return innerErr
		})
	} else if strings.HasPrefix(effectiveModel, "claude") {
		// Resolve API key here where we have the correct execution context
		apiKey, anthropicErr := anthropicconfig.ResolveAPIKey()
		if anthropicErr != nil {
			err = fmt.Errorf("resolving Anthropic API key: %w", anthropicErr)
		} else {
			// Use grove-anthropic package for Claude models with retry
			err = e.executeWithRetry(ctx, job, func() error {
				opts := anthropic.RequestOptions{
					Model:           effectiveModel,
					Prompt:          prompt,
					ContextFiles:    append(promptSourceFiles, contextFiles...),
					WorkDir:         workDir,
					HotContextFile:  hotCtxFile,  // Job-scoped, avoids cross-job race
					ColdContextFile: coldCtxFile, // Job-scoped, avoids cross-job race
					APIKey:          apiKey,
					MaxTokens:       modelpkg.MaxTokens(effectiveModel),
					NoCache:         job.NoCache, // Frontmatter no_cache: true opts out of prompt caching
					Caller:          "grove-flow-oneshot",
					JobID:           job.ID,
					PlanName:        plan.Name,
				}
				if isTUIMode() {
					fmt.Fprintf(output, "\n%s Calling Anthropic API with model: %s\n\n", theme.IconRobot, effectiveModel)
				}
				var innerErr error
				var usage *anthropic.UsageResult
				response, usage, innerErr = e.anthropicRunner.RunWithUsage(ctx, opts)
				if innerErr == nil {
					apiUsage = usage // only the final successful attempt's usage
				}
				return innerErr
			})
		}
	} else if openroutermodels.HasPrefix(effectiveModel) {
		// Resolve API key here where we have the correct execution context.
		// Hard-fail like the anthropic branch (not gemini's silent blank):
		// validation should have caught a missing key already, but the branch
		// must be safe standalone.
		apiKey, orErr := openrouterconfig.ResolveAPIKey()
		if orErr != nil {
			err = fmt.Errorf("resolving OpenRouter API key: %w", orErr)
		} else {
			// Use grove-openrouter package for OpenRouter models with retry.
			err = e.executeWithRetry(ctx, job, func() error {
				opts := openrouter.RequestOptions{
					Model:           effectiveModel, // prefix kept; client strips it
					Prompt:          prompt,
					ContextFiles:    append(promptSourceFiles, contextFiles...),
					WorkDir:         workDir,
					HotContextFile:  hotCtxFile,  // Job-scoped, avoids cross-job race
					ColdContextFile: coldCtxFile, // Job-scoped, avoids cross-job race
					APIKey:          apiKey,
					// MaxTokens omitted (0 = model default). OpenRouter's catalog
					// has heterogeneous output caps; an over-cap max_tokens is an
					// API error on some vendors, so we don't apply the 32000
					// non-anthropic default here.
					Caller:   "grove-flow-oneshot",
					JobID:    job.ID,
					PlanName: plan.Name,
				}
				if isTUIMode() {
					fmt.Fprintf(output, "\n%s Calling OpenRouter API with model: %s\n\n", theme.IconRobot, effectiveModel)
				}
				var innerErr error
				// Run returns (string, error) — no usage accumulation (token/cost
				// tracking lives in the provider's own query ledger).
				response, innerErr = e.openrouterRunner.Run(ctx, opts)
				return innerErr
			})
		}
	} else {
		// Use traditional llm command for other models with retry
		err = e.executeWithRetry(ctx, job, func() error {
			llmOpts := LLMOptions{
				Model:        effectiveModel,
				WorkingDir:   workDir,
				ContextFiles: contextFiles,
				IncludeFiles: promptSourceFiles,
			}
			if isTUIMode() {
				fmt.Fprintf(output, "\n󰚩 Calling LLM API with model: %s\n\n", effectiveModel)
			}
			var innerErr error
			response, innerErr = e.llmClient.Complete(ctx, job, plan, prompt, llmOpts, output)
			return innerErr
		})
	}
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		_ = updateJobFile(job)
		ulog.Error("LLM completion failed").
			Err(err).
			Field("request_id", requestID).
			Field("job_id", job.ID).
			Pretty(theme.DefaultTheme.Error.Render(fmt.Sprintf("%s LLM completion failed: %v", theme.IconError, err))).
			Log(ctx)
		execErr = fmt.Errorf("LLM completion: %w", err)
		return execErr
	}

	// Record the API token/cost usage for this oneshot into the job's
	// token-usage artifact (best-effort — never fail the job over accounting).
	if apiUsage != nil {
		if accErr := AccumulateAPITokenUsage(plan, job, apiUsage); accErr != nil {
			ulog.Warn("Failed to accumulate API token usage").
				Err(accErr).
				Field("job_id", job.ID).
				Log(ctx)
		}
	}

	// Append output to job file
	if err := e.appendToJobFile(response, job); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		_ = updateJobFile(job)
		execErr = fmt.Errorf("appending output to job file: %w", err)
		return execErr
	}

	// Update status to completed if we got here without errors
	job.Status = JobStatusCompleted
	job.EndTime = time.Now()
	if err := updateJobFile(job); err != nil {
		// Log but don't fail - the job executed successfully
		ulog.Warn("Failed to update job file status").
			Err(err).
			Log(ctx)
	}

	// Archive the accumulated token usage into the job .md, for parity with the
	// agent on-disk record (best-effort).
	WriteTokenUsageSection(plan, job)

	writeMetricsRecordQuietly(job, plan)

	return nil
}

// buildPrompt constructs the prompt from job sources and returns context file paths separately.
func (e *OneShotExecutor) buildPrompt(job *Job, plan *Plan, worktreePath string, jobCtx *jobContextPaths) (string, []string, []string, error) {
	var parts []string
	var promptSourceFiles []string // Resolved paths for prompt source files
	var contextFiles []string      // Context files (.grove/context, CLAUDE.md)
	var finalPromptBody string

	// Handle dependencies based on ShouldInline (supports both new inline field and legacy prepend_dependencies)
	if job.ShouldInline(InlineDependencies) {
		// Inline dependency content directly into the prompt body
		log.Debug("inline: [dependencies] enabled - inlining dependency content into prompt body")
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

			log.WithField("count", len(sortedDeps)).Debug("Inlining dependencies into prompt")
			for _, dep := range sortedDeps {
				if dep != nil && dep.FilePath != "" {
					depContent, err := os.ReadFile(dep.FilePath)
					if err != nil {
						return "", nil, nil, fmt.Errorf("reading dependency file %s: %w", dep.FilePath, err)
					}
					log.WithField("file", dep.Filename).Debug("Inlined dependency")
					dependencyContentBuilder.WriteString(fmt.Sprintf("\n\n---\n## Context from %s\n\n", dep.Filename))
					_, depBody, _ := ParseFrontmatter(depContent)
					dependencyContentBuilder.Write(depBody)
				}
			}
			dependencyContentBuilder.WriteString("\n\n---\n\n")
		}
		finalPromptBody = dependencyContentBuilder.String() + job.PromptBody
	} else {
		// Upload dependencies as separate file attachments
		if len(job.Dependencies) > 0 {
			log.WithField("count", len(job.Dependencies)).Debug("Adding dependencies as file attachments")
			for _, dep := range job.Dependencies {
				if dep != nil && dep.FilePath != "" {
					log.WithField("file", dep.Filename).Debug("Uploading dependency as file attachment")
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

		// Resolve include file paths (without reading content)
		for _, source := range job.Include {
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

		// Collect Grove context files — prefer the job-scoped generated context
		// (under .artifacts/<job-id>/) over the shared plan-level file.
		contextFiles = append(contextFiles, e.collectContextFiles(job, plan, worktreePath, jobCtx)...)

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

		// Resolve include file paths (without reading content)
		for _, source := range job.Include {
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

		// Collect Grove context files — prefer the job-scoped generated context
		// (under .artifacts/<job-id>/) over the shared plan-level file.
		contextFiles = append(contextFiles, e.collectContextFiles(job, plan, worktreePath, jobCtx)...)

		// Close the XML prompt structure (non-template path)
		parts = append(parts, "</prompt>")

		prompt := strings.Join(parts, "\n")

		// Check prompt length (without context files which will be passed separately)
		// Prompt length check removed - no longer enforcing limits

		return prompt, promptSourceFiles, contextFiles, nil
	}
}

// collectContextFiles returns the context file paths to attach for a job. When
// a job-scoped context was requested (jobCtx != nil — every real job takes this
// path via regenerateContextInWorktree), that context is AUTHORITATIVE: attach
// only the job-scoped hot/cold files (+ CLAUDE.md) and never substitute the
// shared plan-level context/generated/context. The shared file is written by a
// manager that did not honor this job's strip_comments, so falling back to it
// would upload unstripped context (the strip_comments-ignored bug). Only jobless
// callers (jobCtx == nil) use the shared plan-level lookup. CLAUDE.md from the
// context dir is always included when present.
func (e *OneShotExecutor) collectContextFiles(job *Job, plan *Plan, worktreePath string, jobCtx *jobContextPaths) []string {
	var contextFiles []string
	// Scope to sub-project if job.Repository is set (for ecosystem worktrees)
	contextDir := ScopeToSubProject(worktreePath, job)

	if jobCtx != nil {
		// Job-scoped context is authoritative. uploadFiles() returns only the
		// files that actually exist; if generation failed it returns nothing,
		// and we still refuse the shared fallback (the caller hard-fails on the
		// regen error before reaching here).
		contextFiles = append(contextFiles, jobCtx.uploadFiles()...)
		if contextDir != "" {
			claudePath := filepath.Join(contextDir, "CLAUDE.md")
			if _, err := os.Stat(claudePath); err == nil {
				contextFiles = append(contextFiles, claudePath)
			}
		}
		return contextFiles
	}

	if contextDir != "" {
		// When using a worktree/context dir, ONLY use context from that directory
		ctxMgr := grovecontext.NewManager(contextDir)
		contextPath := ctxMgr.ResolveContextPath()
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
	return contextFiles
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
	if err := os.WriteFile(job.FilePath, []byte(newContent), 0o600); err != nil {
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
	if err := os.WriteFile(job.FilePath, newContent, 0o600); err != nil {
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

	// Emulate the planning-agent behavior of splitting a response containing
	// multiple frontmatter blocks into separate job files. Only active when the
	// test/mock explicitly requests it, so it stays dormant for ordinary mock
	// responses and for production (which never sets these env vars).
	if os.Getenv("GROVE_MOCK_LLM_OUTPUT_MODE") == "split_by_frontmatter" {
		if err := writeSplitFrontmatterJobs(string(content)); err != nil {
			return "", fmt.Errorf("split mock response by frontmatter: %w", err)
		}
	}

	return string(content), nil
}

// writeSplitFrontmatterJobs scans a mock LLM response for embedded frontmatter
// blocks (`---\n<yaml>\n---\n<body>`) and writes each as a sequentially numbered
// job file next to GROVE_CURRENT_JOB_PATH. Generated files are named
// NN-generated-job.md, continuing from the current job's leading number. This
// mirrors the real planning flow where an LLM emits multiple job definitions
// that get materialized as files.
func writeSplitFrontmatterJobs(content string) error {
	currentJobPath := os.Getenv("GROVE_CURRENT_JOB_PATH")
	if currentJobPath == "" {
		return nil
	}
	dir := filepath.Dir(currentJobPath)

	// Determine the starting index from the current job's filename prefix.
	startNum := leadingNumber(filepath.Base(currentJobPath)) + 1
	if startNum < 1 {
		startNum = 1
	}

	// Collect the line indices of `---` delimiters.
	lines := strings.Split(content, "\n")
	var delims []int
	for i, line := range lines {
		if strings.TrimSpace(line) == "---" {
			delims = append(delims, i)
		}
	}

	// Each frontmatter block consumes a pair of delimiters. The body runs from
	// the closing delimiter to the next block's opening delimiter (or EOF).
	blockCount := len(delims) / 2
	for k := 0; k < blockCount; k++ {
		openIdx := delims[2*k]
		closeIdx := delims[2*k+1]
		fm := lines[openIdx+1 : closeIdx]

		bodyEnd := len(lines)
		if 2*k+2 < len(delims) {
			bodyEnd = delims[2*k+2]
		}
		body := lines[closeIdx+1 : bodyEnd]

		var b strings.Builder
		b.WriteString("---\n")
		b.WriteString(strings.Join(fm, "\n"))
		b.WriteString("\n---\n")
		b.WriteString(strings.TrimSpace(strings.Join(body, "\n")))
		b.WriteString("\n")

		filename := fmt.Sprintf("%02d-generated-job.md", startNum+k)
		if err := os.WriteFile(filepath.Join(dir, filename), []byte(b.String()), 0o600); err != nil {
			return err
		}
	}

	return nil
}

// leadingNumber parses the leading integer of a filename like "01-initial.md".
// Returns 0 when there is no leading number.
func leadingNumber(name string) int {
	end := 0
	for end < len(name) && name[end] >= '0' && name[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n := 0
	for i := 0; i < end; i++ {
		n = n*10 + int(name[i]-'0')
	}
	return n
}

// prepareWorktree ensures the worktree exists and is ready.
func (e *OneShotExecutor) prepareWorktree(ctx context.Context, job *Job, plan *Plan) (string, error) {
	if job.Worktree == "" {
		return "", fmt.Errorf("job %s has no worktree specified", job.ID)
	}

	// Get project git root for worktree creation (notebook-aware). Notebook
	// plans hard-fail here instead of falling back to the plan directory —
	// the old silent fallback fabricated a worktree container at the notebook
	// plan dir when discovery transiently raced the daemon's collectors (see
	// resolveGitRootForWorktree).
	gitRoot, err := resolveGitRootForWorktree(ctx, plan.Directory)
	if err != nil {
		return "", err
	}

	// If gitRoot is itself inside a worktree, resolve back to the owning repo
	// so the worktree is created/looked up against the right base.
	realGitRoot := gitRoot
	if workspace.IsWorktreePath(gitRoot) {
		if owner, ok := workspace.WorktreeOwner(gitRoot); ok {
			realGitRoot = owner
		}
	}

	// Resolve the EXISTING worktree through the registry-first, anchor-aware
	// resolver and CREATE only if it does not already exist. The legacy path
	// (FindWorktreePath then GetOrPrepareWorktree) probes only realGitRoot's own
	// .grove-worktrees base, so it misses an anchored container (created with
	// `--anchor <sub-repo>`, which lives under the anchor repo's XDG base) and
	// then `git worktree add`s the superproject — producing an empty-submodule
	// legacy stub that duplicates the real XDG container.
	worktreePath, err := resolveOrPrepareWorktree(ctx, realGitRoot, job.Worktree, plan)
	if err != nil {
		return "", err
	}

	// If we're already inside the resolved worktree (or at the git root that
	// maps onto it), use the current dir so the job runs in place.
	currentDir, _ := os.Getwd()
	resolved := filepath.Clean(worktreePath)
	if filepath.Clean(currentDir) == resolved || filepath.Clean(gitRoot) == resolved {
		return currentDir, nil
	}

	// Automatically initialize state within the new worktree for a better UX.
	groveDir := filepath.Join(worktreePath, ".grove")
	if err := os.MkdirAll(groveDir, 0o755); err != nil {
		// Log a warning but don't fail the job, as this is a convenience feature.
		ulog.Warn("Failed to create .grove directory in worktree").
			Err(err).
			Log(ctx)
	} else {
		// Use a flat map with the plan state key to match how state.Set works.
		planName := filepath.Base(plan.Directory)
		stateData := map[string]string{
			groveplan.StateKey: planName,
		}
		yamlBytes, err := yaml.Marshal(stateData)
		if err == nil {
			statePath := filepath.Join(groveDir, "state.yml")
			// This is a best-effort attempt; failure should not stop the job.
			_ = os.WriteFile(statePath, yamlBytes, 0o600)
		}
	}

	return worktreePath, nil
}

// jobContextPaths holds the per-job, isolated context output paths produced by
// regenerateContextInWorktree. Each concurrently dispatched job writes its
// generated/cached context under <plan>/.artifacts/<job-id>/context/ instead
// of the shared plan-scoped <plan>/context/, so jobs in one plan never clobber
// each other's context (the cross-job last-writer-wins race).
type jobContextPaths struct {
	Hot       string // generated hot context file
	Cold      string // generated cold (cached) context file
	FilesList string // hot context files list (for stats display)
}

// existingPaths returns the hot and cold context paths that actually exist on
// disk (empty string for any that don't, or when the receiver is nil). Used to
// hand explicit, job-scoped context paths to the LLM runners while gracefully
// degrading to their default WorkDir resolution when generation produced
// nothing.
func (j *jobContextPaths) existingPaths() (hot, cold string) {
	if j == nil {
		return "", ""
	}
	if j.Hot != "" {
		if _, err := os.Stat(j.Hot); err == nil {
			hot = j.Hot
		}
	}
	if j.Cold != "" {
		if _, err := os.Stat(j.Cold); err == nil {
			cold = j.Cold
		}
	}
	return hot, cold
}

// uploadFiles returns the existing job-scoped context files (hot then cold) to
// attach for runners that take an explicit context-file list (mock/command and
// grove-anthropic paths). Returns nil when the receiver is nil so callers can
// fall back to the shared plan-level lookup.
func (j *jobContextPaths) uploadFiles() []string {
	hot, cold := j.existingPaths()
	var files []string
	if hot != "" {
		files = append(files, hot)
	}
	if cold != "" {
		files = append(files, cold)
	}
	return files
}

// regenerateContextInWorktree regenerates the context within a worktree.
//
// It returns the rules file path that was used and, when context was generated
// for an identifiable job, the job-scoped paths it was written to (nil
// otherwise — callers then fall back to the shared plan-level lookup).
func (e *OneShotExecutor) regenerateContextInWorktree(ctx context.Context, worktreePath, jobType string, job *Job, plan *Plan) (string, *jobContextPaths, error) {
	writer := grovelogging.GetWriter(ctx)
	ulog.Info("Checking context in worktree").
		Field("job_type", jobType).
		Icon(theme.IconFolder).
		Log(ctx)

	// Scope to sub-project if job.Repository is set (for ecosystem worktrees)
	contextDir := ScopeToSubProject(worktreePath, job)
	if contextDir != worktreePath {
		ulog.Info("Scoping context generation to sub-project").
			Field("context_dir", contextDir).
			Field("repository", job.Repository).
			Pretty(fmt.Sprintf("Scoping context to sub-project: %s", job.Repository)).
			Log(ctx)
	}

	// Build a context manager whose generated/cached output is pinned to a
	// per-job artifacts directory, isolating concurrent jobs in the same plan.
	// NewManagerWithPathsOverride returns a fresh, uncached instance, so the
	// override state is never shared across goroutines. When we have no job id
	// to scope by, fall back to the default (shared, plan-scoped) manager.
	var ctxMgr *grovecontext.Manager
	var jobCtx *jobContextPaths
	if job != nil && job.ID != "" {
		artifactsDir := filepath.Join(plan.Directory, ".artifacts", job.ID, "context")
		jobCtx = &jobContextPaths{
			Hot:       filepath.Join(artifactsDir, "context"),
			Cold:      filepath.Join(artifactsDir, "cached-context"),
			FilesList: filepath.Join(artifactsDir, "context-files"),
		}
		// Wipe any artifacts from a prior run before regenerating. A prior run
		// may have written this job's context with a different strip_comments
		// setting (or a stale file set); clearing first guarantees a regen
		// failure below can never leave last run's context on disk for
		// existingPaths()/uploadFiles() to pick up and upload unstripped.
		_ = os.RemoveAll(artifactsDir)
		if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
			return "", jobCtx, fmt.Errorf("preparing job context dir %s: %w", artifactsDir, err)
		}
		coldList := filepath.Join(artifactsDir, "cached-context-files")
		ctxMgr = grovecontext.NewManagerWithPathsOverride(contextDir, jobCtx.Hot, jobCtx.Cold, jobCtx.FilesList, coldList)
		// Comment stripping mutates plain Manager state, so only enable it on
		// this fresh, job-owned instance (never the shared cached Manager in
		// the else branch). Real jobs always have an ID and take this path.
		if job != nil {
			ctxMgr.SetStripComments(job.IsStripCommentsEnabled())
		}
	} else {
		ctxMgr = grovecontext.NewManager(contextDir)
	}

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

		// 5. Try as a named preset (resolves via notebook presets, .cx/, .cx.work/)
		if !foundPath {
			// Strip .rules extension if present to get the preset name
			presetName := job.RulesFile
			presetName = strings.TrimSuffix(presetName, ".rules")
			ctxMgrForLookup := grovecontext.NewManager(contextDir)
			if resolved, err := ctxMgrForLookup.FindRulesetFile(contextDir, presetName); err == nil {
				rulesFilePath = resolved
				foundPath = true
			}
		}

		if !foundPath {
			return "", jobCtx, fmt.Errorf("rules file '%s' not found in plan directory, current directory, git root, or named presets", job.RulesFile)
		}
		// Treat an empty rules file as not yet authored, matching the absent-file
		// path used for newly created jobs. Do this before context generation so
		// both cases fail through the same pre-provider error funnel.
		if info, err := os.Stat(rulesFilePath); err != nil || info.Size() == 0 {
			return "", jobCtx, fmt.Errorf("rules file '%s' not found in plan directory, current directory, git root, or named presets", job.RulesFile)
		}

		log.WithField("rules_file", rulesFilePath).Info("Using job-specific context")
		fmt.Fprintf(writer, "Using job-specific context from: %s\n", rulesFilePath)

		// Generate context using the custom rules file (job-scoped output)
		if err := ctxMgr.GenerateContextFromRulesFile(rulesFilePath, true); err != nil {
			return rulesFilePath, jobCtx, fmt.Errorf("failed to generate job-specific context: %w", err)
		}

		// Mirror the context summary to the per-job writer so callers
		// capturing job.log see the same "Context Summary: N files, …"
		// header that the default-rules branch emits.
		if files, ferr := ctxMgr.ReadFilesList(ctxMgr.ResolveContextFilesListPath()); ferr == nil {
			if stats, serr := ctxMgr.GetStats("oneshot", files, 10); serr == nil {
				fmt.Fprintf(writer, "%s Context Summary: %d files, %s tokens, %s\n",
					theme.IconFileTree,
					stats.TotalFiles,
					grovecontext.FormatTokenCount(stats.TotalTokens),
					grovecontext.FormatBytes(int(stats.TotalSize)))
			}
		}

		return rulesFilePath, jobCtx, e.displayContextInfo(ctx, contextDir)
	}

	// Check if rules file exists for default context generation
	rulesPath := ctxMgr.ResolveRulesPath()
	if _, err := os.Stat(rulesPath); err != nil {
		if os.IsNotExist(err) {
			// Try to create default rules file using cx reset
			fmt.Fprintf(writer, "No .grove/rules file found. Creating default rules file...\n")

			// Try cx reset to create default rules
			var resetCmd *exec.Cmd
			var resetErr error

			// Try grove cx reset first
			resetCmd = delegation.Command("cx", "--dir", contextDir, "reset")
			resetCmd.Dir = contextDir
			resetCmd.Stdout = writer
			resetCmd.Stderr = writer
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
					fmt.Fprintf(writer, "* Created default .grove/rules file\n")
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
					return "", jobCtx, e.displayContextInfo(ctx, contextDir)
				}

				// Check if we have a TTY before prompting
				if !isatty.IsTerminal(os.Stdin.Fd()) && !isatty.IsCygwinTerminal(os.Stdin.Fd()) {
					fmt.Fprintf(writer, "Warning: Could not create .grove/rules file.\n")
					log.WithField("job_type", jobType).Info("No TTY available, proceeding without context")
					return "", jobCtx, e.displayContextInfo(ctx, contextDir)
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
						cmd := delegation.Command("cx", "edit")
						cmd.Dir = contextDir
						cmd.Stdin = os.Stdin
						cmd.Stdout = writer
						cmd.Stderr = writer

						if err := cmd.Run(); err != nil {
							fmt.Fprintf(writer, "Error running %s edit: %v\n", cxBinary, err)
							fmt.Fprintf(writer, "Please try again or choose a different option.\n")
							continue
						}

						// After edit completes, check if rules file now exists
						if _, err := os.Stat(rulesPath); err == nil {
							fmt.Fprintf(writer, "* Rules file created successfully.\n")
							// Break out of the prompt loop and continue with regeneration
							break
						} else {
							fmt.Fprintf(writer, "Warning: Rules file still not found. Please try again.\n")
							continue
						}

					case "p", "proceed":
						fmt.Fprintf(writer, "Warning: Proceeding without context from rules.\n")
						fmt.Fprintf(writer, "Tip: To add context for future runs, open a new terminal, navigate to the context directory, and run 'cx edit'.\n")
						return "", jobCtx, e.displayContextInfo(ctx, contextDir)

					case "c", "cancel":
						return "", jobCtx, fmt.Errorf("job canceled by user: .grove/rules file not found")

					default:
						fmt.Fprintf(writer, "Error: Invalid choice '%s'. Please choose E, P, or C.\n", choice)
						continue
					}

					// If we reach here from the edit case, break the loop
					break
				}
			}
		} else {
			return "", jobCtx, fmt.Errorf("checking .grove/rules: %w", err)
		}
	}

	// Display absolute path of rules file being used
	absRulesPath, _ := filepath.Abs(rulesPath)
	ulog.Info("Found context rules file, regenerating context").
		Field("rules_file", absRulesPath).
		Icon(theme.IconChecklist).
		Pretty(fmt.Sprintf("%s Using rules: %s", theme.IconChecklist, absRulesPath)).
		Log(ctx)

	// Update context from rules
	if err := ctxMgr.UpdateFromRules(); err != nil {
		return rulesPath, jobCtx, fmt.Errorf("update context from rules: %w", err)
	}

	// Generate context file
	if err := ctxMgr.GenerateContext(true); err != nil {
		return rulesPath, jobCtx, fmt.Errorf("generate context: %w", err)
	}

	// Get and display context statistics
	// Read the files list that was just generated
	files, _ := ctxMgr.ReadFilesList(ctxMgr.ResolveContextFilesListPath())
	stats, err := ctxMgr.GetStats("oneshot", files, 10) // Show top 10 files
	if err != nil {
		ulog.Warn("Failed to get context stats").Err(err).Log(ctx)
	} else {
		// Display summary statistics
		requestID, _ := ctx.Value(contextKey("request_id")).(string)
		ulog.Info("Context summary generated").
			Field("request_id", requestID).
			Field("job_id", job.ID).
			Field("total_files", stats.TotalFiles).
			Field("total_tokens", stats.TotalTokens).
			Field("total_size", stats.TotalSize).
			Pretty(fmt.Sprintf("%s Context Summary: %d files, %s tokens, %s",
				theme.IconFileTree,
				stats.TotalFiles,
				grovecontext.FormatTokenCount(stats.TotalTokens),
				grovecontext.FormatBytes(int(stats.TotalSize)))).
			Log(ctx)

		// Mirror the summary to the per-job writer (job.log) so callers that
		// capture stdout-equivalent output can see it. ulog's Pretty payload
		// goes to the unified log only.
		if writer := grovelogging.GetWriter(ctx); writer != nil {
			fmt.Fprintf(writer, "%s Context Summary: %d files, %s tokens, %s\n",
				theme.IconFileTree,
				stats.TotalFiles,
				grovecontext.FormatTokenCount(stats.TotalTokens),
				grovecontext.FormatBytes(int(stats.TotalSize)))
		}

		// Token limit check removed - no longer enforcing limits

		// Show language distribution if there are files
		if stats.TotalFiles > 0 {
			// Sort languages by token count
			var languages []grovecontext.LanguageStats
			for _, lang := range stats.Languages {
				languages = append(languages, *lang)
			}
			sort.Slice(languages, func(i, j int) bool {
				return languages[i].TotalTokens > languages[j].TotalTokens
			})

			// Build language distribution string for pretty output
			var langDistParts []string
			shown := 0
			for _, lang := range languages {
				if shown >= 5 {
					break
				}
				langDistParts = append(langDistParts,
					fmt.Sprintf("%s: %.1f%%", lang.Name, lang.Percentage))
				shown++
			}

			ulog.Info("Language distribution").
				Field("languages", langDistParts).
				Pretty(fmt.Sprintf("%s Language Distribution: %s",
					theme.IconProject,
					strings.Join(langDistParts, ", "))).
				Log(ctx)
		}
	}

	return rulesPath, jobCtx, nil
}

// displayContextInfo displays information about available context files
func (e *OneShotExecutor) displayContextInfo(ctx context.Context, worktreePath string) error {
	writer := grovelogging.GetWriter(ctx)
	var contextFiles []string
	var totalSize int64

	// Check for context file (notebook-resolved or .grove/context)
	ctxMgr := grovecontext.NewManager(worktreePath)
	groveContextPath := ctxMgr.ResolveContextPath()
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

// buildStreamItems assembles the stream layout's ordered item sequence (spec
// 27): head-region layers (anchor "") in layer order, then the context
// documents (include: files + non-chat dep attachments), then the exchanges —
// with any layer anchored to an exchange interleaved immediately after that
// exchange's history block. A layer whose anchor id is absent from the history
// (an edited chat file dropped the exchange) falls back to the head so its
// bytes are never lost; the lost interleave position is warned. With every
// anchor empty (a ladder-born store reopened as stream) this is the
// stream-at-head layout, byte-identical to the ladder.
func buildStreamItems(ctx context.Context, jobID string, layerPaths []string, anchorsByPath map[string]string, includeFiles []string, history HistoryBlocks) []anthropic.StreamItem {
	historyIDs := make(map[string]bool, len(history))
	for _, hb := range history {
		if hb.ExchangeID != "" {
			historyIDs[hb.ExchangeID] = true
		}
	}
	var headLayers []string
	interleaved := make(map[string][]string)
	for _, path := range layerPaths {
		anchor := anchorsByPath[path]
		switch {
		case anchor == "":
			headLayers = append(headLayers, path)
		case historyIDs[anchor]:
			interleaved[anchor] = append(interleaved[anchor], path)
		default:
			ulog.Warn("Stream layer anchored to an exchange missing from history — placing at head").
				Field("job_id", jobID).
				Field("anchor_exchange", anchor).
				Log(ctx)
			headLayers = append(headLayers, path)
		}
	}
	items := make([]anthropic.StreamItem, 0, len(layerPaths)+len(includeFiles)+len(history))
	for _, path := range headLayers {
		items = append(items, anthropic.StreamItem{Kind: anthropic.RequestBlockLayer, Path: path})
	}
	for _, f := range includeFiles {
		items = append(items, anthropic.StreamItem{Kind: anthropic.RequestBlockContext, Path: f})
	}
	for _, hb := range history {
		items = append(items, anthropic.StreamItem{Kind: anthropic.RequestBlockHistory, Text: hb.Text})
		if hb.ExchangeID != "" {
			for _, path := range interleaved[hb.ExchangeID] {
				items = append(items, anthropic.StreamItem{Kind: anthropic.RequestBlockLayer, Path: path})
			}
		}
	}
	return items
}

// leadingLineageLayerCount returns the length of the leading run of layer paths
// whose provenance is an inherited parent lineage or a dep-transcript (K1). The
// run STOPS at the first non-lineage source, so the auto lineage git-delta
// layer (LayerSourceGitDelta) — whose bytes are worktree-time-dependent and
// differ per sibling — plus the child's own base / rules-diff layers are
// excluded. grove-anthropic places the sibling-reuse cache breakpoint on the
// last layer of this leading prefix (see RequestOptions.LineageLayerCount):
// siblings inheriting the same lineage cache-READ that region instead of
// re-writing it. Lineage integrated mid-chat on a later turn is anchored later
// in the layer list, so it is not part of the leading run and correctly gets no
// boundary breakpoint.
func leadingLineageLayerCount(paths []string, sources map[string]string) int {
	n := 0
	for _, p := range paths {
		switch sources[p] {
		case LayerSourceInherited, LayerSourceDepTranscript:
			n++
		default:
			return n
		}
	}
	return n
}

// executeChatJob handles the conversational logic for chat-type jobs
func (e *OneShotExecutor) executeChatJob(ctx context.Context, job *Job, plan *Plan, output io.Writer) (retErr error) {
	// Generate a unique request ID for tracing this turn
	requestID := "req-" + uuid.New().String()[:8]
	ctx = context.WithValue(ctx, contextKey("request_id"), requestID)
	ulog.Info("Executing chat turn").
		Field("job_id", job.ID).
		Field("request_id", requestID).
		Field("plan_name", plan.Name).
		Icon(theme.IconChat).
		Log(ctx)

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

	// Agent-responded chats (responder: agent) have their response turns
	// written directly into the artifact by a fresh agent session per turn;
	// they must NEVER be dispatched to an LLM provider, regardless of the
	// last turn's speaker. Running one is an explicit no-op.
	if job.IsAgentResponded() {
		ulog.Info("agent-responded chat (responder: agent) — skipping LLM dispatch").
			Field("job", job.Title).
			Log(ctx)
		// strip_comments only affects the inlined context uploaded to an LLM API.
		// responder: agent chats never dispatch to an API — the agent reads raw
		// worktree files — so an explicitly-set strip_comments has no effect. Warn
		// so the user isn't misled into thinking their code was stripped.
		if job.StripComments != nil {
			ulog.Warn("strip_comments has no effect for responder: agent chats (the agent reads raw worktree files)").
				Field("job", job.Title).
				Log(ctx)
		}
		// Ensure status is correctly set to pending_user and return.
		if job.Status != JobStatusPendingUser {
			job.Status = JobStatusPendingUser
			_ = updateJobFile(job)
		}
		return nil
	}

	if len(turns) == 0 {
		return fmt.Errorf("chat file has no turns")
	}

	lastTurn := turns[len(turns)-1]

	// If the last turn is from the LLM, or if it's an empty prompt from the user,
	// the job is not ready to run. Return successfully without changing state.
	if lastTurn.Speaker != "user" || strings.TrimSpace(lastTurn.Content) == "" {
		ulog.Info("Chat job is waiting for user input, skipping execution").
			Field("job", job.Title).
			Log(ctx)
		// Ensure status is correctly set to pending_user and return.
		if job.Status != JobStatusPendingUser {
			job.Status = JobStatusPendingUser
			_ = updateJobFile(job)
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
	defer func() { _ = RemoveLockFile(job.FilePath) }()

	// Update job status to running
	job.Status = JobStatusRunning
	job.StartTime = time.Now()
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}

	// Terminal-failure guard: any error return AFTER the job was marked running
	// must leave the job file in a terminal `failed` state with the error logged
	// to the per-job job.log (and the daemon system log) via ulog.Error. Without
	// this, an error path that only `return`s an error (e.g. pinned_context
	// resolution, template lookup, turn-ID generation, or an API call) leaves the
	// .md stuck at `status: running` forever and nothing lands in the job.log —
	// the exact silent hang seen with an unresolvable pinned_context. It fires
	// only when the status is still `running`, so paths that already set their own
	// terminal status (the regen hard-fails, the API paths that this now also
	// covers) or return nil (waiting-for-user, agent-responded, action=complete,
	// successful turn → pending_user/completed) are left untouched.
	defer func() {
		if retErr != nil && job.Status == JobStatusRunning {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			_ = updateJobFile(job)
			// The full error text must reach the per-job job.log (the ulog
			// writer renders only the message line, not .Err) — spec 19 e2e 14
			// asserts the actionable error is IN job.log, not just last_error.
			fmt.Fprintf(grovelogging.GetWriter(ctx), "Chat turn failed: %v\n", retErr)
			ulog.Error("Chat turn failed").
				Err(retErr).
				Field("job_id", job.ID).
				Log(ctx)
		}
	}()

	var execErr error

	// D5 (spec 19): the pinned_context key was removed. Reject it AFTER the
	// running flip so the deferred terminal-failure guard above turns this
	// into status: failed + last_error + job.log — a visible, actionable
	// failure rather than a silent drop of files the author believed were
	// pinned (e2e scenario 14 asserts exactly this path).
	if pinErr := job.PinnedContextRemovedError(); pinErr != nil {
		execErr = pinErr
		return execErr
	}

	// Resolve the Anthropic cache TTL for this chat up front so a junk
	// cache_ttl fails the turn actionably before any provider work (defaults
	// to 1h for chat jobs — spec 19 D2).
	cacheTTL, ttlErr := job.ChatCacheTTL()
	if ttlErr != nil {
		execErr = ttlErr
		return execErr
	}

	// Resolve the chat cache layout the same way (spec 27): "ladder" (default)
	// or "stream". This single local drives both the request assembly and the
	// per-turn manifest stamp below — leaving either hardcoded would assemble a
	// stream chat as ladder and/or make the manifest lie about the layout.
	cacheLayout, layoutErr := job.ChatCacheLayout()
	if layoutErr != nil {
		execErr = layoutErr
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
		if err := os.WriteFile(job.FilePath, newContent, 0o600); err != nil {
			execErr = fmt.Errorf("writing updated chat file: %w", err)
			return execErr
		}

		// Update the job object with the new template
		job.Template = "chat"

		// Update the content variable for subsequent processing
		content = newContent

		ulog.Success("Added 'template: chat' to job frontmatter").Log(ctx)
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
		ulog.Info("Completing chat job").
			Field("job", job.Title).
			Log(ctx)
		job.Status = JobStatusCompleted
		job.EndTime = time.Now()
		_ = updateJobFile(job)
		// Archive the accumulated token usage into the job .md at completion,
		// for parity with the agent on-disk record (best-effort).
		WriteTokenUsageSection(plan, job)
		// The gated completion path — the one eval rider chats take. No new
		// vector is stamped here (this branch renders no new briefing bytes),
		// so the record joins against the last response-producing turn's
		// vector, which is exactly D12's "a record's vector is the stamping
		// turn's".
		writeMetricsRecordQuietly(job, plan)
		return nil
	}

	// Determine the working directory for the job
	var worktreePath string
	var chatUsedRulesPath string
	var chatJobCtx *jobContextPaths
	if job.Worktree != "" {
		// Prepare git worktree only if explicitly specified
		path, err := e.prepareWorktree(ctx, job, plan)
		if err != nil {
			execErr = fmt.Errorf("prepare worktree: %w", err)
			return execErr
		}
		worktreePath = path

		// Regenerate context in the worktree to ensure chat has latest view
		chatUsedRulesPath, chatJobCtx, err = e.regenerateContextInWorktree(ctx, worktreePath, "chat", job, plan)
		if err != nil {
			// Hard-fail: without stripped, job-scoped context we must not proceed
			// and silently upload the shared, unstripped context (the runner's
			// WorkDir fallback) — that is exactly the strip_comments-ignored bug.
			job.Status = JobStatusFailed
			_ = updateJobFile(job)
			ulog.Error("Failed to regenerate context in worktree").Err(err).Log(ctx)
			execErr = fmt.Errorf("regenerating chat context: %w", err)
			return execErr
		}
	} else {
		// No worktree specified, default to the project git repository root (notebook-aware).
		var err error
		worktreePath, err = GetProjectGitRoot(plan.Directory)
		if err != nil {
			// Fallback to the plan's directory if not in a git repo
			worktreePath = plan.Directory
			ulog.Warn("Not a git repository, using plan directory as working directory").
				Field("workdir", worktreePath).
				Log(ctx)
		}

		// Also regenerate context for non-worktree case if .grove/rules exists
		chatUsedRulesPath, chatJobCtx, err = e.regenerateContextInWorktree(ctx, worktreePath, "chat", job, plan)
		if err != nil {
			// Hard-fail (see the worktree branch above): never fall through to an
			// unstripped shared-context upload.
			job.Status = JobStatusFailed
			_ = updateJobFile(job)
			ulog.Error("Failed to regenerate context").Err(err).Log(ctx)
			execErr = fmt.Errorf("regenerating chat context: %w", err)
			return execErr
		}
	}

	// Chat rules archiving is deferred to after turnID generation so we can
	// store per-turn artifacts and include the path in the grove metadata tag.

	// --- Concept Gathering Logic ---
	if job.GatherConceptNotes || job.GatherConceptPlans {
		_, err := gatherConcepts(ctx, job, plan, worktreePath)
		if err != nil {
			ulog.Warn("Failed to gather concepts").
				Err(err).
				Field("request_id", requestID).
				Field("job_id", job.ID).
				Log(ctx)
		}
		// The concept file (if any) is picked up by context gathering logic below.
	}

	// Scope to sub-project if job.Repository is set (for ecosystem worktrees)
	// This ensures chat uses the correct context files
	worktreePath = ScopeToSubProject(worktreePath, job)

	// Determine effective model with clear precedence. Resolved BEFORE
	// dependency handling because the cross-job lineage guard (spec 19 P5)
	// compares it against each parent chat's effective model.
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

	// --- Cross-job cache lineage (spec 19 P5 / D8) ---
	// Completed chat-type dependencies extend this chat's layer sequence: the
	// layer engine rides their artifacts as inherited read-only refs, their
	// conversations as dep-transcript layers, and a git-delta layer trues the
	// inherited context up to the current worktree. Those deps therefore stop
	// traveling in the prompt: inline: dependencies is superseded for them
	// (the transcript layer is the cached, cheaper vehicle) and they are not
	// uploaded as attachments either. Lineage needs a layer store, so a chat
	// without a rules file keeps legacy dep handling for ALL deps; non-chat
	// deps (agent jobs, oneshots) and non-completed chat deps always keep it.
	var lineageParents []LineageParent
	lineageDepIDs := make(map[string]bool)
	if chatUsedRulesPath != "" {
		for _, dep := range job.Dependencies {
			if dep == nil || dep.Type != JobTypeChat || dep.Status != JobStatusCompleted || dep.FilePath == "" {
				continue
			}
			parentModel := lineageEffectiveModel(dep, plan)
			// The parent's effective template mirrors the child's resolution:
			// dep frontmatter template, else the "chat" default (spec 27 §3).
			parentTemplate := dep.Template
			if parentTemplate == "" {
				parentTemplate = "chat"
			}
			lineageParents = append(lineageParents, LineageParent{
				JobID:         dep.ID,
				Title:         dep.Title,
				FilePath:      dep.FilePath,
				PlanDir:       plan.Directory,
				Model:         parentModel,
				ModelMatch:    parentModel == effectiveModel,
				Template:      parentTemplate,
				TemplateMatch: parentTemplate == directive.Template,
			})
			lineageDepIDs[dep.ID] = true
		}
	}

	// Build the prompt
	// Split the conversation into the two transcript cache regions (spec 19
	// D7 / P4): byte-stable per-turn history blocks and the volatile current
	// turn (the only part carrying status="awaiting_response").
	conversation := FormatConversationRegions(turns)

	// --- Stream lineage splice (spec 27 §3) ---
	// Under the stream layout the FIRST fully-eligible parent's dialogue rides
	// as spliced history blocks (so its interleaved layers reproduce in
	// position) rather than a dep-transcript document. Determined here (before
	// the layer engine) so the child's own layers anchor after the parent's
	// final exchange. Degrade-safe: any doubt — model/template mismatch, a
	// missing manifest, a failed prefix hash-verify, an unreadable/edited chat
	// file — falls back to the transcript-document path (splicedParentHistory
	// stays nil), exactly the ladder behavior. Only the primary parent is
	// spliced; parents 2..n keep their transcript documents.
	var splicedParentHistory HistoryBlocks
	var splicedParentID string
	if cacheLayout == anthropic.CacheLayoutStream {
		for _, parent := range lineageParents {
			if !parent.ModelMatch || !parent.TemplateMatch {
				continue
			}
			content, rerr := os.ReadFile(parent.FilePath)
			if rerr != nil {
				break
			}
			pBlocks, perr := parentTranscriptBlocks(content)
			if perr != nil || len(pBlocks) == 0 {
				break
			}
			manifestPath := locateParentLastManifest(parent.PlanDir, parent.JobID)
			if manifestPath == "" {
				ulog.Warn("Stream lineage: parent has no request manifest — inheriting via transcript document (no history splice)").
					Field("job_id", job.ID).
					Field("parent_job_id", parent.JobID).
					Log(ctx)
				break
			}
			if verr := verifyInheritedPrefix(manifestPath, pBlocks); verr != nil {
				ulog.Warn("Stream lineage: inherited-prefix verify failed — inheriting via transcript document (no history splice)").
					Err(verr).
					Field("job_id", job.ID).
					Field("parent_job_id", parent.JobID).
					Log(ctx)
				break
			}
			splicedParentHistory = pBlocks
			splicedParentID = parent.JobID
			break
		}
	}

	// Handle dependencies - either inline into prompt or collect for upload
	// Uses ShouldInline to support both new inline field and legacy prepend_dependencies
	var dependencyFilePaths []string
	var prependedDependencies []struct {
		Filename string
		Content  string
	}
	if job.ShouldInline(InlineDependencies) && len(job.Dependencies) > 0 {
		// Inline mode: read dependency content for embedding in prompt
		log.Debug("inline: [dependencies] enabled - inlining dependency content into prompt")
		// Sort dependencies by filename for consistent order
		sortedDeps := make([]*Job, len(job.Dependencies))
		copy(sortedDeps, job.Dependencies)
		sort.Slice(sortedDeps, func(i, j int) bool {
			if sortedDeps[i] == nil || sortedDeps[j] == nil {
				return false
			}
			return sortedDeps[i].Filename < sortedDeps[j].Filename
		})

		log.WithField("count", len(sortedDeps)).Debug("Inlining dependencies into prompt")
		for _, dep := range sortedDeps {
			if dep != nil && dep.FilePath != "" {
				if lineageDepIDs[dep.ID] {
					// P5: this dep travels as cached lineage layers (inherited
					// refs + dep-transcript), never as prompt text — no
					// <prepended_dependency> for it (spec 19 e2e 11).
					ulog.Info("Chat dependency rides as a dep-transcript context layer; inline: dependencies is superseded for it").
						Field("job_id", job.ID).
						Field("dep", dep.Filename).
						Log(ctx)
					continue
				}
				depContent, err := os.ReadFile(dep.FilePath)
				if err != nil {
					execErr = fmt.Errorf("reading dependency file %s: %w", dep.FilePath, err)
					return execErr
				}
				log.WithField("file", dep.Filename).Debug("Inlined dependency")
				_, depBody, _ := ParseFrontmatter(depContent)
				prependedDependencies = append(prependedDependencies, struct {
					Filename string
					Content  string
				}{dep.Filename, string(depBody)})
			}
		}
	} else if len(job.Dependencies) > 0 {
		// Upload mode: collect dependency file paths for upload/attachment to LLM
		log.WithField("count", len(job.Dependencies)).Debug("Collecting dependencies for upload")
		for _, dep := range job.Dependencies {
			if dep != nil && dep.FilePath != "" {
				if lineageDepIDs[dep.ID] {
					log.WithField("file", dep.Filename).Debug("Chat dependency rides as lineage layers; not uploading as an attachment")
					continue
				}
				dependencyFilePaths = append(dependencyFilePaths, dep.FilePath)
				log.WithField("file", dep.Filename).Debug("Uploading dependency as file attachment")
			}
		}
	}

	// Resolve include files (new feature for chat jobs)
	var includeFilePaths []string
	if len(job.Include) > 0 {
		log.WithField("count", len(job.Include)).Debug("Collecting include files for upload")
		for _, source := range job.Include {
			sourcePath, err := ResolvePromptSource(source, plan)
			if err != nil {
				return fmt.Errorf("could not find include file %s: %w", source, err)
			}
			includeFilePaths = append(includeFilePaths, sourcePath)
			log.WithField("file", source).Debug("Uploading include file as attachment")
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

	// Add Grove context files — prefer the job-scoped generated context (under
	// .artifacts/<job-id>/) over the shared plan-level file so concurrent chat
	// turns in one plan don't upload each other's context.
	log.WithField("worktree", worktreePath).Debug("Worktree path for chat job")

	// Scope to sub-project if job.Repository is set (for ecosystem worktrees)
	contextDir := ScopeToSubProject(worktreePath, job)

	contextPaths := e.collectContextFiles(job, plan, worktreePath, chatJobCtx)

	// Verify context files exist and collect valid paths
	var validContextPaths []string
	for _, contextPath := range contextPaths {
		if info, err := os.Stat(contextPath); err == nil {
			log.WithFields(logrus.Fields{"file": contextPath, "size_bytes": info.Size()}).Debug("Found context file")
			validContextPaths = append(validContextPaths, contextPath)
		} else {
			log.WithFields(logrus.Fields{"file": contextPath, "error": err}).Warn("Failed to access context file")
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

	// Archive context rules for this turn (per-turn snapshot)
	var chatArchivedRulesPath string
	if chatUsedRulesPath != "" {
		archPath, archiveErr := ArchiveContextRulesForTurn(plan, job.ID, turnID, chatUsedRulesPath)
		if archiveErr != nil {
			ulog.Warn("Failed to archive context rules for chat turn").Err(archiveErr).Log(ctx)
		} else {
			chatArchivedRulesPath = archPath
		}
	}

	// --- Layer engine (spec 19 P3) ---
	// The job's rules file is turned into an append-only sequence of frozen
	// layer artifacts (turn 1 freezes 00-base.xml; later turns diff the
	// re-resolved fileset against the union of existing layers and append
	// rules-diff/delta layers). Those artifacts are the Anthropic upload —
	// byte-stable across turns, which is what keeps the cache prefix warm.
	// Refresh verbs arrive via the turn directive (stamped there by the
	// `flow plan run --append-delta/--rebase-context` flags or a reopen).
	if directive.AppendDelta && directive.RebaseContext {
		execErr = fmt.Errorf("append_delta and rebase_context are mutually exclusive for a single turn")
		return execErr
	}
	var layerResult *LayerEngineResult
	if chatUsedRulesPath != "" {
		// Lineage-overlap advisory (oracle-plays K2): fire only on a genuine
		// turn 1 (no layer store yet — nil manifest). A completed sibling chat
		// NOT already among this job's deps, whose frozen layers cover this
		// turn's resolved fileset, would inherit warm under `-d`. Emitted BEFORE
		// PrepareContextLayers freezes the base, so acting on it is still a dep
		// edit rather than a --rebase-context. Advisory only; errors swallowed.
		if existing, _ := LoadLayerManifest(ContextLayersDir(plan.Directory, job.ID)); existing == nil {
			if advice, _ := AdviseLineageOverlapAtFire(plan, job, contextDir, chatUsedRulesPath, effectiveModel); advice != nil {
				fmt.Fprintf(grovelogging.GetWriter(ctx), "Lineage advisory: %s\n", advice.FormatAdvice())
				ulog.Warn("Lineage overlap: a completed sibling chat's frozen layers cover this chat's rules — `-d` would inherit them warm").
					Field("job_id", job.ID).
					Field("parent_job_id", advice.ParentJobID).
					Field("matched_files", advice.MatchedFiles).
					Field("warm_bytes", advice.MatchedBytes).
					Field("model_match", advice.ModelMatch).
					Log(ctx)
			}
		}

		refresh := LayerRefreshNone
		switch {
		case directive.RebaseContext:
			refresh = LayerRefreshRebase
		case directive.AppendDelta:
			refresh = LayerRefreshAppendDelta
		}
		// lastExchangeID anchors any layer frozen THIS turn to the last completed
		// assistant exchange (spec 27): under stream that places a mid-chat
		// widening immediately after that exchange. It walks the COMBINED history
		// (spliced parent blocks + this chat's own), so on a stream-lineage
		// turn 1 the child's base anchors after the parent's final exchange
		// rather than at the head. Empty on a plain turn 1 and harmless under
		// ladder.
		lastExchangeID := ""
		for _, hb := range conversation.HistoryBlocks {
			if hb.ExchangeID != "" {
				lastExchangeID = hb.ExchangeID
			}
		}
		if lastExchangeID == "" {
			for _, hb := range splicedParentHistory {
				if hb.ExchangeID != "" {
					lastExchangeID = hb.ExchangeID
				}
			}
		}
		layerResult, err = PrepareContextLayers(ctx, LayerEngineParams{
			PlanDir:         plan.Directory,
			JobID:           job.ID,
			ContextDir:      contextDir,
			RulesPath:       chatUsedRulesPath,
			TurnID:          turnID,
			StripComments:   job.IsStripCommentsEnabled(),
			SnapshotEnabled: job.IsContextSnapshotEnabled(),
			Refresh:         refresh,
			Layout:          cacheLayout,
			AnchorExchange:  lastExchangeID,
			Lineage:         lineageParents,
		})
		if err != nil {
			execErr = fmt.Errorf("preparing context layers: %w", err)
			return execErr
		}
	} else if directive.AppendDelta || directive.RebaseContext {
		ulog.Warn("append_delta/rebase_context have no effect: this chat has no rules file, so no layer store exists").
			Field("job_id", job.ID).
			Log(ctx)
	}

	// Empty-freeze gate (oracle-plays J1): the layer engine just froze this
	// turn's context. Before assembling the prompt or dispatching to the
	// provider, verify the freeze actually captured the files the job's own
	// rules resolve to — a rules file that was empty/nothing-matching at freeze
	// time would otherwise fire blind on inherited layers and waste a full
	// generation. A trip returns a plain error; the runtime funnel stamps
	// status: failed + last_error (no persister calls here). Only runs when the
	// engine ran (layerResult != nil, i.e. chatUsedRulesPath != "").
	if layerResult != nil {
		if gateErr := validateFrozenContextCoverage(ctx, plan.Directory, job, contextDir, chatUsedRulesPath); gateErr != nil {
			execErr = gateErr
			return execErr
		}
	}

	// TTL staleness advisory (oracle-plays J5): the universal channel that the
	// daemon path also sees (the CLI warning in plan_run.go is skipped under the
	// daemon). Anthropic-only — the ladder cache is Anthropic-only — and only
	// once the layer store exists (a fresh chat with no lineage has nothing that
	// can go stale). Mirrors the "Context staleness advisory" idiom in
	// layer_engine.go: one writer line plus a ulog.Warn. Read of prior activity
	// only — this turn's manifest isn't written yet.
	if layerResult != nil && strings.HasPrefix(effectiveModel, "claude") {
		if msg, stale := ChatCacheStaleness(plan.Directory, job); stale {
			fmt.Fprintf(grovelogging.GetWriter(ctx),
				"Cache staleness advisory: %s — keep it hot between turns with `flow plan warm %s`\n", msg, job.Filename)
			ulog.Warn("Chat cache lineage is stale — the cached prefix will be cold-written this turn").
				Field("job_id", job.ID).
				Log(ctx)
		}
	}

	// --- Transcript split (spec 19 D7 / P4) ---
	// The chat request travels in three regions, ordered by lifetime:
	//
	//   system (API system param, ladder BP1) — the chat template plus the
	//   static conversation note. This must stay byte-stable for the WHOLE
	//   chat: the system param serializes ahead of every document and text
	//   block (tools → system → messages), so any system change re-writes the
	//   entire request from byte 0 — layers, history, everything. It therefore
	//   contains NOTHING derived from deps, includes, context files, layers,
	//   or turns. Switching the turn template mid-chat does change it; that is
	//   a deliberate persona change and the one intended system bust.
	//
	//   history (per-turn text blocks, ladder BP3 on the last) — completed
	//   turns 1…K−1 from FormatConversationRegions. Append-only: prior turns
	//   serialize byte-identically forever; status="awaiting_response" and
	//   respond_as exist only in the volatile block.
	//
	//   prompt (volatile text block, never cached) — the <context> block plus
	//   the current turn. The <context> block (uploaded-file markers,
	//   prepended dependency bodies) deliberately rides in the volatile
	//   region: marker lists and dep sets mutate mid-chat (a new dep or
	//   include, a context regeneration), so putting them in the system param
	//   would bust BP1 and everything downstream on every such change, and
	//   putting them ahead of the history blocks would break the history
	//   region's append-only prefix. In the volatile tail their bytes re-bill
	//   each turn at the plain input rate — the same cost as today, strictly
	//   cheaper than a premium cache re-write, and the large part (inlined dep
	//   bodies) moves into dep-transcript layers in P5.

	systemPrompt := buildChatSystemPrompt(templateContent)

	contextBlock := buildChatContextBlock(prependedDependencies, dependencyFilePaths, includeFilePaths, validContextPaths)

	var volatileBuilder strings.Builder
	if contextBlock != "" {
		volatileBuilder.WriteString(contextBlock)
		volatileBuilder.WriteString("\n")
	}
	// <current-files> supersession index (spec 27 §5b): when a delta layer has
	// superseded earlier copies, a compact `path → layer N` map rides in the
	// volatile turn (uncached, regenerated fresh each turn, both layouts) so the
	// oracle always knows the winning copy even when a supersession pair
	// straddles interleaved dialogue.
	if layerResult != nil && layerResult.SupersededIndex != "" {
		volatileBuilder.WriteString(layerResult.SupersededIndex)
	}
	volatileBuilder.WriteString(conversation.CurrentTurn)
	volatilePrompt := volatileBuilder.String()

	// Flattened single-prompt form for callers without a block-structured
	// upload: gemini, the mock LLM client, and the briefing file.
	fullPrompt := flattenChatPrompt(systemPrompt, conversation.HistoryBlocks.Texts(), volatilePrompt)

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
		ulog.Warn("Failed to write chat briefing file").
			Err(err).
			Log(ctx)
	} else {
		// Chat vectors are per-turn (D12): each response-producing turn
		// overwrites the artifact, so the vector on disk always describes the
		// turn that produced the latest response.
		stampJobConfigVector(ctx, job, plan, e.config, worktreePath, chatJobCtx, nil, briefingFilePath)
		ulog.Success("Chat briefing file created").
			Field("job_id", job.ID).
			Field("request_id", requestID).
			Field("turn_id", turnID).
			Field("briefing_file_path", briefingFilePath).
			Field("prompt_chars", len(fullPrompt)).
			Pretty(theme.IconSuccess + " Chat briefing: " + theme.DefaultTheme.Accent.Render(briefingFilePath)).
			Log(ctx)
	}

	if len(validContextPaths) > 0 {
		log.WithField("count", len(validContextPaths)).Info("Including context files as attachments")
	} else {
		log.Warn("No context files included in chat prompt")
	}

	// Create LLM options with the effective model (resolved above, before
	// dependency handling — the lineage guard needed it).
	// Combine dependency and include files for upload
	allIncludeFiles := append(dependencyFilePaths, includeFilePaths...)
	llmOpts := LLMOptions{
		Model:        effectiveModel,
		WorkingDir:   contextDir,
		ContextFiles: validContextPaths, // Pass context file paths
		IncludeFiles: allIncludeFiles,   // Pass dependency + include file paths
	}

	// Log memory usage before LLM call
	log.Debug("About to call LLM")
	log.WithField("prompt_length_bytes", len(fullPrompt)).Debug("Prompt length")
	log.WithField("context_files_count", len(llmOpts.ContextFiles)).Debug("Context files")
	for i, cf := range llmOpts.ContextFiles {
		log.WithField("file", cf).Debug(fmt.Sprintf("Context file %d", i+1))
	}

	// NOTE: We intentionally do NOT shell out to `cx generate` here.
	// regenerateContextInWorktree (called above) already generated this job's
	// context in-process and wrote it to the job-scoped .artifacts/<job-id>/
	// paths. A `cx` subprocess cannot see the in-memory path override, so it
	// would (a) regenerate into the shared plan-scoped file and (b) race with
	// other concurrent jobs — exactly the bug this change fixes. The job-scoped
	// paths are handed to the runners explicitly via chatHotCtx/chatColdCtx.
	chatHotCtx, chatColdCtx := chatJobCtx.existingPaths()

	// --- Ladder upload (spec 19 P3) ---
	// The chat path uses the Anthropic stability-ladder cache layout (D1)
	// with the job's cache TTL (D2, default 1h). The document region is the
	// layer engine's frozen artifact sequence, in layers.json order — the
	// cache breakpoint rides on the LAST layer document. Dependency uploads
	// and includes follow as plain ContextFiles (no breakpoint of their own):
	// they become real transcript layers in P5, and once P4 lands they sit
	// under the history-block breakpoint. Under ladder grove-anthropic skips
	// its own hot/cold/CLAUDE.md resolution (D6), so chats with no rules file
	// (no layer store) upload only deps/includes + the volatile prompt.
	var chatLayerFiles []string
	var chatLayerSources map[string]string
	var chatLayerAnchors map[string]string
	if layerResult != nil {
		chatLayerFiles = layerResult.LayerPaths
		chatLayerSources = layerResult.SourcesByPath
		chatLayerAnchors = layerResult.AnchorsByPath
	}

	// The full Anthropic request options for this turn, shared by the live
	// claude dispatch below and the request manifest (the mock path describes
	// the same options so e2e runs assert the real assembly). The chat template
	// travels as SystemPrompt (BP1); how the layers, context docs, and history
	// blocks are laid out depends on the cache layout (spec 19 ladder / spec 27
	// stream). Only the volatile current turn + <context> block ride in Prompt
	// (never cached) either way.
	anthropicOpts := anthropic.RequestOptions{
		Model:        effectiveModel,
		Prompt:       volatilePrompt,
		SystemPrompt: systemPrompt,
		WorkDir:      contextDir,
		CacheLayout:  cacheLayout,
		CacheTTL:     cacheTTL,
		MaxTokens:    modelpkg.MaxTokens(effectiveModel),
		NoCache:      job.NoCache, // Frontmatter no_cache: true opts out of prompt caching
		Caller:       "grove-flow-chat",
		JobID:        job.ID,
		PlanName:     plan.Name,
	}
	if cacheLayout == anthropic.CacheLayoutStream {
		// Stream (spec 27): one ordered heterogeneous sequence replaces the
		// LayerFiles+ContextFiles+HistoryBlocks split. Head layers (anchor "")
		// first, then context docs, then the exchanges — with any layer frozen
		// this or a prior turn interleaved right after the exchange it anchors
		// to. It is the single source of truth, so the split fields stay nil.
		//
		// Stream lineage (spec 27 §3): when a primary parent is spliced, prepend
		// its dialogue as history blocks and drop its transcript document (its
		// interleaved layers reproduce in position via their exchange anchors).
		streamHistory := conversation.HistoryBlocks
		streamLayerFiles := chatLayerFiles
		if len(splicedParentHistory) > 0 {
			streamHistory = append(append(HistoryBlocks{}, splicedParentHistory...), conversation.HistoryBlocks...)
			streamLayerFiles = make([]string, 0, len(chatLayerFiles))
			for _, path := range chatLayerFiles {
				if chatLayerSources[path] == LayerSourceDepTranscript &&
					strings.Contains(filepath.Base(path), "transcript-"+splicedParentID+".") {
					continue // spliced as history instead of an uploaded document
				}
				streamLayerFiles = append(streamLayerFiles, path)
			}
		}
		anthropicOpts.Stream = buildStreamItems(ctx, job.ID, streamLayerFiles, chatLayerAnchors, allIncludeFiles, streamHistory)
		// K1 lineage boundary: count the leading lineage run over the layers that
		// actually ride the stream (streamLayerFiles drops a spliced parent's
		// transcript doc), since buildStreamItems places head layers first in that
		// order — the leading stream items are exactly those lineage layers.
		anthropicOpts.LineageLayerCount = leadingLineageLayerCount(streamLayerFiles, chatLayerSources)
	} else {
		// Ladder (spec 19 D1): frozen layer artifacts form the document region
		// (BP2 on the last), context docs follow with no breakpoint, and the
		// completed prior turns ride as per-turn HistoryBlocks (BP3 on the last).
		anthropicOpts.HistoryBlocks = conversation.HistoryBlocks.Texts()
		anthropicOpts.LayerFiles = chatLayerFiles
		anthropicOpts.ContextFiles = allIncludeFiles
		// K1 lineage boundary: a leading inherited-lineage/dep-transcript prefix
		// gets a 4th breakpoint on its last layer so sibling chats cache-READ the
		// shared transcript region instead of re-writing it.
		anthropicOpts.LineageLayerCount = leadingLineageLayerCount(chatLayerFiles, chatLayerSources)
	}

	// Per-turn request manifest (spec 19 D9): record, next to the briefing
	// file, exactly which bytes go up in which order and where the cache save
	// points sit. Written from the same pre-dispatch data that builds the
	// request — including for mock runs, which are the e2e assertion surface.
	// Best-effort: manifest problems warn and never fail the turn.
	writeTurnManifest := func(provider string, entries []RequestManifestEntry, entriesErr error) {
		if entriesErr != nil {
			ulog.Warn("Failed to assemble request manifest entries").
				Err(entriesErr).
				Field("job_id", job.ID).
				Field("turn_id", turnID).
				Log(ctx)
			return
		}
		// Layer entries carry their layers.json provenance (spec 19 P3).
		AnnotateLayerSources(entries, chatLayerSources)
		manifest := RequestManifest{
			TurnID:    turnID,
			JobID:     job.ID,
			Model:     effectiveModel,
			Provider:  provider,
			CreatedAt: time.Now().UTC(),
			Entries:   entries,
		}
		if provider != requestManifestProviderGemini && provider != requestManifestProviderOpenRouter {
			manifest.CacheLayout = cacheLayout
			manifest.CacheTTL = cacheTTL
			manifest.NoCache = job.NoCache
		}
		if manifestPath, mErr := WriteRequestManifest(plan.Directory, job.ID, turnID, manifest); mErr != nil {
			ulog.Warn("Failed to write request manifest").
				Err(mErr).
				Field("job_id", job.ID).
				Field("turn_id", turnID).
				Log(ctx)
		} else {
			log.WithField("manifest", manifestPath).Debug("Wrote per-turn request manifest")
			// Record this manifest as the lineage's last (spec 27 P3): a stream
			// child locates the parent's last manifest via snapshot.json to
			// verify the inherited prefix. Only chats with a layer store carry a
			// snapshot; best-effort, never fails the turn.
			if layerResult != nil {
				if snapErr := UpdateLayerSnapshotLastManifest(plan.Directory, job.ID, filepath.Base(manifestPath)); snapErr != nil {
					ulog.Warn("Failed to record last request manifest in snapshot").
						Err(snapErr).
						Field("job_id", job.ID).
						Log(ctx)
				}
			}
		}
	}

	// Call LLM based on model type with automatic retry for transient failures
	log.WithField("model", effectiveModel).Debug("Calling LLM")
	var response string
	var apiKey string
	var geminiErr error
	// apiUsage carries the in-process token/cost usage from the anthropic runner
	// (claude branch only), accumulated into the job's token-usage artifact after
	// a successful turn. Gemini/mock/other providers leave it nil.
	var apiUsage *anthropic.UsageResult
	if os.Getenv("GROVE_MOCK_LLM_RESPONSE_FILE") != "" {
		// Check if mocking is enabled - if so, always use llmClient regardless of model.
		// The manifest still describes the REAL ladder assembly for these options —
		// the mock path is the tend e2e suite's assertion surface (D9).
		entries, entriesErr := DescribeChatRequestManifest(anthropicOpts)
		writeTurnManifest(requestManifestProviderMock, entries, entriesErr)
		response, _ = e.llmClient.Complete(ctx, job, plan, fullPrompt, llmOpts, output)
	} else if strings.HasPrefix(effectiveModel, "gemini") {
		// Resolve API key here where we have the correct execution context
		apiKey, geminiErr = geminiconfig.ResolveAPIKey()
		if geminiErr != nil {
			// Don't fail immediately, let the runner handle it for a more consistent error
			apiKey = ""
		}
		// The ladder cache layout is Anthropic-only; gemini keeps its flat
		// upload (hot/cold passed explicitly + dependency/include attachments).
		// Record the flattened shape in the manifest — no breakpoints (D9).
		// The layer store still exists on disk (artifacts ≠ caching) but its
		// artifacts are not what rides up — warn so the user knows the cache
		// lineage semantics don't apply to this chat (spec 19 e2e 20).
		if layerResult != nil {
			fmt.Fprintf(grovelogging.GetWriter(ctx),
				"Context layers warning: model %s is not Anthropic — the layer store is flattened into the gemini upload (job-scoped cx context) and no cache breakpoints apply (the ladder cache layout is Anthropic-only)\n",
				effectiveModel)
			ulog.Warn("Gemini chat: context layers flattened into legacy upload (no Anthropic cache lineage)").
				Field("job_id", job.ID).
				Field("model", effectiveModel).
				Log(ctx)
		}
		var geminiUploads []string
		if chatColdCtx != "" {
			geminiUploads = append(geminiUploads, chatColdCtx)
		}
		if chatHotCtx != "" {
			geminiUploads = append(geminiUploads, chatHotCtx)
		}
		geminiUploads = append(geminiUploads, allIncludeFiles...)
		entries, entriesErr := BuildFlattenedRequestManifestEntries(geminiUploads, fullPrompt)
		writeTurnManifest(requestManifestProviderGemini, entries, entriesErr)
		// Use grove-gemini package for Gemini models with retry
		err = e.executeWithRetry(ctx, job, func() error {
			// The grove context (hot/cold) is passed explicitly via
			// HotContextFile/ColdContextFile below, so PromptFiles carries only
			// dependency + include attachments — avoids uploading the context
			// twice and keeps it job-scoped.
			opts := gemini.RequestOptions{
				Model:            llmOpts.Model,
				Prompt:           fullPrompt,
				APIKey:           apiKey,          // Pass the resolved API key
				PromptFiles:      allIncludeFiles, // Dependency + include attachments
				WorkDir:          contextDir,
				HotContextFile:   chatHotCtx,               // Job-scoped, avoids cross-job race
				ColdContextFile:  chatColdCtx,              // Job-scoped, avoids cross-job race
				SkipConfirmation: e.config.SkipInteractive, // Respect -y flag
				NoCache:          job.NoCache,              // Frontmatter no_cache: true opts out of prompt caching
				// Pass context for better logging
				Caller:   "grove-flow-chat",
				JobID:    job.ID,
				PlanName: plan.Name,
			}
			if isTUIMode() {
				fmt.Fprintf(output, "\n󰚩 Calling Gemini API with model: %s\n\n", effectiveModel)
			}
			var innerErr error
			response, innerErr = e.geminiRunner.Run(ctx, opts)
			return innerErr
		})
		if err != nil {
			ulog.Error("Gemini API call failed").
				Err(err).
				Pretty(theme.DefaultTheme.Error.Render(fmt.Sprintf("%s Gemini API call failed: %v", theme.IconError, err))).
				Log(ctx)
			execErr = fmt.Errorf("Gemini API completion: %w", err)
			return execErr
		}
	} else if strings.HasPrefix(effectiveModel, "claude") {
		// Resolve API key here where we have the correct execution context
		apiKey, anthropicErr := anthropicconfig.ResolveAPIKey()
		if anthropicErr != nil {
			err = fmt.Errorf("resolving Anthropic API key: %w", anthropicErr)
		} else {
			// Record what this turn uploads and where the save points sit (D9)
			// from the exact options the runner is about to receive.
			entries, entriesErr := DescribeChatRequestManifest(anthropicOpts)
			writeTurnManifest(requestManifestProviderAnthropic, entries, entriesErr)
			// Use grove-anthropic package for Claude models with retry, under
			// the ladder layout (anthropicOpts — see the bridge above).
			err = e.executeWithRetry(ctx, job, func() error {
				opts := anthropicOpts
				opts.APIKey = apiKey
				if isTUIMode() {
					fmt.Fprintf(output, "\n%s Calling Anthropic API with model: %s\n\n", theme.IconRobot, effectiveModel)
				}
				var innerErr error
				var usage *anthropic.UsageResult
				response, usage, innerErr = e.anthropicRunner.RunWithUsage(ctx, opts)
				if innerErr == nil {
					apiUsage = usage // only the final successful attempt's usage
				}
				return innerErr
			})
		}
		if err != nil {
			ulog.Error("Anthropic API call failed").
				Err(err).
				Pretty(theme.DefaultTheme.Error.Render(fmt.Sprintf("%s Anthropic API call failed: %v", theme.IconError, err))).
				Log(ctx)
			execErr = fmt.Errorf("Anthropic API completion: %w", err)
			return execErr
		}
	} else if openroutermodels.HasPrefix(effectiveModel) {
		// The Anthropic ladder cache layout is Anthropic-only; OpenRouter keeps
		// a flat upload (hot/cold passed explicitly via the dedicated fields +
		// dependency/include attachments). Record the flattened shape in the
		// manifest — no cache breakpoints (D9). The layer store still exists on
		// disk (artifacts ≠ caching) but its artifacts are not what rides up —
		// warn so the user knows the cache lineage semantics don't apply.
		if layerResult != nil {
			fmt.Fprintf(grovelogging.GetWriter(ctx),
				"Context layers warning: model %s is not Anthropic — the layer store is flattened into the OpenRouter upload (job-scoped cx context) and no cache breakpoints apply (the ladder cache layout is Anthropic-only)\n",
				effectiveModel)
			ulog.Warn("OpenRouter chat: context layers flattened into legacy upload (no Anthropic cache lineage)").
				Field("job_id", job.ID).
				Field("model", effectiveModel).
				Log(ctx)
		}
		var openrouterUploads []string
		if chatColdCtx != "" {
			openrouterUploads = append(openrouterUploads, chatColdCtx)
		}
		if chatHotCtx != "" {
			openrouterUploads = append(openrouterUploads, chatHotCtx)
		}
		openrouterUploads = append(openrouterUploads, allIncludeFiles...)
		entries, entriesErr := BuildFlattenedRequestManifestEntries(openrouterUploads, fullPrompt)
		writeTurnManifest(requestManifestProviderOpenRouter, entries, entriesErr)
		// Resolve API key here where we have the correct execution context.
		apiKey, orErr := openrouterconfig.ResolveAPIKey()
		if orErr != nil {
			err = fmt.Errorf("resolving OpenRouter API key: %w", orErr)
		} else {
			// Use grove-openrouter package for OpenRouter models with retry.
			err = e.executeWithRetry(ctx, job, func() error {
				// hot/cold pass via dedicated fields (Correction 3); only the
				// include set rides in ContextFiles — do NOT flatten hot/cold
				// into ContextFiles or the job-scoped-context guarantees are lost.
				opts := openrouter.RequestOptions{
					Model:           effectiveModel,
					Prompt:          fullPrompt, // SystemPrompt already embedded via flattenChatPrompt
					WorkDir:         contextDir,
					HotContextFile:  chatHotCtx,  // Job-scoped, avoids cross-job race
					ColdContextFile: chatColdCtx, // Job-scoped, avoids cross-job race
					ContextFiles:    allIncludeFiles,
					APIKey:          apiKey,
					Caller:          "grove-flow-chat",
					JobID:           job.ID,
					PlanName:        plan.Name,
				}
				if isTUIMode() {
					fmt.Fprintf(output, "\n%s Calling OpenRouter API with model: %s\n\n", theme.IconRobot, effectiveModel)
				}
				var innerErr error
				response, innerErr = e.openrouterRunner.Run(ctx, opts)
				return innerErr
			})
		}
		if err != nil {
			ulog.Error("OpenRouter API call failed").
				Err(err).
				Pretty(theme.DefaultTheme.Error.Render(fmt.Sprintf("%s OpenRouter API call failed: %v", theme.IconError, err))).
				Log(ctx)
			execErr = fmt.Errorf("OpenRouter API completion: %w", err)
			return execErr
		}
	} else {
		if isTUIMode() {
			fmt.Fprintf(output, "\n󰚩 Calling LLM API with model: %s\n\n", effectiveModel)
		}
		// Use traditional llm command
		response, err = e.llmClient.Complete(ctx, job, plan, fullPrompt, llmOpts, output)
		if err != nil {
			ulog.Error("LLM API call failed").
				Err(err).
				Pretty(theme.DefaultTheme.Error.Render(fmt.Sprintf("%s LLM API call failed: %v", theme.IconError, err))).
				Log(ctx)
			execErr = fmt.Errorf("LLM completion: %w", err)
			return execErr
		}
	}
	log.WithField("response_length_bytes", len(response)).Debug("LLM call succeeded")

	// Accumulate this turn's API token/cost usage into the job's token-usage
	// artifact the moment the response lands — before the cell append and the
	// pending_user flip — so the TUI cell reflects it immediately. Best-effort:
	// never fail the turn over accounting. (Anthropic claude branch only;
	// apiUsage is nil for gemini/mock.)
	if apiUsage != nil {
		if accErr := AccumulateAPITokenUsage(plan, job, apiUsage); accErr != nil {
			ulog.Warn("Failed to accumulate API token usage").
				Err(accErr).
				Field("job_id", job.ID).
				Log(ctx)
		}

		// Advisory per-turn cache-health line (oracle-plays J6). One write
		// reaches job.log, `flow plan run`, and the flow-status TUI. Anthropic
		// only: apiUsage is nil for gemini/mock, so nothing is emitted there.
		// Compute errors degrade to a ulog.Warn and no line — never a job error.
		logPath, _ := GetJobLogPath(plan, job)
		priorHealth, hasPrior := LastCacheHealthFromLog(logPath)
		if health, chErr := ComputeCacheHealth(plan.Directory, job.ID, turnID, apiUsage, priorHealth.HitPct, hasPrior); chErr != nil {
			ulog.Warn("Failed to compute chat turn cache health").
				Err(chErr).
				Field("job_id", job.ID).
				Log(ctx)
		} else if health != nil {
			fmt.Fprintf(grovelogging.GetWriter(ctx), "%s\n", FormatCacheHealthLine(*health))
			ulog.Info("chat turn cache health").
				Field("turn_id", health.TurnID).
				Field("hit_pct", health.HitPct).
				Field("written", health.Written).
				Field("buster", health.Buster).
				Log(ctx)
		}
	}

	// Use the same turnID that was generated earlier for the briefing file
	// This creates a 1:1 correspondence between briefing files and chat turns
	// (turnID was already generated before the LLM call)

	// Append the response to the chat file
	// Use the directive's template (which respects frontmatter > inline directive > default "chat")
	timestamp := time.Now().Format("2006-01-02 15:04:05")

	// Build the grove metadata tag, optionally including the archived rules path
	groveMetadata := fmt.Sprintf(`"id": "%s"`, turnID)
	if chatArchivedRulesPath != "" {
		groveMetadata += fmt.Sprintf(`, "rules_file": "%s"`, chatArchivedRulesPath)
	}
	newCell := fmt.Sprintf("\n<!-- grove: {%s} -->\n## LLM Response (%s)\n\n%s\n\n<!-- grove: {\"template\": \"%s\"} -->\n", groveMetadata, timestamp, response, directive.Template)

	// Append atomically
	if err := os.WriteFile(job.FilePath, append(content, []byte(newCell)...), 0o600); err != nil {
		execErr = fmt.Errorf("appending LLM response: %w", err)
		return execErr
	}

	ulog.Success("Added LLM response to chat").
		Field("chat_file", job.FilePath).
		Pretty(theme.IconSuccess + " Added LLM response to chat: " + theme.DefaultTheme.Accent.Render(job.FilePath)).
		Log(ctx)
	// Determine final status based on auto_complete flag
	finalStatus := JobStatusPendingUser
	statusMessage := "Chat job is now waiting for user input"
	if job.AutoComplete {
		finalStatus = JobStatusCompleted
		statusMessage = "Chat job auto-completed (bypassing review gate)"
	}

	ulog.Success("Chat job response added").
		Field("status", string(finalStatus)).
		Pretty(theme.IconSuccess + " " + statusMessage).
		Log(ctx)

	// Update job status - respect the auto_complete flag
	job.Status = finalStatus
	job.EndTime = time.Now()
	_ = updateJobFile(job)

	// On a terminal transition (auto_complete → completed), archive the
	// accumulated token usage into the job .md for parity with agent jobs.
	// pending_user chats keep only the live artifact until the user completes
	// them (handled by the directive.Action == "complete" path above).
	if finalStatus == JobStatusCompleted {
		WriteTokenUsageSection(plan, job)
		writeMetricsRecordQuietly(job, plan)
	}

	return nil
}

// executeWithRetry wraps an LLM execution function with automatic retry logic for transient failures.
// It respects the job's RetryTransient setting (default 1 = no retries on top of initial attempt).
func (e *OneShotExecutor) executeWithRetry(ctx context.Context, job *Job, fn func() error) error {
	maxAttempts := 1 // Initial attempt
	if job.RetryTransient > 0 {
		maxAttempts += job.RetryTransient
	}

	attempt := 0
	var lastErr error

	for attempt < maxAttempts {
		attempt++
		err := fn()
		if err == nil {
			return nil
		}

		lastErr = err

		// If not a transient error, fail immediately
		if !IsTransientError(err) {
			return err
		}

		// If this was the last attempt, return the error
		if attempt >= maxAttempts {
			return err
		}

		// Exponential backoff: 1s, 2s, 4s, etc.
		backoff := time.Duration(1<<uint(attempt-1)) * time.Second
		ulog.Warn("Transient error, retrying").
			Err(err).
			Field("job_id", job.ID).
			Field("attempt", attempt).
			Field("max_attempts", maxAttempts).
			Field("backoff", backoff.String()).
			Log(ctx)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
			// Continue to next attempt
		}
	}

	return lastErr
}
