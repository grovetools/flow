package orchestration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/grovetools/core/config"
	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/mux"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/core/util/sanitize"
	grovecontext "github.com/grovetools/cx/pkg/context"
	geminiconfig "github.com/grovetools/grove-gemini/pkg/config"
	"github.com/grovetools/grove-gemini/pkg/gemini"
	"github.com/sirupsen/logrus"
)

// InteractiveAgentProvider defines the interface for launching an interactive agent.
type InteractiveAgentProvider interface {
	Launch(ctx context.Context, job *Job, plan *Plan, workDir string, agentArgs []string, briefingFilePath string) error
}

// InteractiveAgentExecutor executes interactive agent jobs in tmux sessions.
type InteractiveAgentExecutor struct {
	skipInteractive bool
	log             *logrus.Entry
	ulog            *grovelogging.UnifiedLogger
	llmClient       LLMClient
	geminiRunner    *gemini.RequestRunner
}

// NewInteractiveAgentExecutor creates a new interactive agent executor.
func NewInteractiveAgentExecutor(llmClient LLMClient, geminiRunner *gemini.RequestRunner, skipInteractive bool) *InteractiveAgentExecutor {
	return &InteractiveAgentExecutor{
		skipInteractive: skipInteractive,
		log:             grovelogging.NewLogger("grove-flow"),
		ulog:            grovelogging.NewUnifiedLogger("grove-flow"),
		llmClient:       llmClient,
		geminiRunner:    geminiRunner,
	}
}

// Name returns the executor name.
func (e *InteractiveAgentExecutor) Name() string {
	return "interactive_agent"
}

