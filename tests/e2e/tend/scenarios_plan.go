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
					return fmt.Errorf("job file '01-create-file.md' should exist")
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
				
				// Add the agent job
				cmdAdd := command.New(flow, "plan", "add", planPath, "--title", "Refactor Code", "--type", "agent", "-p", "Refactor everything.").Dir(ctx.RootDir)
				resultAdd := cmdAdd.Run()
				ctx.ShowCommandOutput(cmdAdd.String(), resultAdd.Stdout, resultAdd.Stderr)
				return resultAdd.Error
			}),
			setupTestEnvironment(),
			harness.NewStep("Launch the agent job", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				// Plans are in ./plans/agent-plan as configured
				jobFile := filepath.Join(ctx.RootDir, "plans", "agent-plan", "01-refactor-code.md")
				
				cmd := command.New(flow, "plan", "launch", jobFile).Dir(ctx.RootDir)
				// Set environment variables for testing
				envVars := []string{"GROVE_FLOW_SKIP_DOCKER_CHECK=true"}
				
				// Add test bin directory to PATH if it exists
				binDir := ctx.GetString("test_bin_dir")
				if binDir != "" {
					currentPath := os.Getenv("PATH")
					envVars = append(envVars, fmt.Sprintf("PATH=%s:%s", binDir, currentPath))
				}
				
				for _, env := range envVars {
					cmd.Env(env)
				}
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
				cmd := command.New(flow, "plan", "init", "inheritance-plan").Dir(ctx.RootDir)
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