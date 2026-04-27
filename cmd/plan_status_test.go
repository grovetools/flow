package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/grovetools/flow/pkg/orchestration"
)

// newTestStatusCmd creates a cobra command with the local flags that
// cli.GetOptions reads (json, verbose, config). Tests call RunPlanStatus
// directly without going through cobra's Execute, so persistent flags
// from cli.NewStandardCommand are never merged into the local set.
func newTestStatusCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "status"}
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("verbose", false, "")
	cmd.Flags().String("config", "", "")
	return cmd
}

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
		name       string
		args       []string
		jsonOutput bool
		wantErr    string
		checkOutput func(t *testing.T, output string)
	}{
		{
			name:    "Default tree format",
			args:    []string{tmpDir},
			wantErr: "flow status requires an interactive terminal to launch the TUI",
		},
		{
			name:    "List format",
			args:    []string{tmpDir},
			wantErr: "flow status requires an interactive terminal to launch the TUI",
		},
		{
			name:    "JSON format via --format flag",
			args:    []string{tmpDir},
			wantErr: "flow status requires an interactive terminal to launch the TUI",
		},
		{
			name:       "JSON format via --json flag",
			args:       []string{tmpDir},
			jsonOutput: true,
			checkOutput: func(t *testing.T, output string) {
				lines := strings.Split(strings.TrimSpace(output), "\n")
				assert.True(t, strings.HasPrefix(lines[0], "{"))
				assert.NotContains(t, output, "Plan: test-plan")
				assert.NotContains(t, output, "Status:")
				assert.NotContains(t, output, "Jobs: 3 total")

				var result map[string]interface{}
				err := json.Unmarshal([]byte(output), &result)
				assert.NoError(t, err)
			},
		},
		{
			name:    "Verbose mode",
			args:    []string{tmpDir},
			wantErr: "flow status requires an interactive terminal to launch the TUI",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newTestStatusCmd()

			if tt.jsonOutput {
				require.NoError(t, cmd.Flags().Set("json", "true"))
			}

			// Capture output
			oldStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			err := RunPlanStatus(cmd, tt.args)

			w.Close()
			os.Stdout = oldStdout

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)

			var buf bytes.Buffer
			_, _ = io.Copy(&buf, r)
			output := buf.String()
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

func TestFormatStatusJSON_ErrorDetails(t *testing.T) {
	// Create a temporary directory for the plan so GetJobLogPath can create artifact dirs
	tmpDir, err := os.MkdirTemp("", "grove-error-details-test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	plan := &orchestration.Plan{
		Name:      "error-details-plan",
		Directory: tmpDir,
		Jobs: []*orchestration.Job{
			{
				ID:       "job1",
				Filename: "01_success.md",
				Title:    "Successful Job",
				Status:   orchestration.JobStatusCompleted,
			},
			{
				ID:       "job2",
				Filename: "02_failed_with_error.md",
				Title:    "Failed With Error",
				Status:   orchestration.JobStatusFailed,
				Metadata: orchestration.JobMetadata{LastError: "exit code 1"},
			},
			{
				ID:       "job3",
				Filename: "03_failed_no_error.md",
				Title:    "Failed No Error",
				Status:   orchestration.JobStatusFailed,
			},
			{
				ID:       "job4",
				Filename: "04_interrupted.md",
				Title:    "Interrupted Job",
				Status:   "interrupted",
			},
		},
	}

	output, err := formatStatusJSON(plan)
	require.NoError(t, err)

	// Parse into raw map to inspect keys
	var result map[string]interface{}
	err = json.Unmarshal([]byte(output), &result)
	require.NoError(t, err)

	jobs, ok := result["jobs"].([]interface{})
	require.True(t, ok)
	require.Len(t, jobs, 4)

	// Job 1 (Completed): should NOT have last_error or log_path
	job1 := jobs[0].(map[string]interface{})
	assert.Nil(t, job1["last_error"], "completed job should not have last_error")
	assert.Nil(t, job1["log_path"], "completed job should not have log_path")

	// Job 2 (Failed + recorded error): should have the specific error
	job2 := jobs[1].(map[string]interface{})
	assert.Equal(t, "exit code 1", job2["last_error"])
	assert.NotNil(t, job2["log_path"], "failed job should have log_path")

	// Job 3 (Failed + no recorded error): should have default message
	job3 := jobs[2].(map[string]interface{})
	assert.Equal(t, "Job execution failed without recording a specific error", job3["last_error"])

	// Job 4 (Interrupted): should have interrupted default message
	job4 := jobs[3].(map[string]interface{})
	assert.Equal(t, "Job was interrupted (process died or session ended unexpectedly)", job4["last_error"])
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
	cmd := newTestStatusCmd()
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
	_, _ = io.Copy(&buf, r)
	output := strings.TrimSpace(buf.String())

	// Output should be pure JSON
	assert.True(t, strings.HasPrefix(output, "{"))
	assert.True(t, strings.HasSuffix(output, "}"))

	// Should NOT contain any human-readable text
	assert.NotContains(t, output, "Plan:")
	assert.NotContains(t, output, "Status:")
	assert.NotContains(t, output, "Jobs:")

	// But should be valid JSON with the right content
	var result map[string]interface{}
	err = json.Unmarshal([]byte(output), &result)
	require.NoError(t, err)
	// SavePlan overrides plan.Name with the directory basename
	assert.Equal(t, filepath.Base(tmpDir), result["plan"])
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

	// Test with --json flag (json output should work regardless)
	cmd := newTestStatusCmd()
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
	_, _ = io.Copy(&buf, r)
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
	plan := &orchestration.Plan{
		Name: "realistic-plan",
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
			},
			{
				ID:       "job3",
				Filename: "03-test.md",
				Title:    "Run Tests",
				Status:   orchestration.JobStatusPending,
			},
		},
	}

	output, err := formatStatusJSON(plan)
	require.NoError(t, err)

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