// Execute runs an interactive agent job in a tmux session and blocks until completion.
// The output writer is ignored for interactive agents as they run in a separate tmux session.
func (e *InteractiveAgentExecutor) Execute(ctx context.Context, job *Job, plan *Plan) error {
	// Early progress: launching an interactive agent can take tens of seconds
	// (memory prefetch, worktree prep, pane attach) with nothing visible until
	// the pane appears. Emit through the same log stream the user already sees.
	e.ulog.Info("Launching interactive agent").
		Field("job_id", job.ID).
		Pretty(theme.IconInteractiveAgent + " Launching interactive agent...").
		Log(ctx)

	// Load config up front: provider resolution and per-provider model
	// validation both need it, and an unknown provider or bad model should
	// fail before any setup work. A malformed config shouldn't hard-fail an
	// agent launch; log and fall back to defaults.
	coreCfg, err := config.LoadFrom(".")
	if err != nil {
		coreCfg = &config.Config{}
	}
	var flowCfg FlowConfig
	if parsed, cfgErr := FlowConfigFrom(coreCfg); cfgErr != nil {
		e.ulog.Warn("Failed to parse flow configuration; using defaults").
			Field("job_id", job.ID).
			Err(cfgErr).
			Log(ctx)
	} else {
		flowCfg = *parsed
	}

	// Resolve the effective provider (job frontmatter > flow.interactive_provider
	// > claude) and fail early on unknown names.
	spec, err := resolveJobProviderSpec(job, flowCfg)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		e.ulog.Error("Agent provider validation failed").
			Field("job_id", job.ID).
			Field("provider", ResolveJobProviderName(job, flowCfg)).
			Err(err).
			Pretty(" " + err.Error()).
			Log(ctx)
		return fmt.Errorf("agent provider: %w", err)
	}

	// Validate model before any setup work so the user gets an actionable
	// error instead of a generic failure after the agent tries to launch.
	if err := ValidateModelForJob(job.Model, job.Type, spec.Name); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		e.ulog.Error("Model validation failed").
			Field("job_id", job.ID).
			Field("model", job.Model).
			Err(err).
			Pretty(" " + err.Error()).
			Log(ctx)
		return fmt.Errorf("model validation: %w", err)
	}

	// Determine workDir first, as it's needed for briefing file generation
	workDir, err := e.determineWorkDir(ctx, job, plan)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to determine working directory: %w", err)
	}

	// Record per-repo start HEADs (commits.json sidecar) on the worktree
	// CONTAINER (workDir may be scoped to a sub-repo); CompleteJob finalizes
	// the record. Best-effort: a capture failure must never block the launch.
	if job.Worktree != "" {
		if container, cerr := resolveJobWorktreeContainer(job, plan); cerr == nil {
			if cerr := CaptureJobCommitsStart(job, plan, container); cerr != nil {
				e.ulog.Warn("Failed to capture start commit record").
					Field("job_id", job.ID).
					Err(cerr).
					Log(ctx)
			}
		}
	}

	var briefingFilePath string

	// If generate_plan_from is true, we first call an LLM to generate a plan from the chat.
	if job.GeneratePlanFrom {
		e.ulog.Info("Generating implementation plan from chat dependency").Log(ctx)
		generatedPlanContent, err := e.generatePlanFromDependencies(ctx, job, plan)
		if err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			if uerr := updateJobFile(job); uerr != nil {
				e.ulog.Warn("Failed to persist job status").
					Field("job_id", job.ID).
					Err(uerr).
					Log(ctx)
			}
			return fmt.Errorf("failed to generate plan from dependencies: %w", err)
		}

		// Write the generated plan to a new briefing file for the agent to execute.
		// The turnID is a unique identifier for this specific generation step.
		bytes := make([]byte, 3)
		_, _ = rand.Read(bytes)
		turnID := "generated-plan-" + hex.EncodeToString(bytes)

		briefingFilePath, err = WriteBriefingFile(plan, job, generatedPlanContent, turnID)
		if err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			if uerr := updateJobFile(job); uerr != nil {
				e.ulog.Warn("Failed to persist job status").
					Field("job_id", job.ID).
					Err(uerr).
					Log(ctx)
			}
			return fmt.Errorf("failed to write generated plan briefing file: %w", err)
		}

		// Log briefing file creation with structured logging
		requestID, _ := ctx.Value(contextKey("request_id")).(string)
		e.ulog.Success("Generated plan briefing file created").
			Field("job_id", job.ID).
			Field("request_id", requestID).
			Field("plan_name", plan.Name).
			Field("job_file", job.FilePath).
			Field("turn_id", turnID).
			Field("briefing_file_path", briefingFilePath).
			Field("prompt_chars", len(generatedPlanContent)).
			Pretty(theme.IconSuccess + " Generated plan briefing: " + theme.DefaultTheme.Accent.Render(briefingFilePath)).
			Log(ctx)
	} else {
		// Gather context files (.grove/context, CLAUDE.md, etc.)
		contextFiles, err := e.gatherContextFiles(job, plan, workDir)
		if err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			if uerr := updateJobFile(job); uerr != nil {
				e.ulog.Warn("Failed to persist job status").
					Field("job_id", job.ID).
					Err(uerr).
					Log(ctx)
			}
			return fmt.Errorf("failed to gather context files: %w", err)
		}

		// Query memory database for related memories, bounded so an offline
		// embedding path can't stall the launch (see memoryPrefetchTimeout).
		e.ulog.Info("Fetching related memories").
			Field("job_id", job.ID).
			Pretty(theme.IconSearch + " Fetching related memories (bounded 15s)...").
			Log(ctx)
		memories := FetchRelatedMemoriesBounded(ctx, job)

		// Build the XML prompt and get the list of files to upload.
		// NOTE: interactive agents currently don't support separate file uploads, so filesToUpload is ignored.
		promptXML, _, err := BuildXMLPrompt(job, plan, workDir, contextFiles, memories)
		if err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			if uerr := updateJobFile(job); uerr != nil {
				e.ulog.Warn("Failed to persist job status").
					Field("job_id", job.ID).
					Err(uerr).
					Log(ctx)
			}
			e.ulog.Error("Failed to build prompt for job").
				Field("job_id", job.ID).
				Field("job_file", job.FilePath).
				Err(err).
				Pretty(" " + err.Error()).
				Log(ctx)
			return fmt.Errorf("failed to build agent XML prompt: %w", err)
		}

		// Write the briefing file for auditing (no turnID for interactive_agent jobs).
		briefingFilePath, err = WriteBriefingFile(plan, job, promptXML, "")
		if err != nil {
			e.ulog.Warn("Failed to write briefing file").Err(err).Log(ctx)
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to write briefing file: %w", err)
		}

		// Log briefing file creation with structured logging
		requestID, _ := ctx.Value(contextKey("request_id")).(string)
		e.ulog.Success("Interactive agent briefing file created").
			Field("job_id", job.ID).
			Field("request_id", requestID).
			Field("plan_name", plan.Name).
			Field("job_file", job.FilePath).
			Field("briefing_file_path", briefingFilePath).
			Field("prompt_chars", len(promptXML)).
			Pretty(theme.IconCode + "  Briefing file created at: " + theme.DefaultTheme.Accent.Render(briefingFilePath)).
			Log(ctx)
	}

	// --- Concept Gathering Logic ---
	if job.GatherConceptNotes || job.GatherConceptPlans {
		conceptContextFile, err := gatherConcepts(ctx, job, plan, workDir)
		if err != nil {
			requestID, _ := ctx.Value(contextKey("request_id")).(string)
			e.ulog.Warn("Failed to gather concepts").
				Err(err).
				Field("request_id", requestID).
				Field("job_id", job.ID).
				Log(ctx)
		} else if conceptContextFile != "" {
			e.ulog.Success("Aggregated concepts context created").
				Field("concept_context_file", conceptContextFile).
				Pretty(theme.IconSuccess + " Aggregated concepts context: " + theme.DefaultTheme.Accent.Render(conceptContextFile)).
				Log(ctx)
		}
	}

	// Determine agent target — must be resolved by the submission path
	// (CLI or TUI). The executor never checks env vars or daemon state.
	var provider InteractiveAgentProvider

	target := ""
	if plan.Orchestration != nil && plan.Orchestration.AgentTarget != "" {
		target = plan.Orchestration.AgentTarget
	}

	grovelogging.NewUnifiedLogger("flow.executor").Info("Executor routing agent job").
		Field("job_id", job.ID).
		Field("agent_target", target).
		Field("provider", spec.Name).
		StructuredOnly().Log(ctx)

	useNative := false
	switch target {
	case "native", "tuimux":
		useNative = true
	case "tmux":
		useNative = false
	default:
		return fmt.Errorf("agent_target not set: job submitted without routing context — this is a bug in the submission path (CLI or TUI should always tag jobs)")
	}

	if useNative {
		gp := NewGrovetermAgentProvider(spec, false, target)
		gp.agentEnv = flowCfg.AgentEnv
		provider = gp
	} else {
		// Fallback to the provider's legacy tmux-based launcher.
		provider = spec.newTmuxProvider(flowCfg.AgentEnv)
	}

	// Get agent args for the selected provider (with the claude bypass default).
	agentArgs := resolveProviderArgs(flowCfg, spec.Name)

	// Append per-job flags (model, effort) per the provider's spec; providers
	// without the corresponding flag reject a non-empty value.
	agentArgs, err = appendProviderJobArgs(spec, agentArgs, job)
	if err != nil {
		return err
	}
	// Record the model the agent will actually run with (for claude: its
	// default when none was passed) into the job frontmatter.
	if spec.BackfillJobModel != nil {
		spec.BackfillJobModel(job)
	}

	// Handle source_block reference if present
	// Resolve it before launching the agent so the agent has the content to work with
	if job.SourceBlock != "" {
		extractedContent, err := resolveSourceBlock(job.SourceBlock, plan)
		if err != nil {
			return fmt.Errorf("resolving source_block: %w", err)
		}
		// Update the job's PromptBody with the resolved content
		if job.PromptBody != "" {
			job.PromptBody = extractedContent + "\n\n" + job.PromptBody
		} else {
			job.PromptBody = extractedContent
		}
		// Clear the source_block field as it's now resolved
		job.SourceBlock = ""
		// Update the job file with the resolved content
		if err := updateJobFile(job); err != nil {
			return fmt.Errorf("updating job file with resolved source_block: %w", err)
		}
	}

	// Delegate to the selected provider with the briefing file path
	return provider.Launch(ctx, job, plan, workDir, agentArgs, briefingFilePath)
}

