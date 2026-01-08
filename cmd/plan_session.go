package cmd

import (
	"github.com/mattsolo1/grove-core/util/sanitize"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattsolo1/grove-core/git"
	"github.com/mattsolo1/grove-core/pkg/tmux"
	"github.com/mattsolo1/grove-core/pkg/workspace"
	groveexec "github.com/mattsolo1/grove-flow/pkg/exec"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
)

// CreateOrSwitchToWorktreeSessionAndRunCommand creates or switches to a tmux session for the worktree and executes a command.
func CreateOrSwitchToWorktreeSessionAndRunCommand(ctx context.Context, plan *orchestration.Plan, worktreeName string, commandToRun []string) error {
	// Only proceed if we're in a terminal and have tmux
	if os.Getenv("TERM") == "" {
		return fmt.Errorf("not in a terminal")
	}

	// Create tmux client
	tmuxClient, err := tmux.NewClient()
	if err != nil {
		return fmt.Errorf("tmux not available: %w", err)
	}

	// Get the project git root for the plan (notebook-aware)
	gitRoot, err := orchestration.GetProjectGitRoot(plan.Directory)
	if err != nil {
		return fmt.Errorf("could not find project git root: %w", err)
	}

	// Defensive check: prevent creating worktrees in notebook repos
	if workspace.IsNotebookRepo(gitRoot) {
		return fmt.Errorf("cannot create worktree session: the plan is located in a notebook git repository at %s. Please run this command from your project directory", gitRoot)
	}

	// If gitRoot is itself a worktree, use the centralized logic to find the actual main repository root
	gitRootInfo, err := workspace.GetProjectByPath(gitRoot)
	if err == nil && gitRootInfo.IsWorktree() && gitRootInfo.ParentProjectPath != "" {
		gitRoot = gitRootInfo.ParentProjectPath
	}

	// Check if we're already in the target worktree
	currentDir, _ := os.Getwd()
	expectedWorktreePath := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
	var worktreePath string

	if currentDir != "" && strings.HasPrefix(currentDir, expectedWorktreePath) {
		// We're already in the worktree, just use it
		worktreePath = expectedWorktreePath
	} else {
		// Prepare the worktree using the centralized helper
		opts := workspace.PrepareOptions{
			GitRoot:      gitRoot,
			WorktreeName: worktreeName,
			BranchName:   worktreeName,
			PlanName:     plan.Name,
		}

		if plan.Config != nil && len(plan.Config.Repos) > 0 {
			opts.Repos = plan.Config.Repos
		}

		worktreePath, err = workspace.Prepare(ctx, opts)
		if err != nil {
			return fmt.Errorf("failed to prepare worktree: %w", err)
		}
	}

	// Session name is derived from the project identifier (notebook-aware)
	projInfo, err := orchestration.ResolveProjectForSessionNaming(worktreePath)
	if err != nil {
		return fmt.Errorf("failed to get project info for session naming: %w", err)
	}
	sessionName := projInfo.Identifier()

	// Check if session already exists
	sessionExists, _ := tmuxClient.SessionExists(ctx, sessionName)

	// Check if we're currently in that session
	inTargetSession := false
	if os.Getenv("TMUX") != "" {
		cmd := exec.Command("tmux", "display-message", "-p", "#S")
		output, err := cmd.Output()
		if err == nil && strings.TrimSpace(string(output)) == sessionName {
			inTargetSession = true
		}
	}

	// If we're already in the target session, just run the command directly without creating a new window
	if inTargetSession {
		// Just execute the command in the current shell
		executor := &groveexec.RealCommandExecutor{}
		commandStr := strings.Join(commandToRun, " ")
		if err := executor.Execute("sh", "-c", commandStr); err != nil {
			return fmt.Errorf("failed to execute command: %w", err)
		}
		return nil
	}

	if !sessionExists {
		// First pane needs to set the active plan before running status
		planName := plan.Name
		setPlanCmd := fmt.Sprintf("flow plan set %s && %s", planName, strings.Join(commandToRun, " "))

		panes := []tmux.PaneOptions{
			{
				Command: setPlanCmd, // Set active plan then run flow plan status -t
			},
		}

		// Create new session with plan window at index 2
		opts := tmux.LaunchOptions{
			SessionName:      sessionName,
			WorkingDirectory: worktreePath,
			WindowName:       "plan",
			WindowIndex:      2,
			Panes:            panes,
		}

		fmt.Printf("🚀 Creating tmux session '%s' for worktree...\n", sessionName)
		if err := tmuxClient.Launch(ctx, opts); err != nil {
			return fmt.Errorf("failed to create tmux session: %w", err)
		}

		// Session created successfully
		fmt.Printf("✓ Session '%s' created\n", sessionName)

		// Switch to the new session if we're already in tmux, but not if launched from another TUI
		isTUIMode := os.Getenv("GROVE_FLOW_TUI_MODE") == "true"
		if os.Getenv("TMUX") != "" && !isTUIMode {
			executor := &groveexec.RealCommandExecutor{}
			if err := executor.Execute("tmux", "switch-client", "-t", sessionName); err != nil {
				fmt.Printf("Note: Could not switch to session (attach manually): tmux attach -t %s\n", sessionName)
			} else {
				fmt.Printf("✓ Switched to session '%s'\n", sessionName)
			}
		} else if !isTUIMode {
			fmt.Printf("  Attach with: tmux attach -t %s\n", sessionName)
		}
		return nil
	}

	// Build the command string
	commandStr := strings.Join(commandToRun, " ")

	// Create a new window for the plan run command
	executor := &groveexec.RealCommandExecutor{}
	// Use a more descriptive window name based on the command
	windowName := commandToRun[2] // e.g., "run" or "status"
	if len(commandToRun) > 3 {
		windowName = fmt.Sprintf("%s-%s", commandToRun[2], commandToRun[3])
	}

	if err := executor.Execute("tmux", "new-window", "-t", sessionName, "-n", windowName, "-c", worktreePath); err != nil {
		// Window might already exist, try to use it
		fmt.Printf("Note: Could not create new window '%s': %v. Attempting to use existing window.\n", windowName, err)
	}

	// Send the command to the window
	targetPane := fmt.Sprintf("%s:%s", sessionName, windowName)

	// Send commands to the window
	// First change to the worktree directory
	cdCmd := fmt.Sprintf("cd %s", worktreePath)
	if err := executor.Execute("tmux", "send-keys", "-t", targetPane, cdCmd, "C-m"); err != nil {
		return fmt.Errorf("failed to send cd command: %w", err)
	}

	// Small delay
	time.Sleep(100 * time.Millisecond)

	// Set the active plan in the worktree
	planName := filepath.Base(plan.Directory)
	setPlanCmd := fmt.Sprintf("flow plan set %s", planName)
	if err := executor.Execute("tmux", "send-keys", "-t", targetPane, setPlanCmd, "C-m"); err != nil {
		return fmt.Errorf("failed to send set plan command: %w", err)
	}

	// Small delay to let the set command complete
	time.Sleep(200 * time.Millisecond)

	// Then run the actual command
	if err := executor.Execute("tmux", "send-keys", "-t", targetPane, commandStr, "C-m"); err != nil {
		return fmt.Errorf("failed to send command '%s': %w", commandStr, err)
	}

	// If we're already in tmux, switch to the session
	if os.Getenv("TMUX") != "" {
		fmt.Printf("✓ Switching to session '%s'...\n", sessionName)
		if err := executor.Execute("tmux", "switch-client", "-t", sessionName); err != nil {
			fmt.Printf("Could not switch to session. Attach with: tmux attach -t %s\n", sessionName)
		}
		// Also switch to the new window
		if err := executor.Execute("tmux", "select-window", "-t", targetPane); err != nil {
			fmt.Printf("Note: Could not switch to window '%s'\n", windowName)
		}
	} else {
		fmt.Printf("Attach with: tmux attach -t %s\n", sessionName)
	}

	return nil
}

