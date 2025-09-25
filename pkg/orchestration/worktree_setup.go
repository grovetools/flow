package orchestration

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-core/git"
)

// WorkspaceInfo represents a workspace from grove ws list --json
type WorkspaceInfo struct {
	Name      string          `json:"name"`
	Path      string          `json:"path"`
	Worktrees []WorktreeInfo  `json:"worktrees"`
}

// WorktreeInfo represents a worktree within a workspace
type WorktreeInfo struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	IsMain bool   `json:"is_main"`
}

// discoverLocalWorkspaces uses grove ws list to find local repository paths
func discoverLocalWorkspaces(ctx context.Context) (map[string]string, error) {
	// Check for test environment override first
	if mockData := os.Getenv("GROVE_TEST_WORKSPACES"); mockData != "" {
		fmt.Printf("DEBUG: Using test workspace data from GROVE_TEST_WORKSPACES\n")
		var workspaces []WorkspaceInfo
		if err := json.Unmarshal([]byte(mockData), &workspaces); err != nil {
			fmt.Printf("DEBUG: Failed to parse GROVE_TEST_WORKSPACES: %v\n", err)
			return make(map[string]string), nil
		}
		
		result := make(map[string]string)
		for _, ws := range workspaces {
			for _, wt := range ws.Worktrees {
				if wt.IsMain {
					result[ws.Name] = wt.Path
					fmt.Printf("DEBUG: Test workspace %s at %s\n", ws.Name, wt.Path)
					break
				}
			}
		}
		return result, nil
	}
	
	cmd := exec.CommandContext(ctx, "grove", "ws", "list", "--json")
	output, err := cmd.CombinedOutput() // Changed to capture stderr as well
	if err != nil {
		fmt.Printf("DEBUG: grove ws list failed: %v\nOutput: %s\n", err, string(output))
		// If grove ws list fails, return empty map (fallback to standard submodule behavior)
		return make(map[string]string), nil
	}
	fmt.Printf("DEBUG: grove ws list output: %s\n", string(output))

	var workspaces []WorkspaceInfo
	if err := json.Unmarshal(output, &workspaces); err != nil {
		return nil, fmt.Errorf("failed to parse grove ws list output: %w", err)
	}

	// Build a map from workspace name to main worktree path
	result := make(map[string]string)
	for _, ws := range workspaces {
		for _, wt := range ws.Worktrees {
			if wt.IsMain {
				result[ws.Name] = wt.Path
				fmt.Printf("DEBUG: Found workspace %s at %s\n", ws.Name, wt.Path)
				break
			}
		}
	}

	fmt.Printf("DEBUG: Discovered %d local workspaces\n", len(result))
	return result, nil
}

// parseGitmodules reads and parses the .gitmodules file
func parseGitmodules(gitmodulesPath string) (map[string]string, error) {
	file, err := os.Open(gitmodulesPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	submodules := make(map[string]string)
	var currentName string
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[submodule") {
			// Extract submodule name from [submodule "name"]
			start := strings.Index(line, "\"")
			end := strings.LastIndex(line, "\"")
			if start != -1 && end != -1 && start < end {
				currentName = line[start+1 : end]
			}
		} else if strings.HasPrefix(line, "path =") && currentName != "" {
			// Extract path
			path := strings.TrimSpace(strings.TrimPrefix(line, "path ="))
			submodules[currentName] = path
		}
	}

	return submodules, scanner.Err()
}

