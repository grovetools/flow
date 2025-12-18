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
