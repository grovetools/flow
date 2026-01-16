package cmd

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/fatih/color"
	"github.com/grovetools/core/fs"
	"github.com/grovetools/core/git"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/state"
	"github.com/grovetools/core/util/sanitize"
	gexec "github.com/grovetools/flow/pkg/exec"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/sirupsen/logrus"
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
	planFinishRebuildBinaries bool
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
		Short: "Finish and clean up a plan and its associated worktree (use: flow finish)",
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
	cmd.Flags().BoolVar(&planFinishRebuildBinaries, "rebuild-binaries", false, "Rebuild binaries in the main repository")
	cmd.Flags().BoolVar(&planFinishArchive, "archive", false, "Archive the plan directory to a local .archive subdirectory")
	cmd.Flags().BoolVar(&planFinishForce, "force", false, "Force git operations (use with caution)")

	return cmd
}

// NewFinishCmd creates the top-level `finish` command.
func NewFinishCmd() *cobra.Command {
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
	cmd.Flags().BoolVar(&planFinishRebuildBinaries, "rebuild-binaries", false, "Rebuild binaries in the main repository")
	cmd.Flags().BoolVar(&planFinishArchive, "archive", false, "Archive the plan directory to a local .archive subdirectory")
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

	// Check if plan is ready for cleanup (must be in review or finished status)
	if plan.Config == nil || (plan.Config.Status != "review" && plan.Config.Status != "finished") {
		return fmt.Errorf("plan is not ready for cleanup. Please run 'flow plan review %s' first.", planName)
	}

	// If status is finished, it's a legacy plan or already processed, so we allow cleanup but warn.
	if plan.Config != nil && plan.Config.Status == "finished" {
		fmt.Println("WARNING:  Warning: This plan is already 'finished'. The new workflow uses a 'review' step.")
		fmt.Println("   Running cleanup directly. In the future, please run 'flow plan review' first.")
	}

	// Gather information - check for git root from current working directory
	cwd, _ := os.Getwd()
	gitRoot, err := git.GetGitRoot(cwd)
	if err != nil {
		gitRoot = "" // Continue without git-related actions
	}

	// Create a workspace provider for efficient lookups
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel) // Suppress discoverer's debug output
	discoveryService := workspace.NewDiscoveryService(logger)
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

					fmt.Printf("        * Merged successfully\n")
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
			Name: "Cleanup Docker Compose environment",
			Check: func() (string, error) {
				// Check if plan was created from a recipe with Docker Compose actions
				if plan.Config == nil || plan.Config.Recipe == "" {
					return "N/A (no recipe)", nil
				}

				// Load the recipe
				flowCfg, _ := loadFlowConfig()
				var getRecipeCmd string
				if flowCfg != nil {
					_, getRecipeCmd, _ = loadFlowConfigWithDynamicRecipes()
				}

				recipe, err := orchestration.GetRecipe(plan.Config.Recipe, getRecipeCmd)
				if err != nil {
					return "N/A (recipe not found)", nil
				}

				// Check for docker_compose actions in init and named actions
				var dockerComposeProjects []string
				allActions := recipe.InitActions
				for _, namedActionList := range recipe.NamedActions {
					allActions = append(allActions, namedActionList...)
				}

				for _, action := range allActions {
					if action.Type == "docker_compose" && action.ProjectName != "" {
						// Render the project name template
						tmpl, err := template.New("project-name").Parse(action.ProjectName)
						if err != nil {
							continue
						}
						templateData := struct {
							PlanName string
						}{
							PlanName: planName,
						}
						var renderedProjectName bytes.Buffer
						if err := tmpl.Execute(&renderedProjectName, templateData); err != nil {
							continue
						}
						projectName := renderedProjectName.String()
						if projectName != "" {
							dockerComposeProjects = append(dockerComposeProjects, projectName)
						}
					}
				}

				if len(dockerComposeProjects) == 0 {
					return "N/A (no Docker services)", nil
				}

				// Check if any of the projects have running containers
				hasRunning := false
				for _, projectName := range dockerComposeProjects {
					cmd := exec.Command("docker", "ps", "--filter", fmt.Sprintf("label=com.docker.compose.project=%s", projectName), "--format", "{{.Names}}")
					output, err := cmd.Output()
					if err == nil && strings.TrimSpace(string(output)) != "" {
						hasRunning = true
						break
					}
				}

				if hasRunning {
					return color.YellowString("Running containers found"), nil
				}
				// Return Available even if no containers running, so item shows in TUI
				return color.YellowString("Available"), nil
			},
			Action: func() error {
				// Load the recipe
				flowCfg, _ := loadFlowConfig()
				var getRecipeCmd string
				if flowCfg != nil {
					_, getRecipeCmd, _ = loadFlowConfigWithDynamicRecipes()
				}

				recipe, err := orchestration.GetRecipe(plan.Config.Recipe, getRecipeCmd)
				if err != nil {
					return fmt.Errorf("failed to load recipe: %w", err)
				}

				// Collect all docker_compose actions
				allActions := recipe.InitActions
				for _, namedActionList := range recipe.NamedActions {
					allActions = append(allActions, namedActionList...)
				}

				// Tear down each Docker Compose project
				foundAny := false
				for _, action := range allActions {
					if action.Type == "docker_compose" && action.ProjectName != "" {
						// Render the project name template
						tmpl, err := template.New("project-name").Parse(action.ProjectName)
						if err != nil {
							continue
						}
						templateData := struct {
							PlanName string
						}{
							PlanName: planName,
						}
						var renderedProjectName bytes.Buffer
						if err := tmpl.Execute(&renderedProjectName, templateData); err != nil {
							continue
						}
						projectName := renderedProjectName.String()

						if projectName == "" {
							continue
						}

						foundAny = true
						fmt.Printf("    Stopping Docker Compose project: %s\n", projectName)
						cmd := exec.Command("docker", "compose", "-p", projectName, "down", "--volumes", "--remove-orphans")
						cmd.Stdout = os.Stdout
						cmd.Stderr = os.Stderr
						if err := cmd.Run(); err != nil {
							fmt.Printf("    Warning: failed to stop project %s: %v\n", projectName, err)
						} else {
							fmt.Printf("    * Stopped and removed Docker Compose project: %s\n", projectName)
						}
					}
				}

				if !foundAny {
					fmt.Println("    No Docker Compose projects to clean up")
				}
				return nil
			},
		},
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
						statusOutput, statusErr := exec.Command("git", "-C", worktreePath, "status", "--porcelain", "--ignore-submodules").Output()
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
					
					fmt.Printf("    * Worktree removed successfully\n")
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
			Name: "Clean up dev binaries from worktree",
			Check: func() (string, error) {
				// Check if grove dev is available
				if _, err := exec.LookPath("grove"); err != nil {
					return "N/A (grove not found)", nil
				}
				return color.YellowString("Available"), nil
			},
			Action: func() error {
				// Run 'grove dev prune' to clean up broken links after worktree removal
				// This will trigger the improved fallback logic in dev_prune.go
				fmt.Printf("    Pruning broken dev links...\n")
				if err := executor.Execute("grove", "dev", "prune"); err != nil {
					// Not critical - just log
					fmt.Printf("    Note: grove dev prune failed: %v\n", err)
				}
				return nil
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
						return color.YellowString("Checked out in worktree"), nil
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
			Name: "Rebuild main repo binaries",
			Check: func() (string, error) {
				if gitRoot == "" {
					return "N/A", nil
				}
				// Check if Makefile exists
				makefilePath := filepath.Join(gitRoot, "Makefile")
				if _, err := os.Stat(makefilePath); os.IsNotExist(err) {
					return "N/A (no Makefile)", nil
				}
				return color.YellowString("Available"), nil
			},
			Action: func() error {
				if gitRoot == "" {
					return nil
				}
				fmt.Printf("    Building binaries in main repository...\n")
				buildCmd := exec.Command("make", "build")
				buildCmd.Dir = gitRoot
				buildCmd.Stdout = os.Stdout
				buildCmd.Stderr = os.Stderr
				if err := buildCmd.Run(); err != nil {
					fmt.Printf("    Warning: build failed: %v\n", err)
					// Don't fail the whole cleanup if build fails
					return nil
				}
				return nil
			},
		},
		{
			Name: "Archive plan directory",
			Check: func() (string, error) {
				// Archiving is available for any plan
				return color.YellowString("Available"), nil
			},
			Action: func() error {
				// Get the parent directory of the plan (e.g., .../plans/)
				plansParentDir := filepath.Dir(planPath)
				planName := filepath.Base(planPath)

				// Create the .archive subdirectory in the plans parent directory
				archiveDir := filepath.Join(plansParentDir, ".archive")
				if err := os.MkdirAll(archiveDir, 0755); err != nil {
					return fmt.Errorf("failed to create archive directory: %w", err)
				}

				// Determine the destination path
				archivePath := filepath.Join(archiveDir, planName)

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
		   status == color.YellowString("Running containers found") ||
		   status == color.YellowString("Has links") ||
		   status == color.YellowString("Checked out in worktree") ||
		   status == color.RedString("Has changes (needs --force)") ||
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
	anyExplicitFlags := planFinishDeleteBranch || planFinishDeleteRemote || planFinishPruneWorktree || planFinishCloseSession || planFinishCleanDevLinks || planFinishRebuildBinaries || planFinishArchive || planFinishForce
	if planFinishYes {
		for _, item := range items {
			item.IsEnabled = item.IsAvailable
		}
	} else if anyExplicitFlags {
		// Always enable merging submodules, docker cleanup, marking as finished, and closing tmux
		items[0].IsEnabled = items[0].IsAvailable                                          // Merge/fast-forward submodules to main
		items[1].IsEnabled = items[1].IsAvailable                                          // Cleanup Docker Compose environment
		items[2].IsEnabled = items[2].IsAvailable                                          // Mark plan as finished
		items[3].IsEnabled = planFinishCloseSession && items[3].IsAvailable               // Close tmux session (before worktree removal!)
		items[4].IsEnabled = planFinishPruneWorktree && items[4].IsAvailable              // Prune git worktree
		items[5].IsEnabled = planFinishCleanDevLinks && items[5].IsAvailable              // Clean up dev binaries
		items[6].IsEnabled = planFinishDeleteBranch && items[6].IsAvailable               // Delete submodule branches
		items[7].IsEnabled = planFinishDeleteBranch && items[7].IsAvailable               // Delete local git branch
		items[8].IsEnabled = planFinishDeleteRemote && items[8].IsAvailable               // Delete remote git branch
		items[9].IsEnabled = planFinishRebuildBinaries && items[9].IsAvailable            // Rebuild main repo binaries
		items[10].IsEnabled = planFinishArchive && items[10].IsAvailable                  // Archive plan directory
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

	// Execute on_finish hook before marking as finished
	if plan.Config != nil && plan.Config.Status == "review" {
		// Find the first job with a note_ref
		var noteRef string
		for _, job := range plan.Jobs {
			if job.NoteRef != "" {
				noteRef = job.NoteRef
				break
			}
		}

		// Execute on_finish hook if it exists
		if plan.Config.Hooks != nil {
			if hookCmdStr, ok := plan.Config.Hooks["on_finish"]; ok && hookCmdStr != "" {
				fmt.Println("▶️  Executing on_finish hook...")

				// Prepare template data
				templateData := struct {
					PlanName string
					NoteRef  string
				}{
					PlanName: planName,
					NoteRef:  noteRef,
				}

				// Render the hook command
				tmpl, err := template.New("hook").Parse(hookCmdStr)
				if err != nil {
					fmt.Printf("Warning: failed to parse on_finish hook template: %v\n", err)
				} else {
					var renderedCmd bytes.Buffer
					if err := tmpl.Execute(&renderedCmd, templateData); err != nil {
						fmt.Printf("Warning: failed to render on_finish hook command: %v\n", err)
					} else {
						// Execute the command
						hookCmd := exec.Command("sh", "-c", renderedCmd.String())
						hookCmd.Stdout = os.Stdout
						hookCmd.Stderr = os.Stderr
						if err := hookCmd.Run(); err != nil {
							fmt.Printf("Warning: on_finish hook execution failed: %v\n", err)
						} else {
							fmt.Println("* on_finish hook executed successfully.")
						}
					}
				}
			}
		}

		// Now mark plan as finished
		plan.Config.Status = "finished"
		configPath := filepath.Join(planPath, ".grove-plan.yml")
		if data, err := yaml.Marshal(plan.Config); err == nil {
			os.WriteFile(configPath, data, 0644)
			fmt.Println("  - Marked plan as finished... Done")
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
			fmt.Println("\n* Unset active plan")
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
			fmt.Printf("      * Deleted branch '%s'\n", worktreeName)
		}
	}

	// Remove the entire ecosystem directory
	if err := os.RemoveAll(ecosystemDir); err != nil {
		return fmt.Errorf("failed to remove ecosystem directory: %w", err)
	}

	fmt.Printf("    * Ecosystem worktree removed successfully\n")
	return nil
}