// SetupSubmodulesForWorktree initializes submodules using linked worktrees when possible.
// For local ecosystem submodules, it creates linked worktrees from their source repos.
// For external submodules, it falls back to standard git submodule behavior.
func SetupSubmodulesForWorktree(ctx context.Context, worktreePath, branchName string) error {
	// First, ensure all tracked files are checked out in the worktree
	// Sometimes worktrees might not have all files checked out initially
	cmdCheckout := exec.CommandContext(ctx, "git", "checkout", "HEAD", "--", ".")
	cmdCheckout.Dir = worktreePath
	if output, err := cmdCheckout.CombinedOutput(); err != nil {
		fmt.Printf("Warning: could not ensure all files checked out in worktree: %s\n", string(output))
	}
	
	// Check if .gitmodules exists in the worktree, if not, there's nothing to do.
	gitmodulesPath := filepath.Join(worktreePath, ".gitmodules")
	if _, err := os.Stat(gitmodulesPath); os.IsNotExist(err) {
		fmt.Printf("No .gitmodules found at %s, skipping submodule setup\n", gitmodulesPath)
		return nil
	}

	fmt.Printf("✓ Initializing submodules in worktree at %s...\n", worktreePath)

	// Parse .gitmodules to get submodule names and paths
	submodulePaths, err := parseGitmodules(gitmodulesPath)
	if err != nil {
		fmt.Printf("Warning: could not parse .gitmodules: %v\n", err)
		// Fall back to standard submodule behavior
		return setupSubmodulesStandard(ctx, worktreePath, branchName)
	}

	// Discover local workspaces
	localWorkspaces, err := discoverLocalWorkspaces(ctx)
	if err != nil {
		fmt.Printf("Warning: failed to discover local workspaces: %v\n", err)
		// Fall back to standard submodule behavior
		return setupSubmodulesStandard(ctx, worktreePath, branchName)
	}

	// First, run standard submodule update to ensure all submodules are initialized
	// This is important for external dependencies
	cmdUpdate := exec.CommandContext(ctx, "git", "submodule", "update", "--init", "--recursive")
	cmdUpdate.Dir = worktreePath
	if output, err := cmdUpdate.CombinedOutput(); err != nil {
		return fmt.Errorf("git submodule update failed: %w\n%s", err, string(output))
	}

	// Derive gitRoot from worktreePath (parent of .grove-worktrees)
	gitRoot := filepath.Dir(filepath.Dir(worktreePath))
	
	// Now process each submodule to potentially replace with linked worktrees
	for submoduleName, submodulePath := range submodulePaths {
		// First check if we can create a linked worktree from the main checkout's submodule
		mainSubmodulePath := filepath.Join(gitRoot, submodulePath)
		
		// Check if the main submodule exists and is a git repository
		if _, err := os.Stat(filepath.Join(mainSubmodulePath, ".git")); err == nil {
			// We can create a linked worktree from the main checkout's submodule
			targetPath := filepath.Join(worktreePath, submodulePath)
			fmt.Printf("  • %s: creating linked worktree from main checkout at %s\n", submoduleName, mainSubmodulePath)

			// Remove the directory created by submodule update
			if err := os.RemoveAll(targetPath); err != nil {
				fmt.Printf("    Warning: could not remove submodule directory: %v\n", err)
				continue
			}

			// Create a linked worktree from the main submodule checkout
			cmdWorktree := exec.CommandContext(ctx, "git", "worktree", "add", "-B", branchName, targetPath)
			cmdWorktree.Dir = mainSubmodulePath
			if output, err := cmdWorktree.CombinedOutput(); err != nil {
				fmt.Printf("    Warning: failed to create linked worktree from main checkout: %v\n%s\n", err, string(output))
				// Try to restore with standard submodule update for this one
				cmdRestore := exec.CommandContext(ctx, "git", "submodule", "update", "--init", submodulePath)
				cmdRestore.Dir = worktreePath
				if restoreErr := cmdRestore.Run(); restoreErr != nil {
					fmt.Printf("    Error: could not restore submodule: %v\n", restoreErr)
				}
			}
			continue
		}
		
		// Fallback: Check if we have this submodule locally via grove ws list
		localRepoPath, hasLocal := localWorkspaces[submoduleName]
		if !hasLocal {
			// No local version, keep the standard submodule clone
			fmt.Printf("  • %s: using standard submodule (no local workspace found)\n", submoduleName)
			continue
		}

		// We have a local version - replace the submodule with a linked worktree
		targetPath := filepath.Join(worktreePath, submodulePath)
		fmt.Printf("  • %s: creating linked worktree from local workspace at %s\n", submoduleName, localRepoPath)

		// Remove the directory created by submodule update
		if err := os.RemoveAll(targetPath); err != nil {
			fmt.Printf("    Warning: could not remove submodule directory: %v\n", err)
			continue
		}

		// Create a linked worktree from the local repository
		cmdWorktree := exec.CommandContext(ctx, "git", "worktree", "add", "-B", branchName, targetPath)
		cmdWorktree.Dir = localRepoPath
		if output, err := cmdWorktree.CombinedOutput(); err != nil {
			fmt.Printf("    Warning: failed to create linked worktree: %v\n%s\n", err, string(output))
			// Try to restore with standard submodule update for this one
			cmdRestore := exec.CommandContext(ctx, "git", "submodule", "update", "--init", submodulePath)
			cmdRestore.Dir = worktreePath
			if restoreErr := cmdRestore.Run(); restoreErr != nil {
				fmt.Printf("    Error: could not restore submodule: %v\n", restoreErr)
			}
		}
	}

	// For any remaining submodules that weren't replaced (external deps),
	// switch them to the parallel branch
	// First try to create the branch, then switch to it if it exists
	foreachCmd := fmt.Sprintf("git checkout -b %s 2>/dev/null || git checkout %s", branchName, branchName)
	cmdForeach := exec.CommandContext(ctx, "git", "submodule", "foreach", "--recursive", foreachCmd)
	cmdForeach.Dir = worktreePath
	if output, err := cmdForeach.CombinedOutput(); err != nil {
		// Log the output for debugging
		fmt.Printf("  Note: submodule branch switching output:\n%s\n", string(output))
	}

	fmt.Printf("✓ Submodules initialized with linked worktrees where possible.\n")
	return nil
}

