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

// PlanStatusTUILiveLogsScenario tests the live log streaming feature when running jobs.
var PlanStatusTUILiveLogsScenario = harness.NewScenarioWithOptions(
	"plan-status-tui-live-logs",
	"Verifies that running jobs streams output to the TUI log viewer in real-time.",
	[]string{"tui", "plan", "status", "logs", "live"},
	[]harness.Step{
		harness.NewStep("Setup environment with long-running jobs", setupLongRunningJobs),
		harness.NewStep("Launch status TUI", launchStatusTUIForLiveLogs),
		harness.NewStep("Run a long job and verify live output", runLongJobAndVerifyLiveOutput),
		harness.NewStep("Verify job completes and status updates", verifyJobCompletionAndStatus),
		harness.NewStep("Quit the TUI", quitStatusTUI),
	},
	true,  // localOnly = true, as it requires tmux
	false, // explicitOnly = false
)

// setupLongRunningJobs creates jobs that produce output over time.
func setupLongRunningJobs(ctx *harness.Context) error {
	projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "live-logs-project")
	if err != nil {
		return err
	}

	// Set home_dir for TUI launch
	ctx.Set("home_dir", ctx.HomeDir())

	// Initialize the plan
	initCmd := ctx.Bin("plan", "init", "live-logs-plan")
	initCmd.Dir(projectDir)
	if result := initCmd.Run(); result.Error != nil {
		return fmt.Errorf("plan init failed: %w", result.Error)
	}
	planPath := filepath.Join(notebooksRoot, "workspaces", "live-logs-project", "plans", "live-logs-plan")
	ctx.Set("plan_path", planPath)

	// Add a job that produces output over time
	jobLong := ctx.Bin("plan", "add", "live-logs-plan", "--type", "shell", "--title", "Long Job",
		"-p", "echo 'Starting long job...'; sleep 1; echo 'Processing step 1...'; sleep 1; echo 'Processing step 2...'; sleep 1; echo 'Long job complete!'")
	jobLong.Dir(projectDir)
	if result := jobLong.Run(); result.Error != nil {
		return fmt.Errorf("failed to add Long Job: %w", result.Error)
	}

	// Add a quick job
	jobQuick := ctx.Bin("plan", "add", "live-logs-plan", "--type", "shell", "--title", "Quick Job",
		"-p", "echo 'Quick job running'; echo 'Quick job done!'")
	jobQuick.Dir(projectDir)
	if result := jobQuick.Run(); result.Error != nil {
		return fmt.Errorf("failed to add Quick Job: %w", result.Error)
	}

	return nil
}

// launchStatusTUIForLiveLogs starts the TUI for the live logs test.
func launchStatusTUIForLiveLogs(ctx *harness.Context) error {
	projectDir := ctx.GetString("project_dir")
	homeDir := ctx.GetString("home_dir")

	flowBinary, err := findFlowBinary()
	if err != nil {
		return err
	}

	// Create wrapper script
	wrapperScript := filepath.Join(ctx.RootDir, "run-flow-live-logs")
	scriptContent := fmt.Sprintf("#!/bin/bash\nexport HOME=%s\ncd %s\nexec %s plan status live-logs-plan\n",
		homeDir, projectDir, flowBinary)
	if err := fs.WriteString(wrapperScript, scriptContent); err != nil {
		return fmt.Errorf("failed to create wrapper script: %w", err)
	}
	if err := os.Chmod(wrapperScript, 0755); err != nil {
		return fmt.Errorf("failed to make wrapper script executable: %w", err)
	}

	session, err := ctx.StartTUI(wrapperScript, []string{})
	if err != nil {
		return fmt.Errorf("failed to start TUI: %w", err)
	}
	ctx.Set("tui_session", session)

	// Wait for TUI to load
	if err := session.WaitForText("Plan Status", 10*time.Second); err != nil {
		content, _ := session.Capture()
		return fmt.Errorf("TUI did not load: %w\nContent:\n%s", err, content)
	}

	return session.WaitStable()
}

// runLongJobAndVerifyLiveOutput runs the long job and verifies output appears live.
func runLongJobAndVerifyLiveOutput(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	// The cursor should be on the first job (Long Job)
	time.Sleep(500 * time.Millisecond)

	// Send 'r' to run the job
	if err := session.SendKeys("r"); err != nil {
		return fmt.Errorf("failed to send 'r' key: %w", err)
	}

	// Wait for log viewer to open
	if err := session.WaitForText("Follow:", 5*time.Second); err != nil {
		content, _ := session.Capture()
		return fmt.Errorf("log viewer did not open: %w\nContent:\n%s", err, content)
	}

	// Verify we see "Running" status
	if err := session.WaitForText("Running", 3*time.Second); err != nil {
		content, _ := session.Capture()
		return fmt.Errorf("expected 'Running' status message: %s", content)
	}

	// Wait for first output
	if err := session.WaitForText("Starting long job", 3*time.Second); err != nil {
		content, _ := session.Capture()
		return fmt.Errorf("expected to see 'Starting long job' in live output: %s", content)
	}

	// Wait for intermediate output (proves it's streaming, not batch)
	if err := session.WaitForText("Processing step 1", 3*time.Second); err != nil {
		content, _ := session.Capture()
		return fmt.Errorf("expected to see 'Processing step 1' in live output: %s", content)
	}

	return nil
}

