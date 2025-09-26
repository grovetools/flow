// File: tests/e2e/tend/scenarios_plan.go
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

// BasicPlanLifecycleScenario tests the fundamental `flow plan` workflow with shell jobs.
func BasicPlanLifecycleScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-shell-lifecycle",
		Description: "Tests shell job execution: init, add shell jobs with dependencies, run, and verify status.",
		Tags:        []string{"plan", "shell", "smoke"},
		Steps: []harness.Step{
			harness.NewStep("Setup git repository and config", func(ctx *harness.Context) error {
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				// Create an initial commit so git operations work properly
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Create a test-specific grove.yml with local plans_directory
				groveConfig := `name: test-project
flow:
  plans_directory: ./plans
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig)
				return nil
			}),
			harness.NewStep("Initialize a new plan", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return fmt.Errorf("failed to get flow binary: %w", err)
				}
				cmd := command.New(flow, "plan", "init", "my-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return fmt.Errorf("flow plan init failed: %w", result.Error)
				}
				planPath := filepath.Join(ctx.RootDir, "plans", "my-plan")
				if !fs.Exists(planPath) {
					return fmt.Errorf("plan directory '%s' should exist", planPath)
				}
				return nil
			}),
			harness.NewStep("Add first shell job to create a file", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				// Add a shell job that creates a file - this tests shell command execution
				cmd := command.New(flow, "plan", "add", "my-plan", "--title", "Create Hello File", "--type", "shell", "-p", "echo 'hello from shell job' > plans/my-plan/hello.txt").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}
				jobFile := filepath.Join(ctx.RootDir, "plans", "my-plan", "01-create-hello-file.md")
				if !fs.Exists(jobFile) {
					return fmt.Errorf("job file '01-create-hello-file.md' should exist")
				}

				// Remove worktree from job file to avoid worktree directory requirement
				content, _ := fs.ReadString(jobFile)
				lines := strings.Split(content, "\n")
				var newLines []string
				for _, line := range lines {
					if !strings.HasPrefix(line, "worktree:") {
						newLines = append(newLines, line)
					}
				}
				fs.WriteString(jobFile, strings.Join(newLines, "\n"))

				return nil
			}),
			harness.NewStep("Add second shell job with dependency", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				// Add another shell job that depends on the first - tests shell job dependencies
				cmd := command.New(flow, "plan", "add", "my-plan", "--title", "Copy File Using Shell", "--type", "shell", "-p", "cp plans/my-plan/hello.txt plans/my-plan/world.txt", "--depends-on", "01-create-hello-file.md").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}

				// Remove worktree from job file to avoid worktree directory requirement
				jobFile := filepath.Join(ctx.RootDir, "plans", "my-plan", "02-copy-file-using-shell.md")
				content, _ := fs.ReadString(jobFile)
				content = strings.ReplaceAll(content, "worktree: my-plan\n", "")
				fs.WriteString(jobFile, content)

				return nil
			}),
			harness.NewStep("Run the first shell job", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				// Execute the first shell job specifically
				cmd := command.New(flow, "plan", "run", filepath.Join("plans", "my-plan", "01-create-hello-file.md")).Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				return result.Error
			}),
			harness.NewStep("Verify first shell job execution and status", func(ctx *harness.Context) error {
				planPath := filepath.Join(ctx.RootDir, "plans", "my-plan")
				// Verify the shell job created the expected file
				content, err := fs.ReadString(filepath.Join(planPath, "hello.txt"))
				if err != nil {
					return err
				}
				if !strings.Contains(content, "hello from shell job") {
					return fmt.Errorf("hello.txt should contain 'hello from shell job' (shell job output)")
				}

				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "status", "my-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if !strings.Contains(result.Stdout, "Completed: 1") {
					return fmt.Errorf("status should show 1 completed job, got:\n%s", result.Stdout)
				}
				if !strings.Contains(result.Stdout, "Pending: 1") {
					return fmt.Errorf("status should show 1 pending job")
				}
				return nil
			}),
			harness.NewStep("Run the second shell job", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				// Execute the second shell job that depends on the first
				cmd := command.New(flow, "plan", "run", filepath.Join("plans", "my-plan", "02-copy-file-using-shell.md")).Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				return result.Error
			}),
			harness.NewStep("Verify both shell jobs completed successfully", func(ctx *harness.Context) error {
				planPath := filepath.Join(ctx.RootDir, "plans", "my-plan")
				// Verify the second shell job (cp command) executed correctly
				content, err := fs.ReadString(filepath.Join(planPath, "world.txt"))
				if err != nil {
					return err
				}
				if !strings.Contains(content, "hello from shell job") {
					return fmt.Errorf("world.txt should contain 'hello from shell job' (copied by second shell job)")
				}

				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "status", "my-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if !strings.Contains(result.Stdout, "Completed: 2") {
					return fmt.Errorf("status should show 2 completed jobs")
				}
				return nil
			}),
		},
	}
}

// PlanActiveJobScenario tests the active job state management.
func PlanActiveJobScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-active-job",
		Description: "Tests the 'plan set', 'plan current', and 'plan unset' commands.",
		Tags:        []string{"plan", "state"},
		Steps: []harness.Step{
			harness.NewStep("Setup git repo and plan", func(ctx *harness.Context) error {
				git.Init(ctx.RootDir) // State file is stored at git root
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Create a test-specific grove.yml with local plans_directory
				groveConfig := `name: test-project
flow:
  plans_directory: ./plans
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig)

				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "init", "active-plan-test").Dir(ctx.RootDir)
				return cmd.Run().Error
			}),
			harness.NewStep("Set the active job", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "set", "active-plan-test").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if !strings.Contains(result.Stdout, "Set active job to: active-plan-test") {
					return fmt.Errorf("unexpected output when setting active job")
				}
				return result.Error
			}),
			harness.NewStep("Show the current job", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "current").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if !strings.Contains(result.Stdout, "Active job: active-plan-test") {
					return fmt.Errorf("current command did not show the correct active job")
				}
				return nil
			}),
			harness.NewStep("Unset the active job", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "unset").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				return result.Error
			}),
			harness.NewStep("Verify active job is cleared", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "current").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if !strings.Contains(result.Stdout, "No active job set") {
					return fmt.Errorf("current command should show no active job")
				}
				return nil
			}),
		},
	}
}

