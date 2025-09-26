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
	"time"

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

// DiscoverLocalWorkspaces uses grove ws list to find local repository paths
func DiscoverLocalWorkspaces(ctx context.Context) (map[string]string, error) {
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
			// Find the main worktree - prioritize is_main:true, but fall back to path matching
			mainPath := ""
			for _, wt := range ws.Worktrees {
				if wt.IsMain {
					mainPath = wt.Path
					break
				}
			}
			
			// If no is_main:true found, use path matching as fallback
			if mainPath == "" && len(ws.Worktrees) > 0 {
				// Normalize paths for comparison (handle case sensitivity)
				wsPathNorm := strings.ToLower(filepath.Clean(ws.Path))
				for _, wt := range ws.Worktrees {
					wtPathNorm := strings.ToLower(filepath.Clean(wt.Path))
					if wsPathNorm == wtPathNorm {
						mainPath = wt.Path
						break
					}
				}
				// If still no match, use the first worktree as a last resort
				if mainPath == "" {
					mainPath = ws.Worktrees[0].Path
				}
			}
			
			if mainPath != "" {
				result[ws.Name] = mainPath
				fmt.Printf("DEBUG: Test workspace %s at %s\n", ws.Name, mainPath)
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
		// Find the main worktree - prioritize is_main:true, but fall back to path matching
		mainPath := ""
		for _, wt := range ws.Worktrees {
			if wt.IsMain {
				mainPath = wt.Path
				break
			}
		}
		
		// If no is_main:true found, use path matching as fallback
		if mainPath == "" && len(ws.Worktrees) > 0 {
			// Normalize paths for comparison (handle case sensitivity)
			wsPathNorm := strings.ToLower(filepath.Clean(ws.Path))
			for _, wt := range ws.Worktrees {
				wtPathNorm := strings.ToLower(filepath.Clean(wt.Path))
				if wsPathNorm == wtPathNorm {
					mainPath = wt.Path
					break
				}
			}
			// If still no match, use the first worktree as a last resort
			if mainPath == "" {
				mainPath = ws.Worktrees[0].Path
			}
		}
		
		if mainPath != "" {
			result[ws.Name] = mainPath
			fmt.Printf("DEBUG: Found workspace %s at %s\n", ws.Name, mainPath)
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
	return SetupSubmodulesForWorktreeWithRepos(ctx, worktreePath, branchName, nil)
}

// SetupSubmodulesForWorktreeWithRepos is like SetupSubmodulesForWorktree but allows filtering which repos to include.
// If repos is nil or empty, all submodules are included.
func SetupSubmodulesForWorktreeWithRepos(ctx context.Context, worktreePath, branchName string, repos []string) error {
	// First, ensure all tracked files are checked out in the worktree
	cmdCheckout := exec.CommandContext(ctx, "git", "checkout", "HEAD", "--", ".")
	cmdCheckout.Dir = worktreePath
	if output, err := cmdCheckout.CombinedOutput(); err != nil {
		fmt.Printf("Warning: could not ensure all files checked out in worktree: %s\n", string(output))
	}

	gitmodulesPath := filepath.Join(worktreePath, ".gitmodules")
	if _, err := os.Stat(gitmodulesPath); os.IsNotExist(err) {
		fmt.Printf("No .gitmodules found at %s, skipping submodule setup\n", gitmodulesPath)
		return nil
	}

	fmt.Printf("✓ Initializing submodules in worktree at %s...\n", worktreePath)

	submodulePaths, err := parseGitmodules(gitmodulesPath)
	if err != nil {
		fmt.Printf("Warning: could not parse .gitmodules: %v. Falling back to standard init.\n", err)
		return setupSubmodulesStandard(ctx, worktreePath, branchName)
	}

	localWorkspaces, err := DiscoverLocalWorkspaces(ctx)
	if err != nil {
		fmt.Printf("Warning: failed to discover local workspaces: %v. Falling back to standard init.\n", err)
		return setupSubmodulesStandard(ctx, worktreePath, branchName)
	}

	// Create a filter map if repos is specified
	repoFilter := make(map[string]bool)
	if len(repos) > 0 {
		for _, repo := range repos {
			repoFilter[repo] = true
		}
	}
	
	// Derive gitRoot from worktreePath (parent of .grove-worktrees)
	gitRoot := filepath.Dir(filepath.Dir(worktreePath))
	
	var externalSubmodules []string

	// Process each submodule, creating linked worktrees for local ones
	for submoduleName, submodulePath := range submodulePaths {
		// Skip if repos filter is specified and this repo is not in the list
		if len(repoFilter) > 0 && !repoFilter[submoduleName] {
			fmt.Printf("  • %s: skipping (not in repos filter)\n", submoduleName)
			continue
		}
		targetPath := filepath.Join(worktreePath, submodulePath)
		
		// First check if we can create a linked worktree from the main checkout's submodule
		mainSubmodulePath := filepath.Join(gitRoot, submodulePath)
		
		// Try to initialize the submodule in the main checkout if it's not already
		if _, err := os.Stat(mainSubmodulePath); err == nil {
			// Directory exists, check if it's initialized
			if _, err := os.Stat(filepath.Join(mainSubmodulePath, ".git")); err != nil {
				// Submodule directory exists but not initialized, try to initialize it
				cmdInit := exec.CommandContext(ctx, "git", "submodule", "update", "--init", "--", submodulePath)
				cmdInit.Dir = gitRoot
				if _, err := cmdInit.CombinedOutput(); err != nil {
					fmt.Printf("    Note: could not initialize submodule %s in main checkout: %v\n", submoduleName, err)
				}
			}
		}
		
		// Check if the main submodule exists and is a git repository
		if _, err := os.Stat(filepath.Join(mainSubmodulePath, ".git")); err == nil {
			// We can create a linked worktree from the main checkout's submodule
			fmt.Printf("  • %s: creating linked worktree from main checkout at %s\n", submoduleName, mainSubmodulePath)

			// Ensure the parent directory exists
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				fmt.Printf("    Warning: could not create parent directory for submodule: %v\n", err)
				continue
			}

			// Check if the worktree already exists and has content
			if _, err := os.Stat(filepath.Join(targetPath, ".git")); err == nil {
				fmt.Printf("    Linked worktree already exists at %s, skipping creation\n", targetPath)
			} else {
				// Remove any existing directory to make room for the worktree
				if err := os.RemoveAll(targetPath); err != nil {
					fmt.Printf("    Warning: could not remove submodule directory: %v\n", err)
					continue
				}

				// Create a linked worktree from the main submodule checkout
				cmdWorktree := exec.CommandContext(ctx, "git", "worktree", "add", targetPath, "-B", branchName)
				cmdWorktree.Dir = mainSubmodulePath
				if output, err := cmdWorktree.CombinedOutput(); err != nil {
					fmt.Printf("    Warning: failed to create linked worktree from main checkout: %v\n%s\n", err, string(output))
					// Don't try to restore with standard submodule update, will handle below
				}
			}
			continue
		}
		
		// Fallback: Check if we have this submodule locally via grove ws list
		localRepoPath, hasLocal := localWorkspaces[submoduleName]
		if hasLocal {
			// We have a local version - replace the submodule with a linked worktree
			fmt.Printf("  • %s: creating linked worktree from local workspace at %s\n", submoduleName, localRepoPath)

			// Ensure the parent directory exists
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				fmt.Printf("    Warning: could not create parent directory for submodule: %v\n", err)
				continue
			}

			// Check if the worktree already exists and has content
			if _, err := os.Stat(filepath.Join(targetPath, ".git")); err == nil {
				fmt.Printf("    Linked worktree already exists at %s, skipping creation\n", targetPath)
			} else {
				// Remove any existing directory to make room for the worktree
				if err := os.RemoveAll(targetPath); err != nil {
					fmt.Printf("    Warning: could not remove existing submodule directory: %v\n", err)
					continue
				}

				cmdWorktree := exec.CommandContext(ctx, "git", "worktree", "add", targetPath, "-B", branchName)
				cmdWorktree.Dir = localRepoPath
				if output, err := cmdWorktree.CombinedOutput(); err != nil {
					fmt.Printf("    Warning: failed to create linked worktree for %s: %v\n%s\n", submoduleName, err, string(output))
				}
			}
		} else {
			// No local version, queue for standard submodule initialization
			fmt.Printf("  • %s: queuing for standard submodule initialization (external dependency)\n", submoduleName)
			externalSubmodules = append(externalSubmodules, submodulePath)
		}
	}

	// Only run git submodule update for external dependencies
	if len(externalSubmodules) > 0 {
		fmt.Printf("✓ Initializing %d external submodule(s)...\n", len(externalSubmodules))
		for _, submodulePath := range externalSubmodules {
			cmdUpdate := exec.CommandContext(ctx, "git", "submodule", "update", "--init", "--recursive", "--", submodulePath)
			cmdUpdate.Dir = worktreePath
			if output, err := cmdUpdate.CombinedOutput(); err != nil {
				fmt.Printf("    Warning: could not initialize external submodule %s: %v\n%s\n", submodulePath, err, string(output))
				// Continue with other submodules even if one fails
			}
		}

		// Switch external submodules to the parallel branch
		for _, submodulePath := range externalSubmodules {
			foreachCmd := fmt.Sprintf("git checkout -b %s 2>/dev/null || git checkout %s", branchName, branchName)
			cmdForeach := exec.CommandContext(ctx, "git", "-C", filepath.Join(worktreePath, submodulePath), "submodule", "foreach", "--recursive", foreachCmd)
			if output, err := cmdForeach.CombinedOutput(); err != nil {
				fmt.Printf("  Note: branch switching output for %s:\n%s\n", submodulePath, string(output))
			}
		}
	}

	fmt.Printf("✓ Submodules initialized with linked worktrees where possible.\n")
	return nil
}

