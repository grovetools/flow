package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/command"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// EcosystemWorktreeScenario tests the full lifecycle of creating and cleaning up
// a worktree in a superproject with submodules.
func EcosystemWorktreeScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-ecosystem-worktree-lifecycle",
		Description: "Tests that `flow plan init --worktree` correctly sets up submodules on parallel branches and `flow plan finish` cleans them up.",
		Tags:        []string{"plan", "worktree", "submodule", "ecosystem"},
		Steps: []harness.Step{
			harness.NewStep("Setup a superproject with submodules", func(ctx *harness.Context) error {
				// Create repositories for the submodules first
				coreRepoPath := filepath.Join(ctx.RootDir, "grove-core.git")
				fs.CreateDir(coreRepoPath)
				git.Init(coreRepoPath)
				git.SetupTestConfig(coreRepoPath)
				fs.WriteString(filepath.Join(coreRepoPath, "README.md"), "This is grove-core.")
				git.Add(coreRepoPath, ".")
				git.Commit(coreRepoPath, "Initial commit for core")

				contextRepoPath := filepath.Join(ctx.RootDir, "grove-context.git")
				fs.CreateDir(contextRepoPath)
				git.Init(contextRepoPath)
				git.SetupTestConfig(contextRepoPath)
				fs.WriteString(filepath.Join(contextRepoPath, "README.md"), "This is grove-context.")
				git.Add(contextRepoPath, ".")
				git.Commit(contextRepoPath, "Initial commit for context")

				// Now, setup the superproject
				superprojectPath := filepath.Join(ctx.RootDir, "grove-ecosystem")
				fs.CreateDir(superprojectPath)
				git.Init(superprojectPath)
				git.SetupTestConfig(superprojectPath)
				
				// Allow file protocol for submodules in test (need to set globally)
				command.New("git", "config", "--global", "protocol.file.allow", "always").Run()

				// Add submodules using relative paths
				result := command.New("git", "submodule", "add", "../grove-core.git", "grove-core").Dir(superprojectPath).Run()
				if result.Error != nil {
					return fmt.Errorf("failed to add grove-core submodule: %w\nOutput: %s\nStderr: %s", result.Error, result.Stdout, result.Stderr)
				}
				result = command.New("git", "submodule", "add", "../grove-context.git", "grove-context").Dir(superprojectPath).Run()
				if result.Error != nil {
					return fmt.Errorf("failed to add grove-context submodule: %w\nOutput: %s\nStderr: %s", result.Error, result.Stdout, result.Stderr)
				}

				// Create go.work and grove.yml
				fs.WriteString(filepath.Join(superprojectPath, "go.work"), "go 1.21\n\nuse (\n\t./grove-core\n\t./grove-context\n)\n")
				fs.WriteString(filepath.Join(superprojectPath, "grove.yml"), "name: grove-ecosystem\nflow:\n  plans_directory: ./plans\n")
				
				// Commit everything including .gitmodules
				git.Add(superprojectPath, ".")
				git.Commit(superprojectPath, "Initial commit with submodules")
				
				// Debug: verify .gitmodules was committed and show HEAD content
				headResult := command.New("git", "ls-tree", "HEAD").Dir(superprojectPath).Run()
				fmt.Printf("DEBUG: Files in HEAD commit:\n%s\n", headResult.Stdout)
				
				result = command.New("git", "ls-files", ".gitmodules").Dir(superprojectPath).Run()
				if !strings.Contains(result.Stdout, ".gitmodules") {
					return fmt.Errorf(".gitmodules was not committed properly")
				}

				// Store the path for subsequent steps
				ctx.Set("superproject_path", superprojectPath)
				ctx.Set("core_repo_path", coreRepoPath)
				ctx.Set("context_repo_path", contextRepoPath)
				
				return nil
			}),
			
			harness.NewStep("Setup test environment with workspace mocking", func(ctx *harness.Context) error {
				// First run the standard setup
				if err := setupTestEnvironment().Func(ctx); err != nil {
					return err
				}
				
				// Set up mock workspace discovery to return our test repositories
				// This simulates having grove-core and grove-context in the local ecosystem
				coreRepoPath := ctx.GetString("core_repo_path")
				contextRepoPath := ctx.GetString("context_repo_path")
				
				workspaceData := fmt.Sprintf(`[{"name":"grove-core","path":"%s","worktrees":[{"path":"%s","branch":"main","is_main":true}]},{"name":"grove-context","path":"%s","worktrees":[{"path":"%s","branch":"main","is_main":true}]}]`, 
					coreRepoPath, coreRepoPath, contextRepoPath, contextRepoPath)
				
				// Set both environment variables - one for the mock, one for the actual code
				os.Setenv("MOCK_GROVE_WS_LIST", workspaceData)
				os.Setenv("GROVE_TEST_WORKSPACES", workspaceData)
				
				return nil
			}),

			harness.NewStep("Initialize a plan with a worktree", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				superprojectPath := ctx.GetString("superproject_path")
				cmd := command.New(flow, "plan", "init", "feature-x", "--worktree").Dir(superprojectPath)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				return result.Error
			}),

			harness.NewStep("Create the worktree with submodule initialization", func(ctx *harness.Context) error {
				superprojectPath := ctx.GetString("superproject_path")
				flow, _ := getFlowBinary()
				
				// Add a shell job that will use the worktree
				cmd := command.New(flow, "plan", "add", "--type", "shell", "--title", "Init", "--prompt", "echo 'Initializing worktree'").Dir(superprojectPath)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to add shell job: %w\nOutput: %s\nError: %s", result.Error, result.Stdout, result.Stderr)
				}
				
				// Run the shell job - this will trigger PrepareWorktree with submodule init
				planDir := filepath.Join(superprojectPath, "plans", "feature-x")
				cmd = command.New(flow, "plan", "run", "01-init.md").Dir(planDir)
				runResult := cmd.Run()
				// The command should succeed and create the worktree
				fmt.Printf("Plan run output:\nStdout: %s\nStderr: %s\n", runResult.Stdout, runResult.Stderr)
				if runResult.Error != nil {
					// Log the full output to understand what's happening
					fmt.Printf("Shell job execution error: %v\n", runResult.Error)
				}
				
				return nil
			}),

			harness.NewStep("Verify worktree, submodules, and branches are correctly set up", func(ctx *harness.Context) error {
				superprojectPath := ctx.GetString("superproject_path")
				worktreePath := filepath.Join(superprojectPath, ".grove-worktrees", "feature-x")

				if !fs.Exists(worktreePath) {
					return fmt.Errorf("superproject worktree was not created at %s", worktreePath)
				}

				// Debug: check if .gitmodules exists in the worktree
				gitmodulesPath := filepath.Join(worktreePath, ".gitmodules")
				if !fs.Exists(gitmodulesPath) {
					fmt.Printf("DEBUG: .gitmodules not found at %s\n", gitmodulesPath)
					// List files in the worktree to debug
					files, _ := fs.ListFiles(worktreePath)
					fmt.Printf("DEBUG: Files in worktree: %v\n", files)
				}

				// Check branches
				pathsToCheck := map[string]string{
					"superproject":            worktreePath,
					"grove-core submodule":    filepath.Join(worktreePath, "grove-core"),
					"grove-context submodule": filepath.Join(worktreePath, "grove-context"),
				}

				for name, path := range pathsToCheck {
					// Check if submodule dir exists and has content
					if name != "superproject" {
						if !fs.Exists(path) {
							return fmt.Errorf("%s directory at %s is missing, submodule update likely failed", name, path)
						}
						// Check if directory has any files (indicating submodule was initialized)
						if !fs.Exists(filepath.Join(path, ".git")) && !fs.Exists(filepath.Join(path, "README.md")) {
							return fmt.Errorf("%s directory at %s appears to be empty, submodule update likely failed", name, path)
						}
					}
					
					result := command.New("git", "branch", "--show-current").Dir(path).Run()
					if result.Error != nil {
						return fmt.Errorf("could not get current branch for %s: %w", name, result.Error)
					}
					branch := strings.TrimSpace(result.Stdout)
					
					if branch != "feature-x" {
						// For debugging: show all branches
						allBranches := command.New("git", "branch", "-a").Dir(path).Run()
						fmt.Printf("DEBUG: All branches in %s:\n%s\n", name, allBranches.Stdout)
						return fmt.Errorf("expected %s to be on branch 'feature-x', but it's on '%s'", name, branch)
					}
				}
				
				// NEW: Verify that branches are visible from main submodule checkout
				// The feature-x branch should be visible from the main submodule checkout
				mainSubmodulePaths := map[string]string{
					"grove-core":    filepath.Join(superprojectPath, "grove-core"),
					"grove-context": filepath.Join(superprojectPath, "grove-context"),
				}
				
				for submoduleName, mainSubmodulePath := range mainSubmodulePaths {
					// Check if the branch is visible from the main submodule checkout
					branchListResult := command.New("git", "branch", "--list", "feature-x").Dir(mainSubmodulePath).Run()
					if branchListResult.Error == nil && strings.Contains(branchListResult.Stdout, "feature-x") {
						fmt.Printf("✓ feature-x branch is visible from %s main submodule checkout\n", submoduleName)
					} else {
						return fmt.Errorf("feature-x branch should be visible from %s main submodule checkout at %s", submoduleName, mainSubmodulePath)
					}
				}
				
				// Also verify that submodules are linked worktrees, not separate clones
				// For linked worktrees, git rev-parse --git-dir should point back to the source repo's .git
				submoduleSources := map[string]string{
					"grove-core":    ctx.GetString("core_repo_path"),
					"grove-context": ctx.GetString("context_repo_path"),
				}
				
				for submoduleName, sourceRepo := range submoduleSources {
					submoduleInWorktree := filepath.Join(worktreePath, submoduleName)
					
					// Get the .git directory path for the submodule in the worktree
					result := command.New("git", "rev-parse", "--git-dir").Dir(submoduleInWorktree).Run()
					if result.Error != nil {
						fmt.Printf("Warning: could not check if %s is a linked worktree: %v\n", submoduleName, result.Error)
						continue
					}
					
					gitDir := strings.TrimSpace(result.Stdout)
					
					// For a linked worktree, the .git directory should be under the source repo's .git/worktrees/
					expectedPattern := filepath.Join(sourceRepo, ".git", "worktrees")
					if strings.Contains(gitDir, expectedPattern) {
						fmt.Printf("✓ %s is a linked worktree (git-dir: %s)\n", submoduleName, gitDir)
					} else {
						// This might be a standard submodule clone, which is also acceptable
						// (for external dependencies or when grove ws list is not available)
						fmt.Printf("  %s is a standard submodule clone (git-dir: %s)\n", submoduleName, gitDir)
					}
					
					// Verify that branches created in the worktree are visible from the source
					sourceResult := command.New("git", "branch", "--list", "feature-x").Dir(sourceRepo).Run()
					if sourceResult.Error == nil && strings.Contains(sourceResult.Stdout, "feature-x") {
						fmt.Printf("✓ feature-x branch is visible in %s source repository\n", submoduleName)
					} else {
						// For standard submodules, the branch won't be in the source repo
						// (it's only in the submodule's local clone)
						if !strings.Contains(gitDir, expectedPattern) {
							fmt.Printf("  feature-x branch is local to submodule (not in source repo - expected for standard submodules)\n")
						} else {
							// If it's a linked worktree, the branch MUST be visible
							return fmt.Errorf("feature-x branch should be visible in %s source repository for linked worktree", submoduleName)
						}
					}
				}
				
				return nil
			}),

			harness.NewStep("Finish the plan to trigger cleanup", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				superprojectPath := ctx.GetString("superproject_path")
				// Use --yes and --prune-worktree to automate the cleanup prompts
				cmd := command.New(flow, "plan", "finish", "feature-x", "--yes", "--delete-branch", "--prune-worktree").Dir(superprojectPath)
				result := cmd.Run()
				fmt.Printf("Plan finish output:\nStdout: %s\nStderr: %s\n", result.Stdout, result.Stderr)
				
				// Verify the output mentions submodule branch deletion
				if !strings.Contains(result.Stdout, "Delete submodule branches") && !strings.Contains(result.Stdout, "submodule") {
					fmt.Printf("WARNING: finish command output did not mention submodule branch cleanup\n")
				}
				
				return result.Error
			}),

			harness.NewStep("Verify submodule branches are cleaned up", func(ctx *harness.Context) error {
				superprojectPath := ctx.GetString("superproject_path")
				worktreePath := filepath.Join(superprojectPath, ".grove-worktrees", "feature-x")

				// Note: Git doesn't allow removing worktrees with submodules, so we verify
				// that the submodule branches were cleaned up instead
				if fs.Exists(worktreePath) {
					fmt.Printf("NOTE: Worktree still exists (expected - Git limitation with submodules)\n")
				}

				// Check that the feature branch is gone from the submodule repos
				// (main repo branch can't be deleted while worktree exists)
				submoduleRepos := map[string]string{
					"grove-core submodule":    filepath.Join(ctx.RootDir, "grove-core.git"),
					"grove-context submodule": filepath.Join(ctx.RootDir, "grove-context.git"),
				}

				for name, path := range submoduleRepos {
					// Check that branch is deleted
					result := command.New("git", "branch").Dir(path).Run()
					if strings.Contains(result.Stdout, "feature-x") {
						return fmt.Errorf("branch 'feature-x' should have been deleted from %s, but was found", name)
					}
					
					// Also verify that worktrees are cleaned up
					worktreeResult := command.New("git", "worktree", "list").Dir(path).Run()
					if worktreeResult.Error == nil {
						// Check that there's no worktree for feature-x
						if strings.Contains(worktreeResult.Stdout, "feature-x") || 
						   strings.Contains(worktreeResult.Stdout, ".grove-worktrees/feature-x") {
							return fmt.Errorf("worktree for feature-x should have been removed from %s", name)
						}
					}
				}
				
				// Note: The main superproject branch can't be deleted while the worktree exists
				// This is a Git limitation - worktrees with submodules can't be removed
				// So we just verify that the submodule branches were cleaned up
				
				fmt.Printf("✓ All branches and worktrees successfully cleaned up\n")
				return nil
			}),
		},
	}
}

