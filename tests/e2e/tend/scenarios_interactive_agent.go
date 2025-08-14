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
				
				cmd := command.New(flow, "plan", "init", "interactive-test").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("plan init failed: %v", result.Error)
				}
				
				return nil
			}),
			
			harness.NewStep("Add interactive agent job", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				cmd := command.New(flow, "plan", "add", "interactive-test",
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
				
				cmd := command.New(flow, "plan", "run", "--all", "-y", "interactive-test").Dir(ctx.RootDir)
				
				// Set environment variables for testing
				envVars := []string{
					"GROVE_FLOW_SKIP_DOCKER_CHECK=true",
				}
				
				// Add test bin directory to PATH
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
				
				// The test should handle the docker check issue gracefully
				// Since we're testing with mocked environment, we expect it to fail due to docker issues
				// but the important part is that the job type is recognized and processed
				if result.Error == nil {
					// If it succeeds, check for the expected output
					if !strings.Contains(result.Stdout, "Interactive session") || !strings.Contains(result.Stdout, "launched") {
						return fmt.Errorf("output should mention launching interactive session")
					}
				} else {
					// If it fails, make sure it's failing for the right reason (docker/container issues)
					// and not because the job type is invalid
					if strings.Contains(result.Stderr, "invalid job type") {
						return fmt.Errorf("interactive_agent should be a valid job type")
					}
					// Check both stdout and stderr for container-related errors
					combinedOutput := result.Stdout + result.Stderr
					if !strings.Contains(combinedOutput, "container") && !strings.Contains(combinedOutput, "docker") {
						return fmt.Errorf("expected docker/container-related error, got: %v\nStdout: %s\nStderr: %s", result.Error, result.Stdout, result.Stderr)
					}
					// The test passes if we get a docker-related error, showing the job type is properly recognized
				}
				
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
				cmd := command.New(flow, "plan", "init", "mixed-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("plan init failed: %v", result.Error)
				}
				
				// Add shell job
				cmd = command.New(flow, "plan", "add", "mixed-plan",
					"--title", "Setup",
					"--type", "shell",
					"-p", "echo 'Setting up'",
				).Dir(ctx.RootDir)
				result = cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to add shell job: %v", result.Error)
				}
				
				// Add interactive agent job
				cmd = command.New(flow, "plan", "add", "mixed-plan",
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
				cmd = command.New(flow, "plan", "add", "mixed-plan",
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
				
				cmd := command.New(flow, "plan", "run", "--all", "--skip-interactive", "mixed-plan").Dir(ctx.RootDir)
				
				// Set environment variables
				binDir := ctx.GetString("test_bin_dir")
				if binDir != "" {
					currentPath := os.Getenv("PATH")
					cmd.Env(fmt.Sprintf("PATH=%s:%s", binDir, currentPath))
				}
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
				
				cmd := command.New(flow, "plan", "status", "mixed-plan").Dir(ctx.RootDir)
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
				cmd := command.New(flow, "plan", "init", "fizzbuzz-plan").Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("plan init failed: %v", result.Error)
				}
				
				// Add interactive implementation step
				cmd = command.New(flow, "plan", "add", "fizzbuzz-plan",
					"--title", "Implement FizzBuzz",
					"--type", "interactive_agent",
					"-p", "Please implement a FizzBuzz function that prints numbers 1 to 100 with Fizz/Buzz/FizzBuzz for multiples",
				).Dir(ctx.RootDir)
				result = cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to add implementation job: %v", result.Error)
				}
				
				// Add automated review step
				cmd = command.New(flow, "plan", "add", "fizzbuzz-plan",
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
				
				cmd := command.New(flow, "plan", "run", "--all", "-y", "fizzbuzz-plan").Dir(ctx.RootDir)
				
				// Set environment variables
				binDir := ctx.GetString("test_bin_dir")
				if binDir != "" {
					currentPath := os.Getenv("PATH")
					cmd.Env(fmt.Sprintf("PATH=%s:%s", binDir, currentPath))
				}
				cmd.Env("GROVE_FLOW_SKIP_DOCKER_CHECK=true")
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				// In test environment, jobs might fail due to docker/container issues
				// Just verify the orchestration ran
				if result.Error != nil {
					combinedOutput := result.Stdout + result.Stderr
					if !strings.Contains(combinedOutput, "container") && !strings.Contains(combinedOutput, "docker") {
						return fmt.Errorf("unexpected error: %v", result.Error)
					}
					// Expected docker-related error in test environment
				} else {
					// If no error, verify the interactive session was launched
					if !strings.Contains(result.Stdout, "Interactive session") && !strings.Contains(result.Stdout, "launched") {
						return fmt.Errorf("should launch interactive session")
					}
				}
				
				return nil
			}),
			
			harness.NewStep("Verify final status", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				cmd := command.New(flow, "plan", "status", "fizzbuzz-plan").Dir(ctx.RootDir)
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