// determineWorkDir determines the working directory for a job based on its worktree configuration.
func (e *InteractiveAgentExecutor) determineWorkDir(ctx context.Context, job *Job, plan *Plan) (string, error) {
	// For jobs with worktrees, we need to prepare the worktree if it doesn't exist yet
	if job.Worktree != "" {
		gitRoot, err := GetProjectGitRoot(plan.Directory)
		if err != nil {
			return "", fmt.Errorf("could not find git root: %w", err)
		}

		// Shared detection resolves the owning repo and whether the worktree
		// already exists; we only prepare a missing one.
		ownerRoot, _, exists := resolveWorktreeForJob(gitRoot, job.Worktree)
		if !exists {
			opts := workspace.PrepareOptions{
				GitRoot:      ownerRoot,
				WorktreeName: job.Worktree,
				BranchName:   job.Worktree,
				PlanName:     plan.Name,
			}

			if plan.Config != nil && len(plan.Config.Repos) > 0 {
				opts.SiblingWorkspaces = plan.Config.Repos
			}
			// Layout is decided by ecosystem-ness, NOT the sibling list: an
			// anchored full-ecosystem worktree persists an empty repos: yet
			// lives in the XDG layout.
			opts.UseXDGWorktrees = workspaceIsEcosystem(ownerRoot)

			if _, err := workspace.Prepare(ctx, opts, CopyProjectFilesToWorktree); err != nil {
				return "", fmt.Errorf("failed to prepare host worktree: %w", err)
			}
		}
	}

	// Use the shared logic to determine the final working directory
	return DetermineWorkingDirectory(plan, job)
}

