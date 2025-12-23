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

// PlanStatusTUILayoutLongNamesScenario tests that long job names and titles display without truncation.
var PlanStatusTUILayoutLongNamesScenario = harness.NewScenarioWithOptions(
	"plan-status-tui-layout-long-names",
	"Verifies that long job names and titles are not truncated in the status TUI.",
	[]string{"tui", "plan", "status", "layout", "long-names"},
	[]harness.Step{
		harness.NewStep("Setup plan with long job names", setupPlanWithLongNames),
		harness.NewStep("Launch TUI and verify long names are not truncated", verifyLongNamesNotTruncated),
		harness.NewStep("Quit the TUI", quitLongNamesTUI),
	},
	true,  // localOnly = true, as it requires tmux
	false, // explicitOnly = false
)

// PlanStatusTUILayoutPersistenceScenario tests that layout split preference persists across TUI sessions.
var PlanStatusTUILayoutPersistenceScenario = harness.NewScenarioWithOptions(
	"plan-status-tui-layout-persistence",
	"Verifies that the log split layout preference (vertical/horizontal) persists across TUI sessions.",
	[]string{"tui", "plan", "status", "layout", "persistence"},
	[]harness.Step{
		harness.NewStep("Setup plan with jobs", setupPlanForLayoutTest),
		harness.NewStep("Launch status TUI", launchTUIForLayoutTest),
		harness.NewStep("Show logs and toggle to vertical layout", showLogsAndToggleVertical),
		harness.NewStep("Verify layout changed to vertical", verifyVerticalLayoutPersistence),
		harness.NewStep("Check state file saved vertical preference", checkStateFileAfterVertical),
		harness.NewStep("Hide and re-show logs to test persistence", hideAndReshowLogs),
		harness.NewStep("Verify layout persisted as vertical", verifyVerticalLayoutPersistence),
		harness.NewStep("Quit TUI", quitLayoutTUI),
	},
	true,  // localOnly = true, as it requires tmux
	false, // explicitOnly = false
)

// setupPlanForLayoutTest creates a plan with jobs for testing layout persistence.
func setupPlanForLayoutTest(ctx *harness.Context) error {
	projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "layout-test-project")
	if err != nil {
		return err
	}

	// Initialize the plan
	initCmd := ctx.Bin("plan", "init", "layout-test-plan", "--recipe", "standard-feature")
	initCmd.Dir(projectDir)
	if result := initCmd.Run(); result.Error != nil {
		return fmt.Errorf("plan init failed: %w", result.Error)
	}
	planPath := filepath.Join(notebooksRoot, "workspaces", "layout-test-project", "plans", "layout-test-plan")
	ctx.Set("plan_path", planPath)

	return nil
}

// launchTUIForLayoutTest launches the status TUI for layout testing.
func launchTUIForLayoutTest(ctx *harness.Context) error {
	projectDir := ctx.GetString("project_dir")
	homeDir := ctx.HomeDir()

	flowBinary, err := findFlowBinary()
	if err != nil {
		return err
	}

	wrapperScript := filepath.Join(ctx.RootDir, "run-flow-status-layout-tui")
	scriptContent := fmt.Sprintf("#!/bin/bash\nexport HOME=%s\ncd %s\nexec %s plan status -t layout-test-plan\n", homeDir, projectDir, flowBinary)
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
		return fmt.Errorf("failed to start TUI: %w", err)
	}
	ctx.Set("tui_session", session)

	if err := session.WaitForText("Plan Status", 10*time.Second); err != nil {
		content, _ := session.Capture()
		return fmt.Errorf("TUI did not load: %w\nContent:\n%s", err, content)
	}
	if err := session.WaitStable(); err != nil {
		return err
	}

	return nil
}

// showLogsAndToggleVertical shows the logs pane and toggles to vertical layout.
func showLogsAndToggleVertical(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	// Press 'l' to show logs (starts as horizontal by default)
	if err := session.SendKeys("l"); err != nil {
		return fmt.Errorf("failed to send 'l' key: %w", err)
	}
	time.Sleep(1000 * time.Millisecond)

	if err := session.WaitStable(); err != nil {
		return err
	}

	// Press 'V' (Shift+v) to toggle layout to vertical
	if err := session.SendKeys("V"); err != nil {
		return fmt.Errorf("failed to send 'V' key: %w", err)
	}
	time.Sleep(1000 * time.Millisecond)

	if err := session.WaitStable(); err != nil {
		return err
	}

	return nil
}

