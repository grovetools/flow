package orchestration

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	grovecontext "github.com/mattsolo1/grove-context/pkg/context"
	"github.com/mattsolo1/grove-core/config"
	"github.com/mattsolo1/grove-core/pkg/workspace"
)

// AgentRunner defines the interface for running agents.
type AgentRunner interface {
	RunAgent(ctx context.Context, worktree string, prompt string) error
}

// HeadlessAgentExecutor executes headless agent jobs in isolated git worktrees.
type HeadlessAgentExecutor struct {
	llmClient   LLMClient
	config      *ExecutorConfig
	agentRunner AgentRunner
}

// defaultAgentRunner implements AgentRunner using grove agent subprocess.
type defaultAgentRunner struct {
	config *ExecutorConfig
}

// NewHeadlessAgentExecutor creates a new headless agent executor.
func NewHeadlessAgentExecutor(llmClient LLMClient, config *ExecutorConfig) *HeadlessAgentExecutor {
	if config == nil {
		config = &ExecutorConfig{
			MaxPromptLength: 1000000,
			Timeout:         30 * time.Minute,
			RetryCount:      1,
			Model:           "default",
		}
	}

	return &HeadlessAgentExecutor{
		llmClient:   llmClient,
		config:      config,
		agentRunner: &defaultAgentRunner{config: config},
	}
}

// Name returns the executor name.
func (e *HeadlessAgentExecutor) Name() string {
	return "headless_agent"
}

// Execute runs an agent job in a worktree.
func (e *HeadlessAgentExecutor) Execute(ctx context.Context, job *Job, plan *Plan, output io.Writer) error {
	log.WithFields(map[string]interface{}{
		"job_id":    job.ID,
		"job_title": job.Title,
		"plan_name": plan.Name,
	}).Info("[HEADLESS] Starting execution")

	persister := NewStatePersister()

	// Create lock file with the current process's PID.
	if err := CreateLockFile(job.FilePath, os.Getpid()); err != nil {
		return fmt.Errorf("failed to create lock file: %w", err)
	}
	// Ensure lock file is removed when execution finishes.
	defer RemoveLockFile(job.FilePath)

	// Update job status to running
	job.StartTime = time.Now()
	if err := job.UpdateStatus(persister, JobStatusRunning); err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}

	log.WithField("job_id", job.ID).Info("[HEADLESS] Job status updated to running")

	var execErr error

	// Defer final status update
	defer func() {
		finalStatus := JobStatusCompleted
		if execErr != nil {
			finalStatus = JobStatusFailed
		}
		job.EndTime = time.Now()
		job.UpdateStatus(persister, finalStatus)
	}()

	// Determine the working directory for the job
	var workDir string
	if job.Worktree != "" {
		var err error
		workDir, err = e.prepareWorktree(ctx, job, plan)
		if err != nil {
			execErr = fmt.Errorf("prepare worktree: %w", err)
			return execErr
		}
	} else {
		var err error
		workDir, err = GetGitRootSafe(plan.Directory)
		if err != nil {
			workDir = plan.Directory
			log.Warn("Not a git repository, using plan directory as working directory")
			prettyLog.WarnPretty(fmt.Sprintf("Not a git repository. Using plan directory: %s", workDir))
		}
	}

	// Scope to sub-project if job.Repository is set
	workDir = ScopeToSubProject(workDir, job)

	// Gather context files (.grove/context, CLAUDE.md, etc.)
	contextFiles := e.gatherContextFiles(job, plan, workDir)

	// Build the XML prompt
	promptXML, _, err := BuildXMLPrompt(job, plan, workDir, contextFiles)
	if err != nil {
		execErr = fmt.Errorf("building XML prompt: %w", err)
		return execErr
	}

	// Write the briefing file for auditing
	briefingFilePath, err := WriteBriefingFile(plan, job, promptXML, "")
	if err != nil {
		log.WithError(err).Warn("Failed to write briefing file")
		execErr = fmt.Errorf("writing briefing file: %w", err)
		return execErr
	}

	// Create instruction to read the briefing file (like interactive_agent does)
	instructionPrompt := fmt.Sprintf("Read the briefing file at '%s' and execute the task.", briefingFilePath)

	// Execute agent with the instruction to read the briefing file
	log.WithField("job_id", job.ID).Info("[HEADLESS] Starting agent execution")
	err = e.runAgentInWorktree(ctx, workDir, instructionPrompt, job, plan)
	if err != nil {
		execErr = fmt.Errorf("run agent: %w", err)
		log.WithFields(map[string]interface{}{
			"job_id": job.ID,
			"error":  err,
		}).Error("[HEADLESS] Agent execution failed")
	} else {
		log.WithField("job_id", job.ID).Info("[HEADLESS] Agent execution completed successfully")
	}

	// After agent completes, archive its session artifacts
	log.WithField("job_id", job.ID).Info("[HEADLESS] Archiving session artifacts")
	if err := ArchiveInteractiveSession(job, plan); err != nil {
		log.WithError(err).Warn("[HEADLESS] Failed to archive session artifacts for headless agent job")
	} else {
		log.WithField("job_id", job.ID).Info("[HEADLESS] Session artifacts archived successfully")
	}

	// Append the formatted transcript using the generalized function
	log.WithField("job_id", job.ID).Info("[HEADLESS] Appending formatted transcript")
	if err := AppendAgentTranscript(job, plan); err != nil {
		log.WithError(err).Warn("[HEADLESS] Failed to append transcript to job file")
	} else {
		log.WithField("job_id", job.ID).Info("[HEADLESS] Formatted transcript appended successfully")
	}

	// Regenerate context
	if err := e.regenerateContextInWorktree(workDir, "headless_agent", job, plan); err != nil {
		log.WithError(err).Warn("Failed to regenerate context")
	}

	return execErr
}