// generatePlanFromDependencies constructs a prompt from chat dependencies and calls an LLM to generate a plan.
func (e *InteractiveAgentExecutor) generatePlanFromDependencies(ctx context.Context, job *Job, plan *Plan) (string, error) {
	if len(job.Dependencies) == 0 {
		return "", fmt.Errorf("job with generate_plan_from has no dependencies")
	}

	// Assume the first dependency is the chat log to be summarized.
	chatDep := job.Dependencies[0]
	chatContentBytes, err := os.ReadFile(chatDep.FilePath)
	if err != nil {
		return "", fmt.Errorf("reading chat dependency file %s: %w", chatDep.FilePath, err)
	}
	_, chatBody, err := ParseFrontmatter(chatContentBytes)
	if err != nil {
		return "", fmt.Errorf("parsing frontmatter from chat dependency: %w", err)
	}

	// Load the agent-xml template for plan generation instructions.
	templateManager := NewTemplateManager()
	template, err := templateManager.FindTemplate("agent-xml")
	if err != nil {
		return "", fmt.Errorf("resolving agent-xml template: %w", err)
	}

	// Combine template prompt with the chat content.
	fullPrompt := fmt.Sprintf("%s\n\n## Chat Transcript\n\n%s", template.Prompt, string(chatBody))

	// Determine the model to use.
	effectiveModel := job.Model
	if effectiveModel == "" && plan.Config != nil {
		effectiveModel = plan.Config.Model
	}
	if effectiveModel == "" {
		effectiveModel = "gemini-2.5-flash" // Fallback
	}

	// Determine working directory for context discovery
	workDir, err := DetermineWorkingDirectory(plan, job)
	if err != nil {
		// Fallback to plan directory if working directory cannot be determined
		workDir = plan.Directory
	}

	// Make the LLM call.
	// Check if mocking is enabled - if so, always use llmClient regardless of model
	if os.Getenv("GROVE_MOCK_LLM_RESPONSE_FILE") != "" {
		opts := LLMOptions{Model: effectiveModel, WorkingDir: workDir}
		return e.llmClient.Complete(ctx, job, plan, fullPrompt, opts, io.Discard)
	}

	if strings.HasPrefix(effectiveModel, "gemini") {
		apiKey, _ := geminiconfig.ResolveAPIKey()
		opts := gemini.RequestOptions{
			Model:            effectiveModel,
			Prompt:           fullPrompt,
			WorkDir:          workDir, // Enable context file discovery
			SkipConfirmation: true,
			APIKey:           apiKey,
			Caller:           "grove-flow-generate-plan",
			JobID:            job.ID,
			PlanName:         plan.Name,
		}
		return e.geminiRunner.Run(ctx, opts)
	}

	// Fallback for other models.
	// Use io.Discard since this is for plan generation and the output isn't streamed
	opts := LLMOptions{Model: effectiveModel, WorkingDir: workDir}
	return e.llmClient.Complete(ctx, job, plan, fullPrompt, opts, io.Discard)
}

