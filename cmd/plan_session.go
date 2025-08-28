package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattsolo1/grove-core/git"
	groveexec "github.com/mattsolo1/grove-flow/pkg/exec"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-tmux/pkg/tmux"
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

	// Get the git root for the plan
	gitRoot, err := orchestration.GetGitRootSafe(plan.Directory)
	if err != nil {
		return fmt.Errorf("could not find git root: %w", err)
	}

	// Get current working directory
	currentDir, err := os.Getwd()
	if err != nil {
		currentDir = ""
	}

	// Check if we're already in the worktree
	var worktreePath string
	expectedWorktreePath := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
	if currentDir != "" && strings.HasPrefix(currentDir, expectedWorktreePath) {
		// We're already in the worktree
		worktreePath = expectedWorktreePath
	} else {
		// Prepare the worktree
		wm := git.NewWorktreeManager()
		worktreePath, err = wm.GetOrPrepareWorktree(ctx, gitRoot, worktreeName, "")
		if err != nil {
			// Check if it's because the worktree already exists
			if strings.Contains(err.Error(), "already checked out") {
				// Worktree exists, just use it
				worktreePath = expectedWorktreePath
			} else {
				return fmt.Errorf("failed to prepare worktree: %w", err)
			}
		} else {
			// New worktree was created, install grove-hooks if available
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
			if err := orchestration.SetupGoWorkspaceForWorktree(worktreePath, gitRoot); err != nil {
				// Log a warning but don't fail the worktree creation
				fmt.Printf("Warning: failed to setup Go workspace in worktree: %v\n", err)
			}
		}
	}

	// Session name is derived from the worktree name
	sessionName := SanitizeForTmuxSession(worktreeName)

	// Check if session already exists
	sessionExists, _ := tmuxClient.SessionExists(ctx, sessionName)

	if !sessionExists {
		// Create new session with workspace window
		opts := tmux.LaunchOptions{
			SessionName:      sessionName,
			WorkingDirectory: worktreePath,
			WindowName:       "workspace",
			Panes: []tmux.PaneOptions{
				{
					Command: "", // Empty = default shell
				},
			},
		}

		fmt.Printf("🚀 Creating tmux session '%s' for worktree...\n", sessionName)
		if err := tmuxClient.Launch(ctx, opts); err != nil {
			return fmt.Errorf("failed to create tmux session: %w", err)
		}
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