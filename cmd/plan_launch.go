package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/mattsolo1/grove-core/docker"
	"github.com/mattsolo1/grove-core/git"
	"github.com/mattsolo1/grove-flow/pkg/exec"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

// LaunchParameters holds all the necessary information for launching a tmux session
type LaunchParameters struct {
	SessionName      string
	Container        string
	HostWorktreePath string
	ContainerWorkDir string
	AgentCommand     string
}

// RunPlanLaunch implements the plan launch command
func RunPlanLaunch(cmd *cobra.Command, jobPath string) error {
	// Check if --host flag was used
	if planLaunchHost {
		return runPlanLaunchHost(jobPath)
	}
	// Parse the job path
	planDir := filepath.Dir(jobPath)
	jobFile := filepath.Base(jobPath)

	// Load the plan
	plan, err := orchestration.LoadPlan(planDir)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	// Find the job
	job, found := plan.GetJobByFilename(jobFile)
	if !found {
		return fmt.Errorf("job not found in plan: %s", jobFile)
	}

	// Validate job type
	if job.Type != orchestration.JobTypeAgent {
		return fmt.Errorf("launch command only supports 'agent' type jobs, got '%s'", job.Type)
	}
	if job.Worktree == "" {
		return fmt.Errorf("agent job must have a 'worktree' specified for interactive launch")
	}

	// Load configuration
	flowCfg, err := loadFlowConfig()
	if err != nil {
		return err
	}
	container := flowCfg.TargetAgentContainer
	if container == "" {
		return fmt.Errorf("'flow.target_agent_container' is not set in your grove.yml")
	}

	// Pre-flight check: verify container is running (unless skipped for testing)
	ctx := context.Background()
	if !shouldSkipDockerCheck() {
		dockerClient, err := docker.NewSDKClient()
		if err != nil {
			return fmt.Errorf("failed to create docker client: %w", err)
		}

		if !dockerClient.IsContainerRunning(ctx, container) {
			return fmt.Errorf("container '%s' is not running. Did you run 'grove-proxy up'?", container)
		}
	}

	// Load full config to get agent args
	fullCfg, err := loadFullConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Get git root
	gitRoot, err := orchestration.GetGitRootSafe(plan.Directory)
	if err != nil {
		return err
	}

	// If UseSuperprojectRoot is enabled, get the superproject root
	if fullCfg.Agent.UseSuperprojectRoot {
		superRoot, err := git.GetSuperprojectRoot(gitRoot)
		if err == nil && superRoot != "" {
			gitRoot = superRoot
		}
	}

	// Prepare the worktree at the git root
	wm := git.NewWorktreeManager()
	worktreePath, err := wm.GetOrPrepareWorktree(ctx, gitRoot, job.Worktree, "interactive")
	if err != nil {
		return fmt.Errorf("failed to prepare worktree: %w", err)
	}

	// Set up Go workspace if this is a Go project
	if err := orchestration.SetupGoWorkspaceForWorktree(worktreePath, gitRoot); err != nil {
		// Log a warning but don't fail the job, as this is a convenience feature
		fmt.Printf("Warning: failed to setup Go workspace in worktree: %v\n", err)
	}

	// Configure Canopy hooks for the worktree
	if err := configureCanopyHooks(worktreePath); err != nil {
		return fmt.Errorf("failed to configure canopy hooks: %w", err)
	}

	// Debug: Log config status
	if verbose := os.Getenv("GROVE_DEBUG"); verbose != "" {
		fmt.Printf("Debug: Agent.MountWorkspaceAtHostPath = %v\n", fullCfg.Agent.MountWorkspaceAtHostPath)
	}

	// Build the agent command
	agentCommand, err := buildAgentCommand(job, plan, worktreePath, fullCfg.Agent.Args)
	if err != nil {
		return fmt.Errorf("failed to build agent command: %w", err)
	}

	// Prepare launch parameters
	repoName := filepath.Base(gitRoot)
	// Use the job title for session name, sanitized for tmux
	sessionTitle := SanitizeForTmuxSession(job.Title)
	params := LaunchParameters{
		SessionName:      fmt.Sprintf("%s__%s", repoName, sessionTitle),
		Container:        container,
		HostWorktreePath: worktreePath,
		AgentCommand:     agentCommand,
	}

	// Calculate container work directory
	relPath, err := filepath.Rel(gitRoot, worktreePath)
	if err != nil {
		return fmt.Errorf("failed to calculate relative path: %w", err)
	}
	if fullCfg.Agent.MountWorkspaceAtHostPath {
		params.ContainerWorkDir = filepath.Join(gitRoot, relPath)
	} else {
		params.ContainerWorkDir = filepath.Join("/workspace", repoName, relPath)
	}

	// Launch the session
	executor := &exec.RealCommandExecutor{}
	return LaunchTmuxSession(executor, params)
}