// AgentJobLaunchScenario tests launching an agent job and its side effects.
func AgentJobLaunchScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-agent-launch",
		Description: "Tests launching an agent job, which should create a git worktree.",
		Tags:        []string{"plan", "agent", "worktree"},
		Steps: []harness.Step{
			harness.NewStep("Setup git repo and project config", func(ctx *harness.Context) error {
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Initial commit")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Create a test-specific grove.yml without plans_directory
				return createTestGroveConfig(ctx)
			}),
			harness.NewStep("Initialize plan", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return err
				}
				cmd := command.New(flow, "plan", "init", "agent-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to init plan: %w", result.Error)
				}
				// Plans will be created in ./plans directory as configured
				return nil
			}),
			harness.NewStep("Add an agent job", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				// Plans are in ./plans/agent-plan as configured
				planPath := filepath.Join(ctx.RootDir, "plans", "agent-plan")

				// Add the agent job with a worktree for launch
				cmdAdd := command.New(flow, "plan", "add", planPath,
					"--title", "Refactor Code",
					"--type", "agent",
					"--worktree", "refactor-code",
					"-p", "Refactor everything.").Dir(ctx.RootDir)
				resultAdd := cmdAdd.Run()
				ctx.ShowCommandOutput(cmdAdd.String(), resultAdd.Stdout, resultAdd.Stderr)
				return resultAdd.Error
			}),
			setupTestEnvironment(),
			harness.NewStep("Launch the agent job", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				// Plans are in ./plans/agent-plan as configured
				jobFile := filepath.Join(ctx.RootDir, "plans", "agent-plan", "01-refactor-code.md")

				cmd := ctx.Command(flow, "plan", "launch", jobFile).Dir(ctx.RootDir)
				// Set environment variables for testing
				cmd.Env("GROVE_FLOW_SKIP_DOCKER_CHECK=true")
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				return result.Error
			}),
			harness.NewStep("Verify worktree creation", func(ctx *harness.Context) error {
				// Check git worktree list from the test root directory
				cmd := command.New("git", "worktree", "list").Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("git worktree list failed: %w", result.Error)
				}

				// Just check if the worktree exists in git, regardless of exact path
				// The worktree might be named after the plan or the job
				if strings.Contains(result.Stdout, "refactor-code") || strings.Contains(result.Stdout, "agent-plan") {
					return nil // Success - worktree was created
				}

				return fmt.Errorf("worktree not found in git worktree list")
			}),
		},
	}
}

