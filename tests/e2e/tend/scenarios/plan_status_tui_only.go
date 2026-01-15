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

// PlanStatusTUIOnlyScenario tests that the status command now only launches TUI
// and verifies that old flags are removed and behavior is consistent.
var PlanStatusTUIOnlyScenario = harness.NewScenarioWithOptions(
	"plan-status-tui-only",
	"Verifies that flow status always launches TUI and old non-TUI flags are removed.",
	[]string{"tui", "plan", "status", "regression"},
	[]harness.Step{
		harness.NewStep("Setup test environment with a plan", setupStatusTUIOnlyEnvironment),
		harness.NewStep("Verify old flags are removed", verifyOldFlagsRemoved),
		harness.NewStep("Test status with active plan launches TUI", testStatusWithActivePlanLaunchesTUI),
		harness.NewStep("Test status with directory argument launches TUI", testStatusWithDirectoryLaunchesTUI),
		harness.NewStep("Test status outside workspace shows error", testStatusWithoutActivePlanShowsError),
		harness.NewStep("Test tmux status command updated", testTmuxStatusCommandUpdated),
	},
	true,  // localOnly = true, requires tmux for TUI testing
	false, // explicitOnly = false
)

// setupStatusTUIOnlyEnvironment creates a test environment with a plan
func setupStatusTUIOnlyEnvironment(ctx *harness.Context) error {
	projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "status-tui-only-project")
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

	planPath := filepath.Join(notebooksRoot, "workspaces", "status-tui-only-project", "plans", "test-plan")
	ctx.Set("plan_path", planPath)
	ctx.Set("plan_name", "test-plan")

	// Add a couple of jobs to make the plan more interesting
	jobA := ctx.Bin("plan", "add", "test-plan", "--type", "shell", "--title", "Job A", "-p", "echo 'A'")
	jobA.Dir(projectDir)
	if result := jobA.Run(); result.Error != nil {
		return fmt.Errorf("failed to add Job A: %w", result.Error)
	}

	jobB := ctx.Bin("plan", "add", "test-plan", "--type", "shell", "--title", "Job B", "-p", "echo 'B'", "-d", "01-job-a.md")
	jobB.Dir(projectDir)
	if result := jobB.Run(); result.Error != nil {
		return fmt.Errorf("failed to add Job B: %w", result.Error)
	}

	return nil
}

// verifyOldFlagsRemoved tests that the old non-TUI flags are no longer accepted
func verifyOldFlagsRemoved(ctx *harness.Context) error {
	projectDir := ctx.GetString("project_dir")
	planName := ctx.GetString("plan_name")

	// Test that old status-specific flags result in error or are not recognized
	// Note: -v/--verbose is a global flag, not a status-specific flag, so we don't test it here
	// Note: --tui/-t is kept for backwards compatibility, so we don't test it here
	oldFlags := []string{
		"--graph",
		"-g",
		"--format",
		"-f",
	}

	for _, flag := range oldFlags {
		cmd := ctx.Bin("plan", "status", planName, flag)
		cmd.Dir(projectDir)
		result := cmd.Run()

		// The command should fail with unknown flag error
		if result.Error == nil {
			return fmt.Errorf("expected flag %s to be removed, but command succeeded", flag)
		}

		// Check that stderr mentions the flag is unknown
		if !strings.Contains(result.Stderr, "unknown flag") && !strings.Contains(result.Stderr, "unknown shorthand") {
			return fmt.Errorf("expected 'unknown flag' error for %s, got: %s", flag, result.Stderr)
		}
	}

	// Also verify that the help text doesn't mention these flags
	helpCmd := ctx.Bin("plan", "status", "--help")
	helpCmd.Dir(projectDir)
	helpResult := helpCmd.Run()
	if helpResult.Error != nil {
		return fmt.Errorf("failed to get help: %w", helpResult.Error)
	}

	flagsToCheck := []string{"--graph", "--format"}
	for _, flag := range flagsToCheck {
		if strings.Contains(helpResult.Stdout, flag) {
			return fmt.Errorf("help text should not mention removed flag %s, but it does:\n%s", flag, helpResult.Stdout)
		}
	}

	return nil
}