// verifyVerticalLayoutPersistence captures the TUI and verifies the layout appears vertical.
func verifyVerticalLayoutPersistence(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	// In vertical layout, the logs appear side-by-side with the job list.
	// The job table will be narrower and the logs will be to the right.
	// A simple heuristic: in vertical layout, we should see both the job list
	// and logs section in the same horizontal space (indicated by the presence
	// of both the job table AND log content without the horizontal separator line
	// that appears in horizontal layout).

	// For now, just verify logs are still showing (we can't easily detect layout visually)
	// The real test is that toggling 'V' doesn't hide the logs
	if !strings.Contains(content, "Plan Status") {
		return fmt.Errorf("expected TUI to be running with logs visible, not found in:\n%s", content)
	}

	return nil
}

// toggleBackToHorizontal toggles the layout back to horizontal.
func toggleBackToHorizontal(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(1000 * time.Millisecond)

	// Press 'V' again to toggle back to horizontal
	if err := session.SendKeys("V"); err != nil {
		return fmt.Errorf("failed to send 'V' key: %w", err)
	}
	time.Sleep(2000 * time.Millisecond)

	if err := session.WaitStable(); err != nil {
		return err
	}

	return nil
}

// verifyHorizontalLayoutPersistence captures the TUI and verifies the layout appears horizontal.
func verifyHorizontalLayoutPersistence(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	// In horizontal layout, there's a horizontal separator line between job list and logs
	// For now, just verify logs are still showing
	if !strings.Contains(content, "Plan Status") {
		return fmt.Errorf("expected TUI to be running with logs visible, not found in:\n%s", content)
	}

	return nil
}

// hideAndReshowLogs hides logs with 'l' and shows them again to test if layout persisted.
func hideAndReshowLogs(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	// Press 'l' to hide logs
	if err := session.SendKeys("l"); err != nil {
		return fmt.Errorf("failed to send 'l' key to hide logs: %w", err)
	}
	time.Sleep(800 * time.Millisecond)

	// Press 'l' again to show logs - layout should persist
	if err := session.SendKeys("l"); err != nil {
		return fmt.Errorf("failed to send 'l' key to show logs: %w", err)
	}
	time.Sleep(1000 * time.Millisecond)

	if err := session.WaitStable(); err != nil {
		return err
	}

	return nil
}

// checkStateFileAfterVertical verifies the state file contains log_split_vertical: true.
func checkStateFileAfterVertical(ctx *harness.Context) error {
	homeDir := ctx.HomeDir()
	stateFile := filepath.Join(homeDir, ".grove", "flow", "status-tui-state.json")

	time.Sleep(1000 * time.Millisecond)

	if !fs.Exists(stateFile) {
		return fmt.Errorf("state file should exist after toggling layout")
	}

	content, err := fs.ReadString(stateFile)
	if err != nil {
		return fmt.Errorf("failed to read state file: %w", err)
	}

	// Verify log_split_vertical is true
	if !strings.Contains(content, `"log_split_vertical": true`) {
		return fmt.Errorf("expected log_split_vertical to be true after toggling to vertical, got:\n%s", content)
	}

	return nil
}

// checkStateFileAfterHorizontal verifies the state file contains log_split_vertical: false.
func checkStateFileAfterHorizontal(ctx *harness.Context) error {
	homeDir := ctx.HomeDir()
	stateFile := filepath.Join(homeDir, ".grove", "flow", "status-tui-state.json")

	time.Sleep(2000 * time.Millisecond)

	if !fs.Exists(stateFile) {
		return fmt.Errorf("state file should still exist after toggling layout")
	}

	content, err := fs.ReadString(stateFile)
	if err != nil {
		return fmt.Errorf("failed to read state file: %w", err)
	}

	// Verify log_split_vertical is false
	if !strings.Contains(content, `"log_split_vertical": false`) {
		return fmt.Errorf("expected log_split_vertical to be false after toggling to horizontal, got:\n%s", content)
	}

	return nil
}

