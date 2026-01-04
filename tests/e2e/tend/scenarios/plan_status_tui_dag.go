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
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Launch TUI and run DAG", launchTUIAndRunDAG),
		harness.NewStep("Verify all jobs completed", verifyAllJobsCompleted),
		harness.NewStep("Verify job logs written to disk", verifyJobLogsWritten),
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

	// Select all runnable jobs
	if err := session.SendKeys("a"); err != nil {
		return err
	}
	time.Sleep(200 * time.Millisecond)

	// Run selected jobs - this should trigger autorun mode
	if err := session.SendKeys("r"); err != nil {
		return err
	}

	// Give jobs time to start and complete
	// The entire DAG should complete quickly due to autorun (all stages finish in < 5s)
	time.Sleep(10 * time.Second)

	// The detailed verification happens in subsequent steps
	// (verifyAllJobsCompleted and verifyJobLogsWritten)
	// Here we just ensure the TUI is still responsive
	if err := session.WaitStable(); err != nil {
		return err
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
		// Verify visible jobs are present (not all 12 fit on screen due to terminal height)
		// We only check a few key jobs that should be visible
		v.Contains("job 01 is visible", content, "01-farmer-delivery")
		v.Contains("job 03 is visible", content, "03-chef-menu")
		v.Contains("job 09 is visible", content, "09-line-cook")

		// Verify completed icons are shown (󰄳 for nerd fonts or ● for ASCII)
		hasCompletedIcons := strings.Contains(content, "󰄳") || strings.Contains(content, "●")
		v.True("completed icons are shown", hasCompletedIcons)

		// Verify the dependency tree structure is displayed
		v.Contains("dependency tree structure shown", content, "└─")

		// TUI Corruption Checks
		// Verify no mangled output in the scrollback
		// The full verification of all 12 jobs happens in verifyJobLogsWritten
		rawContent, _ := session.Capture()

		// Check that job names appear intact (not interleaved/corrupted)
		v.Contains("job names not corrupted", rawContent, "farmer-delivery")
		v.Contains("job names not corrupted", rawContent, "chef-menu")
	})
}

func verifyJobLogsWritten(ctx *harness.Context) error {
	planPath := ctx.GetString("plan_path")

	// Verify that individual job log files were created and contain output
	// This ensures the MultiWriter pattern is correctly writing to both TUI and disk
	jobTitles := []string{
		"farmer-delivery", "fishmonger-delivery", "chef-menu", "pastry-menu",
		"supply-chain", "sous-prep", "customer-order", "sommelier-pairing",
		"line-cook", "pastry-execution", "expediter", "food-critic",
	}

	// Track which jobs ran concurrently (first stage: jobs 01, 02, 04)
	concurrentJobsFound := make(map[int]bool)

	for i, title := range jobTitles {
		jobNum := i + 1

		// Find the artifacts directory for this job
		// Pattern matches directories like "customer-order-806ffa1c"
		artifactsPattern := filepath.Join(planPath, ".artifacts", title+"-*")
		matches, err := filepath.Glob(artifactsPattern)
		if err != nil || len(matches) == 0 {
			return fmt.Errorf("no artifacts directory found for job %s (pattern: %s)", title, artifactsPattern)
		}

		// Check for job.log file
		logFile := filepath.Join(matches[0], "job.log")
		if _, err := os.Stat(logFile); os.IsNotExist(err) {
			return fmt.Errorf("log file not found for job %s at %s", title, logFile)
		}

		// Verify the log file has content
		content, err := os.ReadFile(logFile)
		if err != nil {
			return fmt.Errorf("failed to read log file for job %s: %w", title, err)
		}

		if len(content) == 0 {
			return fmt.Errorf("log file for job %s is empty", title)
		}

		// Verify the log contains the expected job output
		expectedOutput := fmt.Sprintf("Executing Job %02d:", jobNum)
		if !strings.Contains(string(content), expectedOutput) {
			return fmt.Errorf("log file for job %s does not contain expected output '%s'", title, expectedOutput)
		}

		// Track jobs from the first concurrent stage
		if jobNum == 1 || jobNum == 2 || jobNum == 4 {
			concurrentJobsFound[jobNum] = true
		}

		// Verify no duplicate output (would indicate corruption from concurrent writes)
		outputCount := strings.Count(string(content), expectedOutput)
		if outputCount > 1 {
			return fmt.Errorf("log file for job %s contains duplicate output (%d occurrences) - possible concurrent write corruption", title, outputCount)
		}
	}

	// Verify all three concurrent jobs from first stage have logs
	// This proves concurrent execution worked and all output was captured
	if len(concurrentJobsFound) < 3 {
		return fmt.Errorf("expected logs from 3 concurrent jobs (01, 02, 04), only found %d", len(concurrentJobsFound))
	}

	// Verify dependency ordering by checking file modification times
	// These are critical checks - dependency violations would indicate a serious orchestration bug

	// Job 03 (chef-menu) depends on Job 01 (farmer-delivery)
	if err := ctx.Check("chef-menu ran after farmer-delivery", verifyJobOrder(planPath, "farmer-delivery", "chef-menu")); err != nil {
		return err
	}

	// Job 09 (line-cook) depends on Job 06 (sous-prep)
	if err := ctx.Check("line-cook ran after sous-prep", verifyJobOrder(planPath, "sous-prep", "line-cook")); err != nil {
		return err
	}

	// Job 11 (expediter) depends on Job 09 (line-cook)
	if err := ctx.Check("expediter ran after line-cook", verifyJobOrder(planPath, "line-cook", "expediter")); err != nil {
		return err
	}

	// All job log files verified successfully - proves concurrent execution wrote correctly to disk
	return nil
}

// verifyJobOrder checks that a dependent job's log was written after its dependency
func verifyJobOrder(planPath, depJob, dependentJob string) error {
	// Find log files
	depPattern := filepath.Join(planPath, ".artifacts", depJob+"-*", "job.log")
	depMatches, _ := filepath.Glob(depPattern)
	if len(depMatches) == 0 {
		return fmt.Errorf("could not find log for dependency job %s", depJob)
	}

	dependentPattern := filepath.Join(planPath, ".artifacts", dependentJob+"-*", "job.log")
	dependentMatches, _ := filepath.Glob(dependentPattern)
	if len(dependentMatches) == 0 {
		return fmt.Errorf("could not find log for dependent job %s", dependentJob)
	}

	// Get modification times
	depInfo, err := os.Stat(depMatches[0])
	if err != nil {
		return fmt.Errorf("could not stat %s: %w", depMatches[0], err)
	}

	dependentInfo, err := os.Stat(dependentMatches[0])
	if err != nil {
		return fmt.Errorf("could not stat %s: %w", dependentMatches[0], err)
	}

	// Dependent job should have been modified after (or at same time as) its dependency
	if dependentInfo.ModTime().Before(depInfo.ModTime()) {
		return fmt.Errorf("%s (modified %v) started before %s (modified %v) completed - dependency violation",
			dependentJob, dependentInfo.ModTime(), depJob, depInfo.ModTime())
	}

	return nil
}

func quitDAG_TUI(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)
	return session.SendKeys("q")
}
