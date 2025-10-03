// File: tests/e2e/tend/scenarios_launch_debug.go
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

// LaunchDebugScenario tests the launch command with detailed logging of tmux commands
func LaunchDebugScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-launch-debug",
		Description: "Tests launch command with detailed tmux command logging",
		Tags:        []string{"launch", "debug", "tmux"},
		Steps: []harness.Step{
			harness.NewStep("Setup git repo and config", func(ctx *harness.Context) error {
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Create config with plans directory
				configContent := `name: test-project
flow:
  target_agent_container: test-container
  plans_directory: ./plans
`
				return fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)
			}),
			harness.NewStep("Create plan with agent job", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Initialize plan
				cmd := command.New(flow, "plan", "init", "debug-plan").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to init plan: %w", err)
				}
				
				// Add agent job
				cmd = command.New(flow, "plan", "add", "debug-plan", "--title", "Debug Launch", "--type", "agent", "-p", "Test launch").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				return result.Error
			}),
			harness.NewStep("Setup debug tmux mock", func(ctx *harness.Context) error {
				// Create a more sophisticated tmux mock that logs all commands
				tmuxScript := `#!/bin/bash
# Debug tmux mock that logs all commands

LOG_FILE="$HOME/tmux-debug.log"
echo "=== TMUX MOCK CALLED ===" >> "$LOG_FILE"
echo "Date: $(date)" >> "$LOG_FILE"
echo "Command: tmux $*" >> "$LOG_FILE"
echo "PWD: $PWD" >> "$LOG_FILE"
echo "" >> "$LOG_FILE"

# Handle different tmux commands
case "$1" in
    "new-session")
        echo "Creating new session with args: $*" >> "$LOG_FILE"
        # Check for required flags
        if [[ "$*" != *"-d"* ]]; then
            echo "ERROR: Missing -d flag" >> "$LOG_FILE"
            exit 1
        fi
        if [[ "$*" != *"-s"* ]]; then
            echo "ERROR: Missing -s flag" >> "$LOG_FILE"
            exit 1
        fi
        if [[ "$*" != *"-c"* ]]; then
            echo "ERROR: Missing -c flag" >> "$LOG_FILE"
            exit 1
        fi
        echo "SUCCESS: Session created" >> "$LOG_FILE"
        ;;
    
    "new-window")
        echo "Creating new window with args: $*" >> "$LOG_FILE"
        # Extract the docker command being passed
        # Args are: new-window -t session -n agent "docker exec ..."
        # We need to skip the first 5 args to get to the docker command
        shift  # Skip "new-window"
        shift  # Skip "-t"
        shift  # Skip session name
        shift  # Skip "-n"
        shift  # Skip "agent"
        DOCKER_CMD="$*"
        echo "Docker command for window: $DOCKER_CMD" >> "$LOG_FILE"
        
        # Simulate validation of docker command
        if [[ "$DOCKER_CMD" == *"docker exec"* ]]; then
            echo "SUCCESS: Window created with docker exec command" >> "$LOG_FILE"
        else
            echo "ERROR: Invalid docker command" >> "$LOG_FILE"
            exit 1
        fi
        ;;
    
    "send-keys")
        echo "Sending keys with args: $*" >> "$LOG_FILE"
        shift 3  # Skip "send-keys -t target"
        PROMPT="$*"
        echo "Prompt being sent: $PROMPT" >> "$LOG_FILE"
        echo "SUCCESS: Keys sent" >> "$LOG_FILE"
        ;;
    
    "select-window")
        echo "Selecting window with args: $*" >> "$LOG_FILE"
        echo "SUCCESS: Window selected" >> "$LOG_FILE"
        ;;
    
    "kill-session")
        echo "Killing session with args: $*" >> "$LOG_FILE"
        echo "SUCCESS: Session killed" >> "$LOG_FILE"
        ;;
    
    *)
        echo "Unknown tmux command: $1" >> "$LOG_FILE"
        exit 1
        ;;
esac

echo "--- END OF COMMAND ---" >> "$LOG_FILE"
echo "" >> "$LOG_FILE"

# Always succeed unless we explicitly failed above
exit 0
`
				binDir := filepath.Join(ctx.RootDir, "test_bin")
				fs.CreateDir(binDir)
				tmuxPath := filepath.Join(binDir, "tmux")
				if err := fs.WriteString(tmuxPath, tmuxScript); err != nil {
					return err
				}
				if err := os.Chmod(tmuxPath, 0755); err != nil {
					return err
				}
				
				// Also create docker mock
				dockerScript := `#!/bin/bash
echo "Mock docker called with: $@" >> "$HOME/tmux-debug.log"
if [[ "$1" == "ps" ]]; then
    echo "test-container"  # Simulate container is running
fi
`
				dockerPath := filepath.Join(binDir, "docker")
				if err := fs.WriteString(dockerPath, dockerScript); err != nil {
					return err
				}
				if err := os.Chmod(dockerPath, 0755); err != nil {
					return err
				}
				
				ctx.Set("test_bin_dir", binDir)
				return nil
			}),
			harness.NewStep("Launch with debug logging", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				jobFile := filepath.Join(ctx.RootDir, "plans", "debug-plan", "01-debug-launch.md")
				
				cmd := ctx.Command(flow, "plan", "launch", jobFile).Dir(ctx.RootDir)
				// Set environment variables
				cmd.Env("GROVE_FLOW_SKIP_DOCKER_CHECK=true")
				cmd.Env("GROVE_DEBUG=1")  // Enable debug logging
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				// Even if launch fails, we want to see the debug log
				return result.Error
			}),
			harness.NewStep("Analyze tmux debug log", func(ctx *harness.Context) error {
				logPath := filepath.Join(os.Getenv("HOME"), "tmux-debug.log")
				if !fs.Exists(logPath) {
					return fmt.Errorf("tmux debug log not found at %s", logPath)
				}
				
				logContent, err := fs.ReadString(logPath)
				if err != nil {
					return fmt.Errorf("failed to read debug log: %w", err)
				}
				
				fmt.Println("=== TMUX DEBUG LOG ===")
				fmt.Println(logContent)
				fmt.Println("=== END DEBUG LOG ===")
				
				// Verify expected commands were called
				if !strings.Contains(logContent, "new-session") {
					return fmt.Errorf("tmux new-session was not called")
				}
				if !strings.Contains(logContent, "new-window") {
					return fmt.Errorf("tmux new-window was not called")
				}
				if !strings.Contains(logContent, "docker exec") {
					return fmt.Errorf("docker exec command was not passed to new-window")
				}
				if !strings.Contains(logContent, "send-keys") {
					return fmt.Errorf("tmux send-keys was not called")
				}
				
				// Clean up log file
				os.Remove(logPath)
				
				return nil
			}),
		},
	}
}

