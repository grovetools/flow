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
func (e *HeadlessAgentExecutor) Execute(ctx context.Context, job *Job, plan *Plan) error {
	log.WithFields(map[string]interface{}{
		"job_id":    job.ID,
		"job_title": job.Title,
		"plan_name": plan.Name,
	}).Debug("[HEADLESS] Starting execution")

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

	log.WithField("job_id", job.ID).Debug("[HEADLESS] Job status updated to running")

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
		workDir, err = GetProjectGitRoot(plan.Directory)
		if err != nil {
			workDir = plan.Directory
			log.Warn("Not a git repository, using plan directory as working directory")
			prettyLog.WarnPrettyCtx(ctx, fmt.Sprintf("Not a git repository. Using plan directory: %s", workDir))
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
	log.WithField("job_id", job.ID).Debug("[HEADLESS] Starting agent execution")
	err = e.runAgentInWorktree(ctx, workDir, instructionPrompt, job, plan)
	if err != nil {
		execErr = fmt.Errorf("run agent: %w", err)
		log.WithFields(map[string]interface{}{
			"job_id": job.ID,
			"error":  err,
		}).Error("[HEADLESS] Agent execution failed")
	} else {
		log.WithField("job_id", job.ID).Debug("[HEADLESS] Agent execution completed successfully")
	}

	// After agent completes, archive its session artifacts
	log.WithField("job_id", job.ID).Debug("[HEADLESS] Archiving session artifacts")
	if err := ArchiveInteractiveSession(job, plan); err != nil {
		log.WithError(err).Warn("[HEADLESS] Failed to archive session artifacts for headless agent job")
	} else {
		log.WithField("job_id", job.ID).Debug("[HEADLESS] Session artifacts archived successfully")
	}

	// Append the formatted transcript using the generalized function
	log.WithField("job_id", job.ID).Debug("[HEADLESS] Appending formatted transcript")
	if err := AppendAgentTranscript(job, plan); err != nil {
		log.WithError(err).Warn("[HEADLESS] Failed to append transcript to job file")
	} else {
		log.WithField("job_id", job.ID).Debug("[HEADLESS] Formatted transcript appended successfully")
	}

	return execErr
}


// prepareWorktree ensures the worktree exists and is ready.
func (e *HeadlessAgentExecutor) prepareWorktree(ctx context.Context, job *Job, plan *Plan) (string, error) {
	if job.Worktree == "" {
		return "", fmt.Errorf("job %s has no worktree specified", job.ID)
	}

	gitRoot, err := GetProjectGitRoot(plan.Directory)
	if err != nil {
		gitRoot = plan.Directory
	}

	// Check if the worktree directory already exists. If so, skip preparation.
	// This prevents errors when multiple jobs in a plan share the same worktree.
	worktreePath := filepath.Join(gitRoot, ".grove-worktrees", job.Worktree)
	if _, err := os.Stat(worktreePath); err == nil {
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
	}).Debug("[HEADLESS] Running agent on host")

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
	}).Debug("[HEADLESS] Starting Claude CLI with environment variables")

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

	log.WithField("job_id", job.ID).Debug("[HEADLESS] Claude CLI execution completed")
	return nil
}

// RunAgent implements the AgentRunner interface.
func (r *defaultAgentRunner) RunAgent(ctx context.Context, worktree string, prompt string) error {
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

