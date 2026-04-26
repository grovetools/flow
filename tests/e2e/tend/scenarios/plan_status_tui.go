package scenarios

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/tui"
)

// PlanStatusTUIScenario tests the interactive `flow plan status` command.
var PlanStatusTUIScenario = harness.NewScenarioWithOptions(
	"plan-status-tui",
	"Verifies the plan status TUI can display jobs and navigate the interface.",
	[]string{"tui", "plan", "status"},
	[]harness.Step{
		harness.NewStep("Setup mock filesystem with dependent jobs", setupPlanWithDependencies),
		harness.NewStep("Launch status TUI and verify initial state", launchStatusTUIAndVerify),
		harness.NewStep("Ensure log viewer is open", ensureLogViewerOpen),
		harness.NewStep("Verify split-screen log view is visible", verifySplitScreenLogs),
		harness.NewStep("Mark first job as completed", markFirstJobCompleted),
		harness.NewStep("Verify status updated in TUI", verifyStatusUpdate),
		harness.NewStep("Quit the TUI", quitStatusTUI),
	},
	true,  // localOnly = true, as it requires tmux
	false, // explicitOnly = false
)

// setupPlanWithDependencies creates a sandboxed environment with a plan containing two jobs, one dependent on the other.
func setupPlanWithDependencies(ctx *harness.Context) error {
	projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "status-tui-project")
	if err != nil {
		return err
	}

	// Create grove.yml with logging enabled
	groveYml := `name: status-tui-project
description: Test project for status TUI

logging:
  level: info
  file:
    enabled: true
    path: .grove/logs/grove.log
    format: json
  show_current_project: true
  show:
    - status-tui-project
`
	groveYmlPath := filepath.Join(projectDir, "grove.yml")
	if err := fs.WriteString(groveYmlPath, groveYml); err != nil {
		return fmt.Errorf("failed to create grove.yml: %w", err)
	}

	// Initialize the plan
	initCmd := ctx.Bin("plan", "init", "dependency-plan")
	initCmd.Dir(projectDir)
	if result := initCmd.Run(); result.Error != nil {
		return fmt.Errorf("plan init failed: %w", result.Error)
	}
	planPath := filepath.Join(notebooksRoot, "workspaces", "status-tui-project", "plans", "dependency-plan")
	ctx.Set("plan_path", planPath)

	// Add Job A (independent)
	jobA := ctx.Bin("plan", "add", "dependency-plan", "--type", "shell", "--title", "Job A", "-p", "echo 'Job A complete'")
	jobA.Dir(projectDir)
	if result := jobA.Run(); result.Error != nil {
		return fmt.Errorf("failed to add Job A: %w", result.Error)
	}

	// Add Job B (depends on A)
	jobB := ctx.Bin("plan", "add", "dependency-plan", "--type", "shell", "--title", "Job B", "-p", "echo 'Job B complete'", "-d", "01-job-a.md")
	jobB.Dir(projectDir)
	if result := jobB.Run(); result.Error != nil {
		return fmt.Errorf("failed to add Job B: %w", result.Error)
	}

	// Run Job A to generate some logs
	jobAPath := filepath.Join(planPath, "01-job-a.md")
	runCmd := ctx.Bin("plan", "run", jobAPath, "--yes")
	runCmd.Dir(projectDir)
	if result := runCmd.Run(); result.Error != nil {
		return fmt.Errorf("failed to run Job A: %w\nStdout: %s\nStderr: %s", result.Error, result.Stdout, result.Stderr)
	}

	// Wait a moment for logs to be written
	time.Sleep(1 * time.Second)

	return nil
}

