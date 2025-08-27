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
	"github.com/mattsolo1/grove-core/git"
	gexec "github.com/mattsolo1/grove-flow/pkg/exec"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	planFinishYes           bool
	planFinishDeleteBranch  bool
	planFinishPruneWorktree bool
	planFinishCloseSession  bool
	planFinishArchive       bool
	planFinishCleanDevLinks bool
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
	cmd.Flags().BoolVar(&planFinishDeleteBranch, "delete-branch", false, "Delete the local and remote git branch")
	cmd.Flags().BoolVar(&planFinishPruneWorktree, "prune-worktree", false, "Remove the git worktree directory")
	cmd.Flags().BoolVar(&planFinishCloseSession, "close-session", false, "Close the associated tmux session")
	cmd.Flags().BoolVar(&planFinishCleanDevLinks, "clean-dev-links", false, "Clean up development binary links from the worktree")
	cmd.Flags().BoolVar(&planFinishArchive, "archive", false, "Archive the plan directory using 'nb archive'")

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

	// Gather information
	gitRoot, err := git.GetGitRoot(planPath)
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
	repoName := ""
	if gitRoot != "" {
		repoName = filepath.Base(gitRoot)
	}
	sessionName := SanitizeForTmuxSession(fmt.Sprintf("%s__%s", repoName, worktreeName))

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
						return color.YellowString("Exists"), nil
					}
				}
				return "Not found", nil
			},
			Action: func() error {
				worktreePath := filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
				return executor.Execute("git", "worktree", "remove", worktreePath)
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
				return color.YellowString("Exists"), nil
			},
			Action: func() error {
				return executor.Execute("git", "-C", gitRoot, "branch", "-d", branchName)
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
		if status == color.YellowString("Available") || status == color.YellowString("Exists") || status == color.YellowString("Running") {
			item.IsAvailable = true
		}
	}

	// Determine which items to enable
	anyExplicitFlags := planFinishDeleteBranch || planFinishPruneWorktree || planFinishCloseSession || planFinishCleanDevLinks || planFinishArchive
	if planFinishYes {
		for _, item := range items {
			item.IsEnabled = item.IsAvailable
		}
	} else if anyExplicitFlags {
		// Always enable marking as finished
		items[0].IsEnabled = items[0].IsAvailable
		items[1].IsEnabled = planFinishPruneWorktree && items[1].IsAvailable
		items[2].IsEnabled = planFinishDeleteBranch && items[2].IsAvailable
		items[3].IsEnabled = planFinishCloseSession && items[3].IsAvailable
		items[4].IsEnabled = planFinishCleanDevLinks && items[4].IsAvailable
		items[5].IsEnabled = planFinishArchive && items[5].IsAvailable
	} else {
		// Interactive mode
		reader := bufio.NewReader(os.Stdin)
		fmt.Println("Select cleanup actions to perform:")
		for i, item := range items {
			if item.IsAvailable {
				fmt.Printf("  [%d] %-40s (%s)\n", i+1, item.Name, item.Status)
			} else {
				fmt.Printf("      %-40s (%s)\n", item.Name, item.Status)
			}
		}

		fmt.Print("\nEnter numbers to enable (e.g., 1,2,4), or 'all': ")
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(strings.ToLower(input))

		if input == "all" {
			for _, item := range items {
				item.IsEnabled = item.IsAvailable
			}
		} else {
			parts := strings.Split(input, ",")
			for _, part := range parts {
				var choice int
				fmt.Sscanf(strings.TrimSpace(part), "%d", &choice)
				if choice > 0 && choice <= len(items) && items[choice-1].IsAvailable {
					items[choice-1].IsEnabled = true
				}
			}
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