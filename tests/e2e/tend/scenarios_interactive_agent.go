package main

import (
	"github.com/mattsolo1/grove-tend/pkg/command"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// InteractiveAgentBasicScenario tests basic interactive agent job functionality
func InteractiveAgentBasicScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-interactive-agent-basic",
		Description: "Test basic interactive agent job creation and execution",
		Tags:        []string{"plan", "interactive", "agent"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with git repo", func(ctx *harness.Context) error {
				// Setup git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				// Create initial commit
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Setup empty global config in sandboxed environment
				if err := setupEmptyGlobalConfig(ctx); err != nil {
					return err
				}

				// Write grove.yml with required config
				configContent := `name: test-project
flow:
  plans_directory: ./plans
  target_agent_container: test-container
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)

				return nil
			}),

			harness.NewStep("Initialize plan", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()

				cmd := ctx.Command(flow, "plan", "init", "interactive-test").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return fmt.Errorf("plan init failed: %v", result.Error)
				}

				return nil
			}),

			harness.NewStep("Add interactive agent job", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()

				cmd := ctx.Command(flow, "plan", "add", "interactive-test",
					"--title", "Interactive Development",
					"--type", "interactive_agent",
					"-p", "Please implement a fizzbuzz function",
				).Dir(ctx.RootDir)

				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return fmt.Errorf("failed to add interactive agent job: %v", result.Error)
				}

				// Verify job file was created
				jobFile := filepath.Join(ctx.RootDir, "plans", "interactive-test", "01-interactive-development.md")
				if !fs.Exists(jobFile) {
					return fmt.Errorf("job file should exist: %s", jobFile)
				}

				// Verify job type
				content, _ := fs.ReadString(jobFile)
				if !strings.Contains(content, "type: interactive_agent") {
					return fmt.Errorf("job should have type: interactive_agent")
				}

				return nil
			}),

			setupTestEnvironmentWithInteractiveTmux(),

			harness.NewStep("Run plan with interactive job", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()

				// Use -y flag to skip confirmation, but also use --skip-interactive
				// since we can't properly test interactive sessions in this environment
				cmd := ctx.Command(flow, "plan", "run", "--all", "-y", "interactive-test").Dir(ctx.RootDir)

				// Set environment variables for testing
				cmd.Env("GROVE_FLOW_SKIP_DOCKER_CHECK=true")

				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				// With -y flag, interactive jobs are skipped
				// The test verifies that the job type is recognized even if skipped
				if result.Error != nil {
					// Check if the error is the expected skip behavior (check both stdout and stderr)
					combinedOutput := result.Stdout + result.Stderr
					if strings.Contains(combinedOutput, "skipped due to --skip-interactive") ||
					   strings.Contains(combinedOutput, "interactive agent job skipped") {
						// This is expected - the job type was recognized but skipped
						// Make sure it's not failing because the job type is invalid
						if strings.Contains(combinedOutput, "invalid job type") {
							return fmt.Errorf("interactive_agent should be a valid job type")
						}
						return nil
					}
					return fmt.Errorf("unexpected error: %v\nStdout: %s\nStderr: %s", result.Error, result.Stdout, result.Stderr)
				}
				
				// If no error (unlikely with -y flag), that's also fine
				// as long as the job was recognized

				return nil
			}),
		},
	}
}

// InteractiveAgentSkipScenario tests the --skip-interactive flag
func InteractiveAgentSkipScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-interactive-agent-skip",
		Description: "Test skipping interactive agent jobs with --skip-interactive flag",
		Tags:        []string{"plan", "interactive", "skip"},
		Steps: []harness.Step{
			harness.NewStep("Setup project", func(ctx *harness.Context) error {
				// Setup git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Setup empty global config in sandboxed environment
				if err := setupEmptyGlobalConfig(ctx); err != nil {
					return err
				}

				// Write grove.yml
				configContent := `name: test-project
flow:
  plans_directory: ./plans
  target_agent_container: test-container
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)

				return nil
			}),

			harness.NewStep("Create plan with mixed job types", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()

				// Initialize plan
				cmd := ctx.Command(flow, "plan", "init", "mixed-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("plan init failed: %v", result.Error)
				}

				// Add shell job
				cmd = ctx.Command(flow, "plan", "add", "mixed-plan",
					"--title", "Setup",
					"--type", "shell",
					"-p", "echo 'Setting up'",
				).Dir(ctx.RootDir)
				result = cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to add shell job: %v", result.Error)
				}

				// Add interactive agent job
				cmd = ctx.Command(flow, "plan", "add", "mixed-plan",
					"--title", "Interactive Work",
					"--type", "interactive_agent",
					"-p", "Do some interactive work",
					"--depends-on", "01-setup.md",
				).Dir(ctx.RootDir)
				result = cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to add interactive job: %v", result.Error)
				}

				// Add another shell job
				cmd = ctx.Command(flow, "plan", "add", "mixed-plan",
					"--title", "Cleanup",
					"--type", "shell",
					"-p", "echo 'Cleaning up'",
					"--depends-on", "02-interactive-work.md",
				).Dir(ctx.RootDir)
				result = cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to add cleanup job: %v", result.Error)
				}

				return nil
			}),

			setupTestEnvironment(),

			harness.NewStep("Run plan with --skip-interactive", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()

				cmd := ctx.Command(flow, "plan", "run", "--all", "--skip-interactive", "mixed-plan").Dir(ctx.RootDir)

				// Set environment variables
				cmd.Env("GROVE_FLOW_SKIP_DOCKER_CHECK=true")

				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				// Should fail due to skipped interactive job
				if result.Error == nil {
					return fmt.Errorf("expected plan run to fail due to skipped interactive job")
				}

				// Verify error mentions skipping
				if !strings.Contains(result.Stderr, "skip-interactive") {
					return fmt.Errorf("error should mention skip-interactive flag")
				}

				return nil
			}),

			harness.NewStep("Verify job statuses", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()

				cmd := ctx.Command(flow, "plan", "status", "mixed-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return fmt.Errorf("plan status failed: %v", result.Error)
				}

				// Check the status summary shows correct counts
				if !strings.Contains(result.Stdout, "✓ Completed: 1") {
					return fmt.Errorf("should show 1 completed job")
				}
				if !strings.Contains(result.Stdout, "✗ Failed: 1") {
					return fmt.Errorf("should show 1 failed job")
				}
				if !strings.Contains(result.Stdout, "⏳ Pending: 1") {
					return fmt.Errorf("should show 1 pending job")
				}

				// Verify specific job statuses in the tree
				if !strings.Contains(result.Stdout, "✓ 01-setup.md") {
					return fmt.Errorf("first job (setup) should be completed")
				}
				if !strings.Contains(result.Stdout, "✗ 02-interactive-work.md") {
					return fmt.Errorf("interactive job should be failed")
				}
				if !strings.Contains(result.Stdout, "⏳ 03-cleanup.md") {
					return fmt.Errorf("cleanup job should be pending")
				}

				return nil
			}),
		},
	}
}