// setupSubmodulesStandard is the fallback standard submodule setup
func setupSubmodulesStandard(ctx context.Context, worktreePath, branchName string) error {
	// Run standard git submodule update
	cmdUpdate := exec.CommandContext(ctx, "git", "submodule", "update", "--init", "--recursive")
	cmdUpdate.Dir = worktreePath
	if output, err := cmdUpdate.CombinedOutput(); err != nil {
		return fmt.Errorf("git submodule update failed: %w\n%s", err, string(output))
	}

	// Switch each submodule to a parallel branch
	foreachCmd := fmt.Sprintf("git checkout -b %s 2>/dev/null || git checkout %s", branchName, branchName)
	cmdForeach := exec.CommandContext(ctx, "git", "submodule", "foreach", "--recursive", foreachCmd)
	cmdForeach.Dir = worktreePath
	if output, err := cmdForeach.CombinedOutput(); err != nil {
		fmt.Printf("⚠️  Warning during submodule branch switching (this is often safe):\n%s\n", string(output))
	}

	fmt.Printf("✓ Submodules initialized and switched to branch '%s' (standard mode).\n", branchName)
	return nil
}

// PrepareWorktree is a centralized function to get or create a worktree and fully configure it.
// It handles git worktree creation, submodule initialization, Go workspace setup, and state management.
func PrepareWorktree(ctx context.Context, gitRoot, worktreeName, planName string) (string, error) {
	wm := git.NewWorktreeManager()
	worktreePath, err := wm.GetOrPrepareWorktree(ctx, gitRoot, worktreeName, "")
	if err != nil {
		// If the error indicates the worktree already exists, we can proceed. This makes the function resilient.
		if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "is already checked out") {
			worktreePath = filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
		} else {
			return "", fmt.Errorf("failed to prepare worktree using git.WorktreeManager: %w", err)
		}
	}

	// NEW: Setup submodules with parallel branches.
	if err := SetupSubmodulesForWorktree(ctx, worktreePath, worktreeName); err != nil {
		// Log as a warning since some submodule operations might not be critical to the main task.
		fmt.Printf("Warning: failed to setup submodules for worktree '%s': %v\n", worktreeName, err)
	}

	// Existing logic for Go workspace and state management, now centralized here.
	if err := SetupGoWorkspaceForWorktree(worktreePath, gitRoot); err != nil {
		fmt.Printf("Warning: failed to setup Go workspace in worktree: %v\n", err)
	}

	// State management: ensure the worktree knows which plan it's associated with.
	groveDir := filepath.Join(worktreePath, ".grove")
	if err := os.MkdirAll(groveDir, 0755); err == nil {
		stateContent := fmt.Sprintf("active_plan: %s\n", planName)
		statePath := filepath.Join(worktreePath, ".grove", "state.yml")
		_ = os.WriteFile(statePath, []byte(stateContent), 0644)
	}

	return worktreePath, nil
}