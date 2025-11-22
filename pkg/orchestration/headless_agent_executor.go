package orchestration

import (
	"context"
	"fmt"
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
	return "agent"
}

// Execute runs an agent job in a worktree.
func (e *HeadlessAgentExecutor) Execute(ctx context.Context, job *Job, plan *Plan) error {
	// Create lock file with the current process's PID.
	if err := CreateLockFile(job.FilePath, os.Getpid()); err != nil {
		return fmt.Errorf("failed to create lock file: %w", err)
	}
	// Ensure lock file is removed when execution finishes.
	defer RemoveLockFile(job.FilePath)

	// Update job status to running
	job.Status = JobStatusRunning
	job.StartTime = time.Now()

	// Determine the working directory for the job
	var workDir string
	if job.Worktree != "" {
		var err error
		// Existing logic to prepare a git worktree
		workDir, err = e.prepareWorktree(ctx, job, plan)
		if err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("prepare worktree: %w", err)
		}
	} else {
		// NEW: No worktree specified, default to the git repository root.
		var err error
		workDir, err = GetGitRootSafe(plan.Directory)
		if err != nil {
			// Fallback to the plan's directory if not in a git repo
			workDir = plan.Directory
			fmt.Printf("Warning: not a git repository. Using plan directory as working directory: %s\n", workDir)
		}
	}

	// Scope to sub-project if job.Repository is set (for ecosystem worktrees)
	workDir = ScopeToSubProject(workDir, job)

	// Build agent prompt from sources
	prompt, err := buildPromptFromSources(job, plan)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("build prompt: %w", err)
	}

	// Execute agent in working directory context
	if err := e.runAgentInWorktree(ctx, workDir, prompt, job, plan); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("run agent: %w", err)
	}

	// Automatically update context within the working directory
	if os.Getenv("GROVE_DEBUG") != "" {
		fmt.Println("Checking for context update in working directory...")
	}
	ctxMgr := grovecontext.NewManager(workDir)
	
	// Check if job has a custom rules file specified
	if job.RulesFile != "" {
		// Resolve the custom rules file path relative to the plan directory
		rulesFilePath := filepath.Join(plan.Directory, job.RulesFile)
		
		fmt.Printf("Using job-specific context from: %s\n", rulesFilePath)
		
		// Generate context using the custom rules file
		if err := ctxMgr.GenerateContextFromRulesFile(rulesFilePath, true); err != nil {
			// Log a warning, but don't fail the job for a context update failure.
			fmt.Printf("Warning: failed to generate job-specific context: %v\n", err)
		} else {
			fmt.Println("✓ Context updated successfully with job-specific rules.")
			
			// Check token count after successful context generation
			// Read the files list that was just generated
			files, _ := ctxMgr.ReadFilesList(grovecontext.FilesListFile)
			stats, err := ctxMgr.GetStats("agent", files, 10)
			if err != nil {
				fmt.Printf("Warning: failed to get context stats: %v\n", err)
			} else if stats.TotalTokens > 500000 {
				// Fail the job if context exceeds 500k tokens
				job.Status = JobStatusFailed
				job.EndTime = time.Now()
				return fmt.Errorf("context size exceeds limit: %d tokens (max 500,000 tokens)", stats.TotalTokens)
			} else {
				fmt.Printf("Context size: %d tokens\n", stats.TotalTokens)
			}
		}
	} else {
		// Check for default .grove/rules file
		rulesPath := filepath.Join(workDir, ".grove", "rules")
		
		if _, err := os.Stat(rulesPath); err == nil {
			absRulesPath, _ := filepath.Abs(rulesPath)
			if os.Getenv("GROVE_DEBUG") != "" {
				fmt.Printf("Found context rules file, updating context...\n")
				fmt.Printf("  Rules File: %s\n", absRulesPath)
			}
			if err := ctxMgr.UpdateFromRules(); err != nil {
				// Log a warning, but don't fail the job for a context update failure.
				fmt.Printf("Warning: failed to update context file list in worktree: %v\n", err)
			} else {
				if err := ctxMgr.GenerateContext(true); err != nil {
					fmt.Printf("Warning: failed to generate context file in worktree: %v\n", err)
				} else {
					fmt.Println("✓ Context updated successfully in worktree.")

					// Check token count after successful context generation
					// Read the files list that was just generated
					files, _ := ctxMgr.ReadFilesList(grovecontext.FilesListFile)
					stats, err := ctxMgr.GetStats("agent", files, 10)
					if err != nil {
						fmt.Printf("Warning: failed to get context stats: %v\n", err)
					} else if stats.TotalTokens > 500000 {
						// Fail the job if context exceeds 500k tokens
						job.Status = JobStatusFailed
						job.EndTime = time.Now()
						return fmt.Errorf("context size exceeds limit: %d tokens (max 500,000 tokens)", stats.TotalTokens)
					} else {
						fmt.Printf("Context size: %d tokens\n", stats.TotalTokens)
					}
				}
			}
		} else {
			fmt.Println("No .grove/rules file found in worktree, skipping context update.")
		}
	}

	// Update job status on completion
	job.Status = JobStatusCompleted
	job.EndTime = time.Now()

	return nil
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
	// Set up output handling
	logDir := ResolveLogDirectory(plan, job)
	logFile := filepath.Join(logDir, fmt.Sprintf("%s.log", job.ID))
	if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}

	log, err := os.Create(logFile)
	if err != nil {
		return fmt.Errorf("create log file: %w", err)
	}
	defer log.Close()

	// Load grove config to get agent configuration
	coreCfg, err := config.LoadFrom(".")
	if err != nil {
		coreCfg = &config.Config{}
		fmt.Printf("Warning: could not load grove.yml for agent execution: %v\n", err)
	}

	// Extract agent args from config
	type agentConfig struct {
		Args []string `yaml:"args"`
	}
	var agentCfg agentConfig
	coreCfg.UnmarshalExtension("agent", &agentCfg)

	// Git root is no longer needed since we removed container operations

	// Always run in host mode - no container dependencies
	fmt.Fprintf(os.Stdout, "Running job in host mode\n")
	return e.runOnHost(ctx, worktreePath, prompt, job, plan, log, agentCfg.Args)
}