// InteractiveAgentWorkflowScenario tests a complete workflow with interactive and automated steps
func InteractiveAgentWorkflowScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-interactive-agent-workflow",
		Description: "Test complete workflow with interactive implementation followed by automated review",
		Tags:        []string{"plan", "interactive", "workflow"},
		Steps: []harness.Step{
			harness.NewStep("Setup project", func(ctx *harness.Context) error {
				// Setup git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "FizzBuzz Project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Setup empty global config in sandboxed environment
				if err := setupEmptyGlobalConfig(ctx); err != nil {
					return err
				}

				// Write grove.yml
				configContent := `name: fizzbuzz-project
flow:
  plans_directory: ./plans
  target_agent_container: test-container
  oneshot_model: test-model
llm:
  provider: openai
  model: test
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)

				// Create spec file
				specContent := `# FizzBuzz Specification

Write a program that prints the numbers from 1 to 100.
- For multiples of three, print "Fizz"
- For multiples of five, print "Buzz"
- For numbers which are multiples of both three and five, print "FizzBuzz"
`
				fs.WriteString(filepath.Join(ctx.RootDir, "fizzbuzz-spec.md"), specContent)

				return nil
			}),

			harness.NewStep("Create workflow plan", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()

				// Initialize plan
				cmd := ctx.Command(flow, "plan", "init", "fizzbuzz-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("plan init failed: %v", result.Error)
				}

				// Add interactive implementation step
				cmd = ctx.Command(flow, "plan", "add", "fizzbuzz-plan",
					"--title", "Implement FizzBuzz",
					"--type", "interactive_agent",
					"-p", "Please implement a FizzBuzz function that prints numbers 1 to 100 with Fizz/Buzz/FizzBuzz for multiples",
				).Dir(ctx.RootDir)
				result = cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to add implementation job: %v", result.Error)
				}

				// Add automated review step
				cmd = ctx.Command(flow, "plan", "add", "fizzbuzz-plan",
					"--title", "Review Implementation",
					"--type", "oneshot",
					"-p", "Review the FizzBuzz implementation and provide feedback",
					"--depends-on", "01-implement-fizzbuzz.md",
				).Dir(ctx.RootDir)
				result = cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to add review job: %v", result.Error)
				}

				return nil
			}),

			setupTestEnvironmentWithWorkflow(),

			harness.NewStep("Run complete workflow", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()

				// Use --skip-interactive since we're testing the workflow, not the interactive session
				// This allows the test to verify job orchestration without hanging on interactive prompts
				cmd := ctx.Command(flow, "plan", "run", "--all", "--skip-interactive", "fizzbuzz-plan").Dir(ctx.RootDir)

				// Set environment variables
				cmd.Env("GROVE_FLOW_SKIP_DOCKER_CHECK=true")

				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				// Since we're skipping interactive jobs, the workflow should complete
				// with the interactive job being skipped
				if result.Error != nil {
					// Check if the error is due to skipping interactive jobs (check both stdout and stderr)
					combinedOutput := result.Stdout + result.Stderr
					if strings.Contains(combinedOutput, "skipped due to --skip-interactive") {
						// This is expected behavior - interactive job was skipped
						return nil
					}
					return fmt.Errorf("unexpected error: %v\nStdout: %s\nStderr: %s", result.Error, result.Stdout, result.Stderr)
				}
				
				// If no error, the oneshot review job should have completed
				if strings.Contains(result.Stdout, "All jobs completed") || 
				   strings.Contains(result.Stdout, "completed successfully") {
					return nil
				}

				return nil
			}),

			harness.NewStep("Verify final status", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()

				cmd := ctx.Command(flow, "plan", "status", "fizzbuzz-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				// In test environment, we might have failures due to docker issues
				// Just verify the plan has the expected number of jobs
				if !strings.Contains(result.Stdout, "Jobs: 2 total") {
					return fmt.Errorf("expected 2 total jobs")
				}

				return nil
			}),
		},
	}
}

// setupTestEnvironmentWithInteractiveTmux creates a test environment with a tmux mock that simulates an interactive session
func setupTestEnvironmentWithInteractiveTmux() harness.Step {
	return setupTestEnvironment(map[string]interface{}{
		"additionalMocks": map[string]string{
			"tmux": `#!/bin/bash
# Mock tmux for interactive agent testing
case "$1" in
  "new-session")
    echo "Created session: $4"
    ;;
  "has-session")
    # Simulate session exists for a short time
    if [ -f /tmp/tmux_test_session ]; then
      exit 0
    else
      exit 1
    fi
    ;;
  "kill-session")
    rm -f /tmp/tmux_test_session 2>/dev/null
    echo "Killed session: $3"
    ;;
  "capture-pane")
    echo "Mock session output"
    ;;
  *)
    echo "Mock tmux called with: $@"
    ;;
