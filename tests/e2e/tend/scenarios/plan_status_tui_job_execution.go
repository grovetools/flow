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
	"github.com/grovetools/tend/pkg/verify"
)

// PlanStatusTUIJobExecutionScenario tests that the TUI correctly executes only
// the selected jobs without automatically continuing beyond the selection.
// This validates the fix for the autorun bug where selecting jobs in a dependency
// chain would cause the system to continue running jobs beyond the selection.
//
// The bug scenario:
// - User selects jobs A, B, C (where A→B→C is a dependency chain)
// - Only A is currently runnable (B and C are blocked)
// - User presses 'r'
// - System runs A, then B, then C (correct - these were selected)
// - But then continues to D, E, F... (BUG - these were NOT selected)
//
// The fix: Remove autorun logic so only the selected jobs run, then execution stops.
var PlanStatusTUIJobExecutionScenario = harness.NewScenarioWithOptions(
	"plan-status-tui-job-execution",
	"Verifies that TUI only runs selected jobs and stops, even when selecting a dependency chain",
	[]string{"tui", "plan", "status", "job-execution", "regression"},
	[]harness.Step{
		harness.NewStep("Setup plan with dependency chain A→B→C→D", setupDependencyChainPlan),
		harness.SetupMocks(
			harness.Mock{CommandName: "claude"},
			harness.Mock{CommandName: "tmux"},
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Test selecting chain A,B,C stops after C (does not run D)", testSelectedChainStopsCorrectly),
		harness.NewStep("Test selecting single job in chain stops after that job", testSingleJobInChainStops),
	},
	true, // localOnly = true, requires tmux for TUI testing
	true, // explicitOnly = true, needs investigation for context prompt handling
)

// setupDependencyChainPlan creates a plan with A → B → C → D dependency chain
// This matches the actual bug scenario where selecting A, B, C should not run D
func setupDependencyChainPlan(ctx *harness.Context) error {
	projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "chain-project")
	if err != nil {
		return err
	}

	// Initialize the plan
	initCmd := ctx.Bin("plan", "init", "chain-plan")
	initCmd.Dir(projectDir)
	if result := initCmd.Run(); result.Error != nil {
		return fmt.Errorf("plan init failed: %w", result.Error)
	}

	planPath := filepath.Join(notebooksRoot, "workspaces", "chain-project", "plans", "chain-plan")
	ctx.Set("plan_path", planPath)

	// Add Job A (no dependencies) - like 02-spec.md
	jobACmd := ctx.Bin("plan", "add", "chain-plan", "--type", "shell", "--title", "Job-A", "-p", "echo 'A done'")
	jobACmd.Dir(projectDir)
	if result := jobACmd.Run(); result.Error != nil {
		return fmt.Errorf("failed to add Job A: %w", result.Error)
	}

	// Add Job B (depends on A) - like 03-generate-plan.md
	jobBCmd := ctx.Bin("plan", "add", "chain-plan", "--type", "shell", "--title", "Job-B", "-p", "echo 'B done'", "-d", "01-job-a.md")
	jobBCmd.Dir(projectDir)
	if result := jobBCmd.Run(); result.Error != nil {
		return fmt.Errorf("failed to add Job B: %w", result.Error)
	}

	// Add Job C (depends on B) - like 04-implement.md
	jobCCmd := ctx.Bin("plan", "add", "chain-plan", "--type", "shell", "--title", "Job-C", "-p", "echo 'C done'", "-d", "02-job-b.md")
	jobCCmd.Dir(projectDir)
	if result := jobCCmd.Run(); result.Error != nil {
		return fmt.Errorf("failed to add Job C: %w", result.Error)
	}

	// Add Job D (depends on C) - like 05-spec-tests.md (should NOT run when A,B,C selected)
	jobDCmd := ctx.Bin("plan", "add", "chain-plan", "--type", "shell", "--title", "Job-D", "-p", "echo 'D done'", "-d", "03-job-c.md")
	jobDCmd.Dir(projectDir)
	if result := jobDCmd.Run(); result.Error != nil {
		return fmt.Errorf("failed to add Job D: %w", result.Error)
	}

	return nil
}