// gatherContextFiles collects context files (.grove/context, CLAUDE.md, etc.) for the job.
func (e *InteractiveAgentExecutor) gatherContextFiles(job *Job, plan *Plan, workDir string) ([]string, error) {
	var contextFiles []string

	// Scope to sub-project if job.Repository is set (for ecosystem worktrees)
	contextDir := ScopeToSubProject(workDir, job)

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

	return contextFiles, nil
}

// ResolveInteractiveAgentPane returns the tmux target pane string (session:window)
// for an interactive agent job, using the same resolution logic as send/capture.
func ResolveInteractiveAgentPane(plan *Plan, job *Job) (string, error) {
	workDir, err := DetermineWorkingDirectory(plan, job)
	if err != nil {
		return "", fmt.Errorf("could not determine working directory: %w", err)
	}

	projInfo, err := ResolveProjectForSessionNaming(workDir)
	if err != nil {
		return "", fmt.Errorf("failed to resolve project for session naming: %w", err)
	}

	sessionName := projInfo.Identifier("_")
	windowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)
	return fmt.Sprintf("%s:%s", sessionName, windowName), nil
}

// CaptureInteractiveAgentOutput captures the visible output from an interactive agent's pane.
// Tries the daemon's native agent pane capture API first (groveterm), falls back to tmux.
func CaptureInteractiveAgentOutput(plan *Plan, job *Job) (string, error) {
	logger := grovelogging.NewLogger("flow.orchestration.agent")
	logger.WithField("job_id", job.ID).Debug("CaptureOutput called")

	// Determine workDir upfront so we can scope the daemon connection correctly.
	workDir, err := DetermineWorkingDirectory(plan, job)
	if err != nil {
		return "", fmt.Errorf("could not determine working directory: %w", err)
	}

	// Try daemon API first. This works when a native groveterm pane is connected
	// OR when the agent runs as an out-of-process tuimux PTY — the daemon now has
	// a native PtyID capture tier, so a connected terminal is no longer required.
	ctx := context.Background()
	daemonClient := daemon.NewWithAutoStart()
	connected, _ := daemonClient.IsTerminalConnected(ctx)
	session, _ := daemonClient.GetSession(ctx, job.ID)
	hasPty := session != nil && session.PtyID != ""
	if connected || hasPty {
		result, err := daemonClient.CaptureAgentPane(ctx, job.ID)
		daemonClient.Close()
		if err == nil {
			logger.WithFields(map[string]interface{}{
				"tier":   "native",
				"job_id": job.ID,
				"bytes":  len(result),
			}).Debug("Native capture succeeded")
			return result, nil
		}
		logger.WithError(err).WithField("job_id", job.ID).Warn("Native capture failed, falling back to tmux")
	} else {
		daemonClient.Close()
	}

	projInfo, err := ResolveProjectForSessionNaming(workDir)
	if err != nil {
		return "", fmt.Errorf("failed to resolve project for session naming: %w", err)
	}

	sessionName := projInfo.Identifier("_")
	windowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)
	targetPane := fmt.Sprintf("%s:%s", sessionName, windowName)

	engine, err := mux.DetectMuxEngine(ctx)
	if err != nil {
		return "", fmt.Errorf("mux engine not available: %w", err)
	}

	result, err := engine.CapturePane(ctx, targetPane)
	if err != nil {
		return "", err
	}
	logger.WithFields(map[string]interface{}{
		"tier":        "tmux",
		"job_id":      job.ID,
		"tmux_target": targetPane,
		"bytes":       len(result),
	}).Debug("Tmux capture succeeded")
	return result, nil
}