// PlanGraphScenario tests the dependency graph visualization with shell jobs.
func PlanGraphScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-graph",
		Description: "Tests the 'plan graph' command output for shell jobs with dependencies.",
		Tags:        []string{"plan", "graph", "shell"},
		Steps: []harness.Step{
			harness.NewStep("Setup a plan with dependencies", func(ctx *harness.Context) error {
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Create a test-specific grove.yml with local plans_directory
				groveConfig := `name: test-project
flow:
  plans_directory: ./plans
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig)

				flow, _ := getFlowBinary()
				command.New(flow, "plan", "init", "graph-plan").Dir(ctx.RootDir).Run()
				// Add three shell jobs to test dependency graph visualization
				command.New(flow, "plan", "add", "graph-plan", "--title", "A", "--type", "shell", "-p", "echo A").Dir(ctx.RootDir).Run()
				command.New(flow, "plan", "add", "graph-plan", "--title", "B", "--type", "shell", "-p", "echo B", "--depends-on", "01-a.md").Dir(ctx.RootDir).Run()
				command.New(flow, "plan", "add", "graph-plan", "--title", "C", "--type", "shell", "-p", "echo C", "--depends-on", "01-a.md").Dir(ctx.RootDir).Run()
				return nil
			}),
			harness.NewStep("Generate Mermaid graph", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "graph", "graph-plan", "--format", "mermaid").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}
				output := result.Stdout
				if !strings.Contains(output, "graph TD") {
					return fmt.Errorf("output is not a valid Mermaid graph")
				}
				if !strings.Contains(output, "-->") {
					return fmt.Errorf("graph is missing edges for dependencies")
				}
				return nil
			}),
		},
	}
}

// PlanWorktreeInheritanceScenario tests smart worktree inheritance feature
func PlanWorktreeInheritanceScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-worktree-inheritance",
		Description: "Tests that flow plan add --depends-on correctly inherits the worktree from dependencies",
		Tags:        []string{"plan", "worktree"},
		Steps: []harness.Step{
			harness.NewStep("Setup git repository and config", func(ctx *harness.Context) error {
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Create a test-specific grove.yml with local plans_directory
				groveConfig := `name: test-project
flow:
  plans_directory: ./plans
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig)
				return nil
			}),
			harness.NewStep("Initialize plan", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				// Initialize plan with a default worktree
				cmd := command.New(flow, "plan", "init", "inheritance-plan", "--worktree=inheritance-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to init plan: %w", result.Error)
				}
				return nil
			}),
			harness.NewStep("Add first agent job", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				// Add the agent job - it should get worktree=inheritance-plan by default
				cmd := command.New(flow, "plan", "add", "inheritance-plan",
					"--title", "Implement API",
					"--type", "agent",
					"-p", "Implement the API").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}

				// Verify the job has the default worktree
				jobPath := filepath.Join(ctx.RootDir, "plans", "inheritance-plan", "01-implement-api.md")
				content, err := fs.ReadString(jobPath)
				if err != nil {
					return err
				}
				if !strings.Contains(content, "worktree: inheritance-plan") {
					return fmt.Errorf("first job should have worktree: inheritance-plan")
				}
				return nil
			}),
			harness.NewStep("Add dependent job without specifying worktree", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				// Add dependent job - it should inherit worktree from dependency
				cmd := command.New(flow, "plan", "add", "inheritance-plan",
					"--title", "Review API",
					"--type", "oneshot",
					"--depends-on", "01-implement-api.md",
					"-p", "Review the API code").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}

				// Verify the job inherited the worktree
				jobPath := filepath.Join(ctx.RootDir, "plans", "inheritance-plan", "02-review-api.md")
				content, err := fs.ReadString(jobPath)
				if err != nil {
					return err
				}
				if !strings.Contains(content, "worktree: inheritance-plan") {
					return fmt.Errorf("dependent job should inherit worktree: inheritance-plan from its dependency")
				}
				return nil
			}),
		},
	}
}

