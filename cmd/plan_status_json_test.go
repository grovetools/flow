package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlanStatusJSONIntegration tests the actual flow binary output
func TestPlanStatusJSONIntegration(t *testing.T) {
	// Skip if flow binary is not available
	flowPath := "./flow"
	if _, err := os.Stat(flowPath); os.IsNotExist(err) {
		flowPath = "../flow"
		if _, err := os.Stat(flowPath); os.IsNotExist(err) {
			t.Skip("flow binary not found")
		}
	}

	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "grove-integration-test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	planDir := filepath.Join(tmpDir, "test-plan")

	// Initialize a plan
	cmd := exec.Command(flowPath, "plan", "init", planDir, "-s", "/dev/null")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "Failed to init plan: %s", string(output))

	// Create a job file
	jobContent := `---
id: test-job-1
title: Test Job
status: pending
type: oneshot
---

This is a test job.
`
	err = os.WriteFile(filepath.Join(planDir, "01-test.md"), []byte(jobContent), 0644)
	require.NoError(t, err)

	// Test 1: Check --json flag
	t.Run("JSONFlag", func(t *testing.T) {
		cmd := exec.Command(flowPath, "plan", "status", planDir, "--json")
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		
		err := cmd.Run()
		require.NoError(t, err, "Command failed: %s", stderr.String())
		
		output := stdout.String()
		
		// Check that output is pure JSON
		assert.NotEmpty(t, output)
		
		// Should start with { (allowing for whitespace)
		trimmed := bytes.TrimSpace([]byte(output))
		assert.True(t, bytes.HasPrefix(trimmed, []byte("{")), 
			"Output should start with {, but got: %s", string(trimmed[:min(100, len(trimmed))]))
		
		// Parse JSON
		var result map[string]interface{}
		err = json.Unmarshal(trimmed, &result)
		assert.NoError(t, err, "Failed to parse JSON: %s", output)
		
		// Verify structure
		assert.Contains(t, result, "plan")
		assert.Contains(t, result, "jobs")
		assert.Contains(t, result, "statistics")
	})

	// Test 2: Check --format json flag
	t.Run("FormatJSONFlag", func(t *testing.T) {
		cmd := exec.Command(flowPath, "plan", "status", planDir, "--format", "json")
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		
		err := cmd.Run()
		require.NoError(t, err, "Command failed: %s", stderr.String())
		
		output := stdout.String()
		
		// Check that output is pure JSON
		assert.NotEmpty(t, output)
		
		// Should start with { (allowing for whitespace)
		trimmed := bytes.TrimSpace([]byte(output))
		assert.True(t, bytes.HasPrefix(trimmed, []byte("{")), 
			"Output should start with {, but got: %s", string(trimmed[:min(100, len(trimmed))]))
		
		// Parse JSON
		var result map[string]interface{}
		err = json.Unmarshal(trimmed, &result)
		assert.NoError(t, err, "Failed to parse JSON: %s", output)
	})
}