// buildAgentCommand constructs the shell-escaped agent command string
func buildAgentCommand(job *orchestration.Job, plan *orchestration.Plan, worktreePath string, agentArgs []string) (string, error) {
	// Instead of passing the entire content, instruct claude to read the job file
	instruction := fmt.Sprintf("Read the file %s and execute the agent job defined there. ", job.FilePath)

	// Add any relevant context files
	if len(job.PromptSource) > 0 {
		instruction += "Also read these context files: "
		var contextFiles []string
		for _, source := range job.PromptSource {
			// Resolve source to make path relative to worktree for clarity
			resolved, err := orchestration.ResolvePromptSource(source, plan)
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

	// Basic shell escaping: wrap in single quotes and escape internal single quotes
	escapedInstruction := "'" + strings.ReplaceAll(instruction, "'", "'\\''") + "'"

	// Build command with args
	cmdParts := []string{"claude"}
	if job.AgentContinue {
		cmdParts = append(cmdParts, "--continue")
		cmdParts = append(cmdParts, agentArgs...)
		return fmt.Sprintf("echo %s | %s", escapedInstruction, strings.Join(cmdParts, " ")), nil
	}

	cmdParts = append(cmdParts, agentArgs...)
	cmdParts = append(cmdParts, escapedInstruction)

	return strings.Join(cmdParts, " "), nil
}

// LaunchTmuxSession creates and configures the tmux session
func LaunchTmuxSession(executor exec.CommandExecutor, params LaunchParameters) error {
	// Pre-flight check for tmux
	if _, err := executor.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux command not found, please install it to use this feature")
	}

	// Command to run inside docker for the agent window
	dockerCmdStr := fmt.Sprintf("docker exec -it -w %s %s sh", params.ContainerWorkDir, params.Container)

	// Debug: Log the docker command
	if verbose := os.Getenv("GROVE_DEBUG"); verbose != "" {
		fmt.Printf("Debug: Docker command for agent window: %s\n", dockerCmdStr)
		fmt.Printf("Debug: Container working directory: %s\n", params.ContainerWorkDir)
	}

	// --- Execute Tmux Sequence ---
	fmt.Printf("🚀 Launching interactive session '%s'...\n", params.SessionName)

	// 1. Create session with a shell on the host in the worktree directory
	// The -c flag sets the default path for new windows in this session
	if err := executor.Execute("tmux", "new-session", "-d", "-s", params.SessionName, "-c", params.HostWorktreePath); err != nil {
		if strings.Contains(err.Error(), "duplicate session") {
			fmt.Printf("⚠️  Session '%s' already exists. Attach with `tmux attach -t %s`\n",
				params.SessionName, params.SessionName)
			return nil
		}
		return fmt.Errorf("failed to create tmux session: %w", err)
	}

	// Debug: Log what we're doing
	if verbose := os.Getenv("GROVE_DEBUG"); verbose != "" {
		fmt.Printf("Debug: Created session %s, will cd to %s\n", params.SessionName, params.ContainerWorkDir)
	}

	time.Sleep(300 * time.Millisecond) // Give shell time to start

	// 2. Create a second window for the agent command
	// Note: dockerCmdStr must be passed as a single argument to tmux
	// 2. Create a second window for the agent command
	// Note: dockerCmdStr must be passed as a single argument to tmux
	windowName := "agent"
	if params.AgentCommand == "" {
		// If no command, it's just a shell for exploration
		windowName = "shell"
	}

	if err := executor.Execute("tmux", "new-window", "-t", params.SessionName, "-n", windowName, dockerCmdStr); err != nil {
		// Log more details about the failure
		fmt.Printf("⚠️  Failed to create agent window in tmux session '%s'\n", params.SessionName)
		fmt.Printf("   Docker command was: %s\n", dockerCmdStr)
		fmt.Printf("   Error: %v\n", err)

		// Common issues and solutions
		if strings.Contains(err.Error(), "can't find session") {
			fmt.Println("   💡 The tmux session may have been closed. Try running the command again.")
		} else if strings.Contains(err.Error(), "duplicate window") {
			fmt.Println("   💡 A window named 'agent' already exists. Try killing the session first.")
		}

		// Cleanup on failure
		executor.Execute("tmux", "kill-session", "-t", params.SessionName)
		return fmt.Errorf("failed to create agent window: %w", err)
	}

	time.Sleep(300 * time.Millisecond) // Give the new window time to start

	// 3. Send pre-filled prompt to the agent window and execute it, if a command is provided
	if params.AgentCommand != "" {
		if err := executor.Execute("tmux", "send-keys", "-t", params.SessionName+":"+windowName, params.AgentCommand, "C-m"); err != nil {
			// Log details about the failure
			fmt.Printf("⚠️  Failed to send claude-code command to agent window\n")
			fmt.Printf("   Target: %s:%s\n", params.SessionName, windowName)
			fmt.Printf("   Command length: %d characters\n", len(params.AgentCommand))
			fmt.Printf("   Error: %v\n", err)

			// Cleanup on failure
			executor.Execute("tmux", "kill-session", "-t", params.SessionName)
			return fmt.Errorf("failed to send prompt to tmux session: %w", err)
		}
	}

	// 4. Switch back to the first window so user starts there
	executor.Execute("tmux", "select-window", "-t", params.SessionName+":1")

	// Success message
	successMsg := color.GreenString("✓")
	fmt.Printf("%s Interactive session launched.\n", successMsg)
	fmt.Printf("   Attach with: %s\n", color.CyanString("tmux attach -t %s", params.SessionName))

	return nil
}

// runPlanLaunchHost launches a job in host mode (without container)
func runPlanLaunchHost(jobPath string) error {
	// Parse the job path
	planDir := filepath.Dir(jobPath)
	jobFile := filepath.Base(jobPath)

	// Load the plan
	plan, err := orchestration.LoadPlan(planDir)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	// Find the job
	job, found := plan.GetJobByFilename(jobFile)
	if !found {
		return fmt.Errorf("job not found in plan: %s", jobFile)
	}

	// Validate job type
	if job.Type != orchestration.JobTypeAgent && job.Type != orchestration.JobTypeInteractiveAgent {
		return fmt.Errorf("launch command only supports 'agent' or 'interactive_agent' type jobs, got '%s'", job.Type)
	}

	// Get git root
	gitRoot, err := orchestration.GetGitRootSafe(plan.Directory)
	if err != nil {
		return fmt.Errorf("could not find git root: %w", err)
	}

	// Determine working directory
	var workDir string
	if job.Worktree != "" {
		// A worktree is specified, so create/use it on the host
		wm := git.NewWorktreeManager()
		ctx := context.Background()
		worktreePath, err := wm.GetOrPrepareWorktree(ctx, gitRoot, job.Worktree, "interactive-host")
		if err != nil {
			return fmt.Errorf("failed to prepare host worktree: %w", err)
		}
		workDir = worktreePath

		// Set up Go workspace if this is a Go project
		if err := orchestration.SetupGoWorkspaceForWorktree(worktreePath, gitRoot); err != nil {
			// Log a warning but don't fail the job, as this is a convenience feature
			fmt.Printf("Warning: failed to setup Go workspace in worktree: %v\n", err)
		}

		// Configure Canopy hooks for the worktree
		if err := configureCanopyHooks(worktreePath); err != nil {
			return fmt.Errorf("failed to configure canopy hooks: %w", err)
		}
	} else {
		// No worktree, use the main git repository root
		workDir = gitRoot
	}

	// Get repo name and create session/window names
	repoName := filepath.Base(gitRoot)
	sessionName := SanitizeForTmuxSession(repoName)
	windowName := "job-" + SanitizeForTmuxSession(job.Title)

	executor := &exec.RealCommandExecutor{}

	// Ensure tmux session exists
	err = executor.Execute("tmux", "has-session", "-t", sessionName)
	if err != nil { // has-session returns error if session doesn't exist
		fmt.Printf("✓ Tmux session '%s' not found, creating it...\n", sessionName)
		if createErr := executor.Execute("tmux", "new-session", "-d", "-s", sessionName, "-c", gitRoot); createErr != nil {
			return fmt.Errorf("failed to create tmux session '%s': %w", sessionName, createErr)
		}
	}

	// Create new window
	if err := executor.Execute("tmux", "new-window", "-t", sessionName, "-n", windowName, "-c", workDir); err != nil {
		return fmt.Errorf("failed to create new tmux window: %w", err)
	}

	// Load full config to get agent args
	fullCfg, err := loadFullConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Build agent command
	agentCommand, err := buildAgentCommand(job, plan, workDir, fullCfg.Agent.Args)
	if err != nil {
		return fmt.Errorf("failed to build agent command: %w", err)
	}

	// Send command to the new window
	targetPane := fmt.Sprintf("%s:%s", sessionName, windowName)
	if err := executor.Execute("tmux", "send-keys", "-t", targetPane, agentCommand, "C-m"); err != nil {
		return fmt.Errorf("failed to send command to tmux: %w", err)
	}

	// Provide user feedback
	fmt.Printf("✓ Launched job in new window '%s' within session '%s'.\n", windowName, sessionName)
	fmt.Printf("  Working directory: %s\n", workDir)
	fmt.Printf("  Attach with: tmux attach -t %s\n", sessionName)
	return nil
}

// SanitizeForTmuxSession creates a valid tmux session name from a string.
// Moved from chat.go to be reusable.
func SanitizeForTmuxSession(title string) string {
	// Replace spaces and special characters with hyphens
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, title)

	// Convert to lowercase for consistency
	sanitized = strings.ToLower(sanitized)

	// Remove consecutive hyphens
	for strings.Contains(sanitized, "--") {
		sanitized = strings.ReplaceAll(sanitized, "--", "-")
	}

	// Trim hyphens from start and end
	sanitized = strings.Trim(sanitized, "-")

	// Ensure it's not empty
	if sanitized == "" {
		sanitized = "session"
	}

	// Tmux session names should not be too long
	if len(sanitized) > 50 {
		sanitized = sanitized[:50]
	}

	return sanitized
}
