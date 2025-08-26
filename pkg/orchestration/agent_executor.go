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
	"github.com/mattsolo1/grove-core/docker"
	"github.com/mattsolo1/grove-core/git"
)

// AgentRunner defines the interface for running agents.
type AgentRunner interface {
	RunAgent(ctx context.Context, worktree string, prompt string) error
}

// AgentExecutor executes agent jobs in isolated git worktrees.
type AgentExecutor struct {
	llmClient       LLMClient
	config          *ExecutorConfig
	worktreeManager *git.WorktreeManager
	agentRunner     AgentRunner
	dockerClient    docker.Client
}

// defaultAgentRunner implements AgentRunner using grove agent subprocess.
type defaultAgentRunner struct {
	config       *ExecutorConfig
	dockerClient docker.Client
}

// NewAgentExecutor creates a new agent executor.
func NewAgentExecutor(llmClient LLMClient, config *ExecutorConfig, dockerClient docker.Client) *AgentExecutor {
	if config == nil {
		config = &ExecutorConfig{
			MaxPromptLength: 1000000,
			Timeout:         30 * time.Minute,
			RetryCount:      1,
			Model:           "default",
		}
	}

	return &AgentExecutor{
		llmClient:       llmClient,
		config:          config,
		worktreeManager: git.NewWorktreeManager(),
		agentRunner:     &defaultAgentRunner{config: config, dockerClient: dockerClient},
		dockerClient:    dockerClient,
	}
}

// Name returns the executor name.
func (e *AgentExecutor) Name() string {
	return "agent"
}

// Execute runs an agent job in a worktree.
func (e *AgentExecutor) Execute(ctx context.Context, job *Job, plan *Plan) error {
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

	// Automatically update context within the working directory if .grove/rules exists
	if os.Getenv("GROVE_DEBUG") != "" {
		fmt.Println("Checking for context update in working directory...")
	}
	ctxMgr := grovecontext.NewManager(workDir)
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

	// Update job status on completion
	job.Status = JobStatusCompleted
	job.EndTime = time.Now()

	return nil
}

// getTargetContainer determines the target container for the job.
func getTargetContainer(job *Job, plan *Plan) string {
	if job.TargetAgentContainer != "" {
		return job.TargetAgentContainer
	}
	if plan.Config != nil && plan.Config.TargetAgentContainer != "" {
		return plan.Config.TargetAgentContainer
	}
	if plan.Orchestration != nil {
		return plan.Orchestration.TargetAgentContainer
	}
	return ""
}

