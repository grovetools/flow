package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/mattsolo1/grove-core/git"
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

// cleanupItem represents a cleanup action that can be performed
type cleanupItem struct {
	Name        string
	Check       func() (string, error)
	Action      func() error
	Status      string
	IsAvailable bool
	IsEnabled   bool
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

	fmt.Printf("Finishing plan: %s\n\n", color.CyanString(planName))

	// Gather information - check for git root from current working directory
	cwd, _ := os.Getwd()
	gitRoot, err := git.GetGitRoot(cwd)
	if err != nil {
		fmt.Println(color.YellowString("Warning: Not in a git repository. Some cleanup actions are unavailable."))
		gitRoot = "" // Continue without git-related actions
	}

	worktreeName := ""
	if plan.Config != nil {
		worktreeName = plan.Config.Worktree
	}
	if worktreeName == "" {
		fmt.Println(color.YellowString("Warning: No worktree configured in .grove-plan.yml. Some cleanup actions are unavailable."))
	}

	executor := &gexec.RealCommandExecutor{}
	wm := git.NewWorktreeManager()

	branchName := worktreeName // Simple assumption: branch name matches worktree name
	sessionName := SanitizeForTmuxSession(worktreeName)

	// Define cleanup items
	items := []*cleanupItem{
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
				// Check if we need to use --force
				if planFinishForce {
					return executor.Execute("git", "worktree", "remove", "--force", worktreePath)
				}
				// Try regular removal first
				err := executor.Execute("git", "worktree", "remove", worktreePath)
				if err != nil && strings.Contains(err.Error(), "contains modified or untracked files") {
					fmt.Printf("    Retrying with --force due to modified files...\n")
					return executor.Execute("git", "worktree", "remove", "--force", worktreePath)
				}
				return err
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
						// Branch is checked out in a worktree, use force delete
						fmt.Printf("    Retrying with -D (force) due to checked out branch...\n")
						return executor.Execute("git", "-C", gitRoot, "branch", "-D", branchName)
					} else if strings.Contains(err.Error(), "not fully merged") {
						// Branch has unmerged commits, use force delete
						fmt.Printf("    Retrying with -D (force) due to unmerged commits...\n")
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
				_, err := exec.LookPath("nb")
				if err != nil {
					return "N/A (nb not found)", nil
				}
				return color.YellowString("Available"), nil
			},
			Action: func() error {
				return executor.Execute("nb", "archive", planPath)
			},
		},
	}

	// Populate status and availability
	for _, item := range items {
		status, _ := item.Check()
		item.Status = status
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

	// Determine which items to enable
	anyExplicitFlags := planFinishDeleteBranch || planFinishDeleteRemote || planFinishPruneWorktree || planFinishCloseSession || planFinishCleanDevLinks || planFinishArchive || planFinishForce
	if planFinishYes {
		for _, item := range items {
			item.IsEnabled = item.IsAvailable
		}
	} else if anyExplicitFlags {
		// Always enable marking as finished
		items[0].IsEnabled = items[0].IsAvailable                                          // Mark plan as finished
		items[1].IsEnabled = planFinishPruneWorktree && items[1].IsAvailable              // Prune git worktree
		items[2].IsEnabled = planFinishDeleteBranch && items[2].IsAvailable               // Delete local git branch
		items[3].IsEnabled = planFinishDeleteRemote && items[3].IsAvailable               // Delete remote git branch
		items[4].IsEnabled = planFinishCloseSession && items[4].IsAvailable               // Close tmux session
		items[5].IsEnabled = planFinishCleanDevLinks && items[5].IsAvailable              // Clean up dev binaries
		items[6].IsEnabled = planFinishArchive && items[6].IsAvailable                    // Archive plan directory
	} else {
		// Interactive TUI mode
		err := runFinishTUI(planName, items)
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

	fmt.Println("\nPlan cleanup finished.")
	return nil
}