// setupSubmodulesStandard is the fallback standard submodule setup
func setupSubmodulesStandard(ctx context.Context, worktreePath, branchName string) error {
	// Run standard git submodule update - but don't fail completely if some submodules can't be initialized
	cmdUpdate := exec.CommandContext(ctx, "git", "submodule", "update", "--init", "--recursive")
	cmdUpdate.Dir = worktreePath
	if output, err := cmdUpdate.CombinedOutput(); err != nil {
		// Log as warning but continue - some submodules might not have URLs in local ecosystems
		fmt.Printf("⚠️  Warning: git submodule update had issues (this is often safe for local ecosystems):\n%s\n", string(output))
	}

	// Switch each submodule to a parallel branch
	foreachCmd := fmt.Sprintf("git checkout -b %s 2>/dev/null || git checkout %s", branchName, branchName)
	cmdForeach := exec.CommandContext(ctx, "git", "submodule", "foreach", "--recursive", foreachCmd)
	cmdForeach.Dir = worktreePath
	if output, err := cmdForeach.CombinedOutput(); err != nil {
		fmt.Printf("⚠️  Warning during submodule branch switching (this is often safe):\n%s\n", string(output))
	}

	fmt.Printf("✓ Submodules initialized (standard mode).\n")
	return nil
}