// LaunchErrorHandlingScenario tests error handling in launch command
func LaunchErrorHandlingScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-launch-error-handling",
		Description: "Tests error handling when tmux commands fail",
		Tags:        []string{"launch", "error", "tmux"},
		Steps: []harness.Step{
			harness.NewStep("Setup basic environment", func(ctx *harness.Context) error {
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial")
				
				configContent := `flow:
  target_agent_container: test-container
  plans_directory: ./plans
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)
				
				// Create plan and job
				flow, _ := getFlowBinary()
				command.New(flow, "plan", "init", "error-plan").Dir(ctx.RootDir).Run()
				command.New(flow, "plan", "add", "error-plan", "--title", "Error Test", "--type", "agent", "-p", "Test").Dir(ctx.RootDir).Run()
				
				return nil
			}),
			harness.NewStep("Test with failing tmux new-window", func(ctx *harness.Context) error {
				// Create tmux mock that fails on new-window
				tmuxScript := `#!/bin/bash
case "$1" in
    "new-session")
        exit 0  # Success
        ;;
    "new-window")
        # Simulate specific error - extract session name from args
        SESSION_NAME=$(echo "$@" | grep -oE '\S+__\S+' | head -1)
        echo "can't find session: $SESSION_NAME" >&2
        exit 1
        ;;
    "kill-session")
        exit 0  # Allow cleanup
        ;;
    *)
        exit 1
        ;;
