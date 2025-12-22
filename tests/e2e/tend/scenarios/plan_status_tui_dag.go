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
	"github.com/mattsolo1/grove-tend/pkg/verify"
)

var PlanStatusTUIDAGScenario = harness.NewScenarioWithOptions(
	"plan-status-tui-dag-execution",
	"Tests TUI execution of a complex job DAG and verifies concurrent log streaming.",
	[]string{"tui", "plan", "status", "dag", "concurrent"},
	[]harness.Step{
		harness.NewStep("Setup plan with complex DAG", setupComplexDAG),
		harness.SetupMocks(
			harness.Mock{CommandName: "claude"},
			harness.Mock{CommandName: "tmux"},
			harness.Mock{CommandName: "grove"},
		),
		harness.NewStep("Launch TUI and run DAG", launchTUIAndRunDAG),
		harness.NewStep("Verify all jobs completed", verifyAllJobsCompleted),
		harness.NewStep("Quit the TUI", quitDAG_TUI),
	},
	true,  // localOnly
	false, // explicitOnly
)

type jobDefinition struct {
	title    string
	deps     []string
	command  string
	filename string
}

func setupComplexDAG(ctx *harness.Context) error {
	projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "dag-exec-project")
	if err != nil {
		return err
	}

	// Initialize the plan
	initCmd := ctx.Bin("plan", "init", "exec-example")
	initCmd.Dir(projectDir)
	if result := initCmd.Run(); result.Error != nil {
		return fmt.Errorf("plan init failed: %w", result.Error)
	}

	planPath := filepath.Join(notebooksRoot, "workspaces", "dag-exec-project", "plans", "exec-example")
	ctx.Set("plan_path", planPath)

	// Define the job DAG - matches the structure from the spec
	// This creates a complex dependency tree with multiple branches
	jobs := []jobDefinition{
		{title: "farmer-delivery", deps: []string{}, command: "echo 'Executing Job 01: Farmer Delivery...' && sleep 0.3"},
		{title: "fishmonger-delivery", deps: []string{}, command: "echo 'Executing Job 02: Fishmonger Delivery...' && sleep 0.3"},
		{title: "chef-menu", deps: []string{"01-farmer-delivery.md"}, command: "echo 'Executing Job 03: Chef Menu...' && sleep 0.3"},
		{title: "pastry-menu", deps: []string{}, command: "echo 'Executing Job 04: Pastry Menu...' && sleep 0.3"},
		{title: "supply-chain", deps: []string{"03-chef-menu.md"}, command: "echo 'Executing Job 05: Supply Chain...' && sleep 0.3"},
		{title: "sous-prep", deps: []string{"05-supply-chain.md"}, command: "echo 'Executing Job 06: Sous Prep...' && sleep 0.3"},
		{title: "customer-order", deps: []string{"03-chef-menu.md"}, command: "echo 'Executing Job 07: Customer Order...' && sleep 0.3"},
		{title: "sommelier-pairing", deps: []string{"07-customer-order.md"}, command: "echo 'Executing Job 08: Sommelier Pairing...' && sleep 0.3"},
		{title: "line-cook", deps: []string{"06-sous-prep.md"}, command: "echo 'Executing Job 09: Line Cook...' && sleep 0.3"},
		{title: "pastry-execution", deps: []string{"07-customer-order.md"}, command: "echo 'Executing Job 10: Pastry Execution...' && sleep 0.3"},
		{title: "expediter", deps: []string{"09-line-cook.md"}, command: "echo 'Executing Job 11: Expediter...' && sleep 0.3"},
		{title: "food-critic", deps: []string{"09-line-cook.md"}, command: "echo 'Executing Job 12: Food Critic...' && sleep 0.3"},
	}

	// Create job files
	for i, j := range jobs {
		jobNum := i + 1
		filename := fmt.Sprintf("%02d-%s.md", jobNum, j.title)
		jobs[i].filename = filename

		// Build command with dependencies
		args := []string{"plan", "add", "exec-example", "--type", "shell", "--title", j.title, "-p", j.command}
		for _, dep := range j.deps {
			args = append(args, "-d", dep)
		}

		addCmd := ctx.Bin(args...)
		addCmd.Dir(projectDir)
		result := addCmd.Run()
		if err := result.AssertSuccess(); err != nil {
			return fmt.Errorf("failed to add job '%s': %w", j.title, err)
		}
	}

	return nil
}

func launchTUIAndRunDAG(ctx *harness.Context) error {
	projectDir := ctx.GetString("project_dir")
	homeDir := ctx.HomeDir()
	flowBinary, err := findFlowBinary()
	if err != nil {
		return err
	}

	wrapperScript := filepath.Join(ctx.RootDir, "run-dag-tui")
	scriptContent := fmt.Sprintf("#!/bin/bash\nexport HOME=%s\ncd %s\nexec %s plan status exec-example\n", homeDir, projectDir, flowBinary)
	if err := fs.WriteString(wrapperScript, scriptContent); err != nil {
		return fmt.Errorf("failed to create wrapper script: %w", err)
	}
	os.Chmod(wrapperScript, 0755)

	session, err := ctx.StartTUI(wrapperScript, []string{})
	if err != nil {
		return fmt.Errorf("failed to start TUI: %w", err)
	}
	ctx.Set("tui_session", session)

	if err := ctx.Check("TUI loaded with Plan Status header", session.WaitForText("Plan Status", 10*time.Second)); err != nil {
		return err
	}
	if err := session.WaitStable(); err != nil {
		return err
	}

	// Run all jobs in stages by repeatedly selecting all runnable jobs and executing them
	// The orchestrator will handle dependency ordering and concurrent execution
	for i := 0; i < 6; i++ { // Loop enough times to complete the entire DAG
		// Select all runnable jobs
		if err := session.SendKeys("a"); err != nil {
			return err
		}
		time.Sleep(200 * time.Millisecond)

		// Run selected jobs
		if err := session.SendKeys("r"); err != nil {
			return err
		}

		// On the first iteration, verify that logs appear in the log viewer
		// This confirms the concurrent execution is streaming logs correctly
		if i == 0 {
			if err := ctx.Check("first stage logs appear in viewer", session.WaitForText("Executing Job 01", 5*time.Second)); err != nil {
				content, _ := session.Capture(tui.WithCleanedOutput())
				return fmt.Errorf("did not see logs for first stage: %w\n\n%s", err, content)
			}
		}

		// Wait for the TUI to stabilize after job execution
		if err := session.WaitStable(); err != nil {
			return err
		}
		time.Sleep(1 * time.Second) // Extra pause for slower CI environments
	}

	return nil
}

func verifyAllJobsCompleted(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)
	if err := session.WaitStable(); err != nil {
		return err
	}

	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	return ctx.Verify(func(v *verify.Collector) {
		// Count the number of completed jobs (marked with ✓)
		completedCount := strings.Count(content, "✓")
		v.True("all 12 jobs completed", completedCount >= 12)

		// Verify key jobs are visible in the output
		v.Contains("job 01 is visible", content, "01-farmer-delivery")
		v.Contains("job 03 is visible", content, "03-chef-menu")
		v.Contains("job 09 is visible", content, "09-line-cook")
		v.Contains("job 12 is visible", content, "12-food-critic")

		// Verify the dependency tree structure is displayed
		v.Contains("dependency tree structure shown", content, "└─")
	})
}

func quitDAG_TUI(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)
	return session.SendKeys("q")
}