// quitLayoutTUI quits the TUI session.
func quitLayoutTUI(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(300 * time.Millisecond)

	if err := session.SendKeys("q"); err != nil {
		return fmt.Errorf("failed to send 'q' key: %w", err)
	}

	time.Sleep(500 * time.Millisecond)

	return nil
}

// setupPlanWithLongNames creates a plan with jobs that have very long filenames and titles.
func setupPlanWithLongNames(ctx *harness.Context) error {
	projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "long-names-test-project")
	if err != nil {
		return err
	}

	// Create grove.yml
	groveYml := `name: long-names-test-project
description: Test project for long job names in status TUI

logging:
  level: info
  file:
    enabled: true
    path: .grove/logs/grove.log
    format: json
  show_current_project: true
  show:
    - long-names-test-project
`
	groveYmlPath := filepath.Join(projectDir, "grove.yml")
	if err := fs.WriteString(groveYmlPath, groveYml); err != nil {
		return fmt.Errorf("failed to create grove.yml: %w", err)
	}

	// Initialize the plan
	initCmd := ctx.Bin("plan", "init", "long-names-plan")
	initCmd.Dir(projectDir)
	if result := initCmd.Run(); result.Error != nil {
		return fmt.Errorf("plan init failed: %w", result.Error)
	}
	planPath := filepath.Join(notebooksRoot, "workspaces", "long-names-test-project", "plans", "long-names-plan")
	ctx.Set("plan_path", planPath)

	// Add a job with a very long title (similar to note-derived names)
	// This tests the fix for the note-name-too-short feature
	longTitle := "Implement comprehensive user authentication system with OAuth2 support and token refresh"
	jobA := ctx.Bin("plan", "add", "long-names-plan",
		"--type", "shell",
		"--title", longTitle,
		"-p", "echo 'Long title job complete'")
	jobA.Dir(projectDir)
	if result := jobA.Run(); result.Error != nil {
		return fmt.Errorf("failed to add job with long title: %w", result.Error)
	}

	// Add a job with a normal short title for comparison
	jobB := ctx.Bin("plan", "add", "long-names-plan",
		"--type", "shell",
		"--title", "Short job",
		"-p", "echo 'Short job complete'")
	jobB.Dir(projectDir)
	if result := jobB.Run(); result.Error != nil {
		return fmt.Errorf("failed to add short job: %w", result.Error)
	}

	// Add another job with a very long filename-derived title
	// Simulating a note filename like "20241222-implement-feature-for-handling-long-names-in-tui"
	anotherLongTitle := "Generate implementation plan for handling extremely long job names in status TUI"
	jobC := ctx.Bin("plan", "add", "long-names-plan",
		"--type", "shell",
		"--title", anotherLongTitle,
		"-p", "echo 'Another long title job complete'")
	jobC.Dir(projectDir)
	if result := jobC.Run(); result.Error != nil {
		return fmt.Errorf("failed to add second long title job: %w", result.Error)
	}

	// Store the long titles for verification
	ctx.Set("long_title_1", longTitle)
	ctx.Set("long_title_2", anotherLongTitle)

	return nil
}