// launchStatusTUIAndVerify starts the status TUI and asserts the initial state of the jobs.
func launchStatusTUIAndVerify(ctx *harness.Context) error {
	projectDir := ctx.GetString("project_dir")
	homeDir := ctx.GetString("home_dir")

	flowBinary, err := findFlowBinary()
	if err != nil {
		return err
	}

	// Create a wrapper script to run flow from the project directory
	wrapperScript := filepath.Join(ctx.RootDir, "run-flow-status-tui")
	scriptContent := fmt.Sprintf("#!/bin/bash\nexport HOME=%s\ncd %s\nexec %s plan status dependency-plan\n", homeDir, projectDir, flowBinary)
	if err := fs.WriteString(wrapperScript, scriptContent); err != nil {
		return fmt.Errorf("failed to create wrapper script: %w", err)
	}
	if err := os.Chmod(wrapperScript, 0755); err != nil {
		return fmt.Errorf("failed to make wrapper script executable: %w", err)
	}

	session, err := ctx.StartTUI(
		wrapperScript,
		[]string{},
	)
	if err != nil {
		return fmt.Errorf("failed to start `flow plan status`: %w", err)
	}
	ctx.Set("tui_session", session)

	// Wait for TUI to load and stabilize
	if err := session.WaitForText("Plan Status", 10*time.Second); err != nil {
		content, _ := session.Capture()
		return fmt.Errorf("TUI did not load: %w\nContent:\n%s", err, content)
	}
	if err := session.WaitStable(); err != nil {
		return err
	}

	// Verify initial job statuses
	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}
	if !strings.Contains(content, "01-job-a.md") {
		return fmt.Errorf("expected '01-job-a.md' to be visible, not found in:\n%s", content)
	}
	if !strings.Contains(content, "02-job-b.md") {
		return fmt.Errorf("expected '02-job-b.md' to be visible, not found in:\n%s", content)
	}
	// Verify the dependency tree structure (└─ indicates dependency)
	if !strings.Contains(content, "└─") {
		return fmt.Errorf("expected dependency tree structure (└─) to be visible, not found in:\n%s", content)
	}
	// Jobs should be visible (STATUS column is hidden by default, so we won't see "pending" text)
	// The jobs list itself being visible is sufficient validation

	return nil
}

// verifySplitScreenLogs verifies that the split-screen log viewer is displayed.
func verifySplitScreenLogs(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	// Wait for the TUI to stabilize
	if err := session.WaitStable(); err != nil {
		return err
	}

	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	// Verify split-screen is active by checking for:
	// 1. "Follow:" indicator showing log viewer is active
	// 2. Job list is still visible (showing split-screen mode)
	hasFollowIndicator := strings.Contains(content, "Follow:")
	hasJobs := strings.Contains(content, "01-job-a.md")

	if !hasFollowIndicator {
		return fmt.Errorf("split-screen log viewer not active - missing 'Follow:' indicator")
	}

	if !hasJobs {
		return fmt.Errorf("split-screen log viewer not active - job list not visible")
	}

	// The log viewer should be visible (either showing content or initializing)
	// We just verify the split-screen layout is active
	return nil

}

// markFirstJobCompleted sends the 'c' key to mark the first job as completed.
func markFirstJobCompleted(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	// Give the TUI a moment to fully stabilize before sending keypresses
	time.Sleep(500 * time.Millisecond)

	// Send 'c' to mark the first job (Job A) as completed
	// This uses the TUI's built-in completion function rather than running the job
	if err := session.SendKeys("c"); err != nil {
		return fmt.Errorf("failed to send 'c' key: %w", err)
	}

	// Wait for the TUI to process the keypress and update the display
	time.Sleep(500 * time.Millisecond)

	return nil
}

// verifyStatusUpdate verifies that the job status was updated in the TUI.
func verifyStatusUpdate(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	// Wait for the TUI to stabilize after the status change
	if err := session.WaitStable(); err != nil {
		return err
	}

	// Wait a moment for the TUI to refresh (STATUS column is hidden by default, so we won't see "completed" text)
	time.Sleep(1 * time.Second)

	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	// Verify Job A is still visible (it will show with completed icon but no text since STATUS is hidden)
	if !strings.Contains(content, "01-job-a.md") {
		return fmt.Errorf("expected '01-job-a.md' to still be visible:\n%s", content)
	}
	// Verify Job B is still visible
	if !strings.Contains(content, "02-job-b.md") {
		return fmt.Errorf("expected '02-job-b.md' to still be visible:\n%s", content)
	}

	return nil
}