// testStatusWithActivePlanLaunchesTUI verifies that 'flow status' with an active plan launches TUI
func testStatusWithActivePlanLaunchesTUI(ctx *harness.Context) error {
	projectDir := ctx.GetString("project_dir")
	planName := ctx.GetString("plan_name")
	homeDir := ctx.GetString("home_dir")

	// Set the active job first
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

	// Create a wrapper script to run flow status from the project directory
	wrapperScript := filepath.Join(ctx.RootDir, "run-flow-status-active")
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
		return fmt.Errorf("TUI did not load with 'Plan Status' header: %w\nContent:\n%s", err, content)
	}

	// Verify we see the jobs
	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	if !strings.Contains(content, "01-job-a.md") || !strings.Contains(content, "02-job-b.md") {
		return fmt.Errorf("expected to see jobs in TUI, got:\n%s", content)
	}

	// Send 'q' to quit
	if err := session.SendKeys("q"); err != nil {
		return fmt.Errorf("failed to quit TUI: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	return nil
}

// testStatusWithDirectoryLaunchesTUI verifies that 'flow status <directory>' launches TUI
func testStatusWithDirectoryLaunchesTUI(ctx *harness.Context) error {
	projectDir := ctx.GetString("project_dir")
	planPath := ctx.GetString("plan_path")
	homeDir := ctx.GetString("home_dir")

	flowBinary, err := findFlowBinary()
	if err != nil {
		return err
	}

	// Create a wrapper script to run flow status with directory argument
	wrapperScript := filepath.Join(ctx.RootDir, "run-flow-status-dir")
	scriptContent := fmt.Sprintf("#!/bin/bash\nexport HOME=%s\ncd %s\nexec %s plan status %s\n", homeDir, projectDir, flowBinary, planPath)
	if err := fs.WriteString(wrapperScript, scriptContent); err != nil {
		return fmt.Errorf("failed to create wrapper script: %w", err)
	}
	if err := os.Chmod(wrapperScript, 0755); err != nil {
		return fmt.Errorf("failed to make wrapper script executable: %w", err)
	}

	session, err := ctx.StartTUI(wrapperScript, []string{})
	if err != nil {
		return fmt.Errorf("failed to start flow status with directory: %w", err)
	}
	defer session.Close()

	// Wait for TUI to load
	if err := session.WaitForText("Plan Status", 10*time.Second); err != nil {
		content, _ := session.Capture()
		return fmt.Errorf("TUI did not load: %w\nContent:\n%s", err, content)
	}

	// Verify we see the plan content
	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	if !strings.Contains(content, "01-job-a.md") {
		return fmt.Errorf("expected to see jobs in TUI, got:\n%s", content)
	}

	// Send 'q' to quit
	if err := session.SendKeys("q"); err != nil {
		return fmt.Errorf("failed to quit TUI: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	return nil
}

// testStatusWithoutActivePlanShowsError verifies that running status outside a workspace shows an error
func testStatusWithoutActivePlanShowsError(ctx *harness.Context) error {
	homeDir := ctx.GetString("home_dir")

	// Create a new directory that is NOT inside a git repository
	// This ensures we get the "not in a workspace" error
	tempDir := filepath.Join(ctx.RootDir, "non-project-dir")
	if err := fs.CreateDir(tempDir); err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}

	// Clear the active job first
	projectDir := ctx.GetString("project_dir")
	clearCmd := ctx.Bin("plan", "clear")
	clearCmd.Dir(projectDir)
	result := clearCmd.Run()
	if result.Error != nil {
		return fmt.Errorf("failed to clear active plan: %w", result.Error)
	}

	// Run flow plan status from the non-project directory - it should fail with an error
	statusCmd := ctx.Bin("plan", "status")
	statusCmd.Dir(tempDir)
	statusCmd.Env("HOME=" + homeDir)
	statusResult := statusCmd.Run()

	// Should fail with "not in a workspace" error
	if statusResult.Error == nil {
		return fmt.Errorf("expected status command to fail outside a workspace, but it succeeded")
	}

	// Verify the error message mentions workspace
	if !strings.Contains(statusResult.Stderr, "not in a workspace") && !strings.Contains(statusResult.Stderr, "no workspace found") {
		return fmt.Errorf("expected 'not in a workspace' error, got: %s", statusResult.Stderr)
	}

	return nil
}

// testTmuxStatusCommandUpdated verifies that the tmux status command no longer uses -t flag
func testTmuxStatusCommandUpdated(ctx *harness.Context) error {
	projectDir := ctx.GetString("project_dir")

	// We can only verify the command help text here
	cmd := ctx.Bin("tmux", "status", "--help")
	cmd.Dir(projectDir)
	result := cmd.Run()

	// The help should not mention the -t flag
	if strings.Contains(result.Stdout, "-t,") || strings.Contains(result.Stdout, "--tui") {
		return fmt.Errorf("tmux status help still mentions -t/--tui flag: %s", result.Stdout)
	}

	// NOTE: We intentionally do NOT test `flow tmux status <planPath>` here because
	// it calls FocusOrRunTUIWithErrorHandling which switches the user's tmux client
	// to the "plan" window, disrupting their workflow when running tests.

	return nil
}
