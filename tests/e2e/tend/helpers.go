// File: tests/e2e/tend/helpers.go
package main

import (
	"os"
	"path/filepath"

	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// getFlowBinary is a helper to find the `flow` binary path for tests.
func getFlowBinary() (string, error) {
	return FindProjectBinary()
}



// setupTestEnvironment creates a comprehensive test environment with all necessary mocks
// using the harness.SetupMocks step builder.
// Options can be passed to customize the setup:
// - mockLLMResponse: Custom LLM response (default is a generic helpful response)
// - mockDockerContainer: Container name for docker mock (default is "fake-container")
// - statefulGroveHooks: Use stateful grove-hooks mock (default is false)
func setupTestEnvironment(options ...map[string]interface{}) harness.Step {
	// Parse options
	opts := make(map[string]interface{})
	if len(options) > 0 {
		opts = options[0]
	}

	var mocks []harness.Mock

	// Configure LLM mock via environment variable
	llmResponse := "This is a mock LLM response. Based on your idea, I suggest we start by creating a basic project structure with the following components:\n\n1. A main application file\n2. Configuration management\n3. Basic tests\n\nWould you like me to help you set up the initial project structure?"
	if customResponse, ok := opts["mockLLMResponse"].(string); ok {
		llmResponse = customResponse
	}
	os.Setenv("MOCK_LLM_RESPONSE", llmResponse)
	mocks = append(mocks, harness.Mock{CommandName: "llm"})

	// Configure Docker mock
	containerName := "fake-container"
	if customContainer, ok := opts["mockDockerContainer"].(string); ok {
		containerName = customContainer
	}
	os.Setenv("MOCK_DOCKER_CONTAINER", containerName)
	mocks = append(mocks, harness.Mock{CommandName: "docker"})

	// Configure grove-hooks mock
	if stateful, ok := opts["statefulGroveHooks"].(bool); ok && stateful {
		os.Setenv("MOCK_GROVE_HOOKS_STATEFUL", "true")
	} else {
		os.Unsetenv("MOCK_GROVE_HOOKS_STATEFUL")
	}
	mocks = append(mocks, harness.Mock{CommandName: "grove-hooks"})
	
	// Add other standard mocks
	mocks = append(mocks, harness.Mock{CommandName: "tmux"})
	mocks = append(mocks, harness.Mock{CommandName: "nb"})
	mocks = append(mocks, harness.Mock{CommandName: "cx"})
	mocks = append(mocks, harness.Mock{CommandName: "grove"})

	// Use the framework's SetupMocks builder
	setupStep := harness.SetupMocks(mocks...)
	
	// Wrap the setup step to also set GROVE_HOOKS_BINARY environment variable
	return harness.NewStep("Setup test environment", func(ctx *harness.Context) error {
		// First run the original setup
		if err := setupStep.Func(ctx); err != nil {
			return err
		}
		
		// Set GROVE_HOOKS_BINARY to point to the mock
		// The harness creates mocks in a test_bin directory under the test root
		mockPath := filepath.Join(ctx.RootDir, "test_bin", "grove-hooks")
		os.Setenv("GROVE_HOOKS_BINARY", mockPath)
		
		return nil
	})
}

// setupTestEnvironmentWithOptions creates test environment with specified options
func setupTestEnvironmentWithOptions(opts map[string]interface{}) harness.Step {
	return harness.NewStep("Setup test environment with mocks", func(ctx *harness.Context) error {
		// Clean up any previous mock state
		os.RemoveAll("/tmp/grove-hooks-mock-state")
		
		// Set up the environment
		return setupTestEnvironment(opts).Func(ctx)
	})
}