// runOnHost executes the agent directly on the host machine
func (e *HeadlessAgentExecutor) runOnHost(ctx context.Context, worktreePath string, prompt string, job *Job, plan *Plan, log *os.File, agentArgs []string) error {
	// Change to the worktree directory
	originalDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}
	defer os.Chdir(originalDir)

	if err := os.Chdir(worktreePath); err != nil {
		return fmt.Errorf("failed to change to worktree directory: %w", err)
	}

	// Prepare the claude command
	args := []string{"--dangerously-skip-permissions"}
	if agentArgs != nil {
		args = append(args, agentArgs...)
	}

	// Create the command
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = worktreePath
	cmd.Stdin = strings.NewReader(prompt)

	// Capture output
	output, err := cmd.CombinedOutput()

	// Write output to log file regardless of success or failure
	if len(output) > 0 {
		if _, writeErr := log.Write(output); writeErr != nil {
			// Log this, but don't fail the job over it
			fmt.Printf("Warning: failed to write agent output to log: %v\n", writeErr)
		}
	}

	if err != nil {
		return fmt.Errorf("agent execution failed: %w", err)
	}

	// Handle output based on configuration (commit, etc.)
	if job.Output.Type == "commit" && job.Output.Commit.Enabled {
		// Create commit in worktree
		commitCmd := exec.CommandContext(ctx, "git", "add", "-A")
		commitCmd.Dir = worktreePath
		if err := commitCmd.Run(); err != nil {
			return fmt.Errorf("git add: %w", err)
		}

		commitMsg := job.Output.Commit.Message
		if commitMsg == "" {
			commitMsg = fmt.Sprintf("Agent execution for job %s", job.ID)
		}

		commitCmd = exec.CommandContext(ctx, "git", "commit", "-m", commitMsg)
		commitCmd.Dir = worktreePath
		if err := commitCmd.Run(); err != nil {
			// No changes to commit is not an error
			if !strings.Contains(err.Error(), "nothing to commit") {
				return fmt.Errorf("git commit: %w", err)
			}
		}
	}

	return nil
}

// RunAgent implements the AgentRunner interface.
func (r *defaultAgentRunner) RunAgent(ctx context.Context, worktree string, prompt string) error {
	// This is implemented in runAgentInWorktree above
	// This method exists for testing/mocking purposes
	return nil
}

// buildPromptFromSources builds a prompt that instructs the agent to read files.
func buildPromptFromSources(job *Job, plan *Plan) (string, error) {
	var promptFiles []string
	var systemMessage strings.Builder

	// If a template is specified, use the reference-based prompt structure
	if job.Template != "" {
		// Reference-based prompt - load template
		templateManager := NewTemplateManager()
		template, err := templateManager.FindTemplate(job.Template)
		if err != nil {
			return "", fmt.Errorf("resolving template %s: %w", job.Template, err)
		}

		// Start with template content
		systemMessage.WriteString(template.Prompt)
		systemMessage.WriteString("\n\n")
		systemMessage.WriteString("The following files are relevant to your task. Please read their contents before proceeding:\n\n")

		// Get project root for resolving paths - uses workspace discovery with fallbacks
		projectRoot := GetProjectRootSafe(".")

		// Collect paths from prompt sources
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
			if _, err := os.Stat(sourcePath); err == nil {
				promptFiles = append(promptFiles, sourcePath)
			} else {
				// Try alternative resolution strategies
				sourcePath, err = ResolvePromptSource(source, plan)
				if err == nil {
					promptFiles = append(promptFiles, sourcePath)
				}
			}
		}
	} else {
		// Traditional prompt assembly
		systemMessage.WriteString("You are an expert software developer AI assistant.\n")
		systemMessage.WriteString("You have access to a file system containing the project code.\n")
		systemMessage.WriteString("The following files are relevant to your task. Please read their contents before proceeding:\n\n")

		// Collect paths from prompt sources
		for _, source := range job.PromptSource {
			sourcePath := filepath.Join(plan.Directory, source)
			// Check if the file exists to provide a clean list
			if _, err := os.Stat(sourcePath); err == nil {
				promptFiles = append(promptFiles, sourcePath)
			}
		}
	}

	for _, path := range promptFiles {
		systemMessage.WriteString(fmt.Sprintf("- %s\n", path))
	}

	systemMessage.WriteString("\n---\n\n")
	systemMessage.WriteString("## Task\n\n")
	systemMessage.WriteString(job.PromptBody)

	return systemMessage.String(), nil
}