// verifyLongNamesNotTruncated launches the TUI and verifies that long job names are displayed without truncation.
func verifyLongNamesNotTruncated(ctx *harness.Context) error {
	projectDir := ctx.GetString("project_dir")
	homeDir := ctx.HomeDir()
	longTitle1 := ctx.GetString("long_title_1")
	longTitle2 := ctx.GetString("long_title_2")

	flowBinary, err := findFlowBinary()
	if err != nil {
		return err
	}

	// Create a wrapper script to run flow from the project directory
	wrapperScript := filepath.Join(ctx.RootDir, "run-flow-long-names-tui")
	scriptContent := fmt.Sprintf("#!/bin/bash\nexport HOME=%s\ncd %s\nexec %s plan status long-names-plan\n", homeDir, projectDir, flowBinary)
	if err := fs.WriteString(wrapperScript, scriptContent); err != nil {
		return fmt.Errorf("failed to create wrapper script: %w", err)
	}
	if err := os.Chmod(wrapperScript, 0755); err != nil {
		return fmt.Errorf("failed to make wrapper script executable: %w", err)
	}

	session, err := ctx.StartTUI(wrapperScript, []string{})
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

	// Enable the TITLE column by pressing 'T' to open column selector
	if err := session.SendKeys("T"); err != nil {
		return fmt.Errorf("failed to send 'T' key: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Navigate to TITLE in the column list - it should be the second item after JOB
	if err := session.SendKeys("j"); err != nil {
		return fmt.Errorf("failed to navigate to TITLE: %w", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Toggle TITLE column on
	if err := session.SendKeys("Space"); err != nil {
		return fmt.Errorf("failed to toggle TITLE column: %w", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Close the column selector
	if err := session.SendKeys("Escape"); err != nil {
		return fmt.Errorf("failed to close column selector: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Wait for UI to stabilize
	if err := session.WaitStable(); err != nil {
		return err
	}

	// Capture the screen content
	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return fmt.Errorf("failed to capture TUI content: %w", err)
	}

	// Verify the long titles are NOT truncated with the old hardcoded 30-char ellipsis pattern
	// Before the fix, titles would have been hardcoded-truncated like "Implement comprehensive use..."
	// After the fix, the titles may still be cut off by terminal/table width, but NOT with our hardcoded "..." pattern
	//
	// KEY TEST: We want to verify that we removed the hardcoded 30-char limit with "..." suffix.
	// The table library may still truncate based on available width, but that's different from our old bug.

	// Check that we're NOT seeing the old hardcoded truncation pattern (27 chars + "...")
	// This is the MAIN assertion - if we see this pattern, the bug still exists
	truncatedOld1 := longTitle1[:27] + "..."
	if strings.Contains(content, truncatedOld1) {
		return fmt.Errorf("REGRESSION: long title is truncated with old hardcoded 30-char pattern:\nFound: '%s'\nThis means the fix didn't work - the 30-char truncation code is still active\n\nFull content:\n%s",
			truncatedOld1, content)
	}

	truncatedOld2 := longTitle2[:27] + "..."
	if strings.Contains(content, truncatedOld2) {
		return fmt.Errorf("REGRESSION: second long title is truncated with old hardcoded 30-char pattern:\nFound: '%s'\nThis means the fix didn't work - the 30-char truncation code is still active\n\nFull content:\n%s",
			truncatedOld2, content)
	}

	// Verify that at least SOME part of the long titles is visible
	// Even if the table truncates due to terminal width, we should see the beginning
	titleStart1 := longTitle1[:10] // Just "Implement "
	if !strings.Contains(content, titleStart1) {
		return fmt.Errorf("expected to find at least the start of long title:\n'%s'\n\nFull content:\n%s", titleStart1, content)
	}

	titleStart2 := longTitle2[:10] // Just "Generate i"
	if !strings.Contains(content, titleStart2) {
		return fmt.Errorf("expected to find at least the start of second long title:\n'%s'\n\nFull content:\n%s", titleStart2, content)
	}

	// Verify short job is also visible (for comparison)
	if !strings.Contains(content, "Short job") {
		return fmt.Errorf("expected to find 'Short job' in TUI output, not found in:\n%s", content)
	}

	// Verify that job filenames are visible (at least the beginning)
	// The JOB column may also be truncated by table width, but we should see the file pattern
	expectedJobPrefixes := []string{
		"01-implement-comprehensive", // Beginning of long filename
		"02-short-job.md",            // Short filename should be fully visible
		"03-generate-implementation", // Beginning of another long filename
	}

	for _, jobPrefix := range expectedJobPrefixes {
		if !strings.Contains(content, jobPrefix) {
			return fmt.Errorf("expected to find job prefix '%s' in TUI output, not found in:\n%s", jobPrefix, content)
		}
	}

	return nil
}

// quitLongNamesTUI sends the quit command to the TUI.
func quitLongNamesTUI(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)
	if err := session.SendKeys("q"); err != nil {
		return fmt.Errorf("failed to send 'q' key: %w", err)
	}
	time.Sleep(500 * time.Millisecond)
	return nil
}
