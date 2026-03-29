package scenarios

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/tui"
	"github.com/grovetools/tend/pkg/verify"
)

// XMLJobRulesInheritanceScenario tests that creating an XML plan job via the TUI
// inherits the rules_file from the parent job.
var XMLJobRulesInheritanceScenario = harness.NewScenarioWithOptions(
	"xml-job-rules-inheritance",
	"Verifies that XML plan jobs created with 'x' key inherit rules_file from the parent job.",
	[]string{"tui", "plan", "status", "xml", "rules"},
	[]harness.Step{
		harness.NewStep("Setup plan with job that has rules_file", setupXMLRulesInheritancePlan),
		harness.NewStep("Launch status TUI", launchXMLRulesInheritanceTUI),
		harness.NewStep("Create XML job with 'x'", createXMLJobForRulesTest),
		harness.NewStep("Verify XML job inherits rules_file", verifyXMLJobRulesInheritance),
		harness.NewStep("Quit the TUI", quitXMLRulesInheritanceTUI),
	},
	true,  // localOnly = true, requires tmux
	false, // explicitOnly = false
)

func setupXMLRulesInheritancePlan(ctx *harness.Context) error {
	projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "xml-rules-project")
	if err != nil {
		return err
	}

	// Create grove.yml
	groveYml := "name: xml-rules-project\ndescription: Test project for XML rules inheritance\n"
	if err := fs.WriteString(filepath.Join(projectDir, "grove.yml"), groveYml); err != nil {
		return fmt.Errorf("failed to create grove.yml: %w", err)
	}

	// Initialize the plan
	initCmd := ctx.Bin("plan", "init", "xml-rules-plan")
	initCmd.Dir(projectDir)
	if result := initCmd.Run(); result.Error != nil {
		return fmt.Errorf("plan init failed: %w", result.Error)
	}
	planPath := filepath.Join(notebooksRoot, "workspaces", "xml-rules-project", "plans", "xml-rules-plan")
	ctx.Set("plan_path", planPath)

	// Add a job
	addCmd := ctx.Bin("plan", "add", "xml-rules-plan",
		"--type", "oneshot",
		"--title", "Parent Job",
		"-p", "echo 'parent'")
	addCmd.Dir(projectDir)
	if result := addCmd.Run(); result.Error != nil {
		return fmt.Errorf("failed to add job: %w", result.Error)
	}

	// Inject rules_file into the job's frontmatter
	jobPath := filepath.Join(planPath, "01-parent-job.md")
	content, err := os.ReadFile(jobPath)
	if err != nil {
		return fmt.Errorf("failed to read job file: %w", err)
	}
	updates := map[string]interface{}{
		"rules_file": "custom-preset.rules",
	}
	newContent, err := orchestration.UpdateFrontmatter(content, updates)
	if err != nil {
		return fmt.Errorf("failed to update frontmatter: %w", err)
	}
	if err := os.WriteFile(jobPath, newContent, 0644); err != nil {
		return fmt.Errorf("failed to write updated job: %w", err)
	}

	return nil
}

func launchXMLRulesInheritanceTUI(ctx *harness.Context) error {
	projectDir := ctx.GetString("project_dir")
	homeDir := ctx.GetString("home_dir")

	flowBinary, err := findFlowBinary()
	if err != nil {
		return err
	}

	wrapperScript := filepath.Join(ctx.RootDir, "run-flow-xml-rules")
	scriptContent := fmt.Sprintf("#!/bin/bash\nexport HOME=%s\ncd %s\nexec %s plan status xml-rules-plan\n", homeDir, projectDir, flowBinary)
	if err := fs.WriteString(wrapperScript, scriptContent); err != nil {
		return fmt.Errorf("failed to create wrapper script: %w", err)
	}
	if err := os.Chmod(wrapperScript, 0755); err != nil {
		return fmt.Errorf("failed to make wrapper script executable: %w", err)
	}

	session, err := ctx.StartTUI(wrapperScript, []string{})
	if err != nil {
		return fmt.Errorf("failed to start flow plan status: %w", err)
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

func createXMLJobForRulesTest(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	// Press 'x' to create XML job
	if err := session.SendKeys("x"); err != nil {
		return fmt.Errorf("failed to send 'x' key: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Enter title for the job
	if err := session.SendKeys("XML Rules Inherited"); err != nil {
		return fmt.Errorf("failed to enter job title: %w", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Confirm with Enter
	if err := session.SendKeys("Enter"); err != nil {
		return fmt.Errorf("failed to confirm job creation: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	return nil
}

func verifyXMLJobRulesInheritance(ctx *harness.Context) error {
	planPath := ctx.GetString("plan_path")

	// Load the plan to find the XML job
	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	var xmlJob *orchestration.Job
	for _, job := range plan.Jobs {
		if job.Title == "XML Rules Inherited" {
			xmlJob = job
			break
		}
	}

	if xmlJob == nil {
		return fmt.Errorf("XML job not found in plan")
	}

	// Read job file for frontmatter verification
	content, err := os.ReadFile(xmlJob.FilePath)
	if err != nil {
		return fmt.Errorf("failed to read XML job file: %w", err)
	}

	return ctx.Verify(func(v *verify.Collector) {
		v.Equal("XML job inherits rules_file from parent", "custom-preset.rules", xmlJob.RulesFile)
		v.Contains("rules_file in frontmatter", string(content), "rules_file: custom-preset.rules")
	})
}

func quitXMLRulesInheritanceTUI(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)
	return session.SendKeys("q")
}