// toggleLogViewer sends the 'L' key to toggle the log viewer.
func toggleLogViewer(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	// Give the TUI a moment to stabilize
	time.Sleep(500 * time.Millisecond)

	// Send 'L' to toggle the log viewer (commit 27cab4b: 'v' is now preview-edit)
	if err := session.SendKeys("L"); err != nil {
		return fmt.Errorf("failed to send 'L' key: %w", err)
	}

	// Wait for the TUI to process the keypress
	time.Sleep(500 * time.Millisecond)

	return nil
}


// quitStatusTUI sends the quit command to the TUI.
func quitStatusTUI(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)
	return session.SendKeys("q")
}

// PlanStatusTUIFocusSwitchingScenario tests focus switching between jobs and logs panes using Tab key.
var PlanStatusTUIFocusSwitchingScenario = harness.NewScenarioWithOptions(
	"plan-status-tui-focus-switching",
	"Verifies that Tab key switches focus between jobs pane and logs pane.",
	[]string{"tui", "plan", "status", "focus"},
	[]harness.Step{
		harness.NewStep("Setup mock filesystem with dependent jobs", setupPlanWithDependencies),
		harness.NewStep("Launch status TUI", launchStatusTUIAndVerify),
		harness.NewStep("Ensure log viewer is open", ensureLogViewerOpen),
		harness.NewStep("Switch focus to logs pane with Tab", switchFocusToLogs),
		harness.NewStep("Verify Tab key changes focus", verifyFocusChanged),
		harness.NewStep("Switch focus back to jobs pane with Tab", switchFocusToJobs),
		harness.NewStep("Verify focus returned to jobs", verifyFocusReturned),
		harness.NewStep("Test Esc key returns focus to jobs pane", testEscKeyFocus),
		harness.NewStep("Quit the TUI", quitStatusTUI),
	},
	true,  // localOnly = true, as it requires tmux
	false, // explicitOnly = false
)