// prepareWorktree ensures the worktree exists and is ready.
func (e *HeadlessAgentExecutor) prepareWorktree(ctx context.Context, job *Job, plan *Plan) (string, error) {
	if job.Worktree == "" {
		return "", fmt.Errorf("job %s has no worktree specified", job.ID)
	}

	gitRoot, err := GetGitRootSafe(plan.Directory)
	if err != nil {
		gitRoot = plan.Directory
	}

	// Check if we're already in the target worktree
	worktreePath := filepath.Join(gitRoot, ".grove-worktrees", job.Worktree)
	currentDir, err := os.Getwd()
	if err == nil && strings.HasPrefix(currentDir, worktreePath) {
		// Already in the target worktree, just return the path
		fmt.Printf("Already in worktree '%s', skipping preparation\n", job.Worktree)
		return worktreePath, nil
	}

	// The new logic:
	opts := workspace.PrepareOptions{
		GitRoot:      gitRoot,
		WorktreeName: job.Worktree,
		BranchName:   job.Worktree, // Convention: branch name matches worktree name
		PlanName:     plan.Name,
	}

	if plan.Config != nil && len(plan.Config.Repos) > 0 {
		opts.Repos = plan.Config.Repos
	}

	return workspace.Prepare(ctx, opts, CopyProjectFilesToWorktree)
}

// runAgentInWorktree executes the agent in the worktree context.
func (e *HeadlessAgentExecutor) runAgentInWorktree(ctx context.Context, worktreePath string, prompt string, job *Job, plan *Plan) error {
	coreCfg, err := config.LoadFrom(".")
	if err != nil {
		coreCfg = &config.Config{}
	}

	type agentConfig struct {
		Args []string `yaml:"args"`
	}
	var agentCfg agentConfig
	coreCfg.UnmarshalExtension("agent", &agentCfg)

	return e.runOnHost(ctx, worktreePath, prompt, job, plan, agentCfg.Args)
}