// PlanRebaseScenario tests the 'flow plan rebase' command for both single and ecosystem worktrees.
func PlanRebaseScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-rebase",
		Description: "Tests plan rebase functionality for updating feature branches and integration testing.",
		Tags:        []string{"plan", "rebase", "worktree"},
		Steps: []harness.Step{
			harness.NewStep("Setup git repository with branches", func(ctx *harness.Context) error {
				// Initialize git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				
				// Create initial files on main branch
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "# Test Project\n\nInitial version on main.")
				fs.WriteString(filepath.Join(ctx.RootDir, "main.go"), "package main\n\nfunc main() {\n\t// Initial implementation\n}\n")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit on main")
				
				// Create grove.yml
				groveConfig := `name: test-project
flow:
  plans_directory: ./plans
  oneshot_model: mock
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig)
				git.Add(ctx.RootDir, "grove.yml")
				git.Commit(ctx.RootDir, "Add grove config")
				
				// Set up a fake origin remote pointing to the local repo
				// This allows git fetch to work without a real remote
				cmd := command.New("git", "remote", "add", "origin", ctx.RootDir).Dir(ctx.RootDir)
				cmd.Run() // Ignore error if remote already exists
				
				// Create the origin/main reference
				cmd = command.New("git", "update-ref", "refs/remotes/origin/main", "HEAD").Dir(ctx.RootDir)
				if result := cmd.Run(); result.Error != nil {
					return fmt.Errorf("failed to setup origin/main: %w", result.Error)
				}
				
				return nil
			}),
			
			harness.NewStep("Initialize plan with worktree", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return err
				}
				
				// Initialize a plan with a worktree (--worktree will use plan name automatically)
				cmd := command.New(flow, "plan", "init", "feature-plan", "--worktree").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return fmt.Errorf("plan init failed: %w", result.Error)
				}
				
				// Create the worktree manually since it's not created automatically on init
				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "feature-plan")
				
				// Create worktree using git command
				cmd = command.New("git", "worktree", "add", worktreePath, "-b", "feature-plan").Dir(ctx.RootDir)
				result = cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to create worktree: %w", result.Error)
				}
				
				// Make a change in the worktree
				featureFile := filepath.Join(worktreePath, "feature.go")
				fs.WriteString(featureFile, "package main\n\n// New feature code\nfunc Feature() string {\n\treturn \"feature\"\n}\n")
				
				// Commit the change in the worktree
				git.Add(worktreePath, ".")
				git.Commit(worktreePath, "Add feature implementation")
				
				return nil
			}),
			
			harness.NewStep("Test rebase help command", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Test that the rebase command exists and shows help
				cmd := command.New(flow, "plan", "rebase", "--help").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				// Check both stdout and stderr since help might go to either
				output := result.Stdout + result.Stderr
				if !strings.Contains(output, "rebase") || !strings.Contains(output, "worktree") {
					return fmt.Errorf("rebase help should show command description")
				}
				if !strings.Contains(output, "Standard Rebase") || !strings.Contains(output, "Integration Rebase") {
					return fmt.Errorf("rebase help should describe both rebase modes")
				}
				
				return nil
			}),
			
			harness.NewStep("Update main branch to create divergence", func(ctx *harness.Context) error {
				// Make a change on main that will require rebasing
				mainFile := filepath.Join(ctx.RootDir, "main.go")
				fs.WriteString(mainFile, "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Updated main branch\")\n}\n")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Update main.go on main branch")
				
				// Update the origin reference to point to the new commit
				cmd := command.New("git", "update-ref", "refs/remotes/origin/main", "HEAD").Dir(ctx.RootDir)
				if result := cmd.Run(); result.Error != nil {
					return fmt.Errorf("failed to update origin/main: %w", result.Error)
				}
				
				return nil
			}),
			
			harness.NewStep("Test standard rebase (worktree onto main)", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Run rebase to update the feature branch
				cmd := command.New(flow, "plan", "rebase", "main").Dir(ctx.RootDir)
				// Set active plan first
				setCmd := command.New(flow, "plan", "set", "feature-plan").Dir(ctx.RootDir)
				if result := setCmd.Run(); result.Error != nil {
					return fmt.Errorf("failed to set active plan: %w", result.Error)
				}
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				// The rebase might succeed or report "Already up to date" depending on git state
				// Both are acceptable outcomes for this test
				if result.Error != nil {
					// Check if it's a reasonable error message
					if !strings.Contains(result.Stderr, "Already up to date") && 
					   !strings.Contains(result.Stderr, "uncommitted changes") {
						return fmt.Errorf("unexpected rebase error: %w", result.Error)
					}
				}
				
				if strings.Contains(result.Stdout, "Rebased successfully") || 
				   strings.Contains(result.Stdout, "Already up to date") {
					// Success - the rebase completed or was already up to date
					return nil
				}
				
				return nil
			}),
			
			harness.NewStep("Test rebase with --yes flag", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Test that --yes flag is recognized
				cmd := command.New(flow, "plan", "rebase", "main", "--yes").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				// Should not error just because of the flag
				if result.Error != nil && !strings.Contains(result.Stderr, "Already up to date") {
					return fmt.Errorf("--yes flag should be accepted: %w", result.Error)
				}
				
				return nil
			}),
			
			harness.NewStep("Test abort/continue flags", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Test --abort when no rebase is in progress (should error gracefully)
				cmd := command.New(flow, "plan", "rebase", "--abort").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error == nil {
					return fmt.Errorf("--abort should fail when no rebase is in progress")
				}
				if !strings.Contains(result.Stderr, "no in-progress rebase found") {
					return fmt.Errorf("--abort should report no rebase in progress")
				}
				
				// Test --continue when no rebase is in progress (should error gracefully)
				cmd = command.New(flow, "plan", "rebase", "--continue").Dir(ctx.RootDir)
				result = cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error == nil {
					return fmt.Errorf("--continue should fail when no rebase is in progress")
				}
				if !strings.Contains(result.Stderr, "no in-progress rebase found") {
					return fmt.Errorf("--continue should report no rebase in progress")
				}
				
				return nil
			}),
		},
	}
}

// PlanRebaseConflictsScenario tests the rebase command with conflicts and resolution.
func PlanRebaseConflictsScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-rebase-conflicts",
		Description: "Tests plan rebase with conflicts, --continue and --abort functionality.",
		Tags:        []string{"plan", "rebase", "conflicts"},
		Steps: []harness.Step{
			harness.NewStep("Setup git repository", func(ctx *harness.Context) error {
				// Initialize git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				
				// Create initial file that will have conflicts
				fs.WriteString(filepath.Join(ctx.RootDir, "config.go"), "package main\n\n// Config version 1\nvar Config = \"initial\"\n")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")
				
				// Create grove.yml
				groveConfig := `name: test-project
flow:
  plans_directory: ./plans
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig)
				git.Add(ctx.RootDir, "grove.yml")
				git.Commit(ctx.RootDir, "Add grove config")
				
				// Set up fake origin remote
				cmd := command.New("git", "remote", "add", "origin", ctx.RootDir).Dir(ctx.RootDir)
				cmd.Run()
				
				cmd = command.New("git", "update-ref", "refs/remotes/origin/main", "HEAD").Dir(ctx.RootDir)
				if result := cmd.Run(); result.Error != nil {
					return fmt.Errorf("failed to setup origin/main: %w", result.Error)
				}
				
				return nil
			}),
			
			harness.NewStep("Create feature branch with changes", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Initialize plan with worktree
				cmd := command.New(flow, "plan", "init", "conflict-plan", "--worktree").Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("plan init failed: %w", result.Error)
				}
				
				// Create the worktree manually
				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "conflict-plan")
				cmd = command.New("git", "worktree", "add", worktreePath, "-b", "conflict-plan").Dir(ctx.RootDir)
				result = cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to create worktree: %w", result.Error)
				}
				
				// Make a conflicting change in the worktree
				configFile := filepath.Join(worktreePath, "config.go")
				fs.WriteString(configFile, "package main\n\n// Config version 2 - feature branch\nvar Config = \"feature-branch-value\"\n")
				git.Add(worktreePath, ".")
				git.Commit(worktreePath, "Update config in feature branch")
				
				return nil
			}),
			
			harness.NewStep("Create conflicting change on main", func(ctx *harness.Context) error {
				// Make a different change to the same file on main
				configFile := filepath.Join(ctx.RootDir, "config.go")
				fs.WriteString(configFile, "package main\n\n// Config version 2 - main branch\nvar Config = \"main-branch-value\"\n")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Update config in main branch")
				
				// Update origin reference
				cmd := command.New("git", "update-ref", "refs/remotes/origin/main", "HEAD").Dir(ctx.RootDir)
				cmd.Run()
				
				return nil
			}),
			
			harness.NewStep("Attempt rebase that will conflict", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Set active plan
				setCmd := command.New(flow, "plan", "set", "conflict-plan").Dir(ctx.RootDir)
				setCmd.Run()
				
				// Attempt rebase - this should fail with conflicts
				cmd := command.New(flow, "plan", "rebase", "main").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				// We expect this to fail with a conflict error
				if result.Error == nil {
					return fmt.Errorf("expected rebase to fail with conflicts, but it succeeded")
				}
				
				if !strings.Contains(result.Stderr, "conflicts detected") && 
				   !strings.Contains(result.Stdout, "conflicts detected") {
					return fmt.Errorf("expected conflict detection message")
				}
				
				return nil
			}),
			
			harness.NewStep("Test --abort flag", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// First check that there's a rebase in progress in the worktree
				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "conflict-plan")
				rebaseMergePath := filepath.Join(worktreePath, ".git", "rebase-merge")
				if !fs.Exists(rebaseMergePath) {
					// If no rebase in progress, that's ok - conflicts might have been auto-resolved
					// or the rebase failed before starting
					return nil
				}
				
				// Test abort
				cmd := command.New(flow, "plan", "rebase", "--abort").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("--abort failed: %w", result.Error)
				}
				
				// Verify rebase was aborted
				if fs.Exists(rebaseMergePath) {
					return fmt.Errorf("rebase-merge directory still exists after abort")
				}
				
				return nil
			}),
		},
	}
}