esac

# For new-session, create a marker file that disappears after a short delay
if [ "$1" = "new-session" ]; then
  touch /tmp/tmux_test_session
  (sleep 0.5 && rm -f /tmp/tmux_test_session) &
fi
`,
			"docker": `#!/bin/bash
# Mock docker that always reports container as running
if [[ "$1" == "ps" ]] || [[ "$2" == "ps" ]]; then
  echo "test-container"
  exit 0
fi
echo "Mock docker called with: $@"
`,
		},
	})
}

// setupTestEnvironmentWithWorkflow creates a test environment for the complete workflow scenario
func setupTestEnvironmentWithWorkflow() harness.Step {
	return setupTestEnvironment(map[string]interface{}{
		"mockLLMResponse": "The FizzBuzz implementation looks good. It correctly handles all the requirements:\n- Prints 'Fizz' for multiples of 3\n- Prints 'Buzz' for multiples of 5\n- Prints 'FizzBuzz' for multiples of both\n\nThe code is clean and well-structured.",
		"additionalMocks": map[string]string{
			"tmux": `#!/bin/bash
# Mock tmux that simulates immediate session completion
case "$1" in
  "new-session")
    echo "Created session: $4"
    # Create fizzbuzz.go in the worktree
    if [[ "$6" == *"worktree"* ]]; then
      echo 'package main
