package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/grovetools/core/cli"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunPlanStatus(t *testing.T) {
	// Create a temporary directory for test plans
	tmpDir, err := os.MkdirTemp("", "grove-plan-status-test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create a test plan
	testPlan := &orchestration.Plan{
		Name:      "test-plan",
		Directory: tmpDir,
		Jobs: []*orchestration.Job{
			{
				ID:       "job1",
				Filename: "01_setup.md",
				Title:    "Setup environment",
				Status:   orchestration.JobStatusCompleted,
			},
			{
				ID:       "job2",
				Filename: "02_build.md",
				Title:    "Build application",
				Status:   orchestration.JobStatusRunning,
				Dependencies: []*orchestration.Job{
					{ID: "job1"},
				},
			},
			{
				ID:       "job3",
				Filename: "03_test.md",
				Title:    "Run tests",
				Status:   orchestration.JobStatusPending,
				Dependencies: []*orchestration.Job{
					{ID: "job2"},
				},
			},
		},
	}

	// Save the test plan
	err = orchestration.SavePlan(tmpDir, testPlan)
	require.NoError(t, err)

	tests := []struct {
		name           string
		args           []string
		flags          map[string]interface{}
		jsonOutput     bool
		expectedFormat string
		checkOutput    func(t *testing.T, output string)
	}{
		{
			name:           "Default tree format",
			args:           []string{tmpDir},
			expectedFormat: "tree",
			checkOutput: func(t *testing.T, output string) {
				// Should contain plan name
				assert.Contains(t, output, "test-plan")
				// Should contain job filenames
				assert.Contains(t, output, "01_setup.md")
				assert.Contains(t, output, "02_build.md")
				assert.Contains(t, output, "03_test.md")
				// Should contain status summary
				assert.Contains(t, output, "Jobs: 3 total")
				assert.Contains(t, output, "Completed: 1")
				assert.Contains(t, output, "Running: 1")
				assert.Contains(t, output, "Pending: 1")
			},
		},
		{
			name:           "List format",
			args:           []string{tmpDir},
			flags:          map[string]interface{}{"format": "list"},
			expectedFormat: "list",
			checkOutput: func(t *testing.T, output string) {
				// Should contain job filenames and titles
				assert.Contains(t, output, "01_setup.md - Setup environment")
				assert.Contains(t, output, "02_build.md - Build application")
				assert.Contains(t, output, "03_test.md - Run tests")
			},
		},
		{
			name:           "JSON format via --format flag",
			args:           []string{tmpDir},
			flags:          map[string]interface{}{"format": "json"},
			expectedFormat: "json",
			checkOutput: func(t *testing.T, output string) {
				// Should be valid JSON
				var result map[string]interface{}
				err := json.Unmarshal([]byte(output), &result)
				assert.NoError(t, err)

				// Check structure
				assert.Equal(t, "test-plan", result["plan"])
				jobs, ok := result["jobs"].([]interface{})
				assert.True(t, ok)
				assert.Len(t, jobs, 3)

				stats, ok := result["statistics"].(map[string]interface{})
				assert.True(t, ok)
				assert.Equal(t, float64(3), stats["total"])
				assert.Equal(t, float64(1), stats["completed"])
				assert.Equal(t, float64(1), stats["running"])
				assert.Equal(t, float64(1), stats["pending"])
			},
		},
		{
			name:           "JSON format via --json flag",
			args:           []string{tmpDir},
			jsonOutput:     true,
			expectedFormat: "json",
			checkOutput: func(t *testing.T, output string) {
				// Should be ONLY JSON without any status summary
				lines := strings.Split(strings.TrimSpace(output), "\n")

				// First line should start with { (beginning of JSON)
				assert.True(t, strings.HasPrefix(lines[0], "{"))

				// Should NOT contain the status summary text
				assert.NotContains(t, output, "Plan: test-plan")
				assert.NotContains(t, output, "Status:")
				assert.NotContains(t, output, "Jobs: 3 total")

				// Should be valid JSON
				var result map[string]interface{}
				err := json.Unmarshal([]byte(output), &result)
				assert.NoError(t, err)
			},
		},
		{
			name:           "Verbose mode",
			args:           []string{tmpDir},
			flags:          map[string]interface{}{"verbose": true, "format": "list"},
			expectedFormat: "list",
			checkOutput: func(t *testing.T, output string) {
				// Should contain IDs in verbose mode
				assert.Contains(t, output, "ID: job1")
				assert.Contains(t, output, "ID: job2")
				assert.Contains(t, output, "ID: job3")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset flags
			statusVerbose = false
			statusGraph = false
			statusFormat = "tree"

			// Create command with standard flags
			cmd := cli.NewStandardCommand("status", "Show plan status")

			// Set up CLI options if JSON output is requested
			if tt.jsonOutput {
				// Set the json flag directly on the command
				require.NoError(t, cmd.Flags().Set("json", "true"))
			}

			// Apply test flags
			if tt.flags != nil {
				if v, ok := tt.flags["verbose"]; ok {
					statusVerbose = v.(bool)
				}
				if v, ok := tt.flags["format"]; ok {
					statusFormat = v.(string)
				}
			}

			// Capture output
			oldStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			// Run command
			err := RunPlanStatus(cmd, tt.args)
			require.NoError(t, err)

			// Restore stdout and get output
			w.Close()
			os.Stdout = oldStdout

			var buf bytes.Buffer
			io.Copy(&buf, r)
			output := buf.String()

			// Check output
			tt.checkOutput(t, output)
		})
	}
}