// runOnHost executes the agent directly on the host machine.
func (e *HeadlessAgentExecutor) runOnHost(ctx context.Context, worktreePath string, prompt string, job *Job, plan *Plan, agentArgs []string) error {
	log.WithFields(map[string]interface{}{
		"job_id":     job.ID,
		"worktree":   worktreePath,
		"agent_args": agentArgs,
	}).Info("[HEADLESS] Running agent on host")

	originalDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}
	defer os.Chdir(originalDir)

	if err := os.Chdir(worktreePath); err != nil {
		return fmt.Errorf("failed to change to worktree directory: %w", err)
	}

	args := []string{"--dangerously-skip-permissions"}
	if agentArgs != nil {
		args = append(args, agentArgs...)
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = worktreePath
	cmd.Stdin = strings.NewReader(prompt)

	// Set environment variables to enable grove-hooks integration for session registration.
	escapedTitle := "'" + strings.ReplaceAll(job.Title, "'", "'\\''") + "'"
	cmd.Env = append(os.Environ(),
		"GROVE_FLOW_JOB_ID="+job.ID,
		"GROVE_FLOW_JOB_PATH="+job.FilePath,
		"GROVE_FLOW_PLAN_NAME="+plan.Name,
		"GROVE_FLOW_JOB_TITLE="+escapedTitle,
	)

	log.WithFields(map[string]interface{}{
		"job_id": job.ID,
		"env": map[string]string{
			"GROVE_FLOW_JOB_ID":    job.ID,
			"GROVE_FLOW_JOB_PATH":  job.FilePath,
			"GROVE_FLOW_PLAN_NAME": plan.Name,
			"GROVE_FLOW_JOB_TITLE": escapedTitle,
		},
	}).Info("[HEADLESS] Starting Claude CLI with environment variables")

	// We use cmd.Run() and don't capture output. The agent process itself handles logging.
	// We also redirect stdout/stderr to /dev/null to prevent cluttering the main process output.
	// The real logs are accessed via `aglogs`.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Run(); err != nil {
		log.WithFields(map[string]interface{}{
			"job_id": job.ID,
			"error":  err,
		}).Error("[HEADLESS] Claude CLI execution failed")
		return fmt.Errorf("agent execution failed: %w", err)
	}

	log.WithField("job_id", job.ID).Info("[HEADLESS] Claude CLI execution completed")
	return nil
}

// RunAgent implements the AgentRunner interface.
func (r *defaultAgentRunner) RunAgent(ctx context.Context, worktree string, prompt string) error {
	return nil
}


// regenerateContextInWorktree regenerates the context within a worktree.
func (e *HeadlessAgentExecutor) regenerateContextInWorktree(worktreePath string, jobType string, job *Job, plan *Plan) error {
	log.WithField("job_type", jobType).Info("Checking context in worktree")
	prettyLog.InfoPretty(fmt.Sprintf("Checking context in worktree for %s job...", jobType))

	contextDir := ScopeToSubProject(worktreePath, job)
	if contextDir != worktreePath {
		log.WithField("context_dir", contextDir).Info("Scoping context generation to sub-project")
		prettyLog.InfoPretty(fmt.Sprintf("Scoping context to sub-project: %s", job.Repository))
	}

	ctxMgr := grovecontext.NewManager(contextDir)

	if job != nil && job.RulesFile != "" {
		rulesFilePath := filepath.Join(plan.Directory, job.RulesFile)
		log.WithField("rules_file", rulesFilePath).Info("Using job-specific context")
		prettyLog.InfoPretty(fmt.Sprintf("Using job-specific context from: %s", rulesFilePath))

		if err := ctxMgr.GenerateContextFromRulesFile(rulesFilePath, true); err != nil {
			return fmt.Errorf("failed to generate job-specific context: %w", err)
		}
		return e.displayContextInfo(contextDir)
	}

	rulesPath := filepath.Join(contextDir, ".grove", "rules")
	if _, err := os.Stat(rulesPath); err != nil {
		if os.IsNotExist(err) {
			return e.displayContextInfo(contextDir)
		}
		return fmt.Errorf("checking .grove/rules: %w", err)
	}

	if err := ctxMgr.UpdateFromRules(); err != nil {
		return fmt.Errorf("update context from rules: %w", err)
	}

	if err := ctxMgr.GenerateContext(true); err != nil {
		return fmt.Errorf("generate context: %w", err)
	}

	return e.displayContextInfo(contextDir)
}

// displayContextInfo displays information about available context files
func (e *HeadlessAgentExecutor) displayContextInfo(worktreePath string) error {
	var contextFiles []string
	var totalSize int64

	groveContextPath := filepath.Join(worktreePath, ".grove", "context")
	if info, err := os.Stat(groveContextPath); err == nil && !info.IsDir() {
		contextFiles = append(contextFiles, groveContextPath)
		totalSize += info.Size()
	}

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

// gatherContextFiles collects context files (.grove/context, CLAUDE.md, etc.) for the job.
func (e *HeadlessAgentExecutor) gatherContextFiles(job *Job, plan *Plan, workDir string) []string {
	var contextFiles []string

	// Scope to sub-project if job.Repository is set (for ecosystem worktrees)
	contextDir := ScopeToSubProject(workDir, job)

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

	return contextFiles
}