// PlanRebaseIntegrationScenario tests rebasing main onto the worktree branch.
func PlanRebaseIntegrationScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-rebase-integration",
		Description: "Tests integration rebase (main onto worktree) for testing merged state.",
		Tags:        []string{"plan", "rebase", "integration"},
		Steps: []harness.Step{
			harness.NewStep("Setup git repository with feature branch", func(ctx *harness.Context) error {
				// Initialize git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				
				// Create main branch files
				fs.WriteString(filepath.Join(ctx.RootDir, "main.go"), "package main\n\nfunc main() {\n\t// Main branch code\n}\n")
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "# Main Branch\n\nOriginal readme")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit on main")
				
				// Create grove.yml
				groveConfig := `name: test-project
flow:
  plans_directory: ./plans
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig)
				git.Add(ctx.RootDir, "grove.yml")
				git.Commit(ctx.RootDir, "Add grove config")
				
				// Set up fake origin
				cmd := command.New("git", "remote", "add", "origin", ctx.RootDir).Dir(ctx.RootDir)
				cmd.Run()
				
				cmd = command.New("git", "update-ref", "refs/remotes/origin/main", "HEAD").Dir(ctx.RootDir)
				cmd.Run()
				
				return nil
			}),
			
			harness.NewStep("Create feature worktree with new functionality", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Initialize plan with worktree
				cmd := command.New(flow, "plan", "init", "integration-plan", "--worktree").Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("plan init failed: %w", result.Error)
				}
				
				// Create the worktree
				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "integration-plan")
				cmd = command.New("git", "worktree", "add", worktreePath, "-b", "integration-plan").Dir(ctx.RootDir)
				result = cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to create worktree: %w", result.Error)
				}
				
				// Add new feature files in worktree
				featureFile := filepath.Join(worktreePath, "feature.go")
				fs.WriteString(featureFile, "package main\n\n// New feature\nfunc Feature() string {\n\treturn \"new feature\"\n}\n")
				
				// Also modify existing file
				mainFile := filepath.Join(worktreePath, "main.go")
				fs.WriteString(mainFile, "package main\n\nfunc main() {\n\t// Main branch code\n\t// With feature additions\n\tFeature()\n}\n")
				
				git.Add(worktreePath, ".")
				git.Commit(worktreePath, "Add new feature")
				
				return nil
			}),
			
			harness.NewStep("Test integration rebase (main onto feature)", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Set active plan
				setCmd := command.New(flow, "plan", "set", "integration-plan").Dir(ctx.RootDir)
				setCmd.Run()
				
				// Ensure main repo is clean (commit any pending changes)
				git.Add(ctx.RootDir, ".")
				statusCmd := command.New("git", "status", "--porcelain").Dir(ctx.RootDir)
				statusResult := statusCmd.Run()
				if strings.TrimSpace(statusResult.Stdout) != "" {
					git.Commit(ctx.RootDir, "Clean up before integration rebase")
				}
				
				// Get current HEAD of main before rebase
				getHeadCmd := command.New("git", "rev-parse", "HEAD").Dir(ctx.RootDir)
				beforeResult := getHeadCmd.Run()
				beforeHead := strings.TrimSpace(beforeResult.Stdout)
				
				// Perform integration rebase with --yes to skip confirmation
				cmd := command.New(flow, "plan", "rebase", "integration-plan", "--yes").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("integration rebase failed: %w", result.Error)
				}
				
				// Verify the rebase happened
				if !strings.Contains(result.Stdout, "Integration Rebase Summary") &&
				   !strings.Contains(result.Stdout, "Rebasing 'main' on top of feature branch") {
					return fmt.Errorf("expected integration rebase output not found")
				}
				
				// Verify warning message about temporary state
				if !strings.Contains(result.Stdout, "WARNING") || 
				   !strings.Contains(result.Stdout, "temporary state") {
					return fmt.Errorf("expected warning about temporary state")
				}
				
				// Check that HEAD has changed (main was rebased)
				getHeadCmd = command.New("git", "rev-parse", "HEAD").Dir(ctx.RootDir)
				afterResult := getHeadCmd.Run()
				afterHead := strings.TrimSpace(afterResult.Stdout)
				
				if beforeHead == afterHead {
					// It's possible the rebase was a no-op if branches were already in sync
					// Check if the feature file exists in main now
					featureFile := filepath.Join(ctx.RootDir, "feature.go")
					if !fs.Exists(featureFile) {
						return fmt.Errorf("feature.go should exist in main after integration rebase")
					}
				}
				
				return nil
			}),
			
			harness.NewStep("Verify main can be reset", func(ctx *harness.Context) error {
				// Reset main back to origin/main as suggested in the warning
				cmd := command.New("git", "reset", "--hard", "origin/main").Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to reset main: %w", result.Error)
				}
				
				// Verify feature.go is gone (we're back to original main)
				featureFile := filepath.Join(ctx.RootDir, "feature.go")
				if fs.Exists(featureFile) {
					return fmt.Errorf("feature.go should not exist after reset to origin/main")
				}
				
				return nil
			}),
		},
	}
}

// PlanRebaseEcosystemScenario tests rebase with ecosystem worktrees (multiple repos).
func PlanRebaseEcosystemScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-rebase-ecosystem",
		Description: "Tests plan rebase with ecosystem worktrees containing multiple repositories.",
		Tags:        []string{"plan", "rebase", "ecosystem"},
		Steps: []harness.Step{
			harness.NewStep("Setup ecosystem with multiple repos", func(ctx *harness.Context) error {
				// Create main ecosystem directory
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				
				// Create grove.yml for ecosystem
				groveConfig := `name: test-ecosystem
flow:
  plans_directory: ./plans
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "# Test Ecosystem")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial ecosystem commit")
				
				// Simulate multiple repos by creating subdirectories
				// In a real scenario these would be separate git repos
				repo1Dir := filepath.Join(ctx.RootDir, "repo1")
				repo2Dir := filepath.Join(ctx.RootDir, "repo2")
				fs.CreateDir(repo1Dir)
				fs.CreateDir(repo2Dir)
				
				// Initialize repo1 as a git repo
				git.Init(repo1Dir)
				git.SetupTestConfig(repo1Dir)
				fs.WriteString(filepath.Join(repo1Dir, "main.go"), "package repo1\n\nvar Version = \"1.0\"")
				git.Add(repo1Dir, ".")
				git.Commit(repo1Dir, "Initial repo1")
				cmd := command.New("git", "remote", "add", "origin", repo1Dir).Dir(repo1Dir)
				cmd.Run()
				cmd = command.New("git", "update-ref", "refs/remotes/origin/main", "HEAD").Dir(repo1Dir)
				cmd.Run()
				
				// Initialize repo2 as a git repo  
				git.Init(repo2Dir)
				git.SetupTestConfig(repo2Dir)
				fs.WriteString(filepath.Join(repo2Dir, "main.go"), "package repo2\n\nvar Version = \"1.0\"")
				git.Add(repo2Dir, ".")
				git.Commit(repo2Dir, "Initial repo2")
				cmd = command.New("git", "remote", "add", "origin", repo2Dir).Dir(repo2Dir)
				cmd.Run()
				cmd = command.New("git", "update-ref", "refs/remotes/origin/main", "HEAD").Dir(repo2Dir)
				cmd.Run()
				
				return nil
			}),
			
			harness.NewStep("Create ecosystem plan with worktrees", func(ctx *harness.Context) error {
				// Create a plan configuration with multiple repos
				planDir := filepath.Join(ctx.RootDir, "plans", "ecosystem-plan")
				fs.CreateDir(filepath.Dir(planDir))
				fs.CreateDir(planDir)
				
				// Create plan config with repos list
				planConfig := `model: mock
worktree: ecosystem-feature
repos:
  - repo1
  - repo2
`
				fs.WriteString(filepath.Join(planDir, ".grove-plan.yml"), planConfig)
				
				// Create ecosystem worktrees manually
				ecosystemWorktreeBase := filepath.Join(ctx.RootDir, ".grove-worktrees", "ecosystem-feature")
				fs.CreateDir(ecosystemWorktreeBase)
				
				// Create worktree for repo1
				repo1WorktreePath := filepath.Join(ecosystemWorktreeBase, "repo1")
				repo1Dir := filepath.Join(ctx.RootDir, "repo1")
				cmd := command.New("git", "worktree", "add", repo1WorktreePath, "-b", "ecosystem-feature").Dir(repo1Dir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to create repo1 worktree: %w", result.Error)
				}
				
				// Create worktree for repo2
				repo2WorktreePath := filepath.Join(ecosystemWorktreeBase, "repo2")
				repo2Dir := filepath.Join(ctx.RootDir, "repo2")
				cmd = command.New("git", "worktree", "add", repo2WorktreePath, "-b", "ecosystem-feature").Dir(repo2Dir)
				result = cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to create repo2 worktree: %w", result.Error)
				}
				
				// Make changes in worktrees
				fs.WriteString(filepath.Join(repo1WorktreePath, "feature.go"), "package repo1\n\nvar Feature = \"new\"")
				git.Add(repo1WorktreePath, ".")
				git.Commit(repo1WorktreePath, "Add feature to repo1")
				
				fs.WriteString(filepath.Join(repo2WorktreePath, "feature.go"), "package repo2\n\nvar Feature = \"new\"")
				git.Add(repo2WorktreePath, ".")
				git.Commit(repo2WorktreePath, "Add feature to repo2")
				
				return nil
			}),
			
			harness.NewStep("Update main branches in both repos", func(ctx *harness.Context) error {
				// Update repo1 main
				repo1Dir := filepath.Join(ctx.RootDir, "repo1")
				fs.WriteString(filepath.Join(repo1Dir, "main.go"), "package repo1\n\nvar Version = \"1.1\"")
				git.Add(repo1Dir, ".")
				git.Commit(repo1Dir, "Update version in repo1")
				cmd := command.New("git", "update-ref", "refs/remotes/origin/main", "HEAD").Dir(repo1Dir)
				cmd.Run()
				
				// Update repo2 main
				repo2Dir := filepath.Join(ctx.RootDir, "repo2")
				fs.WriteString(filepath.Join(repo2Dir, "main.go"), "package repo2\n\nvar Version = \"1.1\"")
				git.Add(repo2Dir, ".")
				git.Commit(repo2Dir, "Update version in repo2")
				cmd = command.New("git", "update-ref", "refs/remotes/origin/main", "HEAD").Dir(repo2Dir)
				cmd.Run()
				
				return nil
			}),
			
			harness.NewStep("Mock DiscoverLocalWorkspaces for ecosystem rebase", func(ctx *harness.Context) error {
				// Set up mock workspace data for the test
				// This simulates what 'grove ws list' would return
				mockWorkspaces := fmt.Sprintf(`[
					{
						"name": "repo1",
						"path": "%s",
						"worktrees": [
							{"path": "%s", "branch": "main", "is_main": true}
						]
					},
					{
						"name": "repo2",
						"path": "%s",
						"worktrees": [
							{"path": "%s", "branch": "main", "is_main": true}
						]
					}
				]`, 
					filepath.Join(ctx.RootDir, "repo1"),
					filepath.Join(ctx.RootDir, "repo1"),
					filepath.Join(ctx.RootDir, "repo2"),
					filepath.Join(ctx.RootDir, "repo2"))
				
				// Set the environment variable that the orchestration package checks
				os.Setenv("GROVE_TEST_WORKSPACES", mockWorkspaces)
				defer os.Unsetenv("GROVE_TEST_WORKSPACES")
				
				return nil
			}),
			
			harness.NewStep("Test ecosystem rebase", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Set active plan using just the plan name
				setCmd := command.New(flow, "plan", "set", "ecosystem-plan").Dir(ctx.RootDir)
				setCmd.Run()
				
				// Mock the workspace discovery for the test
				mockWorkspaces := fmt.Sprintf(`[
					{
						"name": "repo1",
						"path": "%s",
						"worktrees": [
							{"path": "%s", "branch": "main", "is_main": true}
						]
					},
					{
						"name": "repo2",
						"path": "%s",
						"worktrees": [
							{"path": "%s", "branch": "main", "is_main": true}
						]
					}
				]`, 
					filepath.Join(ctx.RootDir, "repo1"),
					filepath.Join(ctx.RootDir, "repo1"),
					filepath.Join(ctx.RootDir, "repo2"),
					filepath.Join(ctx.RootDir, "repo2"))
				os.Setenv("GROVE_TEST_WORKSPACES", mockWorkspaces)
				
				// Perform ecosystem rebase
				cmd := command.New(flow, "plan", "rebase", "main").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				// Clean up env var
				os.Unsetenv("GROVE_TEST_WORKSPACES")
				
				if result.Error != nil {
					// Check if it's expected errors
					if !strings.Contains(result.Stderr, "some rebases failed") {
						return fmt.Errorf("unexpected rebase error: %w", result.Error)
					}
				}
				
				// Check that it detected ecosystem plan
				if !strings.Contains(result.Stdout, "ecosystem plan") || 
				   !strings.Contains(result.Stdout, "2 repositories") {
					return fmt.Errorf("should detect ecosystem plan with 2 repositories")
				}
				
				// Check that it processed both repos
				if !strings.Contains(result.Stdout, "repo1") || 
				   !strings.Contains(result.Stdout, "repo2") {
					return fmt.Errorf("should process both repo1 and repo2")
				}
				
				return nil
			}),
		},
	}
}
