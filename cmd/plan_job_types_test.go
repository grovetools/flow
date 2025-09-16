package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanJobTypesCommand(t *testing.T) {
	// Ensure binary exists
	flowPath := "./bin/flow"
	if _, err := os.Stat(flowPath); os.IsNotExist(err) {
		// Try to build if it doesn't exist
		buildCmd := exec.Command("make", "build")
		if buildErr := buildCmd.Run(); buildErr != nil {
			t.Skip("Skipping test: flow binary not available and build failed")
		}
	}

	t.Run("text output", func(t *testing.T) {
		// Execute command
		cmd := exec.Command(flowPath, "plan", "job-types")
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		require.NoError(t, err, "Command failed: %s", stderr.String())

		output := stdout.String()

		// Verify header
		assert.Contains(t, output, "TYPE")
		assert.Contains(t, output, "DESCRIPTION")

		// Verify job types are present
		assert.Contains(t, output, "oneshot")
		assert.Contains(t, output, "agent")
		assert.Contains(t, output, "headless_agent")
		assert.Contains(t, output, "shell")
		assert.Contains(t, output, "chat")
		assert.Contains(t, output, "interactive_agent")
		assert.Contains(t, output, "generate-recipe")

		// Verify descriptions
		assert.Contains(t, output, "Single execution job")
		assert.Contains(t, output, "Autonomous agent job")
		assert.Contains(t, output, "Headless agent job")
		assert.Contains(t, output, "Shell command execution")
		assert.Contains(t, output, "Interactive chat session")
		assert.Contains(t, output, "Interactive agent job")
		assert.Contains(t, output, "Recipe generation job")
	})

	t.Run("JSON output", func(t *testing.T) {
		// Execute command with --json flag
		cmd := exec.Command(flowPath, "plan", "job-types", "--json")
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		require.NoError(t, err, "Command failed: %s", stderr.String())

		output := stdout.String()

		// Parse JSON
		var jobTypes []jobTypeInfo
		err = json.Unmarshal([]byte(output), &jobTypes)
		require.NoError(t, err, "output should be valid JSON")

		// Verify we have all job types
		assert.Len(t, jobTypes, 7)

		// Create a map for easier verification
		typeMap := make(map[string]string)
		for _, jt := range jobTypes {
			typeMap[jt.Type] = jt.Description
		}

		// Verify specific job types
		assert.Equal(t, "Single execution job that runs once", typeMap["oneshot"])
		assert.Equal(t, "Autonomous agent job for complex tasks", typeMap["agent"])
		assert.Equal(t, "Headless agent job without user interaction", typeMap["headless_agent"])
		assert.Equal(t, "Shell command execution job", typeMap["shell"])
		assert.Equal(t, "Interactive chat session job", typeMap["chat"])
		assert.Equal(t, "Interactive agent job with user input", typeMap["interactive_agent"])
		assert.Equal(t, "Recipe generation job for automation", typeMap["generate-recipe"])
	})

	t.Run("aliases work", func(t *testing.T) {
		// Execute command with alias
		cmd := exec.Command(flowPath, "plan", "types")
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		require.NoError(t, err, "Command failed: %s", stderr.String())

		output := stdout.String()

		// Verify output contains expected content
		assert.Contains(t, output, "TYPE")
		assert.Contains(t, output, "oneshot")
	})

	t.Run("no args accepted", func(t *testing.T) {
		// Execute command with unexpected args
		cmd := exec.Command(flowPath, "plan", "job-types", "extra-arg")
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()

		// Should error due to unexpected args
		require.Error(t, err)
		errOutput := stderr.String()
		assert.True(t, strings.Contains(errOutput, "unknown command") || strings.Contains(errOutput, "accepts 0 arg"))
	})
}