esac
`
				binDir := filepath.Join(ctx.RootDir, "test_bin")
				fs.CreateDir(binDir)
				tmuxPath := filepath.Join(binDir, "tmux")
				fs.WriteString(tmuxPath, tmuxScript)
				os.Chmod(tmuxPath, 0755)
				
				// Docker mock
				dockerScript := `#!/bin/bash
[[ "$1" == "ps" ]] && echo "test-container"
exit 0
`
				fs.WriteString(filepath.Join(binDir, "docker"), dockerScript)
				os.Chmod(filepath.Join(binDir, "docker"), 0755)
				
				flow, _ := getFlowBinary()
				jobFile := filepath.Join(ctx.RootDir, "plans", "error-plan", "01-error-test.md")
				
				cmd := ctx.Command(flow, "plan", "launch", jobFile).Dir(ctx.RootDir)
				cmd.Env("GROVE_FLOW_SKIP_DOCKER_CHECK=true")
				
				result := cmd.Run()
				
				// Show the command output for debugging
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				// We expect this to fail
				if result.Error == nil {
					return fmt.Errorf("expected launch to fail, but it succeeded")
				}
				
				// Check that error message is informative
				// The enhanced error messages are printed to stdout
				combinedOutput := result.Stdout + result.Stderr
				
				if !strings.Contains(combinedOutput, "Failed to create agent window") &&
				   !strings.Contains(combinedOutput, "failed to create agent window") {
					return fmt.Errorf("error message should mention 'failed to create agent window', got stdout: %s, stderr: %s", result.Stdout, result.Stderr)
				}
				
				// Check for the enhanced error details
				if !strings.Contains(result.Stdout, "Docker command was:") {
					return fmt.Errorf("expected to see 'Docker command was:' in output, got: %s", result.Stdout)
				}
				
				// Check for helpful hint
				if !strings.Contains(result.Stdout, "💡") || !strings.Contains(result.Stdout, "tmux session may have been closed") {
					return fmt.Errorf("expected to see helpful hint about tmux session, got: %s", result.Stdout)
				}
				
				return nil
			}),
		},
	}
}

// LaunchDockerExecFailureScenario tests handling when docker exec fails inside tmux
func LaunchDockerExecFailureScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-launch-docker-exec-failure",
		Description: "Tests error handling when docker exec command fails",
		Tags:        []string{"launch", "error", "docker"},
		Steps: []harness.Step{
			harness.NewStep("Setup environment", func(ctx *harness.Context) error {
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial")
				
				configContent := `flow:
  target_agent_container: missing-container
  plans_directory: ./plans
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)
				
				// Create plan and job
				flow, _ := getFlowBinary()
				command.New(flow, "plan", "init", "docker-fail").Dir(ctx.RootDir).Run()
				command.New(flow, "plan", "add", "docker-fail", "--title", "Docker Fail Test", "--type", "agent", "-p", "Test docker failure").Dir(ctx.RootDir).Run()
				
				return nil
			}),
			harness.NewStep("Test with docker exec failure", func(ctx *harness.Context) error {
				// Create a more sophisticated tmux mock that simulates successful window creation
				// but the docker exec would fail (which we can't directly test without real docker)
				tmuxScript := `#!/bin/bash
LOG_FILE="$HOME/tmux-docker-fail.log"
echo "TMUX called: $*" >> "$LOG_FILE"

case "$1" in
    "new-session")
        exit 0  # Success
        ;;
    "new-window")
        # Log the docker command that would be executed
        echo "Would execute in new window: ${@:7}" >> "$LOG_FILE"
        # Check if it's trying to exec into a non-existent container
        if [[ "$*" == *"missing-container"* ]]; then
            echo "WARNING: Attempting to exec into missing-container" >> "$LOG_FILE"
            # In real scenario, tmux would create the window but docker exec would fail
            # We simulate tmux succeeding but log the potential docker issue
            exit 0
        fi
        exit 0
        ;;
    "send-keys")
        exit 0  # Success
        ;;
    "select-window")
        exit 0  # Success
        ;;
    "kill-session")
        exit 0  # Allow cleanup
        ;;
    *)
        exit 1
        ;;
esac
`
				binDir := filepath.Join(ctx.RootDir, "test_bin")
				fs.CreateDir(binDir)
				tmuxPath := filepath.Join(binDir, "tmux")
				fs.WriteString(tmuxPath, tmuxScript)
				os.Chmod(tmuxPath, 0755)
				
				// Docker mock that shows container doesn't exist
				dockerScript := `#!/bin/bash
if [[ "$1" == "ps" ]] && [[ "$*" == *"missing-container"* ]]; then
    # Container not found
    exit 1
fi
exit 0
`
				fs.WriteString(filepath.Join(binDir, "docker"), dockerScript)
				os.Chmod(filepath.Join(binDir, "docker"), 0755)
				
				flow, _ := getFlowBinary()
				jobFile := filepath.Join(ctx.RootDir, "plans", "docker-fail", "01-docker-fail-test.md")
				
				cmd := ctx.Command(flow, "plan", "launch", jobFile).Dir(ctx.RootDir)
				cmd.Env("GROVE_FLOW_SKIP_DOCKER_CHECK=true")
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				// Check if log file was created with warnings
				logPath := filepath.Join(os.Getenv("HOME"), "tmux-docker-fail.log")
				if fs.Exists(logPath) {
					logContent, _ := fs.ReadString(logPath)
					fmt.Println("=== Docker Failure Log ===")
					fmt.Println(logContent)
					fmt.Println("=== End Log ===")
					
					// Verify warning was logged
					if !strings.Contains(logContent, "WARNING: Attempting to exec into missing-container") {
						return fmt.Errorf("expected warning about missing container in log")
					}
					
					// Clean up
					os.Remove(logPath)
				}
				
				// The launch would appear to succeed from tmux's perspective
				// but the user would see the docker exec fail in the tmux window
				return result.Error
			}),
		},
	}
}

