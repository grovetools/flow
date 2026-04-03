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

	// Test 2: Check failed job details in JSON output
	t.Run("FailedJobDetails", func(t *testing.T) {
		// Create a plan with a failing job
		failDir := filepath.Join(tmpDir, "fail-plan")
		initCmd := exec.Command(flowPath, "plan", "init", failDir, "-s", "/dev/null")
		initOutput, err := initCmd.CombinedOutput()
		require.NoError(t, err, "Failed to init fail plan: %s", string(initOutput))

		// Create a failing shell job
		failJobContent := `---
id: fail-job-1
title: Failing Job
status: pending
type: shell
prompt: "exit 1"
---

This job will fail.
`
		err = os.WriteFile(filepath.Join(failDir, "01-failing-job.md"), []byte(failJobContent), 0644)
		require.NoError(t, err)

		// Create a successful job
		successJobContent := `---
id: success-job-1
title: Successful Job
status: pending
type: shell
prompt: "echo success"
---

This job will succeed.
`
		err = os.WriteFile(filepath.Join(failDir, "02-successful-job.md"), []byte(successJobContent), 0644)
		require.NoError(t, err)

		// Run the plan (expect failure)
		runCmd := exec.Command(flowPath, "plan", "run", failDir, "--all", "--yes")
		runCmd.Env = append(os.Environ(), "GROVE_SKIP_PID_CHECK=true")
		_ = runCmd.Run() // Expected to fail

		// Get JSON status
		statusCmd := exec.Command(flowPath, "plan", "status", failDir, "--json")
		statusCmd.Env = append(os.Environ(), "GROVE_SKIP_PID_CHECK=true")
		var stdout, stderr bytes.Buffer
		statusCmd.Stdout = &stdout
		statusCmd.Stderr = &stderr

		err = statusCmd.Run()
		require.NoError(t, err, "Status command failed: %s", stderr.String())

		var result struct {
			Jobs []struct {
				Title     string `json:"title"`
				Status    string `json:"status"`
				LastError string `json:"last_error"`
				LogPath   string `json:"log_path"`
			} `json:"jobs"`
		}
		err = json.Unmarshal(stdout.Bytes(), &result)
		require.NoError(t, err, "Failed to parse JSON: %s", stdout.String())
		require.Len(t, result.Jobs, 2)

		// Find the failing and successful jobs
		for _, job := range result.Jobs {
			if job.Status == "failed" {
				assert.NotEmpty(t, job.LastError, "failed job should have last_error")
				assert.NotEmpty(t, job.LogPath, "failed job should have log_path")
			} else if job.Status == "completed" {
				assert.Empty(t, job.LastError, "completed job should not have last_error")
				assert.Empty(t, job.LogPath, "completed job should not have log_path")
			}
		}
	})

	// Test 3: Check --format json flag
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