// SendInputToInteractiveAgent sends input text to an interactive agent.
// Tries the daemon's native agent pane input API first (groveterm), falls back to tmux.
func SendInputToInteractiveAgent(plan *Plan, job *Job, input string) error {
	logger := grovelogging.NewLogger("flow.orchestration.agent")
	logger.WithFields(map[string]interface{}{
		"job_id":    job.ID,
		"input_len": len(input),
	}).Debug("SendInput called")

	ctx := context.Background()

	// Determine workDir upfront so we can scope the daemon connection correctly.
	workDir, err := DetermineWorkingDirectory(plan, job)
	if err != nil {
		return fmt.Errorf("could not determine working directory: %w", err)
	}

	// Try daemon API first — works when agent runs in a native groveterm pane.
	daemonClient := daemon.NewWithAutoStart()
	connected, _ := daemonClient.IsTerminalConnected(ctx)
	if connected {
		logger.WithFields(map[string]interface{}{
			"tier":   "native",
			"job_id": job.ID,
		}).Info("Dispatching input")
		// Build the payload with vim-mode handling and submit key.
		payload := buildAgentInputPayload(workDir, job.Provider, input)
		err := daemonClient.SendAgentInput(ctx, job.ID, payload)
		daemonClient.Close()
		if err == nil {
			logger.Debug("Native send succeeded")
			return nil
		}
		logger.WithError(err).WithField("job_id", job.ID).Warn("Native send failed, falling back to tmux")
	} else {
		daemonClient.Close()
	}

	// Fallback: tmux send-keys on the project's tmux session.

	projInfo, err := ResolveProjectForSessionNaming(workDir)
	if err != nil {
		return fmt.Errorf("failed to resolve project for session naming: %w", err)
	}

	sessionName := projInfo.Identifier("_")
	windowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)
	targetPane := fmt.Sprintf("%s:%s", sessionName, windowName)

	inputMode := resolveInputMode(workDir, job.Provider)

	if !connected {
		logger.WithFields(map[string]interface{}{
			"tier":        "tmux",
			"job_id":      job.ID,
			"tmux_target": targetPane,
		}).Info("Dispatching input")
	}

	engine, err := mux.DetectMuxEngine(ctx)
	if err != nil {
		return fmt.Errorf("mux engine not available: %w", err)
	}

	if inputMode == "vim" {
		if err := engine.SendKeys(ctx, targetPane, "Escape", "i", input); err != nil {
			return fmt.Errorf("failed to send input text: %w", err)
		}
	} else {
		if err := engine.SendKeys(ctx, targetPane, input); err != nil {
			return fmt.Errorf("failed to send input text: %w", err)
		}
	}

	if err := engine.SendKeys(ctx, targetPane, "C-m"); err != nil {
		return fmt.Errorf("failed to send submit key: %w", err)
	}

	logger.WithField("tmux_target", targetPane).Debug("Send succeeded")
	return nil
}

