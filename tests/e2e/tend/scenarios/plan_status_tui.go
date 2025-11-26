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

// PlanStatusTUIScenario tests the interactive `flow plan status -t` command.
var PlanStatusTUIScenario = harness.NewScenarioWithOptions(
	"plan-status-tui",
	"Verifies the plan status TUI can display jobs and navigate the interface.",
	[]string{"tui", "plan", "status"},
	[]harness.Step{
		harness.NewStep("Setup mock filesystem with dependent jobs", setupPlanWithDependencies),
		harness.NewStep("Launch status TUI and verify initial state", launchStatusTUIAndVerify),
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
	scriptContent := fmt.Sprintf("#!/bin/bash\nexport HOME=%s\ncd %s\nexec %s plan status -t dependency-plan\n", homeDir, projectDir, flowBinary)
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
		return fmt.Errorf("failed to start `flow plan status -t`: %w", err)
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
	// Both should show as pending in the TUI
	if !strings.Contains(content, "pending") {
		return fmt.Errorf("expected 'pending' status to be visible, not found in:\n%s", content)
	}

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

	// Wait for the "completed" status to appear (the TUI should refresh)
	if err := session.WaitForText("completed", 5*time.Second); err != nil {
		content, _ := session.Capture()
		return fmt.Errorf("status did not change to 'completed': %w\nContent:\n%s", err, content)
	}

	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	// Verify Job A is now completed
	if !strings.Contains(content, "01-job-a.md") {
		return fmt.Errorf("expected '01-job-a.md' to still be visible:\n%s", content)
	}
	// Verify Job B is still visible
	if !strings.Contains(content, "02-job-b.md") {
		return fmt.Errorf("expected '02-job-b.md' to still be visible:\n%s", content)
	}

	return nil
}

// quitStatusTUI sends the quit command to the TUI.
func quitStatusTUI(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)
	return session.SendKeys("q")
}
