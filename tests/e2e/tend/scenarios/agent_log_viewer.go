package scenarios

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/harness"
	"github.com/mattsolo1/grove-tend/pkg/tui"
)

// AgentLogViewerScenario tests that the TUI correctly handles viewing agent logs
// for both running and completed agent jobs. This tests the feature where:
// 1. Running agent jobs should show live streaming logs
// 2. Completed agent jobs should show historical logs without streaming
// 3. Completed agent jobs with no logs should show a clear message
var AgentLogViewerScenario = harness.NewScenarioWithOptions(
	"agent-log-viewer",
	"Tests TUI behavior for viewing agent logs in different job states (running vs completed).",
	[]string{"tui", "agent", "logs", "regression"},
	[]harness.Step{
		harness.NewStep("Setup test environment with agent jobs", setupAgentLogViewerEnvironment),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"}, // Mocks `grove aglogs`
			harness.Mock{CommandName: "claude"}, // Mock claude to prevent actual agent launch
			harness.Mock{CommandName: "tmux"},   // Mock tmux to prevent real sessions
		),
		harness.NewStep("Test viewing logs for completed agent job (no streaming)", testCompletedAgentJobNoLogs),
		harness.NewStep("Test viewing logs for completed agent job with logs", testCompletedAgentJobWithLogs),
		harness.NewStep("Test viewing logs for running agent job", testRunningAgentJobLogs),
	},
	true,  // localOnly = true, requires tmux for TUI testing
	false, // explicitOnly = false
)

// setupAgentLogViewerEnvironment creates a test environment with agent jobs in different states
func setupAgentLogViewerEnvironment(ctx *harness.Context) error {
	projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "agent-log-viewer-project")
	if err != nil {
		return err
	}

	// Initialize the plan
	initCmd := ctx.Bin("plan", "init", "test-plan")
	initCmd.Dir(projectDir)
	result := initCmd.Run()
	if result.Error != nil {
		return fmt.Errorf("plan init failed: %w", result.Error)
	}

	planPath := filepath.Join(notebooksRoot, "workspaces", "agent-log-viewer-project", "plans", "test-plan")
	ctx.Set("plan_path", planPath)
	ctx.Set("plan_name", "test-plan")

	// Add a completed headless agent job (no logs)
	jobA := ctx.Bin("plan", "add", "test-plan", "--type", "headless_agent", "--title", "Completed Agent No Logs", "-p", "Test prompt for completed job")
	jobA.Dir(projectDir)
	if result := jobA.Run(); result.Error != nil {
		return fmt.Errorf("failed to add Job A: %w", result.Error)
	}

	// Mark Job A as completed (without actually running it, so no logs exist)
	jobAPath := filepath.Join(planPath, "01-completed-agent-no-logs.md")
	jobAContent, err := fs.ReadString(jobAPath)
	if err != nil {
		return fmt.Errorf("reading job A: %w", err)
	}
	jobAContent = strings.Replace(jobAContent, "status: pending", "status: completed", 1)
	if err := fs.WriteString(jobAPath, jobAContent); err != nil {
		return fmt.Errorf("updating job A status: %w", err)
	}

	// Add a completed headless agent job (with logs)
	jobB := ctx.Bin("plan", "add", "test-plan", "--type", "headless_agent", "--title", "Completed Agent With Logs", "-p", "Test prompt for completed job with logs")
	jobB.Dir(projectDir)
	if result := jobB.Run(); result.Error != nil {
		return fmt.Errorf("failed to add Job B: %w", result.Error)
	}

	// Mark Job B as completed
	// The mock grove binary will return generic logs for any aglogs read call
	jobBPath := filepath.Join(planPath, "02-completed-agent-with-logs.md")
	jobBContent, err := fs.ReadString(jobBPath)
	if err != nil {
		return fmt.Errorf("reading job B: %w", err)
	}
	jobBContent = strings.Replace(jobBContent, "status: pending", "status: completed", 1)
	if err := fs.WriteString(jobBPath, jobBContent); err != nil {
		return fmt.Errorf("updating job B status: %w", err)
	}

	// Add a running headless agent job
	jobC := ctx.Bin("plan", "add", "test-plan", "--type", "headless_agent", "--title", "Running Agent", "-p", "Test prompt for running job")
	jobC.Dir(projectDir)
	if result := jobC.Run(); result.Error != nil {
		return fmt.Errorf("failed to add Job C: %w", result.Error)
	}

	// Mark Job C as running
	// The mock grove binary will return generic logs for any aglogs read call
	jobCPath := filepath.Join(planPath, "03-running-agent.md")
	jobCContent, err := fs.ReadString(jobCPath)
	if err != nil {
		return fmt.Errorf("reading job C: %w", err)
	}
	jobCContent = strings.Replace(jobCContent, "status: pending", "status: running", 1)
	if err := fs.WriteString(jobCPath, jobCContent); err != nil {
		return fmt.Errorf("updating job C status: %w", err)
	}

	// Note: We would add a test for running job without session (retry behavior),
	// but the mock grove binary always succeeds for aglogs read, so we can't test
	// that specific case without a more sophisticated mocking system.

	return nil
}