// PrepareWorktree is a centralized function to get or create a worktree and fully configure it.
// It handles git worktree creation, submodule initialization, Go workspace setup, and state management.
func PrepareWorktree(ctx context.Context, gitRoot, worktreeName, planName string) (string, error) {
	return PrepareWorktreeWithRepos(ctx, gitRoot, worktreeName, planName, nil)
}

// PrepareWorktreeWithRepos is like PrepareWorktree but allows specifying which repos to include.
// When repos are specified, it creates an "ecosystem worktree" with individual worktrees for each repo.
func PrepareWorktreeWithRepos(ctx context.Context, gitRoot, worktreeName, planName string, repos []string) (string, error) {
	// If repos are specified, create an ecosystem worktree structure
	if len(repos) > 0 {
		return PrepareEcosystemWorktree(ctx, gitRoot, worktreeName, planName, repos)
	}

	// Otherwise, use the traditional approach
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
	if err := SetupSubmodulesForWorktreeWithRepos(ctx, worktreePath, worktreeName, repos); err != nil {
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

	// Create a generic workspace marker file for grove-meta to detect
	// This enables automatic workspace-aware binary resolution
	markerPath := filepath.Join(worktreePath, ".grove-workspace")
	markerContent := fmt.Sprintf("branch: %s\nplan: %s\ncreated_at: %s\n",
		worktreeName, planName, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(markerPath, []byte(markerContent), 0644); err != nil {
		fmt.Printf("Warning: could not create .grove-workspace marker file: %v\n", err)
	}

	return worktreePath, nil
}

// PrepareEcosystemWorktree creates a simplified ecosystem worktree structure.
// Instead of creating a worktree of the superproject and dealing with submodules,
// it creates individual worktrees for each specified repo directly from their source locations.
func PrepareEcosystemWorktree(ctx context.Context, gitRoot, worktreeName, planName string, repos []string) (string, error) {
	fmt.Printf("✓ Creating ecosystem worktree for repos: %v\n", repos)
	
	// Create the ecosystem directory structure
	ecosystemDir := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
	if err := os.MkdirAll(ecosystemDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create ecosystem directory: %w", err)
	}

	// Discover local workspace paths using grove ws list
	localWorkspaces, err := DiscoverLocalWorkspaces(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to discover local workspaces: %w", err)
	}

	// For filtering display purposes, show what repos are being filtered out
	// We'll use the local workspaces as the full set of available repos for filtering display
	repoFilter := make(map[string]bool)
	for _, repo := range repos {
		repoFilter[repo] = true
	}
	
	// Show filtering behavior for all available repos
	for repoName := range localWorkspaces {
		if !repoFilter[repoName] {
			fmt.Printf("  • %s: skipping (not in repos filter)\n", repoName)
		}
	}

	// Create individual worktrees for each specified repo
	for _, repo := range repos {
		repoPath, exists := localWorkspaces[repo]
		if !exists {
			fmt.Printf("Warning: repo '%s' not found in local workspaces, skipping\n", repo)
			continue
		}

		targetPath := filepath.Join(ecosystemDir, repo)
		fmt.Printf("  • %s: creating linked worktree\n", repo)

		// Check if the worktree already exists
		if _, err := os.Stat(filepath.Join(targetPath, ".git")); err == nil {
			fmt.Printf("    Worktree already exists, skipping creation\n")
			continue
		}

		// Remove any existing directory to make room for the worktree
		if err := os.RemoveAll(targetPath); err != nil {
			fmt.Printf("    Warning: could not remove existing directory: %v\n", err)
			continue
		}

		// Create a linked worktree from the source repo
		cmdWorktree := exec.CommandContext(ctx, "git", "worktree", "add", targetPath, "-B", worktreeName)
		cmdWorktree.Dir = repoPath
		if output, err := cmdWorktree.CombinedOutput(); err != nil {
			fmt.Printf("    Warning: failed to create worktree for %s: %v\n%s\n", repo, err, string(output))
			continue
		}

		fmt.Printf("    ✓ Created worktree on branch %s\n", worktreeName)
	}

	// Generate a go.work file in the ecosystem directory
	if err := generateGoWorkspaceForEcosystem(ecosystemDir, repos); err != nil {
		fmt.Printf("Warning: failed to generate go.work file: %v\n", err)
	}

	// Create state management for the ecosystem worktree
	groveDir := filepath.Join(ecosystemDir, ".grove")
	if err := os.MkdirAll(groveDir, 0755); err == nil {
		stateContent := fmt.Sprintf("active_plan: %s\necosystem_repos: %v\n", planName, repos)
		statePath := filepath.Join(ecosystemDir, ".grove", "state.yml")
		_ = os.WriteFile(statePath, []byte(stateContent), 0644)
	}

	// Create a generic workspace marker file for grove-meta to detect
	// This enables automatic workspace-aware binary resolution
	markerPath := filepath.Join(ecosystemDir, ".grove-workspace")
	markerContent := fmt.Sprintf("branch: %s\nplan: %s\ncreated_at: %s\n",
		worktreeName, planName, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(markerPath, []byte(markerContent), 0644); err != nil {
		fmt.Printf("Warning: could not create .grove-workspace marker file: %v\n", err)
	}

	// Create a .git file to prevent git from seeing this as part of the parent repository
	// This makes the ecosystem worktree directory appear as if it's not in a git repo
	gitFilePath := filepath.Join(ecosystemDir, ".git")
	gitFileContent := "gitdir: /dev/null\n"
	if err := os.WriteFile(gitFilePath, []byte(gitFileContent), 0644); err != nil {
		fmt.Printf("Warning: could not create .git isolation file: %v\n", err)
	}

	// Also add a .gitignore to ignore common files
	gitignorePath := filepath.Join(ecosystemDir, ".gitignore")
	gitignoreContent := "go.work.sum\n.DS_Store\n"
	_ = os.WriteFile(gitignorePath, []byte(gitignoreContent), 0644)

	fmt.Printf("✓ Ecosystem worktree created at %s\n", ecosystemDir)
	return ecosystemDir, nil
}

// generateGoWorkspaceForEcosystem creates a go.work file for the ecosystem worktree
func generateGoWorkspaceForEcosystem(ecosystemDir string, repos []string) error {
	var workspaceContent strings.Builder
	workspaceContent.WriteString("go 1.21\n\n")
	
	for _, repo := range repos {
		repoPath := filepath.Join(ecosystemDir, repo)
		// Check if this repo has a go.mod file
		if _, err := os.Stat(filepath.Join(repoPath, "go.mod")); err == nil {
			workspaceContent.WriteString(fmt.Sprintf("use ./%s\n", repo))
		}
	}

	workspacePath := filepath.Join(ecosystemDir, "go.work")
	if err := os.WriteFile(workspacePath, []byte(workspaceContent.String()), 0644); err != nil {
		return fmt.Errorf("failed to write go.work file: %w", err)
	}

	fmt.Printf("  ✓ Generated go.work file at %s\n", workspacePath)
	return nil
}