// LaunchContainerNotRunningScenario tests handling when container isn't running
func LaunchContainerNotRunningScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-launch-container-not-running",
		Description: "Tests error handling when target container is not running",
		Tags:        []string{"launch", "error", "docker", "preflight"},
		Steps: []harness.Step{
			harness.NewStep("Setup environment with non-existent container", func(ctx *harness.Context) error {
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial")
				
				configContent := `flow:
  target_agent_container: not-running-container
  plans_directory: ./plans
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)
				
				// Create plan and job
				flow, _ := getFlowBinary()
				command.New(flow, "plan", "init", "no-container").Dir(ctx.RootDir).Run()
				command.New(flow, "plan", "add", "no-container", "--title", "No Container Test", "--type", "agent", "-p", "Test missing container").Dir(ctx.RootDir).Run()
				
				return nil
			}),
			harness.NewStep("Test pre-flight check catches missing container", func(ctx *harness.Context) error {
				// Create docker mock that shows container is not running
				dockerScript := `#!/bin/bash
# Mock docker that reports container is not running
if [[ "$1" == "ps" ]]; then
    # Don't output the container name (it's not running)
    exit 0
fi
if [[ "$1" == "container" ]] && [[ "$2" == "inspect" ]]; then
    # Container doesn't exist
    echo "Error: No such container: not-running-container" >&2
    exit 1
fi
exit 0
`
				binDir := filepath.Join(ctx.RootDir, "test_bin")
				fs.CreateDir(binDir)
				dockerPath := filepath.Join(binDir, "docker")
				fs.WriteString(dockerPath, dockerScript)
				os.Chmod(dockerPath, 0755)
				
				// Don't need tmux mock since we shouldn't get that far
				
				flow, _ := getFlowBinary()
				jobFile := filepath.Join(ctx.RootDir, "plans", "no-container", "01-no-container-test.md")
				
				cmd := ctx.Command(flow, "plan", "launch", jobFile).Dir(ctx.RootDir)
				// Don't skip docker check - we want to test the pre-flight check
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				// We expect this to fail with a helpful error
				if result.Error == nil {
					return fmt.Errorf("expected launch to fail due to missing container, but it succeeded")
				}
				
				// Check that error message mentions the container isn't running
				if !strings.Contains(result.Stderr, "not running") &&
				   !strings.Contains(result.Stderr, "not-running-container") {
					return fmt.Errorf("error should mention container not running, got: %s", result.Stderr)
				}
				
				return nil
			}),
		},
	}
}

// LaunchSilentFailureScenario tests the case where tmux commands succeed but window doesn't actually open
func LaunchSilentFailureScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-launch-silent-failure",
		Description: "Tests scenario where tmux new-window succeeds but window doesn't actually open",
		Tags:        []string{"launch", "error", "tmux", "silent"},
		Steps: []harness.Step{
			harness.NewStep("Setup environment", func(ctx *harness.Context) error {
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial")
				
				configContent := `flow:
  target_agent_container: test-container
  plans_directory: ./plans
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)
				
				// Create plan and job
				flow, _ := getFlowBinary()
				command.New(flow, "plan", "init", "silent-fail").Dir(ctx.RootDir).Run()
				command.New(flow, "plan", "add", "silent-fail", "--title", "Silent Fail Test", "--type", "agent", "-p", "Test silent failure").Dir(ctx.RootDir).Run()
				
				return nil
			}),
			harness.NewStep("Test silent failure with detailed logging", func(ctx *harness.Context) error {
				// Create tmux mock that succeeds but logs what would happen
				tmuxScript := `#!/bin/bash
LOG_FILE="$HOME/tmux-silent-fail.log"
echo "=== TMUX MOCK CALLED ===" >> "$LOG_FILE"
echo "Command: tmux $*" >> "$LOG_FILE"

case "$1" in
    "new-session")
        echo "Creating session (success)" >> "$LOG_FILE"
        exit 0
        ;;
    "new-window")
        echo "SIMULATING SILENT FAILURE: new-window returns success but window doesn't actually open" >> "$LOG_FILE"
        # In real scenario, this might happen due to:
        # - Permission issues
        # - Terminal compatibility
        # - Race conditions
        # - Docker command failing immediately
        echo "Docker command that would fail: ${@:7}" >> "$LOG_FILE"
        # Return success even though window won't actually work
        exit 0
        ;;
    "send-keys")
        echo "Attempting to send keys to non-existent window" >> "$LOG_FILE"
        # This might fail in real scenario if window didn't open
        exit 0
        ;;
    "select-window")
        exit 0
        ;;
    "kill-session")
        exit 0
        ;;
    *)
        exit 1
        ;;
esac
`
				binDir := filepath.Join(ctx.RootDir, "test_bin")
				fs.CreateDir(binDir)
				tmuxPath := filepath.Join(binDir, "tmux")
				fs.WriteString(tmuxPath, tmuxScript)
				os.Chmod(tmuxPath, 0755)
				
				// Docker mock
				dockerScript := `#!/bin/bash
[[ "$1" == "ps" ]] && echo "test-container"
exit 0
`
				fs.WriteString(filepath.Join(binDir, "docker"), dockerScript)
				os.Chmod(filepath.Join(binDir, "docker"), 0755)
				
				flow, _ := getFlowBinary()
				jobFile := filepath.Join(ctx.RootDir, "plans", "silent-fail", "01-silent-fail-test.md")
				
				// Enable debug mode
				cmd := ctx.Command(flow, "plan", "launch", jobFile).Dir(ctx.RootDir)
				cmd.Env("GROVE_FLOW_SKIP_DOCKER_CHECK=true")
				cmd.Env("GROVE_DEBUG=1")
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				// Check log file
				logPath := filepath.Join(os.Getenv("HOME"), "tmux-silent-fail.log")
				if fs.Exists(logPath) {
					logContent, _ := fs.ReadString(logPath)
					fmt.Println("=== Silent Failure Log ===")
					fmt.Println(logContent)
					fmt.Println("=== End Log ===")
					
					// Verify the silent failure was simulated
					if !strings.Contains(logContent, "SIMULATING SILENT FAILURE") {
						return fmt.Errorf("expected silent failure simulation in log")
					}
					
					// Clean up
					os.Remove(logPath)
				}
				
				// The command would succeed even though the window didn't actually open
				// This demonstrates why the enhanced error logging is important
				if result.Error != nil {
					return fmt.Errorf("expected command to succeed (simulating silent failure), but got error: %v", result.Error)
				}
				
				// Check that debug output was shown
				if !strings.Contains(result.Stdout, "Debug:") {
					return fmt.Errorf("expected debug output to be shown with GROVE_DEBUG=1")
				}
				
				return nil
			}),
		},
	}
}