func TestFormatStatusJSON(t *testing.T) {
	plan := &orchestration.Plan{
		Name: "test-plan",
		Jobs: []*orchestration.Job{
			{
				ID:       "job1",
				Filename: "01_setup.md",
				Title:    "Setup",
				Status:   orchestration.JobStatusCompleted,
			},
			{
				ID:       "job2",
				Filename: "02_build.md",
				Title:    "Build",
				Status:   orchestration.JobStatusPending,
			},
		},
	}

	output, err := formatStatusJSON(plan)
	require.NoError(t, err)

	// Parse JSON
	var result map[string]interface{}
	err = json.Unmarshal([]byte(output), &result)
	require.NoError(t, err)

	// Verify structure
	assert.Equal(t, "test-plan", result["plan"])

	jobs, ok := result["jobs"].([]interface{})
	assert.True(t, ok)
	assert.Len(t, jobs, 2)

	stats, ok := result["statistics"].(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, float64(2), stats["total"])
	assert.Equal(t, float64(1), stats["completed"])
	assert.Equal(t, float64(1), stats["pending"])
}

func TestJSONOutputSuppressesHumanReadableText(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "grove-json-test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create a simple test plan
	testPlan := &orchestration.Plan{
		Name:      "json-test-plan",
		Directory: tmpDir,
		Jobs: []*orchestration.Job{
			{
				ID:       "job1",
				Filename: "test.md",
				Title:    "Test Job",
				Status:   orchestration.JobStatusPending,
			},
		},
	}

	err = orchestration.SavePlan(tmpDir, testPlan)
	require.NoError(t, err)

	// Test with --json flag
	cmd := cli.NewStandardCommand("status", "Show plan status")
	require.NoError(t, cmd.Flags().Set("json", "true"))

	// Capture output
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err = RunPlanStatus(cmd, []string{tmpDir})
	require.NoError(t, err)

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := strings.TrimSpace(buf.String())

	// Output should be pure JSON
	assert.True(t, strings.HasPrefix(output, "{"))
	assert.True(t, strings.HasSuffix(output, "}"))

	// Should NOT contain any human-readable text
	assert.NotContains(t, output, "Plan:")
	assert.NotContains(t, output, "Status:")
	assert.NotContains(t, output, "Jobs:")
	assert.NotContains(t, output, "total")

	// But should be valid JSON with the right content
	var result map[string]interface{}
	err = json.Unmarshal([]byte(output), &result)
	require.NoError(t, err)
	assert.Equal(t, "json-test-plan", result["plan"])
}

