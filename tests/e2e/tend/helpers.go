// File: tests/e2e/tend/helpers.go
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mattsolo1/grove-tend/pkg/command"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// getFlowBinary is a helper to find the `flow` binary path for tests.
func getFlowBinary() (string, error) {
	flowBinary := os.Getenv("FLOW_BINARY")
	if flowBinary != "" {
		return flowBinary, nil
	}

	// Try to find the binary relative to the test execution directory
	candidates := []string{
		"./bin/flow",
		"../bin/flow",
		"../../bin/flow",
		"../../../bin/flow",
		"../../../../bin/flow",
	}

	for _, candidate := range candidates {
		if absPath, err := filepath.Abs(candidate); err == nil {
			if _, err := os.Stat(absPath); err == nil {
				return absPath, nil
			}
		}
	}

	return "", fmt.Errorf("flow binary not found. Build it with 'make build' or set FLOW_BINARY env var")
}

// getCommandWithTestBin returns a function that prepends the test bin directory to PATH.
func getCommandWithTestBin(ctx *harness.Context) func(program string, args ...string) *command.Command {
	return func(program string, args ...string) *command.Command {
		cmd := command.New(program, args...)
		binDir := ctx.GetString("test_bin_dir")
		if binDir != "" {
			currentPath := os.Getenv("PATH")
			cmd.Env(fmt.Sprintf("PATH=%s:%s", binDir, currentPath))
		}
		return cmd
	}
}

// setupMockLLM creates a mock 'llm' command and adds it to the PATH.
func setupMockLLM() harness.Step {
	return harness.NewStep("Setup mock LLM command", func(ctx *harness.Context) error {
		scriptContent := `#!/bin/bash
# Mock 'llm' command for testing
# Real LLM commands return just the response text, not frontmatter
echo "This is a mock LLM response. Based on your idea, I suggest we start by creating a basic project structure with the following components:

1. A main application file
2. Configuration management
3. Basic tests

Would you like me to help you set up the initial project structure?"
`
		binDir := filepath.Join(ctx.RootDir, "test_bin")
		if err := fs.CreateDir(binDir); err != nil {
			return err
		}
		scriptPath := filepath.Join(binDir, "llm")
		if err := fs.WriteString(scriptPath, scriptContent); err != nil {
			return err
		}
		if err := os.Chmod(scriptPath, 0755); err != nil {
			return err
		}

		// Store the bin directory in context for later use
		ctx.Set("test_bin_dir", binDir)
		return nil
	})
}

// setupMocks creates mock 'tmux' and 'docker' commands.
func setupMocks() harness.Step {
	return harness.NewStep("Setup mock tmux and docker", func(ctx *harness.Context) error {
		// Mock for `tmux`
		tmuxScript := "#!/bin/bash\necho \"Mock tmux called with: $@\""

		// Mock for `docker`. We need to mock `docker ps` to show the container is running.
		dockerScript := `#!/bin/bash
# Handle docker ps with various flag combinations
if [[ "$1" == "ps" ]]; then
  # Check if we're filtering for fake-container
  for arg in "$@"; do
    if [[ "$arg" == *"fake-container"* ]]; then
      echo "fake-container" # Simulate container is running
      exit 0
    fi
  done
fi
`
		binDir := filepath.Join(ctx.RootDir, "test_bin")
		fs.CreateDir(binDir)
		fs.WriteString(filepath.Join(binDir, "tmux"), tmuxScript)
		os.Chmod(filepath.Join(binDir, "tmux"), 0755)
		fs.WriteString(filepath.Join(binDir, "docker"), dockerScript)
		os.Chmod(filepath.Join(binDir, "docker"), 0755)

		// Store the bin directory in context for later use
		ctx.Set("test_bin_dir", binDir)
		return nil
	})
}