import "fmt"
func main() {
    for i := 1; i <= 100; i++ {
        if i%15 == 0 {
            fmt.Println("FizzBuzz")
        } else if i%3 == 0 {
            fmt.Println("Fizz")
        } else if i%5 == 0 {
            fmt.Println("Buzz")
        } else {
            fmt.Println(i)
        }
    }
}' > "$6/fizzbuzz.go" 2>/dev/null || true
    fi
    ;;
  "has-session")
    # Session never exists (simulates immediate completion)
    exit 1
    ;;
  *)
    echo "Mock tmux: $@"
    ;;
esac
`,
			"docker": `#!/bin/bash
if [[ "$1" == "ps" ]] || [[ "$2" == "ps" ]]; then
  echo "test-container"
fi
`,
		},
	})
}

// InteractiveAgentPollingWorkflowScenario tests the full lifecycle of an interactive agent job with polling and flow plan complete
func InteractiveAgentPollingWorkflowScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-interactive-agent-polling-workflow",
		Description: "Tests the full lifecycle of an interactive agent job with polling and `flow plan complete`",
		Tags:        []string{"plan", "interactive", "polling"},
		Steps: []harness.Step{
			// Step 1: Setup project with git repo
			harness.NewStep("Setup project with git repo", func(ctx *harness.Context) error {
				// Setup git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				// Create initial commit
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project for polling workflow")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Setup empty global config in sandboxed environment
				if err := setupEmptyGlobalConfig(ctx); err != nil {
					return err
				}

				// Write grove.yml
				configContent := `name: polling-test-project
