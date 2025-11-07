package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/mattsolo1/grove-core/config"
	"github.com/mattsolo1/grove-core/fs"
	"github.com/mattsolo1/grove-core/git"
	"github.com/mattsolo1/grove-core/pkg/workspace"
	"github.com/mattsolo1/grove-core/state"
	"github.com/mattsolo1/grove-core/util/sanitize"
	gexec "github.com/mattsolo1/grove-flow/pkg/exec"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	planFinishYes             bool
	planFinishDeleteBranch    bool
	planFinishDeleteRemote    bool
	planFinishPruneWorktree   bool
	planFinishCloseSession    bool
	planFinishArchive         bool
	planFinishCleanDevLinks   bool
	planFinishForce           bool
)

// repoStatus represents the merge status of a single repository
type repoStatus struct {
	Name   string
	Status string // "merged", "needs_merge", "needs_rebase", "not_found"
}

// cleanupItem represents a cleanup action that can be performed
type cleanupItem struct {
	Name        string
	Check       func() (string, error)
	Action      func() error
	Status      string
	IsAvailable bool
	IsEnabled   bool
	Details     []repoStatus // Optional detailed status information for complex items
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

// removeLinkedSubmoduleWorktrees removes linked worktrees from submodule source repositories
func removeLinkedSubmoduleWorktrees(ctx context.Context, gitRoot, worktreeName string, provider *workspace.Provider) error {
	worktreePath := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
	gitmodulesPath := filepath.Join(worktreePath, ".gitmodules")

	// Check if .gitmodules exists
	if _, err := os.Stat(gitmodulesPath); os.IsNotExist(err) {
		// No submodules to clean up
		return nil
	}

	// Parse .gitmodules
	submodulePaths, err := parseGitmodules(gitmodulesPath)
	if err != nil {
		return fmt.Errorf("failed to parse .gitmodules: %w", err)
	}

	// Discover local workspaces
	var localWorkspaces map[string]string
	if provider != nil {
		localWorkspaces = provider.LocalWorkspaces()
	} else {
		// If we can't discover workspaces, we can't clean up linked worktrees
		// but this shouldn't fail the entire cleanup
		return nil
	}
	
	// For each submodule, check if it's a linked worktree and remove it
	for submoduleName, submodulePath := range submodulePaths {
		// Path to the submodule worktree inside the superproject worktree
		submoduleWorktreePath := filepath.Join(worktreePath, submodulePath)
		
		// First, try to remove from main checkout's submodule
		mainSubmodulePath := filepath.Join(gitRoot, submodulePath)
		if _, err := os.Stat(filepath.Join(mainSubmodulePath, ".git")); err == nil {
			// Check if this is a worktree of the main submodule
			cmd := exec.CommandContext(ctx, "git", "worktree", "list", "--porcelain")
			cmd.Dir = mainSubmodulePath
			output, err := cmd.Output()
			if err == nil && strings.Contains(string(output), submoduleWorktreePath) {
				// This is a linked worktree of the main submodule, remove it
				fmt.Printf("    Removing linked worktree for %s\n", submoduleName)
				removeCmd := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", submoduleWorktreePath)
				removeCmd.Dir = mainSubmodulePath
				if err := removeCmd.Run(); err != nil {
					fmt.Printf("      Warning: failed to remove worktree from main checkout: %v\n", err)
				} else {
					continue // Successfully removed, skip to next submodule
				}
			}
		}
		
		// Fallback: try to remove from local workspace if it exists
		localRepoPath, hasLocal := localWorkspaces[submoduleName]
		if !hasLocal {
			continue // Not a linked worktree
		}
		
		// Check if this is actually a worktree of the local repo
		cmd := exec.CommandContext(ctx, "git", "worktree", "list", "--porcelain")
		cmd.Dir = localRepoPath
		output, err := cmd.Output()
		if err != nil {
			continue // Can't verify, skip
		}
		
		// Look for this worktree in the output
		if strings.Contains(string(output), submoduleWorktreePath) {
			// This is a linked worktree, remove it
			fmt.Printf("    Removing linked worktree for %s\n", submoduleName)
			removeCmd := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", submoduleWorktreePath)
			removeCmd.Dir = localRepoPath
			if err := removeCmd.Run(); err != nil {
				fmt.Printf("      Warning: failed to remove worktree: %v\n", err)
			}
		}
	}
	
	return nil
}

// NewPlanFinishCmd creates the `plan finish` command.
func NewPlanFinishCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "finish [directory]",
		Short: "Finish and clean up a plan and its associated worktree",
		Long: `Guides through the process of cleaning up a completed plan.
This can include removing the git worktree, deleting the branch, closing tmux sessions, and archiving the plan.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runPlanFinish,
	}

	cmd.Flags().BoolVarP(&planFinishYes, "yes", "y", false, "Automatically confirm all cleanup actions")
	cmd.Flags().BoolVar(&planFinishDeleteBranch, "delete-branch", false, "Delete the local git branch")
	cmd.Flags().BoolVar(&planFinishDeleteRemote, "delete-remote", false, "Delete the remote git branch")
	cmd.Flags().BoolVar(&planFinishPruneWorktree, "prune-worktree", false, "Remove the git worktree directory")
	cmd.Flags().BoolVar(&planFinishCloseSession, "close-session", false, "Close the associated tmux session")
	cmd.Flags().BoolVar(&planFinishCleanDevLinks, "clean-dev-links", false, "Clean up development binary links from the worktree")
	cmd.Flags().BoolVar(&planFinishArchive, "archive", false, "Archive the plan directory using 'nb archive'")
	cmd.Flags().BoolVar(&planFinishForce, "force", false, "Force git operations (use with caution)")

	return cmd
}

func runPlanFinish(cmd *cobra.Command, args []string) error {
	var dir string
	if len(args) > 0 {
		dir = args[0]
	}

	planPath, err := resolvePlanPathWithActiveJob(dir)
	if err != nil {
		return err
	}
	planName := filepath.Base(planPath)

	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	// Gather information - check for git root from current working directory
	cwd, _ := os.Getwd()
	gitRoot, err := git.GetGitRoot(cwd)
	if err != nil {
		gitRoot = "" // Continue without git-related actions
	}

	// Create a workspace provider for efficient lookups
	discoveryService := workspace.NewDiscoveryService(nil) // logger is optional
	discoveryResult, err := discoveryService.DiscoverAll()
	if err != nil {
		fmt.Printf("Warning: failed to discover workspaces for cleanup: %v\n", err)
	}
	var provider *workspace.Provider
	if discoveryResult != nil {
		provider = workspace.NewProvider(discoveryResult)
	}

	worktreeName := ""
	if plan.Config != nil {
		worktreeName = plan.Config.Worktree
	}

	executor := &gexec.RealCommandExecutor{}
	wm := git.NewWorktreeManager()

	branchName := worktreeName // Simple assumption: branch name matches worktree name
	sessionName := sanitize.SanitizeForTmuxSession(worktreeName)

	// Define cleanup items
	// Use a shared variable for repo details that the Check function can populate
	var sharedRepoDetails []repoStatus

	mergeItem := &cleanupItem{
		Name: "Merge/fast-forward submodules to main",
		Check: func() (string, error) {
			if worktreeName == "" || gitRoot == "" {
				return "N/A", nil
			}
			// Check if .grove/workspace file exists at git root (for ecosystem)
			workspaceFile := filepath.Join(gitRoot, ".grove", "workspace")

			if _, err := os.Stat(workspaceFile); os.IsNotExist(err) {
				return "N/A (not ecosystem)", nil
			}

			// Read and parse the workspace file
			data, err := os.ReadFile(workspaceFile)
			if err != nil {
				return "N/A (read error)", nil
			}

			// WorkspaceMetadata matches grove-meta/cmd/dev_workspace.go:WorkspaceMetadata
			var workspaceConfig struct {
				Branch    string   `yaml:"branch"`
				Plan      string   `yaml:"plan"`
				CreatedAt string   `yaml:"created_at"`
				Ecosystem bool     `yaml:"ecosystem"`
				Repos     []string `yaml:"repos,omitempty"`
			}

			if err := yaml.Unmarshal(data, &workspaceConfig); err != nil {
				return "N/A (parse error)", nil
			}

			if !workspaceConfig.Ecosystem || len(workspaceConfig.Repos) == 0 {
				return "N/A (not ecosystem)", nil
			}

			// Discover local workspaces and check status of each repo
			if provider == nil {
				return color.YellowString("Available (discovery failed)"), nil
			}
			localWorkspaces := provider.LocalWorkspaces()

			totalRepos := len(workspaceConfig.Repos)
			needsMerge := 0
			alreadyMerged := 0
			notFound := 0
			needsRebase := 0

			// Collect detailed status for each repo
			var repoDetails []repoStatus

			for _, repoName := range workspaceConfig.Repos {
				repoPath, exists := localWorkspaces[repoName]
				if !exists {
					notFound++
					repoDetails = append(repoDetails, repoStatus{Name: repoName, Status: "not_found"})
					continue
				}

				// Check if branch exists
				branchCheckCmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+worktreeName)
				branchCheckCmd.Dir = repoPath
				if err := branchCheckCmd.Run(); err != nil {
					notFound++
					repoDetails = append(repoDetails, repoStatus{Name: repoName, Status: "not_found"})
					continue
				}

				// Check if main exists
				mainCheckCmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/main")
				mainCheckCmd.Dir = repoPath
				if err := mainCheckCmd.Run(); err != nil {
					notFound++
					repoDetails = append(repoDetails, repoStatus{Name: repoName, Status: "not_found"})
					continue
				}

				// Check commits ahead
				aheadCmd := exec.Command("git", "rev-list", "--count", "main.."+worktreeName)
				aheadCmd.Dir = repoPath
				aheadOutput, err := aheadCmd.Output()
				if err != nil {
					repoDetails = append(repoDetails, repoStatus{Name: repoName, Status: "not_found"})
					continue
				}
				aheadCount := strings.TrimSpace(string(aheadOutput))

				if aheadCount == "0" || aheadCount == "" {
					alreadyMerged++
					repoDetails = append(repoDetails, repoStatus{Name: repoName, Status: "merged"})
					continue
				}

				// Check if fast-forward is possible
				mergeBaseCmd := exec.Command("git", "merge-base", "main", worktreeName)
				mergeBaseCmd.Dir = repoPath
				mergeBaseOutput, err := mergeBaseCmd.Output()
				if err != nil {
					repoDetails = append(repoDetails, repoStatus{Name: repoName, Status: "not_found"})
					continue
				}
				mergeBase := strings.TrimSpace(string(mergeBaseOutput))

				mainRevCmd := exec.Command("git", "rev-parse", "main")
				mainRevCmd.Dir = repoPath
				mainRevOutput, err := mainRevCmd.Output()
				if err != nil {
					repoDetails = append(repoDetails, repoStatus{Name: repoName, Status: "not_found"})
					continue
				}
				mainRev := strings.TrimSpace(string(mainRevOutput))

				if mergeBase == mainRev {
					needsMerge++
					repoDetails = append(repoDetails, repoStatus{Name: repoName, Status: "needs_merge"})
				} else {
					needsRebase++
					repoDetails = append(repoDetails, repoStatus{Name: repoName, Status: "needs_rebase"})
				}
			}

			// Store details in the shared variable
			sharedRepoDetails = repoDetails

			// Build status message
			var statusParts []string
			if needsMerge > 0 {
				statusParts = append(statusParts, color.YellowString("%d to merge", needsMerge))
			}
			if alreadyMerged > 0 {
				statusParts = append(statusParts, color.GreenString("%d merged", alreadyMerged))
			}
			if needsRebase > 0 {
				statusParts = append(statusParts, color.RedString("%d need rebase", needsRebase))
			}
			if notFound > 0 {
				statusParts = append(statusParts, color.New(color.Faint).Sprintf("%d skipped", notFound))
			}

			if len(statusParts) == 0 {
				return color.YellowString("Available"), nil
			}

			status := fmt.Sprintf("%d repos: %s", totalRepos, strings.Join(statusParts, ", "))
			return status, nil
		},
		Action: func() error {
				// Read the workspace file to get ecosystem configuration (at git root)
				workspaceFile := filepath.Join(gitRoot, ".grove", "workspace")

				data, err := os.ReadFile(workspaceFile)
				if err != nil {
					return fmt.Errorf("failed to read workspace file: %w", err)
				}

				// WorkspaceMetadata matches grove-meta/cmd/dev_workspace.go:WorkspaceMetadata
				var workspaceConfig struct {
					Branch    string   `yaml:"branch"`
					Plan      string   `yaml:"plan"`
					CreatedAt string   `yaml:"created_at"`
					Ecosystem bool     `yaml:"ecosystem"`
					Repos     []string `yaml:"repos,omitempty"`
				}

				if err := yaml.Unmarshal(data, &workspaceConfig); err != nil {
					return fmt.Errorf("failed to parse workspace file: %w", err)
				}

				if !workspaceConfig.Ecosystem || len(workspaceConfig.Repos) == 0 {
					return nil
				}

				fmt.Printf("    Merging/fast-forwarding submodule branches to main...\n")

				// Discover local workspaces
				if provider == nil {
					return fmt.Errorf("cannot merge submodules; workspace discovery failed")
				}
				localWorkspaces := provider.LocalWorkspaces()

				hasErrors := false
				for _, repoName := range workspaceConfig.Repos {
					repoPath, exists := localWorkspaces[repoName]
					if !exists {
						fmt.Printf("      Warning: repo '%s' not found in local workspaces, skipping\n", repoName)
						continue
					}

					// Check if the branch exists
					branchCheckCmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+worktreeName)
					branchCheckCmd.Dir = repoPath
					if err := branchCheckCmd.Run(); err != nil {
						// Branch doesn't exist, skip
						continue
					}

					// Check if main branch exists
					mainCheckCmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/main")
					mainCheckCmd.Dir = repoPath
					if err := mainCheckCmd.Run(); err != nil {
						fmt.Printf("      Warning: main branch not found in %s, skipping\n", repoName)
						continue
					}

					// Check if branch is ahead of main
					aheadCmd := exec.Command("git", "rev-list", "--count", "main.."+worktreeName)
					aheadCmd.Dir = repoPath
					aheadOutput, err := aheadCmd.Output()
					if err != nil {
						fmt.Printf("      Warning: failed to check commits ahead for %s: %v\n", repoName, err)
						continue
					}
					aheadCount := strings.TrimSpace(string(aheadOutput))

					if aheadCount == "0" || aheadCount == "" {
						// Already merged, skip
						continue
					}

					fmt.Printf("      • %s: merging %s commits to main\n", repoName, aheadCount)

					// Checkout main
					checkoutCmd := exec.Command("git", "checkout", "main")
					checkoutCmd.Dir = repoPath
					if output, err := checkoutCmd.CombinedOutput(); err != nil {
						fmt.Printf("        Error: failed to checkout main: %s\n", string(output))
						hasErrors = true
						continue
					}

					// Try to merge (fast-forward only)
					mergeCmd := exec.Command("git", "merge", "--ff-only", worktreeName)
					mergeCmd.Dir = repoPath
					if output, err := mergeCmd.CombinedOutput(); err != nil {
						outputStr := string(output)
						if strings.Contains(outputStr, "Not possible to fast-forward") {
							fmt.Printf("        Warning: cannot fast-forward %s (needs rebase), skipping\n", repoName)
						} else {
							fmt.Printf("        Error: failed to merge: %s\n", outputStr)
							hasErrors = true
						}
						continue
					}

					fmt.Printf("        ✓ Merged successfully\n")
				}

				if hasErrors {
					return fmt.Errorf("some submodules failed to merge")
				}

				return nil
			},
	}

	items := []*cleanupItem{
		mergeItem,
		{
			Name: "Mark plan as finished in .grove-plan.yml",
			Check: func() (string, error) {
				configPath := filepath.Join(planPath, ".grove-plan.yml")
				data, err := os.ReadFile(configPath)
				if err != nil {
					return "Not found", nil
				}
				var config map[string]interface{}
				if err := yaml.Unmarshal(data, &config); err != nil {
					return "Invalid YAML", nil
				}
				if status, ok := config["status"].(string); ok && status == "finished" {
					return color.GreenString("Already finished"), nil
				}
				return color.YellowString("Available"), nil
			},
			Action: func() error {
				configPath := filepath.Join(planPath, ".grove-plan.yml")
				data, err := os.ReadFile(configPath)
				if err != nil && !os.IsNotExist(err) {
					return err
				}
				var config map[string]interface{}
				if len(data) > 0 {
					if err := yaml.Unmarshal(data, &config); err != nil {
						return err
					}
				}
				if config == nil {
					config = make(map[string]interface{})
				}
				config["status"] = "finished"
				newData, err := yaml.Marshal(config)
				if err != nil {
					return err
				}
				return os.WriteFile(configPath, newData, 0644)
			},
		},
		{
			Name: "Prune git worktree",
			Check: func() (string, error) {
				if worktreeName == "" || gitRoot == "" {
					return "N/A", nil
				}
				worktrees, err := wm.ListWorktrees(context.Background(), gitRoot)
				if err != nil {
					return "Error", err
				}
				for _, wt := range worktrees {
					if strings.HasSuffix(wt.Path, worktreeName) {
						// Check if worktree has modifications or untracked files
						worktreePath := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
						statusOutput, statusErr := exec.Command("git", "-C", worktreePath, "status", "--porcelain").Output()
						if statusErr != nil {
							return color.YellowString("Exists"), nil // Default to exists if we can't check
						}
						if strings.TrimSpace(string(statusOutput)) != "" {
							return color.RedString("Has changes (needs --force)"), nil
						}
						return color.YellowString("Exists"), nil
					}
				}
				return "Not found", nil
			},
			Action: func() error {
				worktreePath := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
				
				// Check if this is an ecosystem worktree (has repos configuration)
				if plan.Config != nil && len(plan.Config.Repos) > 0 {
					return cleanupEcosystemWorktree(context.Background(), gitRoot, worktreeName, plan.Config.Repos, provider)
				}
				
				// Check if worktree has submodules
				hasSubmodules := false
				if _, err := os.Stat(filepath.Join(worktreePath, ".gitmodules")); err == nil {
					hasSubmodules = true
				}
				
				// First, remove any linked submodule worktrees
				if hasSubmodules {
					if err := removeLinkedSubmoduleWorktrees(context.Background(), gitRoot, worktreeName, provider); err != nil {
						fmt.Printf("    Warning: failed to remove linked submodule worktrees: %v\n", err)
					}
				}
				
				// Now remove the main worktree
				// Check if we need to use --force
				removeCmd := "git"
				removeArgs := []string{"worktree", "remove"}
				if planFinishForce {
					removeArgs = append(removeArgs, "--force")
				}
				removeArgs = append(removeArgs, worktreePath)
				
				// Try removal
				err := executor.Execute(removeCmd, removeArgs...)
				
				// Handle known Git limitation with submodules
				if err != nil && hasSubmodules && strings.Contains(err.Error(), "working trees containing submodules") {
					// Git won't remove it, but we can do it safely ourselves
					fmt.Printf("    Note: Git won't remove worktrees with submodules, removing manually...\n")
					
					// First, make sure we're not deleting something important
					// Check if there are uncommitted changes (but ignore submodule status)
					statusCmd := exec.Command("git", "-C", worktreePath, "status", "--porcelain", "--ignore-submodules")
					if statusOutput, statusErr := statusCmd.Output(); statusErr == nil {
						if strings.TrimSpace(string(statusOutput)) != "" && !planFinishForce {
							fmt.Printf("    Warning: Worktree has uncommitted changes. Use --force to remove anyway.\n")
							return fmt.Errorf("worktree has uncommitted changes")
						}
					}
					
					// Remove the directory
					if err := os.RemoveAll(worktreePath); err != nil {
						fmt.Printf("    Error: Failed to remove worktree directory: %v\n", err)
						return err
					}
					
					// Clean up git's worktree metadata
					pruneCmd := exec.Command("git", "-C", gitRoot, "worktree", "prune")
					if err := pruneCmd.Run(); err != nil {
						fmt.Printf("    Warning: Failed to prune worktree metadata: %v\n", err)
					}
					
					fmt.Printf("    ✓ Worktree removed successfully\n")
					return nil // Success
				}
				
				// Handle other errors
				if err != nil && !planFinishForce && strings.Contains(err.Error(), "contains modified or untracked files") {
					fmt.Printf("    Retrying with --force due to modified files...\n")
					return executor.Execute("git", "worktree", "remove", "--force", worktreePath)
				}
				
				return err
			},
		},
		{
			Name: "Delete submodule branches",
			Check: func() (string, error) {
				if branchName == "" || gitRoot == "" {
					return "N/A", nil
				}
				if _, err := os.Stat(filepath.Join(gitRoot, ".gitmodules")); os.IsNotExist(err) {
					return "N/A (no submodules)", nil
				}
				// A simple check is sufficient; the action will handle non-existent branches.
				return color.YellowString("Available"), nil
			},
			Action: func() error {
				// First try the standard approach for regular submodules
				foreachCmd := fmt.Sprintf("git branch -D %s 2>/dev/null || true", branchName)
				cmd := exec.Command("git", "-C", gitRoot, "submodule", "foreach", foreachCmd)
				_ = cmd.Run() // Ignore errors as branches may not exist
				
				// Now handle linked worktree submodules
				gitmodulesPath := filepath.Join(gitRoot, ".gitmodules")
				if _, err := os.Stat(gitmodulesPath); err == nil {
					// Parse .gitmodules
					submodulePaths, _ := parseGitmodules(gitmodulesPath)

					// Discover local workspaces
					var localWorkspaces map[string]string
					if provider != nil {
						localWorkspaces = provider.LocalWorkspaces()
					} else {
						localWorkspaces = make(map[string]string)
					}

					// Delete branches and worktrees from repositories
					for submoduleName, submodulePath := range submodulePaths {
						worktreePath := filepath.Join(gitRoot, ".grove-worktrees", branchName, submodulePath)
						
						// First try to remove worktree from main checkout's submodule
						mainSubmodulePath := filepath.Join(gitRoot, submodulePath)
						if _, err := os.Stat(filepath.Join(mainSubmodulePath, ".git")); err == nil {
							// Remove the linked worktree from main checkout's submodule
							removeWorktreeCmd := exec.Command("git", "-C", mainSubmodulePath, "worktree", "remove", "--force", worktreePath)
							if output, err := removeWorktreeCmd.CombinedOutput(); err != nil {
								// Ignore errors - worktree might not exist or already be removed
								if !strings.Contains(string(output), "not a working tree") && !strings.Contains(string(output), "No such file") {
									fmt.Printf("    Note: could not remove worktree for %s from main checkout: %s\n", submoduleName, string(output))
								}
							}
							
							// Delete the branch from the main checkout's submodule
							deleteCmd := exec.Command("git", "-C", mainSubmodulePath, "branch", "-D", branchName)
							if output, err := deleteCmd.CombinedOutput(); err != nil {
								// Only report if it's not a "branch not found" error
								if !strings.Contains(string(output), "not found") {
									fmt.Printf("    Note: could not delete branch '%s' from %s main checkout: %v\n", branchName, submoduleName, err)
								}
							}
						}
						
						// Also try to clean up from local workspace if it exists
						if localRepoPath, hasLocal := localWorkspaces[submoduleName]; hasLocal {
							// Remove the linked worktree if it exists
							removeWorktreeCmd := exec.Command("git", "-C", localRepoPath, "worktree", "remove", "--force", worktreePath)
							if output, err := removeWorktreeCmd.CombinedOutput(); err != nil {
								// Ignore errors - worktree might not exist or already be removed
								if !strings.Contains(string(output), "not a working tree") && !strings.Contains(string(output), "No such file") {
									fmt.Printf("    Note: could not remove worktree for %s from local workspace: %s\n", submoduleName, string(output))
								}
							}
							
							// Delete the branch from the local workspace repository
							deleteCmd := exec.Command("git", "-C", localRepoPath, "branch", "-D", branchName)
							if output, err := deleteCmd.CombinedOutput(); err != nil {
								// Only report if it's not a "branch not found" error
								if !strings.Contains(string(output), "not found") {
									fmt.Printf("    Warning: failed to delete branch '%s' from %s local workspace: %v\n", branchName, submoduleName, err)
								}
							}
						}
					}
				}
				
				return nil
			},
		},
		{
			Name: "Delete local git branch",
			Check: func() (string, error) {
				if branchName == "" || gitRoot == "" {
					return "N/A", nil
				}
				output, err := exec.Command("git", "-C", gitRoot, "branch", "--list", branchName).Output()
				if err != nil {
					return "Error", err
				}
				if strings.TrimSpace(string(output)) == "" {
					return "Not found", nil
				}
				
				// Check if branch has commits ahead of the default branch (try main, then master)
				baseBranches := []string{"main", "master"}
				for _, baseBranch := range baseBranches {
					// Check if base branch exists
					_, branchCheckErr := exec.Command("git", "-C", gitRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+baseBranch).Output()
					if branchCheckErr != nil {
						continue // Base branch doesn't exist, try next
					}
					
					aheadOutput, aheadErr := exec.Command("git", "-C", gitRoot, "rev-list", "--count", baseBranch+".."+branchName).Output()
					if aheadErr == nil {
						aheadCount := strings.TrimSpace(string(aheadOutput))
						if aheadCount != "0" && aheadCount != "" {
							return color.RedString("Has " + aheadCount + " commits ahead of " + baseBranch), nil
						}
					}
					break // Found a valid base branch, stop checking
				}
				
				// Check if branch is checked out in any worktree
				worktreeList, wtErr := exec.Command("git", "-C", gitRoot, "worktree", "list").Output()
				if wtErr != nil {
					return color.YellowString("Exists"), nil // Default to exists if we can't check
				}
				
				worktreeLines := strings.Split(string(worktreeList), "\n")
				for _, line := range worktreeLines {
					if strings.Contains(line, "["+branchName+"]") {
						return color.RedString("Checked out in worktree"), nil
					}
				}
				
				return color.YellowString("Exists"), nil
			},
			Action: func() error {
				// Try regular delete first
				err := executor.Execute("git", "-C", gitRoot, "branch", "-d", branchName)
				if err != nil {
					if strings.Contains(err.Error(), "checked out at") {
						// Branch is checked out in a worktree - just force delete it
						// By this point, the worktree should have been removed already
						fmt.Printf("    Using -D (force) to delete branch that was in worktree...\n")
						return executor.Execute("git", "-C", gitRoot, "branch", "-D", branchName)
					} else if strings.Contains(err.Error(), "not fully merged") {
						// Branch has unmerged commits, use force delete
						fmt.Printf("    Using -D (force) due to unmerged commits...\n")
						return executor.Execute("git", "-C", gitRoot, "branch", "-D", branchName)
					}
				}
				return err
			},
		},
		{
			Name: "Delete remote git branch",
			Check: func() (string, error) {
				if branchName == "" || gitRoot == "" {
					return "N/A", nil
				}
				
				// Check if remote branch exists (try origin first)
				remoteOutput, remoteErr := exec.Command("git", "-C", gitRoot, "ls-remote", "--heads", "origin", branchName).Output()
				if remoteErr != nil {
					return "N/A (no remote)", nil
				}
				
				if strings.TrimSpace(string(remoteOutput)) == "" {
					return "Not found", nil
				}
				
				return color.YellowString("Exists on origin"), nil
			},
			Action: func() error {
				// Delete remote branch
				return executor.Execute("git", "-C", gitRoot, "push", "origin", "--delete", branchName)
			},
		},
		{
			Name: "Close tmux session",
			Check: func() (string, error) {
				if sessionName == "" {
					return "N/A", nil
				}
				err := executor.Execute("tmux", "has-session", "-t", sessionName)
				if err == nil {
					return color.YellowString("Running"), nil
				}
				return "Not found", nil
			},
			Action: func() error {
				return executor.Execute("tmux", "kill-session", "-t", sessionName)
			},
		},
		{
			Name: "Clean up dev binaries from worktree",
			Check: func() (string, error) {
				if worktreeName == "" || gitRoot == "" {
					return "N/A", nil
				}
				worktreePath := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
				// Check if grove dev is available
				if _, err := exec.LookPath("grove"); err != nil {
					return "N/A (grove not found)", nil
				}
				// Check if there are any dev links from this worktree
				output, err := exec.Command("grove", "dev", "list").Output()
				if err != nil {
					return "Error checking", nil
				}
				if strings.Contains(string(output), worktreePath) {
					return color.YellowString("Has links"), nil
				}
				return "No links", nil
			},
			Action: func() error {
				if worktreeName == "" || gitRoot == "" {
					return nil
				}
				worktreePath := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
				// Get list of all binaries and their links
				output, err := exec.Command("grove", "dev", "list").Output()
				if err != nil {
					return fmt.Errorf("failed to list dev binaries: %w", err)
				}
				
				// Parse output to find links from this worktree
				lines := strings.Split(string(output), "\n")
				var currentBinary string
				linksToRemove := make(map[string][]string) // binary -> aliases
				
				for _, line := range lines {
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "Binary: ") {
						currentBinary = strings.TrimPrefix(line, "Binary: ")
					} else if currentBinary != "" && strings.Contains(line, worktreePath) {
						// Extract alias from line like "  alias (/path/to/worktree)"
						parts := strings.Fields(line)
						if len(parts) >= 1 {
							alias := strings.TrimPrefix(parts[0], "* ")
							if linksToRemove[currentBinary] == nil {
								linksToRemove[currentBinary] = []string{}
							}
							linksToRemove[currentBinary] = append(linksToRemove[currentBinary], alias)
						}
					}
				}
				
				// Remove each link and potentially switch back to main
				for binary, aliases := range linksToRemove {
					for _, alias := range aliases {
						// Check if this is the current link (marked with *)
						isCurrent := false
						for _, line := range lines {
							if strings.HasPrefix(line, "* "+alias+" ") && strings.Contains(line, worktreePath) {
								isCurrent = true
								break
							}
						}
						
						// Remove the link
						if err := executor.Execute("grove", "dev", "unlink", binary, alias); err != nil {
							// Log but don't fail - link might already be gone
							fmt.Printf("    Warning: failed to unlink %s:%s: %v\n", binary, alias, err)
							continue
						}
						
						// If this was the current link, try to switch back to the main repo version
						if isCurrent {
							// Look for a link from the main repo (not in .grove-worktrees)
							var mainAlias string
							inBinarySection := false
							for _, line := range lines {
								if strings.HasPrefix(line, "Binary: " + binary) {
									inBinarySection = true
								} else if strings.HasPrefix(line, "Binary: ") {
									inBinarySection = false
								} else if inBinarySection && strings.Contains(line, "(") && strings.Contains(line, ")") {
									// Extract path from line like "  alias (/path/to/repo)"
									start := strings.Index(line, "(")
									end := strings.Index(line, ")")
									if start != -1 && end != -1 {
										path := line[start+1:end]
										// Check if this is a main repo (not a worktree)
										if !strings.Contains(path, ".grove-worktrees") {
											parts := strings.Fields(line[:start])
											if len(parts) >= 1 {
												mainAlias = strings.TrimPrefix(parts[0], "* ")
												break
											}
										}
									}
								}
							}
							
							// Switch to main alias if found
							if mainAlias != "" {
								if err := executor.Execute("grove", "dev", "use", binary, mainAlias); err != nil {
									fmt.Printf("    Warning: failed to switch %s back to %s: %v\n", binary, mainAlias, err)
								}
							}
						}
					}
				}
				return nil
			},
		},
		{
			Name: "Archive plan directory with 'nb'",
			Check: func() (string, error) {
				// Check if plan is inside an nb workspace with /plans/ directory
				// Expected format: ~/Documents/nb/repos/{repo}/main/plans/{plan}
				homeDir, _ := os.UserHomeDir()
				nbRoot := filepath.Join(homeDir, "Documents", "nb")

				if !strings.HasPrefix(planPath, nbRoot) {
					return "N/A (not in nb workspace)", nil
				}

				// Check if path contains /plans/
				if !strings.Contains(planPath, string(filepath.Separator)+"plans"+string(filepath.Separator)) {
					return "N/A (not in plans directory)", nil
				}

				return color.YellowString("Available"), nil
			},
			Action: func() error {
				// Use NotebookLocator to determine the correct archive path
				// Load config and create NotebookLocator
				coreCfg, err := config.LoadDefault()
				if err != nil {
					// Proceed with empty config if none exists (Local Mode)
					coreCfg = &config.Config{}
				}

				locator := workspace.NewNotebookLocator(coreCfg)

				// Get the workspace context for the current plan
				// We need to find the workspace node for this plan
				gitRoot, err := git.GetGitRoot(planPath)
				if err != nil {
					return fmt.Errorf("failed to find git root: %w", err)
				}

				// Get the workspace node
				workspaceNode, err := workspace.GetProjectByPath(gitRoot)
				if err != nil {
					return fmt.Errorf("failed to get workspace node: %w", err)
				}

				// Get the archive directory for plans using NotebookLocator
				// For centralized mode: notebooks/{workspace}/notes/archive/
				// For local mode: {project}/.notebook/notes/archive/
				archiveNotesDir, err := locator.GetNotesDir(workspaceNode, "archive")
				if err != nil {
					return fmt.Errorf("failed to get archive notes dir: %w", err)
				}

				// Plans should go into archive/plans/ subdirectory
				archiveDir := filepath.Join(archiveNotesDir, "plans")
				planName := filepath.Base(planPath)
				archivePath := filepath.Join(archiveDir, planName)

				// Create archive directory if it doesn't exist
				if err := os.MkdirAll(archiveDir, 0755); err != nil {
					return fmt.Errorf("failed to create archive directory: %w", err)
				}

				// Check if destination already exists
				if _, err := os.Stat(archivePath); err == nil {
					return fmt.Errorf("archive destination already exists: %s", archivePath)
				}

				// Move the directory (rename is more efficient than copy+delete)
				if err := os.Rename(planPath, archivePath); err != nil {
					// If rename fails (e.g., cross-device), fall back to copy
					if err := fs.CopyDir(planPath, archivePath); err != nil {
						return fmt.Errorf("failed to copy plan to archive: %w", err)
					}
					// Remove original after successful copy
					if err := os.RemoveAll(planPath); err != nil {
						return fmt.Errorf("failed to remove original plan directory: %w", err)
					}
				}

				fmt.Printf("    Archived plan to: %s\n", archivePath)
				return nil
			},
		},
	}

	// Populate status and availability
	for _, item := range items {
		status, _ := item.Check()
		item.Status = status

		// Copy shared repo details to mergeItem after its Check has been called
		if item == mergeItem && len(sharedRepoDetails) > 0 {
			item.Details = sharedRepoDetails
		}

		// Mark as available if it's a positive state (yellow/green) or warning state (red) that can still be attempted
		if status == color.YellowString("Available") || 
		   status == color.YellowString("Exists") || 
		   status == color.YellowString("Exists on origin") ||
		   status == color.YellowString("Running") ||
		   status == color.YellowString("Has links") ||
		   status == color.RedString("Has changes (needs --force)") ||
		   status == color.RedString("Checked out in worktree") ||
		   strings.Contains(status, "commits ahead of") {
			item.IsAvailable = true
		}
	}

	// Check if branch exists and is merged (no commits ahead of main)
	branchIsMerged := false
	branchExists := false
	if branchName != "" && gitRoot != "" {
		// First check if the branch exists
		branchCheckCmd := exec.Command("git", "-C", gitRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branchName)
		if branchCheckCmd.Run() == nil {
			branchExists = true

			// Branch exists, now check if it's merged
			baseBranches := []string{"main", "master"}
			for _, baseBranch := range baseBranches {
				// Check if base branch exists
				_, baseCheckErr := exec.Command("git", "-C", gitRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+baseBranch).Output()
				if baseCheckErr != nil {
					continue // Base branch doesn't exist, try next
				}

				aheadOutput, aheadErr := exec.Command("git", "-C", gitRoot, "rev-list", "--count", baseBranch+".."+branchName).Output()
				if aheadErr == nil {
					aheadCount := strings.TrimSpace(string(aheadOutput))
					if aheadCount == "0" || aheadCount == "" {
						branchIsMerged = true
					}
				}
				break // Found a valid base branch, stop checking
			}
		}
	}

	// Determine which items to enable
	anyExplicitFlags := planFinishDeleteBranch || planFinishDeleteRemote || planFinishPruneWorktree || planFinishCloseSession || planFinishCleanDevLinks || planFinishArchive || planFinishForce
	if planFinishYes {
		for _, item := range items {
			item.IsEnabled = item.IsAvailable
		}
	} else if anyExplicitFlags {
		// Always enable merging submodules and marking as finished
		items[0].IsEnabled = items[0].IsAvailable                                          // Merge/fast-forward submodules to main
		items[1].IsEnabled = items[1].IsAvailable                                          // Mark plan as finished
		items[2].IsEnabled = planFinishPruneWorktree && items[2].IsAvailable              // Prune git worktree
		items[3].IsEnabled = planFinishDeleteBranch && items[3].IsAvailable               // Delete submodule branches
		items[4].IsEnabled = planFinishDeleteBranch && items[4].IsAvailable               // Delete local git branch
		items[5].IsEnabled = planFinishDeleteRemote && items[5].IsAvailable               // Delete remote git branch
		items[6].IsEnabled = planFinishCloseSession && items[6].IsAvailable               // Close tmux session
		items[7].IsEnabled = planFinishCleanDevLinks && items[7].IsAvailable              // Clean up dev binaries
		items[8].IsEnabled = planFinishArchive && items[8].IsAvailable                    // Archive plan directory
	} else {
		// Interactive TUI mode
		err := runFinishTUI(planName, items, branchIsMerged, branchExists)
		if err != nil {
			if err.Error() == "user aborted" {
				fmt.Println("\nCleanup aborted.")
				return nil
			}
			return err
		}
	}

	// Execute enabled actions
	fmt.Println("\nPerforming selected actions...")
	executed := false
	for _, item := range items {
		if item.IsEnabled {
			executed = true
			fmt.Printf("  - %-40s... ", item.Name)
			err := item.Action()
			if err != nil {
				fmt.Println(color.RedString("Failed"))
				fmt.Printf("    %s\n", err)
			} else {
				fmt.Println(color.GreenString("Done"))
			}
		}
	}

	if !executed {
		fmt.Println("No actions selected.")
	}

	// Check if the finished plan was the active plan and unset it
	activePlan, err := getActivePlanWithMigration()
	if err == nil && activePlan == planName {
		if err := state.Delete("flow.active_plan"); err != nil {
			fmt.Printf("Warning: could not unset active plan: %v\n", err)
		} else {
			// Also delete legacy key
			_ = state.Delete("active_plan")
			fmt.Println("\n✓ Unset active plan")
		}
	}

	fmt.Println("\nPlan cleanup finished.")
	return nil
}

// cleanupEcosystemWorktree removes an ecosystem worktree by cleaning up individual repo worktrees
func cleanupEcosystemWorktree(ctx context.Context, gitRoot, worktreeName string, repos []string, provider *workspace.Provider) error {
	ecosystemDir := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
	fmt.Printf("    Cleaning up ecosystem worktree at %s\n", ecosystemDir)

	// Discover local workspace paths
	var localWorkspaces map[string]string
	if provider != nil {
		localWorkspaces = provider.LocalWorkspaces()
	} else {
		fmt.Printf("    Warning: workspace discovery failed, cannot clean up submodule branches\n")
		localWorkspaces = make(map[string]string)
	}

	// Remove individual repo worktrees and delete branches
	for _, repo := range repos {
		repoWorktreePath := filepath.Join(ecosystemDir, repo)
		fmt.Printf("    • %s: removing worktree and branch\n", repo)
		
		// Find the source repository path
		repoPath, exists := localWorkspaces[repo]
		if !exists {
			fmt.Printf("      Warning: repo '%s' not found in local workspaces, skipping branch cleanup\n", repo)
			// Still try to remove the directory if it exists
			if err := os.RemoveAll(repoWorktreePath); err != nil {
				fmt.Printf("      Warning: failed to remove directory %s: %v\n", repoWorktreePath, err)
			}
			continue
		}

		// Remove the worktree from the source repository
		removeWorktreeCmd := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", repoWorktreePath)
		removeWorktreeCmd.Dir = repoPath
		if output, err := removeWorktreeCmd.CombinedOutput(); err != nil {
			// If worktree removal fails, try to remove the directory manually
			if !strings.Contains(string(output), "not a working tree") && !strings.Contains(string(output), "No such file") {
				fmt.Printf("      Warning: git worktree remove failed, removing directory manually: %s\n", string(output))
			}
			if err := os.RemoveAll(repoWorktreePath); err != nil {
				fmt.Printf("      Warning: failed to remove directory %s: %v\n", repoWorktreePath, err)
			}
		}

		// Delete the branch from the source repository
		deleteBranchCmd := exec.CommandContext(ctx, "git", "branch", "-D", worktreeName)
		deleteBranchCmd.Dir = repoPath
		if output, err := deleteBranchCmd.CombinedOutput(); err != nil {
			if !strings.Contains(string(output), "not found") {
				fmt.Printf("      Warning: failed to delete branch '%s' from %s: %s\n", worktreeName, repo, string(output))
			}
		} else {
			fmt.Printf("      ✓ Deleted branch '%s'\n", worktreeName)
		}
	}

	// Remove the entire ecosystem directory
	if err := os.RemoveAll(ecosystemDir); err != nil {
		return fmt.Errorf("failed to remove ecosystem directory: %w", err)
	}

	fmt.Printf("    ✓ Ecosystem worktree removed successfully\n")
	return nil
}