// prepareWorktree ensures the worktree exists and is ready.
func (e *AgentExecutor) prepareWorktree(ctx context.Context, job *Job, plan *Plan) (string, error) {
	if job.Worktree == "" {
		return "", fmt.Errorf("job %s has no worktree specified", job.ID)
	}

	// Get the REAL project root BEFORE changing context
	projectRoot, err := GetProjectRoot()
	if err != nil {
		// Log a warning but don't fail, symlinking is a convenience
		fmt.Printf("Warning: could not find project root for template symlinking: %v\n", err)
		projectRoot = ""
	}

	// Get git root for worktree creation
	gitRoot, err := GetGitRootSafe(plan.Directory)
	if err != nil {
		// Fallback to plan directory if not in a git repo
		gitRoot = plan.Directory
	}

	// Use the shared method to get or prepare the worktree at the git root
	worktreePath, err := e.worktreeManager.GetOrPrepareWorktree(ctx, gitRoot, job.Worktree, "")
	if err != nil {
		return "", err
	}

	// Symlink templates using the centralized function
	if err := SymlinkTemplates(worktreePath, projectRoot, nil); err != nil {
		fmt.Printf("Warning: failed to symlink templates: %v\n", err)
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

	// Set up Go workspace if this is a Go project
	if err := SetupGoWorkspaceForWorktree(worktreePath, gitRoot); err != nil {
		// Log a warning but don't fail the job, as this is a convenience feature
		fmt.Printf("Warning: failed to setup Go workspace in worktree: %v\n", err)
	}

	// Automatically initialize state within the new worktree for a better UX.
	groveDir := filepath.Join(worktreePath, ".grove")
	if err := os.MkdirAll(groveDir, 0755); err != nil {
		// Log a warning but don't fail the job, as this is a convenience feature.
		fmt.Printf("Warning: failed to create .grove directory in worktree: %v\n", err)
	} else {
		// For the active_plan value, store just the plan name (not the full path)
		// This allows 'flow plan status' to work correctly from within the worktree
		planName := filepath.Base(plan.Directory)
		stateContent := fmt.Sprintf("active_plan: %s\n", planName)
		statePath := filepath.Join(groveDir, "state.yml")
		// This is a best-effort attempt; failure should not stop the job.
		_ = os.WriteFile(statePath, []byte(stateContent), 0644)
	}

	return worktreePath, nil
}

// runAgentInWorktree executes the agent in the worktree context.
func (e *AgentExecutor) runAgentInWorktree(ctx context.Context, worktreePath string, prompt string, job *Job, plan *Plan) error {
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

	// Load grove config to check mount_workspace_at_host_path setting
	coreCfg, err := config.LoadFrom(".") // Use grove-core's loader
	if err != nil {
		// Proceed with default behavior if config can't be loaded
		coreCfg = &config.Config{}
		fmt.Printf("Warning: could not load grove.yml for agent execution: %v\n", err)
	}

	// Get git root for targeted mode
	// First try from the plan directory
	var gitRoot string
	if coreCfg.Agent.UseSuperprojectRoot {
		gitRoot, err = git.GetSuperprojectRoot(plan.Directory)
		if err != nil {
			// If that fails (e.g., when using plans_directory), try from current working directory
			cwd, _ := os.Getwd()
			gitRoot, err = git.GetSuperprojectRoot(cwd)
			if err != nil {
				return fmt.Errorf("could not find superproject root from plan directory or current directory: %w", err)
			}
		}
	} else {
		gitRoot, err = git.GetGitRoot(plan.Directory)
		if err != nil {
			// If that fails (e.g., when using plans_directory), try from current working directory
			cwd, _ := os.Getwd()
			gitRoot, err = git.GetGitRoot(cwd)
			if err != nil {
				return fmt.Errorf("could not find git root from plan directory or current directory: %w", err)
			}
		}
	}

	targetContainer := getTargetContainer(job, plan)

	// Determine execution mode based on container presence
	if targetContainer != "" {
		// Container mode - existing behavior
		fmt.Fprintf(os.Stdout, "Running job in targeted agent container: %s\n", targetContainer)
		return e.runInContainer(ctx, worktreePath, prompt, job, plan, targetContainer, log, coreCfg, gitRoot)
	} else {
		// Host mode - run directly on the host
		fmt.Fprintf(os.Stdout, "Running job in host mode (no container)\n")
		return e.runOnHost(ctx, worktreePath, prompt, job, plan, log, coreCfg)
	}
}

// runInContainer executes the agent in a Docker container
func (e *AgentExecutor) runInContainer(ctx context.Context, worktreePath string, prompt string, job *Job, plan *Plan, targetContainer string, log *os.File, coreCfg *config.Config, gitRoot string) error {
	// Get repo name from git root
	repoName := filepath.Base(gitRoot)

	// Convert host worktree path to container path
	// The container mounts the git root at its host path.
	relPath, err := filepath.Rel(gitRoot, worktreePath)
	if err != nil {
		return fmt.Errorf("failed to get relative worktree path: %w", err)
	}

	var containerWorkDir string
	if coreCfg.Agent.MountWorkspaceAtHostPath {
		// Path inside container matches host path
		containerWorkDir = filepath.Join(gitRoot, relPath)
	} else {
		// Default behavior: path is under /workspace
		containerWorkDir = fmt.Sprintf("/workspace/%s/%s", repoName, relPath)
	}

	// For non-interactive orchestration, we need to skip permission prompts
	shellCommand := fmt.Sprintf("cd %s && claude --dangerously-skip-permissions", containerWorkDir)

	// Execute the command using the Docker client
	stdout, stderr, err := e.dockerClient.ExecInContainer(ctx, targetContainer, []string{"bash", "-c", shellCommand}, strings.NewReader(prompt))
	if err != nil {
		return fmt.Errorf("agent execution failed: %w", err)
	}

	// Write output to both log file and console
	if stdout != "" {
		fmt.Print(stdout)
		if _, writeErr := log.WriteString(stdout); writeErr != nil {
			fmt.Printf("Warning: failed to write stdout to log: %v\n", writeErr)
		}
	}

	if stderr != "" {
		fmt.Fprint(os.Stderr, stderr)
		if _, writeErr := log.WriteString(stderr); writeErr != nil {
			fmt.Printf("Warning: failed to write stderr to log: %v\n", writeErr)
		}
	}

	// Handle output based on configuration
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

// runOnHost executes the agent directly on the host machine
func (e *AgentExecutor) runOnHost(ctx context.Context, worktreePath string, prompt string, job *Job, plan *Plan, log *os.File, coreCfg *config.Config) error {
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
	if coreCfg.Agent.Args != nil {
		args = append(args, coreCfg.Agent.Args...)
	}

	// Create the command
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = worktreePath
	cmd.Stdin = strings.NewReader(prompt)

	// Capture output
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Write any output we got even if there was an error
		if len(output) > 0 {
			fmt.Print(string(output))
			if _, writeErr := log.Write(output); writeErr != nil {
				fmt.Printf("Warning: failed to write output to log: %v\n", writeErr)
			}
		}
		return fmt.Errorf("agent execution failed: %w", err)
	}

	// Write output to both log file and console
	fmt.Print(string(output))
	if _, writeErr := log.Write(output); writeErr != nil {
		fmt.Printf("Warning: failed to write output to log: %v\n", writeErr)
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

		// Get project root for resolving paths
		projectRoot, err := GetProjectRoot()
		if err != nil {
			return "", fmt.Errorf("failed to get project root: %w", err)
		}

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