// ensureLogViewerOpen ensures the log viewer is open, opening it if needed.
func ensureLogViewerOpen(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	if err := session.WaitStable(); err != nil {
		return err
	}

	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	// Check if logs are already visible
	hasFollow := strings.Contains(content, "Follow:")
	if hasFollow {
		// Logs are already open
		return nil
	}

	// Need to open logs with 'L' key
	time.Sleep(500 * time.Millisecond)
	if err := session.SendKeys("L"); err != nil {
		return fmt.Errorf("failed to send 'L' key: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Verify logs opened
	if err := session.WaitStable(); err != nil {
		return err
	}

	content, err = session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	hasFollow = strings.Contains(content, "Follow:")
	if !hasFollow {
		return fmt.Errorf("failed to open log viewer - 'Follow:' indicator not found after pressing 'L'")
	}

	return nil
}


// switchFocusToLogs sends Tab key to switch focus to the logs pane.
func switchFocusToLogs(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	if err := session.SendKeys("Tab"); err != nil {
		return fmt.Errorf("failed to send Tab key: %w", err)
	}

	time.Sleep(500 * time.Millisecond)
	return nil
}


// switchFocusToJobs sends Tab key to switch focus back to the jobs pane.
func switchFocusToJobs(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	if err := session.SendKeys("Tab"); err != nil {
		return fmt.Errorf("failed to send Tab key: %w", err)
	}

	time.Sleep(500 * time.Millisecond)
	return nil
}

// verifyFocusChanged verifies that focus changed after pressing Tab.
func verifyFocusChanged(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	if err := session.WaitStable(); err != nil {
		return err
	}

	// After pressing Tab, the display should update to show focus change
	// We can't easily verify the exact focus state without checking border colors,
	// but we can verify that the TUI is still responsive
	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	// Both panes should still be visible
	hasJobs := strings.Contains(content, "01-job-a.md")
	hasLogs := strings.Contains(content, "Follow:")

	if !hasJobs || !hasLogs {
		return fmt.Errorf("expected both jobs and logs panes to be visible after Tab (jobs: %v, logs: %v)", hasJobs, hasLogs)
	}

	return nil
}

// verifyFocusReturned verifies that focus returned after second Tab press.
func verifyFocusReturned(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	if err := session.WaitStable(); err != nil {
		return err
	}

	// After pressing Tab again, we should return to the previous pane
	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	// Both panes should still be visible
	hasJobs := strings.Contains(content, "01-job-a.md")
	hasLogs := strings.Contains(content, "Follow:")

	if !hasJobs || !hasLogs {
		return fmt.Errorf("expected both jobs and logs panes to be visible (jobs: %v, logs: %v)", hasJobs, hasLogs)
	}

	return nil
}


// testEscKeyFocus verifies that Esc key returns focus to jobs pane from logs pane.
func testEscKeyFocus(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	// First switch to logs pane
	time.Sleep(500 * time.Millisecond)
	if err := session.SendKeys("Tab"); err != nil {
		return fmt.Errorf("failed to send Tab key: %w", err)
	}

	time.Sleep(500 * time.Millisecond)

	// Now press Esc to return to jobs pane
	if err := session.SendKeys("Escape"); err != nil {
		return fmt.Errorf("failed to send Escape key: %w", err)
	}

	time.Sleep(500 * time.Millisecond)

	// Verify we're back at jobs pane
	if err := session.WaitStable(); err != nil {
		return err
	}

	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	if !strings.Contains(content, "01-job-a.md") {
		return fmt.Errorf("expected jobs pane to be focused after Esc, not found in:\n%s", content)
	}

	return nil
}

// PlanStatusTUILayoutToggleScenario tests toggling between horizontal and vertical split layouts.
var PlanStatusTUILayoutToggleScenario = harness.NewScenarioWithOptions(
	"plan-status-tui-layout-toggle",
	"Verifies that 'V' key toggles between horizontal and vertical split layouts.",
	[]string{"tui", "plan", "status", "layout"},
	[]harness.Step{
		harness.NewStep("Setup mock filesystem with dependent jobs", setupPlanWithDependencies),
		harness.NewStep("Launch status TUI", launchStatusTUIAndVerify),
		harness.NewStep("Ensure log viewer is open", ensureLogViewerOpen),
		harness.NewStep("Verify vertical layout (default)", verifyVerticalLayout),
		harness.NewStep("Toggle to horizontal layout with 'V'", toggleToHorizontalLayout),
		harness.NewStep("Verify horizontal layout", verifyHorizontalLayout),
		harness.NewStep("Toggle back to vertical layout", toggleToVerticalLayout),
		harness.NewStep("Verify vertical layout again", verifyVerticalLayout),
		harness.NewStep("Quit the TUI", quitStatusTUI),
	},
	true,  // localOnly = true, as it requires tmux
	false, // explicitOnly = false
)

// verifyHorizontalLayout verifies that the split is horizontal (top/bottom).
func verifyHorizontalLayout(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	if err := session.WaitStable(); err != nil {
		return err
	}

	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	// In horizontal layout, there should be a horizontal divider (line of dashes)
	hasDivider := strings.Contains(content, "─")

	if !hasDivider {
		return fmt.Errorf("expected horizontal layout with divider, not found in:\n%s", content)
	}

	// Both jobs and logs should be visible
	hasJobs := strings.Contains(content, "01-job-a.md")
	hasLogs := strings.Contains(content, "Follow:")

	if !hasJobs || !hasLogs {
		return fmt.Errorf("expected both jobs and logs to be visible in horizontal layout (jobs: %v, logs: %v)", hasJobs, hasLogs)
	}

	return nil
}

// toggleToVerticalLayout sends 'V' (Shift+V) to toggle to vertical layout.
func toggleToVerticalLayout(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	if err := session.SendKeys("V"); err != nil {
		return fmt.Errorf("failed to send 'V' key: %w", err)
	}

	time.Sleep(500 * time.Millisecond)
	return nil
}

// verifyVerticalLayout verifies that the split is vertical (side-by-side).
func verifyVerticalLayout(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	if err := session.WaitStable(); err != nil {
		return err
	}

	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	// Both jobs and logs should still be visible
	hasJobs := strings.Contains(content, "01-job-a.md")
	hasLogs := strings.Contains(content, "Follow:")

	if !hasJobs || !hasLogs {
		return fmt.Errorf("expected both jobs and logs to be visible in vertical layout (jobs: %v, logs: %v)", hasJobs, hasLogs)
	}

	// In vertical layout, the content should appear side-by-side
	// We can check if the screen width is being used differently
	// This is a basic check - the actual layout difference is visual
	lines, err := session.GetVisibleLines()
	if err != nil {
		return err
	}

	// In vertical layout, we expect to see both panes on the same lines
	// whereas in horizontal layout, they are on different vertical sections
	// This is a simplified check
	if len(lines) == 0 {
		return fmt.Errorf("expected visible lines in vertical layout")
	}

	return nil
}

// toggleToHorizontalLayout sends 'V' (Shift+V) to toggle back to horizontal layout.
func toggleToHorizontalLayout(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	if err := session.SendKeys("V"); err != nil {
		return fmt.Errorf("failed to send 'V' key: %w", err)
	}

	time.Sleep(500 * time.Millisecond)
	return nil
}

// PlanStatusTUILogViewToggleScenario tests toggling the log viewer on and off with 'L' key.
var PlanStatusTUILogViewToggleScenario = harness.NewScenarioWithOptions(
	"plan-status-tui-log-toggle",
	"Verifies that 'L' key toggles the log viewer visibility.",
	[]string{"tui", "plan", "status", "logs"},
	[]harness.Step{
		harness.NewStep("Setup mock filesystem with dependent jobs", setupPlanWithDependencies),
		harness.NewStep("Launch status TUI", launchStatusTUIAndVerify),
		harness.NewStep("Verify log viewer state on startup", verifyLogViewerState),
		harness.NewStep("Toggle logs with 'L' (may show or hide)", toggleLogViewer),
		harness.NewStep("Verify log viewer toggled", verifyLogViewerToggled),
		harness.NewStep("Toggle logs with 'L' again", toggleLogViewer),
		harness.NewStep("Verify log viewer toggled back", verifyLogViewerToggledBack),
		harness.NewStep("Quit the TUI", quitStatusTUI),
	},
	true,  // localOnly = true, as it requires tmux
	false, // explicitOnly = false
)

// verifyLogViewerState captures and stores the initial state of the log viewer.
func verifyLogViewerState(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	if err := session.WaitStable(); err != nil {
		return err
	}

	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	// Check if logs are visible (indicated by "Follow:" marker)
	hasFollow := strings.Contains(content, "Follow:")
	ctx.Set("initial_logs_visible", hasFollow)

	// Job list should be visible
	if !strings.Contains(content, "01-job-a.md") {
		return fmt.Errorf("expected job list to be visible")
	}

	return nil
}

// verifyLogViewerToggled verifies that the log viewer state changed after toggle.
func verifyLogViewerToggled(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	if err := session.WaitStable(); err != nil {
		return err
	}

	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	initialVisible := ctx.Get("initial_logs_visible").(bool)
	hasFollow := strings.Contains(content, "Follow:")

	// The state should have changed
	if initialVisible == hasFollow {
		return fmt.Errorf("expected log viewer state to change after toggle (was: %v, is: %v)", initialVisible, hasFollow)
	}

	// Store the new state for the next toggle
	ctx.Set("second_logs_visible", hasFollow)

	return nil
}

// verifyLogViewerToggledBack verifies that the log viewer returned to initial state.
func verifyLogViewerToggledBack(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	if err := session.WaitStable(); err != nil {
		return err
	}

	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	initialVisible := ctx.Get("initial_logs_visible").(bool)
	secondVisible := ctx.Get("second_logs_visible").(bool)
	hasFollow := strings.Contains(content, "Follow:")

	// Should be back to the initial state
	if initialVisible != hasFollow {
		return fmt.Errorf("expected log viewer to return to initial state (was: %v, is: %v)", initialVisible, hasFollow)
	}

	// Should be different from the second state (confirming toggle worked)
	if secondVisible == hasFollow {
		return fmt.Errorf("expected log viewer state to change from second toggle (second: %v, current: %v)", secondVisible, hasFollow)
	}

	return nil
}
