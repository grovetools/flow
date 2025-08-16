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
	return FindProjectBinary()
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


// setupTestEnvironment creates a comprehensive test environment with all necessary mocks.
// Options can be passed to customize the setup:
// - mockLLMResponse: Custom LLM response (default is a generic helpful response)
// - mockDockerContainer: Container name for docker mock (default is "fake-container")
// - additionalMocks: Map of additional mock commands to create
func setupTestEnvironment(options ...map[string]interface{}) harness.Step {
	return harness.NewStep("Setup test environment", func(ctx *harness.Context) error {
		// Parse options
		opts := make(map[string]interface{})
		if len(options) > 0 {
			opts = options[0]
		}
		
		// Create test bin directory
		binDir := filepath.Join(ctx.RootDir, "test_bin")
		if err := fs.CreateDir(binDir); err != nil {
			return err
		}
		
		// Mock LLM
		llmResponse := "This is a mock LLM response. Based on your idea, I suggest we start by creating a basic project structure with the following components:\n\n1. A main application file\n2. Configuration management\n3. Basic tests\n\nWould you like me to help you set up the initial project structure?"
		if customResponse, ok := opts["mockLLMResponse"].(string); ok {
			llmResponse = customResponse
		}
		
		llmScript := fmt.Sprintf(`#!/bin/bash
# Mock 'llm' command for testing
# Real LLM commands return just the response text, not frontmatter
echo "%s"
`, llmResponse)
		
		if err := fs.WriteString(filepath.Join(binDir, "llm"), llmScript); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Join(binDir, "llm"), 0755); err != nil {
			return err
		}
		
		// Mock tmux
		tmuxScript := "#!/bin/bash\necho \"Mock tmux called with: $@\""
		if err := fs.WriteString(filepath.Join(binDir, "tmux"), tmuxScript); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Join(binDir, "tmux"), 0755); err != nil {
			return err
		}
		
		// Mock docker
		containerName := "fake-container"
		if customContainer, ok := opts["mockDockerContainer"].(string); ok {
			containerName = customContainer
		}
		
		dockerScript := fmt.Sprintf(`#!/bin/bash
# Handle docker ps with various flag combinations
if [[ "$1" == "ps" ]]; then
  # Check if we're filtering for %s
  for arg in "$@"; do
    if [[ "$arg" == *"%s"* ]]; then
      echo "%s" # Simulate container is running
      exit 0
    fi
  done
fi
`, containerName, containerName, containerName)
		
		if err := fs.WriteString(filepath.Join(binDir, "docker"), dockerScript); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Join(binDir, "docker"), 0755); err != nil {
			return err
		}
		
		// Mock grove-hooks to prevent sessions from being created during tests
		groveHooksScript := `#!/bin/bash
# Mock 'grove-hooks' command for testing
# This prevents actual sessions from being created during e2e tests
# Simply acknowledge the command was called
echo "[MOCK] grove-hooks called with: $@" >&2
# Log to a file for debugging
MOCK_LOG="/tmp/grove-hooks-mock.log"
echo "$(date): grove-hooks $@" >> "$MOCK_LOG"
echo "PATH=$PATH" >> "$MOCK_LOG"
echo "PWD=$PWD" >> "$MOCK_LOG"
echo "STDIN:" >> "$MOCK_LOG"
cat >> "$MOCK_LOG"
echo -e "\n---" >> "$MOCK_LOG"
exit 0
`
		if err := fs.WriteString(filepath.Join(binDir, "grove-hooks"), groveHooksScript); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Join(binDir, "grove-hooks"), 0755); err != nil {
			return err
		}
		
		// Add any additional mocks
		if additionalMocks, ok := opts["additionalMocks"].(map[string]string); ok {
			for name, script := range additionalMocks {
				if err := fs.WriteString(filepath.Join(binDir, name), script); err != nil {
					return err
				}
				if err := os.Chmod(filepath.Join(binDir, name), 0755); err != nil {
					return err
				}
			}
		}
		
		// Store the bin directory in context for later use
		ctx.Set("test_bin_dir", binDir)
		return nil
	})
}