// CreateOrSwitchToMainRepoSessionAndRunCommand creates or switches to a tmux session in the main repo and executes a command.
// This is similar to CreateOrSwitchToWorktreeSessionAndRunCommand but operates in the main repository.
func CreateOrSwitchToMainRepoSessionAndRunCommand(ctx context.Context, planName string, commandToRun []string) error {
	// Only proceed if we're in a terminal and have tmux
	if os.Getenv("TERM") == "" {
		return fmt.Errorf("not in a terminal")
	}

	// Create tmux client
	tmuxClient, err := tmux.NewClient()
	if err != nil {
		return fmt.Errorf("tmux not available: %w", err)
	}

	// Get git root
	gitRoot, err := git.GetGitRoot(".")
	if err != nil {
		return fmt.Errorf("could not find git root: %w", err)
	}

	// If gitRoot is itself a worktree, resolve to the parent repository
	gitRootInfo, err := workspace.GetProjectByPath(gitRoot)
	if err == nil && gitRootInfo.IsWorktree() && gitRootInfo.ParentProjectPath != "" {
		gitRoot = gitRootInfo.ParentProjectPath
	}

	// Generate session name using the project identifier
	projInfo, err := workspace.GetProjectByPath(gitRoot)
	if err != nil {
		return fmt.Errorf("failed to get project info for session naming: %w", err)
	}
	sessionTitle := fmt.Sprintf("plan-%s", sanitize.SanitizeForTmuxSession(planName))
	sessionName := fmt.Sprintf("%s__%s", projInfo.Identifier(), sessionTitle)

	// Check if session already exists
	executor := &groveexec.RealCommandExecutor{}
	sessions, err := tmuxClient.ListSessions(ctx)
	if err == nil {
		for _, session := range sessions {
			if session == sessionName {
				// Session exists, switch to it
				fmt.Printf("✓ Switching to existing session '%s'...\n", sessionName)
				
				isTUIMode := os.Getenv("GROVE_FLOW_TUI_MODE") == "true"
				if !isTUIMode {
					// If we're already in tmux, switch to the session
					if os.Getenv("TMUX") != "" {
						if err := executor.Execute("tmux", "switch-client", "-t", sessionName); err != nil {
							fmt.Printf("Could not switch to session. Attach with: tmux attach -t %s\n", sessionName)
						}
					} else {
						// Not in tmux, attach to the session
						if err := executor.Execute("tmux", "attach-session", "-t", sessionName); err != nil {
							return fmt.Errorf("failed to attach to session: %w", err)
						}
					}
				}
				return nil
			}
		}
	}

	// Session doesn't exist, create it
	fmt.Printf("✓ Creating new session '%s' in main repository...\n", sessionName)
	
	// Create the session
	if err := executor.Execute("tmux", "new-session", "-d", "-s", sessionName, "-c", gitRoot); err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}

	// Rename the initial window to "plan"
	if err := executor.Execute("tmux", "rename-window", "-t", sessionName, "plan"); err != nil {
		// Clean up on failure
		executor.Execute("tmux", "kill-session", "-t", sessionName)
		return fmt.Errorf("failed to rename window: %w", err)
	}

	// Move the "plan" window to index 2
	if err := executor.Execute("tmux", "move-window", "-s", fmt.Sprintf("%s:plan", sessionName), "-t", "2"); err != nil {
		// Clean up on failure
		executor.Execute("tmux", "kill-session", "-t", sessionName)
		return fmt.Errorf("failed to move window: %w", err)
	}

	// Send the command to the session
	commandStr := strings.Join(commandToRun, " ")
	if err := executor.Execute("tmux", "send-keys", "-t", sessionName, commandStr, "C-m"); err != nil {
		// Clean up on failure
		executor.Execute("tmux", "kill-session", "-t", sessionName)
		return fmt.Errorf("failed to send command: %w", err)
	}

	isTUIMode := os.Getenv("GROVE_FLOW_TUI_MODE") == "true"
	if !isTUIMode {
		// If we're already in tmux, switch to the new session
		if os.Getenv("TMUX") != "" {
			fmt.Printf("✓ Switching to session '%s'...\n", sessionName)
			if err := executor.Execute("tmux", "switch-client", "-t", sessionName); err != nil {
				fmt.Printf("Could not switch to session. Attach with: tmux attach -t %s\n", sessionName)
			}
		} else {
			// Not in tmux, attach to the new session
			fmt.Printf("✓ Attaching to session '%s'...\n", sessionName)
			if err := executor.Execute("tmux", "attach-session", "-t", sessionName); err != nil {
				return fmt.Errorf("failed to attach to session: %w", err)
			}
		}
	}

	return nil
}