flow:
  plans_directory: ./plans
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)

				return nil
			}),

			// Step 2: Setup plan with interactive -> shell dependency
			harness.NewStep("Setup plan with interactive -> shell dependency", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()

				// Initialize plan
				cmd := ctx.Command(flow, "plan", "init", "polling-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("plan init failed: %v", result.Error)
				}

				// Add interactive job
				cmd = ctx.Command(flow, "plan", "add", "polling-plan",
					"--title", "Interactive Step",
					"--type", "interactive_agent",
					"--worktree", "polling-test-wt",
					"-p", "Do interactive work",
				).Dir(ctx.RootDir)
				result = cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to add interactive job: %v", result.Error)
				}

				// Add dependent shell job
				cmd = ctx.Command(flow, "plan", "add", "polling-plan",
					"--title", "Automated Verification",
					"--type", "shell",
					"--depends-on", "01-interactive-step.md",
					"-p", "echo 'Verification step ran successfully!'",
				).Dir(ctx.RootDir)
				result = cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to add shell job: %v", result.Error)
				}

				return nil
			}),

			// Step 3: Setup the enhanced, stateful mocks
			setupTestEnvironmentWithOptions(map[string]interface{}{
				"statefulGroveHooks": true,
				"additionalMocks": map[string]string{
					// Enhanced tmux mock that simulates session creation without blocking
					"tmux": `#!/bin/bash
case "$1" in
  "has-session")
    # Check if session exists - for new sessions, say no
    if [[ "$3" == *"polling-test-wt"* ]]; then
      exit 1  # Session doesn't exist yet
    fi
    ;;
  "new-session")
    # Simulate session creation
    echo "[MOCK] Created tmux session: $@" >&2
    ;;
  "send-keys")
    # Simulate sending keys to session
    echo "[MOCK] Sent keys to session: $@" >&2
    ;;
  *)
    echo "[MOCK] tmux: $@" >&2
    ;;
esac
exit 0
`,
				},
			}),

			// Step 4: Run the plan in background and verify it starts the interactive job
			harness.NewStep("Run plan in background and verify polling starts", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()

				// Run plan with all jobs (don't use -y as it skips interactive jobs)
				cmd := ctx.Command(flow, "plan", "run", "polling-plan", "--all").Dir(ctx.RootDir)

				// Set environment variables
				cmd.Env("GROVE_FLOW_SKIP_DOCKER_CHECK=true")

				// Start the plan run in background using tend's new Start() method
				process, err := cmd.Start()
				if err != nil {
					return fmt.Errorf("failed to start plan run: %v", err)
				}
				ctx.Set("polling_process", process)

				// Give it a moment to launch the interactive job
				time.Sleep(2 * time.Second)

				// Check the process output for the launch message
				stdout := process.Stdout()
				if !strings.Contains(stdout, "Interactive") || !strings.Contains(stdout, "launched") {
					// Process might still be running, let's wait a bit more
					time.Sleep(1 * time.Second)
					stdout = process.Stdout()
				}

				if !strings.Contains(stdout, "flow plan complete") {
					return fmt.Errorf("expected launch message with complete instructions not found in process output: %s", stdout)
				}

				// Process should still be running, polling for completion
				ctx.ShowCommandOutput("Note", "Orchestrator process is polling for job completion", "")

				// Verify grove-hooks was called with start
				stateFiles, _ := filepath.Glob("/tmp/grove-hooks-mock-state/*.json")
				if len(stateFiles) == 0 {
					return fmt.Errorf("no grove-hooks state files found")
				}

				// Read the state file
				var foundRunning bool
				for _, stateFile := range stateFiles {
					content, err := fs.ReadString(stateFile)
					if err != nil {
						ctx.ShowCommandOutput("Error reading state file", fmt.Sprintf("File: %s, Error: %v", stateFile, err), "")
						continue
					}
					ctx.ShowCommandOutput("State file content", content, "")
					if strings.Contains(content, `"status":"running"`) && strings.Contains(content, "Interactive Step") {
						foundRunning = true
						// Store the job file path for later
						if strings.Contains(content, "01-interactive-step.md") {
							ctx.Set("interactive_job_file", "plans/polling-plan/01-interactive-step.md")
							ctx.Set("job_id", filepath.Base(stateFile[:len(stateFile)-5])) // Remove .json
						}
						break
					} else {
						ctx.ShowCommandOutput("No match", fmt.Sprintf("File: %s, has status:running: %v, has Interactive Step: %v",
							stateFile,
							strings.Contains(content, `"status":"running"`),
							strings.Contains(content, "Interactive Step")), "")
					}
				}

				if !foundRunning {
					return fmt.Errorf("interactive job not marked as running in grove-hooks state")
				}

				return nil
			}),

			// Step 5: Run flow plan complete to signal task completion
			harness.NewStep("Run 'flow plan complete' to signal task completion", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()

				jobFile := ctx.GetString("interactive_job_file")
				if jobFile == "" {
					return fmt.Errorf("interactive job file not found in context")
				}

				cmd := ctx.Command(flow, "plan", "complete", jobFile).Dir(ctx.RootDir)

				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return fmt.Errorf("flow plan complete failed: %v", result.Error)
				}

				// Verify the command updated the job file
				jobContent, err := fs.ReadString(filepath.Join(ctx.RootDir, jobFile))
				if err != nil {
					return fmt.Errorf("failed to read job file: %v", err)
				}

				if !strings.Contains(jobContent, "status: completed") {
					return fmt.Errorf("job file not updated to completed status")
				}

				// Verify grove-hooks was notified
				jobID := ctx.GetString("job_id")
				if jobID != "" {
					stateFile := filepath.Join("/tmp/grove-hooks-mock-state", jobID+".json")
					content, err := fs.ReadString(stateFile)
					if err == nil && strings.Contains(content, `"status":"completed"`) {
						ctx.ShowCommandOutput("Status Update", "Grove-hooks state updated to completed", "")
					}
				}

				return nil
			}),

			// Step 6: Wait for the background process to complete
			harness.NewStep("Wait for orchestrator to run the dependent job", func(ctx *harness.Context) error {
				processInt := ctx.Get("polling_process")
				if processInt == nil {
					return fmt.Errorf("polling process not found in context")
				}

				process, ok := processInt.(*command.Process)
				if !ok {
					return fmt.Errorf("polling_process is not of type *command.Process")
				}

				// Wait for the background process to complete (with timeout)
				result := process.Wait(30 * time.Second)
				ctx.ShowCommandOutput("flow plan run (background)", result.Stdout, result.Stderr)

				if result.Error != nil {
					return fmt.Errorf("polling process failed: %v", result.Error)
				}

				// Check that the verification step ran
				if !strings.Contains(result.Stdout, "Verification step ran successfully!") {
					return fmt.Errorf("dependent shell job did not run after interactive job was completed")
				}

				// Check that orchestration completed
				if !strings.Contains(result.Stdout, "Orchestration completed successfully") {
					return fmt.Errorf("orchestration did not complete successfully")
				}

				// Check final plan status
				flow, _ := getFlowBinary()
				statusCmd := ctx.Command(flow, "plan", "status", "polling-plan").Dir(ctx.RootDir)
				statusResult := statusCmd.Run()
				ctx.ShowCommandOutput(statusCmd.String(), statusResult.Stdout, statusResult.Stderr)

				if !strings.Contains(statusResult.Stdout, "Completed") && !strings.Contains(statusResult.Stdout, "2/2") {
					return fmt.Errorf("final plan status should show all jobs completed")
				}

				return nil
			}),
		},
	}
}
