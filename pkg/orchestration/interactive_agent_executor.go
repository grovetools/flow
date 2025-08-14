package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattsolo1/grove-core/config"
	"github.com/mattsolo1/grove-core/docker"
	"github.com/mattsolo1/grove-core/git"
	"github.com/mattsolo1/grove-tmux/pkg/tmux"
)

// InteractiveAgentExecutor executes interactive agent jobs in tmux sessions.
type InteractiveAgentExecutor struct {
	dockerClient    docker.Client
	skipInteractive bool
}

// NewInteractiveAgentExecutor creates a new interactive agent executor.
func NewInteractiveAgentExecutor(dockerClient docker.Client, skipInteractive bool) *InteractiveAgentExecutor {
	return &InteractiveAgentExecutor{
		dockerClient:    dockerClient,
		skipInteractive: skipInteractive,
	}
}

// Name returns the executor name.
func (e *InteractiveAgentExecutor) Name() string {
	return "interactive_agent"
}

// Execute runs an interactive agent job in a tmux session and blocks until completion.
func (e *InteractiveAgentExecutor) Execute(ctx context.Context, job *Job, plan *Plan) error {
	// Check if interactive jobs should be skipped
	if e.skipInteractive {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("interactive agent job skipped due to --skip-interactive flag")
	}

	// Update job status to running
	job.Status = JobStatusRunning
	job.StartTime = time.Now()

	// Get the container from job or plan orchestration config
	container := job.TargetAgentContainer
	if container == "" && plan.Orchestration != nil {
		container = plan.Orchestration.TargetAgentContainer
	}
	if container == "" {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("no target agent container specified")
	}

	// Verify container is running (skip if dockerClient is nil for testing)
	skipDockerCheck := os.Getenv("GROVE_FLOW_SKIP_DOCKER_CHECK") == "true"
	if e.dockerClient != nil && !skipDockerCheck && !e.dockerClient.IsContainerRunning(ctx, container) {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("container '%s' is not running", container)
	}

	// Prepare worktree
	var worktreePath string
	if job.Worktree != "" {
		wm := git.NewWorktreeManager()
		gitRoot, err := GetGitRootSafe(plan.Directory)
		if err != nil {
			gitRoot = plan.Directory
		}

		worktreePath, err = wm.GetOrPrepareWorktree(ctx, gitRoot, job.Worktree, "interactive")
		if err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to prepare worktree: %w", err)
		}

		// Note: Canopy hooks configuration is handled in the cmd package
		// Skip it here to avoid dependency issues
	} else {
		// No worktree specified, use git root or plan directory
		var err error
		worktreePath, err = GetGitRootSafe(plan.Directory)
		if err != nil {
			worktreePath = plan.Directory
		}
	}

	// Load full config to get agent args (same as plan_launch)
	fullCfg, err := config.LoadFrom(".")
	if err != nil {
		// Proceed with minimal config
		fullCfg = &config.Config{}
	}
	
	// Build agent command
	agentCommand, err := e.buildAgentCommand(job, plan, worktreePath, fullCfg.Agent.Args)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to build agent command: %w", err)
	}

	// Create tmux client
	tmuxClient, err := tmux.NewClient()
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("tmux not available: %w", err)
	}

	// Generate session name
	sessionName := e.generateSessionName(plan, job)

	// Get git root to calculate relative path
	gitRoot, err := GetGitRootSafe(plan.Directory)
	if err != nil {
		gitRoot = plan.Directory
	}
	
	// Get repo name from git root
	repoName := filepath.Base(gitRoot)
	
	// Calculate container work directory (same logic as plan_launch)
	relPath, err := filepath.Rel(gitRoot, worktreePath)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to calculate relative path: %w", err)
	}
	
	var containerWorkDir string
	if fullCfg.Agent.MountWorkspaceAtHostPath {
		containerWorkDir = filepath.Join(gitRoot, relPath)
	} else {
		containerWorkDir = filepath.Join("/workspace", repoName, relPath)
	}

	// Create docker command for agent pane
	dockerCmd := fmt.Sprintf("docker exec -it -w %s %s sh", containerWorkDir, container)

	// Launch tmux session with two windows (matching plan_launch behavior)
	// First create session with host window
	opts := tmux.LaunchOptions{
		SessionName:      sessionName,
		WorkingDirectory: worktreePath,
		WindowName:       "host",
		Panes: []tmux.PaneOptions{
			{
				// Host shell in worktree (default shell, no command needed)
			},
		},
	}

	if err := tmuxClient.Launch(ctx, opts); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to launch tmux session: %w", err)
	}

	// Create second window for agent
	if err := tmuxClient.NewWindow(ctx, sessionName, "agent", dockerCmd); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		// Clean up session on failure
		_ = tmuxClient.KillSession(ctx, sessionName)
		return fmt.Errorf("failed to create agent window: %w", err)
	}

	// Small delay to ensure window is ready
	time.Sleep(300 * time.Millisecond)

	// Send agent command to the agent window
	if err := tmuxClient.SendKeys(ctx, sessionName+":agent", agentCommand, "C-m"); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		// Clean up session on failure
		_ = tmuxClient.KillSession(ctx, sessionName)
		return fmt.Errorf("failed to send agent command: %w", err)
	}

	// Switch back to first window so user starts in host shell
	_ = tmuxClient.SelectWindow(ctx, sessionName+":1")

	// Print user instructions
	fmt.Printf("🚀 Interactive session '%s' launched.\n", sessionName)
	fmt.Printf("   Attach with: tmux attach -t %s\n", sessionName)
	fmt.Printf("\n👉 Close the session (type 'exit' in all panes) to continue the plan.\n")

	// Block and wait for session to close
	err = tmuxClient.WaitForSessionClose(ctx, sessionName, 2*time.Second)

	// Handle interruption
	if err != nil {
		if ctx.Err() != nil {
			fmt.Printf("\n⚠️  Interrupted. Cleaning up session '%s'...\n", sessionName)
			// Use a new context for cleanup since the original was cancelled
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tmuxClient.KillSession(cleanupCtx, sessionName)
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("interrupted by user")
		}
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("wait for session failed: %w", err)
	}

	// Capture pane output for logging (optional)
	// We capture both panes to preserve the full interaction
	hostOutput, _ := tmuxClient.CapturePane(context.Background(), sessionName+":0.0")
	agentOutput, _ := tmuxClient.CapturePane(context.Background(), sessionName+":0.1")

	// Append captured output to job file
	if hostOutput != "" || agentOutput != "" {
		output := fmt.Sprintf("\n## Session Output\n\n### Host Pane\n```\n%s\n```\n\n### Agent Pane\n```\n%s\n```\n",
			hostOutput, agentOutput)
		
		// Get state persister to append output
		persister := NewStatePersister()
		if err := persister.AppendJobOutput(job, output); err != nil {
			// Non-fatal error, just log it
			fmt.Printf("Warning: failed to save session output: %v\n", err)
		}
	}

	// Success
	fmt.Printf("✅ Session '%s' closed. Continuing plan...\n", sessionName)
	job.Status = JobStatusCompleted
	job.EndTime = time.Now()
	return nil
}