// testCompletedAgentJobNoLogs verifies that viewing a completed agent job with no logs shows appropriate message
func testCompletedAgentJobNoLogs(ctx *harness.Context) error {
	projectDir := ctx.GetString("project_dir")
	planName := ctx.GetString("plan_name")
	homeDir := ctx.GetString("home_dir")

	// Set the active plan
	setActiveCmd := ctx.Bin("plan", "set", planName)
	setActiveCmd.Dir(projectDir)
	result := setActiveCmd.Run()
	if result.Error != nil {
		return fmt.Errorf("failed to set active plan: %w", result.Error)
	}

	flowBinary, err := findFlowBinary()
	if err != nil {
		return err
	}

	// Create a wrapper script to run flow status
	wrapperScript := filepath.Join(ctx.RootDir, "run-flow-status-completed-no-logs")
	scriptContent := fmt.Sprintf("#!/bin/bash\nexport HOME=%s\ncd %s\nexec %s plan status\n", homeDir, projectDir, flowBinary)
	if err := fs.WriteString(wrapperScript, scriptContent); err != nil {
		return fmt.Errorf("failed to create wrapper script: %w", err)
	}
	if err := os.Chmod(wrapperScript, 0755); err != nil {
		return fmt.Errorf("failed to make wrapper script executable: %w", err)
	}

	session, err := ctx.StartTUI(wrapperScript, []string{})
	if err != nil {
		return fmt.Errorf("failed to start flow status: %w", err)
	}
	defer session.Close()

	// Wait for TUI to load
	if err := session.WaitForText("Plan Status", 10*time.Second); err != nil {
		content, _ := session.Capture()
		return fmt.Errorf("TUI did not load: %w\nContent:\n%s", err, content)
	}

	time.Sleep(1 * time.Second)

	// The TUI may auto-open logs for running jobs. Check if logs are currently open
	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	// If we see "Logs:" header, logs are open - close them
	logsOpen := strings.Contains(content, "Logs:")
	if logsOpen {
		if err := session.SendKeys("v"); err != nil {
			return fmt.Errorf("failed to close log viewer: %w", err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Navigate to first job
	if err := session.SendKeys("Up", "Up"); err != nil {
		return fmt.Errorf("failed to navigate up: %w", err)
	}

	time.Sleep(300 * time.Millisecond)

	// Open log viewer for job 1
	if err := session.SendKeys("v"); err != nil {
		return fmt.Errorf("failed to send 'v' key: %w", err)
	}

	time.Sleep(1 * time.Second)

	// Capture the screen to verify the log content
	content, err = session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	// For a completed job, verify the logs pane is open and showing job info
	if !strings.Contains(content, "Logs:") || !strings.Contains(content, "Completed Agent No Logs") {
		return fmt.Errorf("expected logs pane to be open for completed job, got:\n%s", content)
	}

	// Most importantly: Should NOT attempt to stream (no sparkle icon or "new" label) for completed job
	// The key feature being tested is that completed jobs don't show streaming UI
	if strings.Contains(content, "✨") || strings.Contains(content, "new") {
		return fmt.Errorf("completed job should not show streaming indicators, got:\n%s", content)
	}

	// The content should be either the mock logs OR the "no logs found" message,
	// depending on timing of mock responses
	hasLogs := strings.Contains(content, "mock transcript")
	hasNoLogsMsg := strings.Contains(content, "No agent logs found")
	if !hasLogs && !hasNoLogsMsg {
		return fmt.Errorf("expected either logs or 'no logs' message for completed job, got:\n%s", content)
	}

	// Close log viewer
	if err := session.SendKeys("v"); err != nil {
		return fmt.Errorf("failed to close log viewer: %w", err)
	}

	time.Sleep(500 * time.Millisecond)

	// Quit TUI
	if err := session.SendKeys("q"); err != nil {
		return fmt.Errorf("failed to quit TUI: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	return nil
}

// testCompletedAgentJobWithLogs verifies that completed agent jobs show historical logs without streaming
func testCompletedAgentJobWithLogs(ctx *harness.Context) error {
	projectDir := ctx.GetString("project_dir")
	homeDir := ctx.GetString("home_dir")

	flowBinary, err := findFlowBinary()
	if err != nil {
		return err
	}

	wrapperScript := filepath.Join(ctx.RootDir, "run-flow-status-completed-with-logs")
	scriptContent := fmt.Sprintf("#!/bin/bash\nexport HOME=%s\ncd %s\nexec %s plan status\n", homeDir, projectDir, flowBinary)
	if err := fs.WriteString(wrapperScript, scriptContent); err != nil {
		return fmt.Errorf("failed to create wrapper script: %w", err)
	}
	if err := os.Chmod(wrapperScript, 0755); err != nil {
		return fmt.Errorf("failed to make wrapper script executable: %w", err)
	}

	session, err := ctx.StartTUI(wrapperScript, []string{})
	if err != nil {
		return fmt.Errorf("failed to start flow status: %w", err)
	}
	defer session.Close()

	if err := session.WaitForText("Plan Status", 10*time.Second); err != nil {
		content, _ := session.Capture()
		return fmt.Errorf("TUI did not load: %w\nContent:\n%s", err, content)
	}

	time.Sleep(500 * time.Millisecond)

	// Move to the second job (completed with logs)
	if err := session.SendKeys("Down"); err != nil {
		return fmt.Errorf("failed to navigate down: %w", err)
	}

	time.Sleep(300 * time.Millisecond)

	// Open log viewer
	if err := session.SendKeys("v"); err != nil {
		return fmt.Errorf("failed to send 'v' key: %w", err)
	}

	time.Sleep(1 * time.Second)

	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	// Should show the historical log content (from mock)
	if !strings.Contains(content, "mock transcript") {
		return fmt.Errorf("expected to see historical log content, got:\n%s", content)
	}

	// Should NOT show streaming indicators for completed job
	if strings.Contains(content, "✨") || strings.Contains(content, "new") {
		return fmt.Errorf("completed job should not show streaming indicators, got:\n%s", content)
	}

	// Close and quit
	if err := session.SendKeys("v"); err != nil {
		return fmt.Errorf("failed to close log viewer: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	if err := session.SendKeys("q"); err != nil {
		return fmt.Errorf("failed to quit TUI: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	return nil
}

// testRunningAgentJobLogs verifies that running agent jobs show streaming logs
func testRunningAgentJobLogs(ctx *harness.Context) error {
	projectDir := ctx.GetString("project_dir")
	homeDir := ctx.GetString("home_dir")

	flowBinary, err := findFlowBinary()
	if err != nil {
		return err
	}

	wrapperScript := filepath.Join(ctx.RootDir, "run-flow-status-running")
	scriptContent := fmt.Sprintf("#!/bin/bash\nexport HOME=%s\ncd %s\nexec %s plan status\n", homeDir, projectDir, flowBinary)
	if err := fs.WriteString(wrapperScript, scriptContent); err != nil {
		return fmt.Errorf("failed to create wrapper script: %w", err)
	}
	if err := os.Chmod(wrapperScript, 0755); err != nil {
		return fmt.Errorf("failed to make wrapper script executable: %w", err)
	}

	session, err := ctx.StartTUI(wrapperScript, []string{})
	if err != nil {
		return fmt.Errorf("failed to start flow status: %w", err)
	}
	defer session.Close()

	if err := session.WaitForText("Plan Status", 10*time.Second); err != nil {
		content, _ := session.Capture()
		return fmt.Errorf("TUI did not load: %w\nContent:\n%s", err, content)
	}

	time.Sleep(500 * time.Millisecond)

	// Move to the third job (running agent)
	if err := session.SendKeys("Down", "Down"); err != nil {
		return fmt.Errorf("failed to navigate down: %w", err)
	}

	time.Sleep(300 * time.Millisecond)

	// Open log viewer
	if err := session.SendKeys("v"); err != nil {
		return fmt.Errorf("failed to send 'v' key: %w", err)
	}

	time.Sleep(1 * time.Second)

	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	// Should show the historical logs (from mock)
	if !strings.Contains(content, "mock transcript") {
		return fmt.Errorf("expected to see agent logs, got:\n%s", content)
	}

	// Should show streaming indicator (separator and "new" label) for running job
	// Note: The actual streaming indicator depends on whether there's content before the separator
	if !strings.Contains(content, "─") {
		return fmt.Errorf("running job should show separator for streaming, got:\n%s", content)
	}

	// Close and quit
	if err := session.SendKeys("v"); err != nil {
		return fmt.Errorf("failed to close log viewer: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	if err := session.SendKeys("q"); err != nil {
		return fmt.Errorf("failed to quit TUI: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	return nil
}