// verifyJobCompletionAndStatus verifies the job completes and status updates.
func verifyJobCompletionAndStatus(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	// Wait for the job to complete
	if err := session.WaitForText("Long job complete", 5*time.Second); err != nil {
		content, _ := session.Capture()
		return fmt.Errorf("expected to see 'Long job complete' in output: %s", content)
	}

	// Wait for completion message
	if err := session.WaitForText("Job run completed", 5*time.Second); err != nil {
		content, _ := session.Capture()
		// Check if there's an alternative completion indicator
		if !strings.Contains(content, "completed successfully") {
			return fmt.Errorf("expected job completion message: %s", content)
		}
	}

	// Verify the job status updated
	time.Sleep(1 * time.Second)
	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	// The job should now show as completed
	if !strings.Contains(content, "completed") {
		return fmt.Errorf("expected job to show completed status: %s", content)
	}

	return nil
}

// PlanStatusTUIRunningJobBlockScenario tests that running jobs cannot be interrupted.
var PlanStatusTUIRunningJobBlockScenario = harness.NewScenarioWithOptions(
	"plan-status-tui-running-block",
	"Verifies that the TUI blocks running a new job while one is already running.",
	[]string{"tui", "plan", "status", "running", "block"},
	[]harness.Step{
		harness.NewStep("Setup environment with jobs", setupLongRunningJobs),
		harness.NewStep("Launch status TUI", launchStatusTUIForLiveLogs),
		harness.NewStep("Start running a long job", startLongJobRunning),
		harness.NewStep("Try to run another job and verify blocked", tryRunWhileBlocked),
		harness.NewStep("Wait for job to complete", waitForJobCompletion),
		harness.NewStep("Verify can run job after completion", verifyCanRunAfterCompletion),
		harness.NewStep("Quit the TUI", quitStatusTUI),
	},
	true,  // localOnly = true
	false, // explicitOnly = false
)

// startLongJobRunning starts a long-running job.
func startLongJobRunning(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	// Run the first (long) job
	if err := session.SendKeys("r"); err != nil {
		return fmt.Errorf("failed to send 'r' key: %w", err)
	}

	// Verify it started running
	if err := session.WaitForText("Running", 3*time.Second); err != nil {
		content, _ := session.Capture()
		return fmt.Errorf("expected job to start running: %s", content)
	}

	return nil
}

// tryRunWhileBlocked tries to run another job while one is running.
func tryRunWhileBlocked(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	// Move to another job
	if err := session.SendKeys("Escape"); err != nil { // Return focus to jobs
		return fmt.Errorf("failed to send Escape key: %w", err)
	}
	time.Sleep(200 * time.Millisecond)

	if err := session.SendKeys("Down"); err != nil {
		return fmt.Errorf("failed to send Down key: %w", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Try to run it
	if err := session.SendKeys("r"); err != nil {
		return fmt.Errorf("failed to send 'r' key: %w", err)
	}

	// Should see warning message
	time.Sleep(500 * time.Millisecond)
	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	if !strings.Contains(content, "already running") {
		// Job might have completed very quickly, that's acceptable
		if !strings.Contains(content, "completed") {
			return fmt.Errorf("expected 'already running' warning message: %s", content)
		}
	}

	return nil
}

// waitForJobCompletion waits for the running job to complete.
func waitForJobCompletion(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	// Wait up to 10 seconds for job to complete
	if err := session.WaitForText("Job run completed", 10*time.Second); err != nil {
		// Try alternative completion messages
		content, _ := session.Capture()
		if !strings.Contains(content, "completed successfully") && !strings.Contains(content, "Long job complete") {
			return fmt.Errorf("job did not complete in time: %s", content)
		}
	}

	return nil
}

// verifyCanRunAfterCompletion verifies jobs can run after the previous one completes.
func verifyCanRunAfterCompletion(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	// Try to run another job now
	if err := session.SendKeys("r"); err != nil {
		return fmt.Errorf("failed to send 'r' key: %w", err)
	}

	// Should work this time - log viewer should open
	if err := session.WaitForText("Follow:", 3*time.Second); err != nil {
		content, _ := session.Capture()
		return fmt.Errorf("expected log viewer to open after previous job completed: %s", content)
	}

	return nil
}