// buildAgentCommand constructs the agent command for the interactive session.
func (e *InteractiveAgentExecutor) buildAgentCommand(job *Job, plan *Plan, worktreePath string, agentArgs []string) (string, error) {
	// Build instruction for the agent
	instruction := fmt.Sprintf("Read the file %s and execute the agent job defined there. ", job.FilePath)

	// Add context files if specified
	if len(job.PromptSource) > 0 {
		instruction += "Also read these context files: "
		var contextFiles []string
		for _, source := range job.PromptSource {
			resolved, err := ResolvePromptSource(source, plan)
			relPath := source // fallback
			if err == nil {
				if p, err := filepath.Rel(worktreePath, resolved); err == nil {
					relPath = p
				} else {
					relPath = resolved
				}
			}
			contextFiles = append(contextFiles, relPath)
		}
		instruction += strings.Join(contextFiles, ", ")
	}

	// Shell escape the instruction
	escapedInstruction := "'" + strings.ReplaceAll(instruction, "'", "'\\''") + "'"

	// Build command with agent args
	cmdParts := []string{"claude"}
	cmdParts = append(cmdParts, agentArgs...)
	cmdParts = append(cmdParts, escapedInstruction)

	return strings.Join(cmdParts, " "), nil
}

// generateSessionName creates a unique session name for the interactive job.
func (e *InteractiveAgentExecutor) generateSessionName(plan *Plan, job *Job) string {
	// Sanitize job title for tmux
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, job.Title)

	// Limit length and remove leading/trailing dashes
	if len(sanitized) > 50 {
		sanitized = sanitized[:50]
	}
	sanitized = strings.Trim(sanitized, "-")

	return fmt.Sprintf("%s__%s", plan.Name, sanitized)
}