func TestJSONFlagOverridesFormatFlag(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "grove-format-override-test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create a test plan
	testPlan := &orchestration.Plan{
		Name:      "override-test-plan",
		Directory: tmpDir,
		Jobs: []*orchestration.Job{
			{
				ID:       "job1",
				Filename: "test.md",
				Title:    "Test Job",
				Status:   orchestration.JobStatusPending,
			},
		},
	}

	err = orchestration.SavePlan(tmpDir, testPlan)
	require.NoError(t, err)

	// Test with --json flag AND --format tree (json should win)
	cmd := cli.NewStandardCommand("status", "Show plan status")
	require.NoError(t, cmd.Flags().Set("json", "true"))

	// Set format to tree, but JSON should override it
	statusFormat = "tree"

	// Capture output
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err = RunPlanStatus(cmd, []string{tmpDir})
	require.NoError(t, err)

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := strings.TrimSpace(buf.String())

	// Output should still be pure JSON despite format flag
	assert.True(t, strings.HasPrefix(output, "{"))
	assert.True(t, strings.HasSuffix(output, "}"))

	// Verify it's valid JSON
	var result map[string]interface{}
	err = json.Unmarshal([]byte(output), &result)
	require.NoError(t, err)
}

func TestPlanStatusJSONOutputWithNonEmptyPlan(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "grove-nonempty-test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create a more realistic test plan with multiple jobs
	testPlan := &orchestration.Plan{
		Name:      "realistic-plan",
		Directory: tmpDir,
		Jobs: []*orchestration.Job{
			{
				ID:       "job1",
				Filename: "01-setup.md",
				Title:    "Setup Environment",
				Status:   orchestration.JobStatusCompleted,
			},
			{
				ID:       "job2",
				Filename: "02-build.md",
				Title:    "Build Application",
				Status:   orchestration.JobStatusRunning,
				Dependencies: []*orchestration.Job{
					{ID: "job1"},
				},
				DependsOn: []string{"01-setup.md"},
			},
			{
				ID:       "job3",
				Filename: "03-test.md",
				Title:    "Run Tests",
				Status:   orchestration.JobStatusPending,
				Dependencies: []*orchestration.Job{
					{ID: "job2"},
				},
				DependsOn: []string{"02-build.md"},
			},
		},
	}

	// Properly set up job references
	testPlan.JobsByID = make(map[string]*orchestration.Job)
	for _, job := range testPlan.Jobs {
		testPlan.JobsByID[job.ID] = job
	}
	// Fix dependencies
	testPlan.Jobs[1].Dependencies = []*orchestration.Job{testPlan.Jobs[0]}
	testPlan.Jobs[2].Dependencies = []*orchestration.Job{testPlan.Jobs[1]}

	err = orchestration.SavePlan(tmpDir, testPlan)
	require.NoError(t, err)

	// Test with --json flag
	cmd := cli.NewStandardCommand("status", "Show plan status")
	require.NoError(t, cmd.Flags().Set("json", "true"))

	// Capture output
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err = RunPlanStatus(cmd, []string{tmpDir})
	require.NoError(t, err)

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := strings.TrimSpace(buf.String())

	// First character should be { (start of JSON)
	assert.True(t, strings.HasPrefix(output, "{"), "Output should start with {, but got: "+output[:min(100, len(output))])

	// Parse and validate JSON structure
	var result map[string]interface{}
	err = json.Unmarshal([]byte(output), &result)
	require.NoError(t, err, "Failed to parse JSON output")

	// Verify structure
	assert.Equal(t, "realistic-plan", result["plan"])

	jobs, ok := result["jobs"].([]interface{})
	assert.True(t, ok)
	assert.Len(t, jobs, 3)

	// Check statistics
	stats, ok := result["statistics"].(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, float64(3), stats["total"])
	assert.Equal(t, float64(1), stats["completed"])
	assert.Equal(t, float64(1), stats["running"])
	assert.Equal(t, float64(1), stats["pending"])
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
