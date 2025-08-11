package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	
	"github.com/fatih/color"
	"github.com/mattsolo1/grove-flow/pkg/exec"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-core/docker"
	"github.com/mattsolo1/grove-core/git"
)

// launchParameters holds all the necessary information for launching a tmux session
type launchParameters struct {
	sessionName      string
	container        string
	hostWorktreePath string
	containerWorkDir string
	agentCommand     string
}

// RunPlanLaunch implements the plan launch command
func RunPlanLaunch(jobPath string) error {
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
	sessionTitle := sanitizeForTmuxSession(job.Title)
	params := launchParameters{
		sessionName:      fmt.Sprintf("%s__%s", repoName, sessionTitle),
		container:        container,
		hostWorktreePath: worktreePath,
		agentCommand:     agentCommand,
	}
	
	// Calculate container work directory
	relPath, err := filepath.Rel(gitRoot, worktreePath)
	if err != nil {
		return fmt.Errorf("failed to calculate relative path: %w", err)
	}
	if fullCfg.Agent.MountWorkspaceAtHostPath {
		params.containerWorkDir = filepath.Join(gitRoot, relPath)
	} else {
		params.containerWorkDir = filepath.Join("/workspace", repoName, relPath)
	}
	
	// Launch the session
	executor := &exec.RealCommandExecutor{}
	return launchTmuxSession(executor, params)
}

// buildAgentCommand constructs the shell-escaped agent command string
func buildAgentCommand(job *orchestration.Job, plan *orchestration.Plan, worktreePath string, agentArgs []string) (string, error) {
	var promptBuilder strings.Builder
	promptBuilder.WriteString(job.PromptBody)
	
	if len(job.PromptSource) > 0 {
		promptBuilder.WriteString("\n\nRelevant files for context:\n")
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
			promptBuilder.WriteString(fmt.Sprintf("- %s\n", relPath))
		}
	}
	
	finalPrompt := promptBuilder.String()
	// Basic shell escaping: wrap in single quotes and escape internal single quotes
	escapedPrompt := "'" + strings.ReplaceAll(finalPrompt, "'", "'\\''") + "'"
	
	// Build command with args
	cmdParts := []string{"claude"}
	cmdParts = append(cmdParts, agentArgs...)
	cmdParts = append(cmdParts, escapedPrompt)
	
	return strings.Join(cmdParts, " "), nil
}

// launchTmuxSession creates and configures the tmux session
func launchTmuxSession(executor exec.CommandExecutor, params launchParameters) error {
	// Pre-flight check for tmux
	if _, err := executor.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux command not found, please install it to use this feature")
	}
	
	// Command to run inside docker for the agent window
	dockerCmdStr := fmt.Sprintf("docker exec -it -w %s %s sh", params.containerWorkDir, params.container)
	
	// Debug: Log the docker command
	if verbose := os.Getenv("GROVE_DEBUG"); verbose != "" {
		fmt.Printf("Debug: Docker command for agent window: %s\n", dockerCmdStr)
		fmt.Printf("Debug: Container working directory: %s\n", params.containerWorkDir)
	}
	
	// --- Execute Tmux Sequence ---
	fmt.Printf("🚀 Launching interactive session '%s'...\n", params.sessionName)
	
	// 1. Create session with a shell on the host in the worktree directory
	// The -c flag sets the default path for new windows in this session
	if err := executor.Execute("tmux", "new-session", "-d", "-s", params.sessionName, "-c", params.hostWorktreePath); err != nil {
		if strings.Contains(err.Error(), "duplicate session") {
			fmt.Printf("⚠️  Session '%s' already exists. Attach with `tmux attach -t %s`\n", 
				params.sessionName, params.sessionName)
			return nil
		}
		return fmt.Errorf("failed to create tmux session: %w", err)
	}
	
	// Debug: Log what we're doing
	if verbose := os.Getenv("GROVE_DEBUG"); verbose != "" {
		fmt.Printf("Debug: Created session %s, will cd to %s\n", params.sessionName, params.containerWorkDir)
	}
	
	time.Sleep(300 * time.Millisecond) // Give shell time to start
	
	// 2. Create a second window for the agent command
	// Note: dockerCmdStr must be passed as a single argument to tmux
	if err := executor.Execute("tmux", "new-window", "-t", params.sessionName, "-n", "agent", dockerCmdStr); err != nil {
		// Log more details about the failure
		fmt.Printf("⚠️  Failed to create agent window in tmux session '%s'\n", params.sessionName)
		fmt.Printf("   Docker command was: %s\n", dockerCmdStr)
		fmt.Printf("   Error: %v\n", err)
		
		// Common issues and solutions
		if strings.Contains(err.Error(), "can't find session") {
			fmt.Println("   💡 The tmux session may have been closed. Try running the command again.")
		} else if strings.Contains(err.Error(), "duplicate window") {
			fmt.Println("   💡 A window named 'agent' already exists. Try killing the session first.")
		}
		
		// Cleanup on failure
		executor.Execute("tmux", "kill-session", "-t", params.sessionName)
		return fmt.Errorf("failed to create agent window: %w", err)
	}
	
	time.Sleep(300 * time.Millisecond) // Give the new window time to start
	
	// 3. Send pre-filled prompt to the agent window and execute it
	if err := executor.Execute("tmux", "send-keys", "-t", params.sessionName+":agent", params.agentCommand, "C-m"); err != nil {
		// Log details about the failure
		fmt.Printf("⚠️  Failed to send claude-code command to agent window\n")
		fmt.Printf("   Target: %s:agent\n", params.sessionName)
		fmt.Printf("   Command length: %d characters\n", len(params.agentCommand))
		fmt.Printf("   Error: %v\n", err)
		
		// Cleanup on failure
		executor.Execute("tmux", "kill-session", "-t", params.sessionName)
		return fmt.Errorf("failed to send prompt to tmux session: %w", err)
	}
	
	// 4. Switch back to the first window so user starts there
	executor.Execute("tmux", "select-window", "-t", params.sessionName+":1")
	
	// Success message
	successMsg := color.GreenString("✓")
	fmt.Printf("%s Interactive agent session launched.\n", successMsg)
	fmt.Printf("   Attach with: %s\n", color.CyanString("tmux attach -t %s", params.sessionName))
	
	return nil
}