// testSelectedChainStopsCorrectly validates the main bug fix:
// - User selects jobs A, B, C in a dependency chain
// - System runs A, B, C (correct - these were selected)
// - System stops and does NOT run D (the fix)
func testSelectedChainStopsCorrectly(ctx *harness.Context) error {
	projectDir := ctx.GetString("project_dir")
	homeDir := ctx.GetString("home_dir")

	flowBinary, err := findFlowBinary()
	if err != nil {
		return err
	}

	// Create a wrapper script to run flow status
	wrapperScript := filepath.Join(ctx.RootDir, "run-flow-status-chain")
	scriptContent := fmt.Sprintf("#!/bin/bash\nexport HOME=%s\ncd %s\nexec %s plan status chain-plan\n", homeDir, projectDir, flowBinary)
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

	// Wait for the TUI to stabilize
	if err := session.WaitStable(); err != nil {
		return err
	}

	// The cursor starts at the bottom-most job (Job D), so navigate to Job A first
	// Move up 3 times: D -> C -> B -> A
	for i := 0; i < 3; i++ {
		if err := session.SendKeys("Up"); err != nil {
			return fmt.Errorf("failed to move up: %w", err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Select jobs A, B, C (the first three jobs)
	// Job A should now be focused, so select it
	if err := session.SendKeys(" "); err != nil { // Space to select Job A
		return fmt.Errorf("failed to select Job A: %w", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Move down to Job B and select it
	if err := session.SendKeys("Down"); err != nil {
		return fmt.Errorf("failed to move to Job B: %w", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := session.SendKeys(" "); err != nil { // Space to select Job B
		return fmt.Errorf("failed to select Job B: %w", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Move down to Job C and select it
	if err := session.SendKeys("Down"); err != nil {
		return fmt.Errorf("failed to move to Job C: %w", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := session.SendKeys(" "); err != nil { // Space to select Job C
		return fmt.Errorf("failed to select Job C: %w", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Press 'r' to run the selected jobs (A, B, C)
	if err := session.SendKeys("r"); err != nil {
		return fmt.Errorf("failed to send 'r' key: %w", err)
	}

	// A context prompt may appear for each job that needs context
	// Send 'p' multiple times to proceed without context for all jobs
	for i := 0; i < 4; i++ {
		time.Sleep(500 * time.Millisecond)
		if err := session.SendKeys("p"); err != nil {
			return fmt.Errorf("failed to send 'p' key to proceed: %w", err)
		}
	}
	time.Sleep(1 * time.Second)

	// Wait for jobs to start running
	var content string
	if err := session.WaitForText("Running", 10*time.Second); err != nil {
		content, _ = session.Capture()
		return fmt.Errorf("jobs did not start running: %w\nContent:\n%s", err, content)
	}

	// Wait for all jobs to complete
	// We need to wait long enough for A→B→C to complete sequentially
	if err := session.WaitForText("completed successfully", 15*time.Second); err != nil {
		content, _ = session.Capture()
		return fmt.Errorf("jobs did not complete: %w\nContent:\n%s", err, content)
	}

	// CRITICAL: Wait additional time to ensure Job D does NOT auto-start
	// If the bug were present, Job D would start automatically after C completes
	time.Sleep(3 * time.Second)

	// Capture final state
	content, captureErr := session.Capture(tui.WithCleanedOutput())
	if captureErr != nil {
		return captureErr
	}

	// Verify that Jobs A, B, C ran successfully
	if err := ctx.Verify(func(v *verify.Collector) {
		v.Contains("Job A output is present", content, "A done")
		v.Contains("Job B output is present", content, "B done")
		v.Contains("Job C output is present", content, "C done")
	}); err != nil {
		return err
	}

	// CRITICAL ASSERTION: Job D should NOT have run
	// This is the bug fix - even though C completed (making D runnable),
	// D should not run because it was not in the original selection
	if strings.Contains(content, "D done") {
		return fmt.Errorf("BUG DETECTED: Job D ran automatically even though it was not selected! The autorun bug is still present.")
	}

	return nil
}

// testSingleJobInChainStops verifies that selecting and running just one job
// in a chain stops after that job, even if it makes the next job runnable
func testSingleJobInChainStops(ctx *harness.Context) error {
	projectDir := ctx.GetString("project_dir")
	homeDir := ctx.GetString("home_dir")

	flowBinary, err := findFlowBinary()
	if err != nil {
		return err
	}

	// Create a wrapper script to run flow status
	wrapperScript := filepath.Join(ctx.RootDir, "run-flow-status-single")
	scriptContent := fmt.Sprintf("#!/bin/bash\nexport HOME=%s\ncd %s\nexec %s plan status chain-plan\n", homeDir, projectDir, flowBinary)
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

	// Wait for the TUI to stabilize
	if err := session.WaitStable(); err != nil {
		return err
	}

	// The cursor starts at the bottom-most job (Job D), so navigate to Job A first
	// Move up 3 times: D -> C -> B -> A
	for i := 0; i < 3; i++ {
		if err := session.SendKeys("Up"); err != nil {
			return fmt.Errorf("failed to move up: %w", err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Press 'r' to run the focused job (Job A) without explicitly selecting it
	if err := session.SendKeys("r"); err != nil {
		return fmt.Errorf("failed to send 'r' key: %w", err)
	}

	// A context prompt may appear - send 'p' to proceed without context
	time.Sleep(500 * time.Millisecond)
	if err := session.SendKeys("p"); err != nil {
		return fmt.Errorf("failed to send 'p' key to proceed: %w", err)
	}
	time.Sleep(1 * time.Second)

	// Wait for job to start running
	var content string
	if err := session.WaitForText("Running", 10*time.Second); err != nil {
		content, _ = session.Capture()
		return fmt.Errorf("job did not start running: %w\nContent:\n%s", err, content)
	}

	// Wait for job completion
	if err := session.WaitForText("completed successfully", 10*time.Second); err != nil {
		content, _ = session.Capture()
		return fmt.Errorf("job did not complete: %w\nContent:\n%s", err, content)
	}

	// Wait for TUI to stabilize and ensure no autorun
	time.Sleep(2 * time.Second)

	// Capture final state
	content, captureErr := session.Capture(tui.WithCleanedOutput())
	if captureErr != nil {
		return captureErr
	}

	// Verify that Job A ran
	if err := ctx.Verify(func(v *verify.Collector) {
		v.Contains("Job A output is present", content, "A done")
	}); err != nil {
		return err
	}

	// Job B should NOT have run - we only selected/ran Job A
	if strings.Contains(content, "B done") {
		return fmt.Errorf("Job B should NOT have run automatically, but found 'B done' in output")
	}

	return nil
}