// buildAgentInputPayload constructs the input string with vim-mode handling and trailing CR.
// providerOverride is the job's per-job provider ("" = use the configured/global one).
func buildAgentInputPayload(workDir, providerOverride, input string) string {
	inputMode := resolveInputMode(workDir, providerOverride)
	if inputMode == "vim" {
		return "\x1bi" + input + "\r"
	}
	return input + "\r"
}

// resolveInputMode determines the input mode for the effective provider: the
// provider spec's default (claude: vim), overridable per provider via
// [flow.providers.<name>].input_mode. providerOverride is the job's per-job
// provider ("" = fall back to flow.interactive_provider / claude).
func resolveInputMode(workDir, providerOverride string) string {
	coreCfg, cfgErr := config.LoadFrom(workDir)
	if cfgErr != nil {
		coreCfg = &config.Config{}
	}
	var flowCfg FlowConfig
	if parsed, err := FlowConfigFrom(coreCfg); err != nil {
		log.WithError(err).Warn("Failed to parse flow configuration; using default input mode")
	} else {
		flowCfg = *parsed
	}

	providerName := providerOverride
	if providerName == "" {
		providerName = flowCfg.InteractiveProvider
	}
	if providerName == "" {
		providerName = defaultAgentProviderName
	}

	inputMode := "vim" // historical default for unknown providers
	if spec, ok := LookupAgentProvider(providerName); ok {
		inputMode = spec.DefaultInputMode
	}
	if providerCfg, ok := flowCfg.Providers[providerName]; ok && providerCfg.InputMode != "" {
		inputMode = providerCfg.InputMode
	}
	return inputMode
}

// SendInterruptToInteractiveAgent sends Ctrl+C to interrupt an interactive agent.
// Tries the daemon's native agent pane input API first (groveterm), falls back to tmux.
func SendInterruptToInteractiveAgent(plan *Plan, job *Job) error {
	logger := grovelogging.NewLogger("flow.orchestration.agent")
	logger.WithField("job_id", job.ID).Debug("SendInterrupt called")

	ctx := context.Background()

	// Determine workDir upfront so we can scope the daemon connection correctly.
	workDir, err := DetermineWorkingDirectory(plan, job)
	if err != nil {
		return fmt.Errorf("could not determine working directory: %w", err)
	}

	// Try daemon API first — send Ctrl+C via native pane.
	daemonClient := daemon.NewWithAutoStart()
	connected, _ := daemonClient.IsTerminalConnected(ctx)
	if connected {
		logger.WithFields(map[string]interface{}{
			"tier":   "native",
			"job_id": job.ID,
		}).Info("Dispatching interrupt")
		err := daemonClient.SendAgentInput(ctx, job.ID, "\x03")
		daemonClient.Close()
		if err == nil {
			logger.Debug("Native interrupt succeeded")
			return nil
		}
		logger.WithError(err).WithField("job_id", job.ID).Warn("Native interrupt failed, falling back to tmux")
	} else {
		daemonClient.Close()
	}

	projInfo, err := ResolveProjectForSessionNaming(workDir)
	if err != nil {
		return fmt.Errorf("failed to resolve project for session naming: %w", err)
	}

	sessionName := projInfo.Identifier("_")
	windowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)
	targetPane := fmt.Sprintf("%s:%s", sessionName, windowName)

	if !connected {
		logger.WithFields(map[string]interface{}{
			"tier":        "tmux",
			"job_id":      job.ID,
			"tmux_target": targetPane,
		}).Info("Dispatching interrupt")
	}

	engine, err := mux.DetectMuxEngine(ctx)
	if err != nil {
		return fmt.Errorf("mux engine not available: %w", err)
	}

	if err := engine.SendKeys(ctx, targetPane, "C-c"); err != nil {
		return err
	}
	logger.WithField("tmux_target", targetPane).Debug("Interrupt succeeded")
	return nil
}