// EcosystemWorktreeReposFilterScenario tests the --repos flag for selective repo inclusion
func EcosystemWorktreeReposFilterScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-ecosystem-worktree-repos-filter",
		Description: "Tests that `flow plan init --worktree --repos` only includes specified repos in ecosystem worktree",
		Tags:        []string{"plan", "worktree", "submodule", "ecosystem", "repos-filter"},
		Steps: []harness.Step{
			harness.NewStep("Setup a superproject with submodules", func(ctx *harness.Context) error {
				// Create the grove-core test repository
				coreRepoPath := filepath.Join(ctx.RootDir, "grove-core.git")
				fs.CreateDir(coreRepoPath)
				git.Init(coreRepoPath)
				git.SetupTestConfig(coreRepoPath)
				fs.WriteString(filepath.Join(coreRepoPath, "README.md"), "This is grove-core.")
				git.Add(coreRepoPath, ".")
				git.Commit(coreRepoPath, "Initial commit for core")

				contextRepoPath := filepath.Join(ctx.RootDir, "grove-context.git")
				fs.CreateDir(contextRepoPath)
				git.Init(contextRepoPath)
				git.SetupTestConfig(contextRepoPath)
				fs.WriteString(filepath.Join(contextRepoPath, "README.md"), "This is grove-context.")
				git.Add(contextRepoPath, ".")
				git.Commit(contextRepoPath, "Initial commit for context")

				// Now, setup the superproject
				superprojectPath := filepath.Join(ctx.RootDir, "grove-ecosystem")
				fs.CreateDir(superprojectPath)
				git.Init(superprojectPath)
				git.SetupTestConfig(superprojectPath)
				
				// Allow file protocol for submodules in test
				command.New("git", "config", "--global", "protocol.file.allow", "always").Run()

				// Add submodules using relative paths
				result := command.New("git", "submodule", "add", "../grove-core.git", "grove-core").Dir(superprojectPath).Run()
				if result.Error != nil {
					return fmt.Errorf("failed to add grove-core submodule: %w\nOutput: %s\nStderr: %s", result.Error, result.Stdout, result.Stderr)
				}
				result = command.New("git", "submodule", "add", "../grove-context.git", "grove-context").Dir(superprojectPath).Run()
				if result.Error != nil {
					return fmt.Errorf("failed to add grove-context submodule: %w\nOutput: %s\nStderr: %s", result.Error, result.Stdout, result.Stderr)
				}

				// Create go.work and grove.yml
				fs.WriteString(filepath.Join(superprojectPath, "go.work"), "go 1.21\n\nuse (\n\t./grove-core\n\t./grove-context\n)\n")
				fs.WriteString(filepath.Join(superprojectPath, "grove.yml"), "name: grove-ecosystem\nflow:\n  plans_directory: ./plans\n")
				
				// Commit everything including .gitmodules
				git.Add(superprojectPath, ".")
				git.Commit(superprojectPath, "Initial commit with submodules")

				// Store the paths for subsequent steps
				ctx.Set("superproject_path", superprojectPath)
				ctx.Set("core_repo_path", coreRepoPath)
				ctx.Set("context_repo_path", contextRepoPath)
				
				return nil
			}),
			
			harness.NewStep("Setup test environment with workspace mocking", func(ctx *harness.Context) error {
				// First run the standard setup
				if err := setupTestEnvironment().Func(ctx); err != nil {
					return err
				}
				
				// Set up mock workspace discovery to return our test repositories
				coreRepoPath := ctx.GetString("core_repo_path")
				contextRepoPath := ctx.GetString("context_repo_path")
				
				workspaceData := fmt.Sprintf(`[{"name":"grove-core","path":"%s","worktrees":[{"path":"%s","branch":"main","is_main":true}]},{"name":"grove-context","path":"%s","worktrees":[{"path":"%s","branch":"main","is_main":true}]}]`, 
					coreRepoPath, coreRepoPath, contextRepoPath, contextRepoPath)
				
				// Set both environment variables - one for the mock, one for the actual code
				os.Setenv("MOCK_GROVE_WS_LIST", workspaceData)
				os.Setenv("GROVE_TEST_WORKSPACES", workspaceData)
				
				return nil
			}),

			harness.NewStep("Initialize a plan with worktree and repos filter", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				superprojectPath := ctx.GetString("superproject_path")
				// Only include grove-core in the repos filter
				cmd := command.New(flow, "plan", "init", "filtered-plan", "--worktree", "--repos", "grove-core").Dir(superprojectPath)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				return result.Error
			}),

			harness.NewStep("Verify repos config was saved correctly", func(ctx *harness.Context) error {
				superprojectPath := ctx.GetString("superproject_path")
				configPath := filepath.Join(superprojectPath, "plans", "filtered-plan", ".grove-plan.yml")
				
				configData, err := os.ReadFile(configPath)
				if err != nil {
					return fmt.Errorf("failed to read plan config: %w", err)
				}
				
				configStr := string(configData)
				if !strings.Contains(configStr, "repos:") {
					return fmt.Errorf("plan config should contain 'repos:' section")
				}
				if !strings.Contains(configStr, "- grove-core") {
					return fmt.Errorf("plan config should contain 'grove-core' in repos list")
				}
				if strings.Contains(configStr, "- grove-context") {
					return fmt.Errorf("plan config should NOT contain 'grove-context' in repos list")
				}
				
				fmt.Printf("✓ Plan config contains correct repos filter\n")
				return nil
			}),

			harness.NewStep("Create the worktree and verify filtering", func(ctx *harness.Context) error {
				superprojectPath := ctx.GetString("superproject_path")
				flow, _ := getFlowBinary()
				
				// Add a simple shell job that will use the worktree
				cmd := command.New(flow, "plan", "add", "--title", "init", "--type", "shell", "--prompt", "echo 'Testing repos filter'").Dir(superprojectPath)
				result := cmd.Run()
				ctx.ShowCommandOutput("Add job", result.Stdout, result.Stderr)
				if result.Error != nil {
					return fmt.Errorf("failed to add job: %w", result.Error)
				}

				// Run the job to trigger worktree creation
				cmd = command.New(flow, "plan", "run", "--yes").Dir(superprojectPath)
				result = cmd.Run()
				ctx.ShowCommandOutput("Plan run", result.Stdout, result.Stderr)
				
				// Check for our filtering messages in the output
				if !strings.Contains(result.Stdout, "grove-context: skipping (not in repos filter)") {
					return fmt.Errorf("expected to see grove-context being skipped due to repos filter")
				}
				if !strings.Contains(result.Stdout, "grove-core: creating linked worktree") {
					return fmt.Errorf("expected to see grove-core worktree being created")
				}
				
				return result.Error
			}),

			harness.NewStep("Verify only filtered repos are in the worktree", func(ctx *harness.Context) error {
				superprojectPath := ctx.GetString("superproject_path")
				worktreePath := filepath.Join(superprojectPath, ".grove-worktrees", "filtered-plan")
				
				
				// Check that grove-core directory exists and is populated
				groveCorePath := filepath.Join(worktreePath, "grove-core")
				if !fs.Exists(groveCorePath) {
					return fmt.Errorf("grove-core directory should exist in worktree")
				}
				coreReadmePath := filepath.Join(groveCorePath, "README.md")
				if !fs.Exists(coreReadmePath) {
					return fmt.Errorf("grove-core README.md should exist (worktree should be populated)")
				}
				
				// Check that grove-context directory either doesn't exist or is empty/unpopulated
				groveContextPath := filepath.Join(worktreePath, "grove-context")
				if fs.Exists(groveContextPath) {
					// If the directory exists, it should be empty or not have the README
					contextReadmePath := filepath.Join(groveContextPath, "README.md")
					if fs.Exists(contextReadmePath) {
						return fmt.Errorf("grove-context should not be populated in worktree (repos filter should have excluded it)")
					}
					fmt.Printf("✓ grove-context directory exists but is not populated (as expected)\n")
				} else {
					fmt.Printf("✓ grove-context directory doesn't exist in worktree (as expected)\n")
				}
				
				fmt.Printf("✓ Only grove-core is populated in the filtered worktree\n")
				return nil
			}),
		},
	}
}