package scenarios

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattsolo1/grove-core/tui/theme"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/harness"
	"github.com/mattsolo1/grove-tend/pkg/tui"
	"github.com/mattsolo1/grove-tend/pkg/verify"
)

// PlanStatusTUIMultiSelectScenario tests multi-job selection and batch operations.
var PlanStatusTUIMultiSelectScenario = harness.NewScenarioWithOptions(
	"plan-status-tui-multi-select",
	"Verifies multi-job selection with space, 'a', and 'N' keys in the status TUI.",
	[]string{"tui", "plan", "status", "multi-select"},
	[]harness.Step{
		harness.NewStep("Setup plan with multiple jobs", setupMultiSelectPlan),
		harness.NewStep("Launch status TUI", launchMultiSelectTUI),
		harness.NewStep("Toggle single job selection with space", toggleSingleJobSelection),
		harness.NewStep("Verify SEL column appears with checkbox", verifySingleSelection),
		harness.NewStep("Deselect job with space again", deselectSingleJob),
		harness.NewStep("Verify SEL column disappears", verifyNoSelection),
		harness.NewStep("Select all jobs with 'a'", selectAllJobs),
		harness.NewStep("Verify all jobs selected", verifyAllJobsSelected),
		harness.NewStep("Deselect all jobs with 'N'", deselectAllJobs),
		harness.NewStep("Verify all jobs deselected", verifyAllJobsDeselected),
		harness.NewStep("Quit the TUI", quitStatusTUI),
	},
	true,  // localOnly = true, requires tmux
	false, // explicitOnly = false
)

// PlanStatusTUIBatchArchiveScenario tests batch archiving multiple selected jobs.
var PlanStatusTUIBatchArchiveScenario = harness.NewScenarioWithOptions(
	"plan-status-tui-batch-archive",
	"Verifies archiving multiple selected jobs with 'X' key.",
	[]string{"tui", "plan", "status", "multi-select", "archive"},
	[]harness.Step{
		harness.NewStep("Setup plan with multiple jobs", setupMultiSelectPlan),
		harness.NewStep("Launch status TUI", launchMultiSelectTUI),
		harness.NewStep("Select two jobs for archiving", selectTwoJobs),
		harness.NewStep("Archive selected jobs with 'X'", archiveSelectedJobs),
		harness.NewStep("Verify jobs archived", verifyJobsArchived),
		harness.NewStep("Quit the TUI", quitStatusTUI),
	},
	true,  // localOnly = true, requires tmux
	false, // explicitOnly = false
)

// PlanStatusTUIBatchSetStatusScenario tests batch status updates on selected jobs.
var PlanStatusTUIBatchSetStatusScenario = harness.NewScenarioWithOptions(
	"plan-status-tui-batch-set-status",
	"Verifies setting status on multiple selected jobs with 'S' key.",
	[]string{"tui", "plan", "status", "multi-select", "status"},
	[]harness.Step{
		harness.NewStep("Setup plan with multiple jobs", setupMultiSelectPlan),
		harness.NewStep("Launch status TUI", launchMultiSelectTUI),
		harness.NewStep("Select two pending jobs", selectTwoJobs),
		harness.NewStep("Set status to 'hold' with 'S'", setStatusToHold),
		harness.NewStep("Verify status updated for both jobs", verifyStatusUpdatedToHold),
		harness.NewStep("Quit the TUI", quitStatusTUI),
	},
	true,  // localOnly = true, requires tmux
	false, // explicitOnly = false
)

// PlanStatusTUIBatchXMLDepsScenario tests creating an XML job depending on selected jobs.
var PlanStatusTUIBatchXMLDepsScenario = harness.NewScenarioWithOptions(
	"plan-status-tui-batch-xml-deps",
	"Verifies creating XML job with 'x' key that depends on selected jobs.",
	[]string{"tui", "plan", "status", "multi-select", "xml"},
	[]harness.Step{
		harness.NewStep("Setup plan with multiple jobs", setupMultiSelectPlan),
		harness.NewStep("Launch status TUI", launchMultiSelectTUI),
		harness.NewStep("Mark two jobs as completed", markTwoJobsCompleted),
		harness.NewStep("Select the two completed jobs", selectTwoJobs),
		harness.NewStep("Create XML job with 'x'", createXMLJobFromSelected),
		harness.NewStep("Verify XML job depends on selected jobs", verifyXMLJobDependencies),
		harness.NewStep("Quit the TUI", quitStatusTUI),
	},
	true,  // localOnly = true, requires tmux
	false, // explicitOnly = false
)

// PlanStatusTUIBatchImplementDepsScenario tests creating an implement job depending on selected jobs.
var PlanStatusTUIBatchImplementDepsScenario = harness.NewScenarioWithOptions(
	"plan-status-tui-batch-implement-deps",
	"Verifies creating implement job with 'i' key that depends on selected jobs.",
	[]string{"tui", "plan", "status", "multi-select", "implement"},
	[]harness.Step{
		harness.NewStep("Setup plan with multiple jobs", setupMultiSelectPlan),
		harness.NewStep("Launch status TUI", launchMultiSelectTUI),
		harness.NewStep("Select two jobs", selectTwoJobs),
		harness.NewStep("Create implement job with 'i'", createImplementJobFromSelected),
		harness.NewStep("Verify implement job depends on selected jobs", verifyImplementJobDependencies),
		harness.NewStep("Quit the TUI", quitStatusTUI),
	},
	true,  // localOnly = true, requires tmux
	false, // explicitOnly = false
)

// PlanStatusTUISingleJobArchiveScenario tests single-job archive when no selections exist.
var PlanStatusTUISingleJobArchiveScenario = harness.NewScenarioWithOptions(
	"plan-status-tui-single-job-archive",
	"Verifies archiving single job under cursor with 'X' when no jobs selected.",
	[]string{"tui", "plan", "status", "archive"},
	[]harness.Step{
		harness.NewStep("Setup plan with multiple jobs", setupMultiSelectPlan),
		harness.NewStep("Launch status TUI", launchMultiSelectTUI),
		harness.NewStep("Navigate to second job", navigateToSecondJob),
		harness.NewStep("Archive job under cursor with 'X'", archiveCursorJob),
		harness.NewStep("Verify only cursor job archived", verifySingleJobArchived),
		harness.NewStep("Quit the TUI", quitStatusTUI),
	},
	true,  // localOnly = true, requires tmux
	false, // explicitOnly = false
)

// PlanStatusTUISingleJobSetStatusScenario tests single-job status update when no selections exist.
var PlanStatusTUISingleJobSetStatusScenario = harness.NewScenarioWithOptions(
	"plan-status-tui-single-job-set-status",
	"Verifies setting status on single job under cursor with 'S' when no jobs selected.",
	[]string{"tui", "plan", "status", "status"},
	[]harness.Step{
		harness.NewStep("Setup plan with multiple jobs", setupMultiSelectPlan),
		harness.NewStep("Launch status TUI", launchMultiSelectTUI),
		harness.NewStep("Navigate to second job", navigateToSecondJob),
		harness.NewStep("Set status to 'hold' with 'S'", setStatusToHoldCursor),
		harness.NewStep("Verify only cursor job status updated", verifySingleJobStatusUpdated),
		harness.NewStep("Quit the TUI", quitStatusTUI),
	},
	true,  // localOnly = true, requires tmux
	false, // explicitOnly = false
)

// PlanStatusTUIBatchChangeTypeScenario tests changing job type on selected jobs.
var PlanStatusTUIBatchChangeTypeScenario = harness.NewScenarioWithOptions(
	"plan-status-tui-batch-change-type",
	"Verifies changing job type on multiple selected jobs with 'Y' key.",
	[]string{"tui", "plan", "status", "multi-select", "type"},
	[]harness.Step{
		harness.NewStep("Setup plan with multiple jobs", setupMultiSelectPlan),
		harness.NewStep("Launch status TUI", launchMultiSelectTUI),
		harness.NewStep("Select two jobs", selectTwoJobs),
		harness.NewStep("Change type to 'oneshot' with 'Y'", changeTypeToOneshot),
		harness.NewStep("Verify type updated for both jobs", verifyTypeUpdatedToOneshot),
		harness.NewStep("Quit the TUI", quitStatusTUI),
	},
	true,  // localOnly = true, requires tmux
	false, // explicitOnly = false
)

// PlanStatusTUIBatchChangeTemplateScenario tests changing job template on selected jobs.
var PlanStatusTUIBatchChangeTemplateScenario = harness.NewScenarioWithOptions(
	"plan-status-tui-batch-change-template",
	"Verifies changing job template on multiple selected jobs with 'E' key.",
	[]string{"tui", "plan", "status", "multi-select", "template"},
	[]harness.Step{
		harness.NewStep("Setup plan with multiple jobs", setupMultiSelectPlan),
		harness.NewStep("Launch status TUI", launchMultiSelectTUI),
		harness.NewStep("Select two jobs", selectTwoJobs),
		harness.NewStep("Change template to 'agent-xml' with 'E'", changeTemplateToAgentXml),
		harness.NewStep("Verify template updated for both jobs", verifyTemplateUpdatedToAgentXml),
		harness.NewStep("Quit the TUI", quitStatusTUI),
	},
	true,  // localOnly = true, requires tmux
	false, // explicitOnly = false
)

// Helper functions

// setupMultiSelectPlan creates a plan with four jobs for testing multi-selection.
func setupMultiSelectPlan(ctx *harness.Context) error {
	projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "multi-select-project")
	if err != nil {
		return err
	}

	// Create grove.yml with logging enabled
	groveYml := `name: multi-select-project
description: Test project for multi-select TUI

logging:
  level: info
  file:
    enabled: true
    path: .grove/logs/grove.log
    format: json
  show_current_project: true
  show:
    - multi-select-project
`
	groveYmlPath := filepath.Join(projectDir, "grove.yml")
	if err := fs.WriteString(groveYmlPath, groveYml); err != nil {
		return fmt.Errorf("failed to create grove.yml: %w", err)
	}

	// Initialize the plan
	initCmd := ctx.Bin("plan", "init", "multi-select-plan")
	initCmd.Dir(projectDir)
	if result := initCmd.Run(); result.Error != nil {
		return fmt.Errorf("plan init failed: %w", result.Error)
	}
	planPath := filepath.Join(notebooksRoot, "workspaces", "multi-select-project", "plans", "multi-select-plan")
	ctx.Set("plan_path", planPath)

	// Add Job 1
	job1 := ctx.Bin("plan", "add", "multi-select-plan", "--type", "shell", "--title", "CX", "-p", "echo 'cx'")
	job1.Dir(projectDir)
	if result := job1.Run(); result.Error != nil {
		return fmt.Errorf("failed to add Job 1: %w", result.Error)
	}

	// Add Job 2
	job2 := ctx.Bin("plan", "add", "multi-select-plan", "--type", "shell", "--title", "Spec", "-p", "echo 'spec'")
	job2.Dir(projectDir)
	if result := job2.Run(); result.Error != nil {
		return fmt.Errorf("failed to add Job 2: %w", result.Error)
	}

	// Add Job 3
	job3 := ctx.Bin("plan", "add", "multi-select-plan", "--type", "shell", "--title", "Generate Plan", "-p", "echo 'plan'")
	job3.Dir(projectDir)
	if result := job3.Run(); result.Error != nil {
		return fmt.Errorf("failed to add Job 3: %w", result.Error)
	}

	// Add Job 4
	job4 := ctx.Bin("plan", "add", "multi-select-plan", "--type", "shell", "--title", "Implement", "-p", "echo 'implement'")
	job4.Dir(projectDir)
	if result := job4.Run(); result.Error != nil {
		return fmt.Errorf("failed to add Job 4: %w", result.Error)
	}

	return nil
}

// launchMultiSelectTUI launches the status TUI for the multi-select plan.
func launchMultiSelectTUI(ctx *harness.Context) error {
	projectDir := ctx.GetString("project_dir")
	homeDir := ctx.GetString("home_dir")

	flowBinary, err := findFlowBinary()
	if err != nil {
		return err
	}

	// Create wrapper script
	wrapperScript := filepath.Join(ctx.RootDir, "run-flow-multi-select")
	scriptContent := fmt.Sprintf("#!/bin/bash\nexport HOME=%s\ncd %s\nexec %s plan status multi-select-plan\n", homeDir, projectDir, flowBinary)
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

	// Wait for TUI to load
	if err := session.WaitForText("Plan Status", 10*time.Second); err != nil {
		content, _ := session.Capture()
		return fmt.Errorf("TUI did not load: %w\nContent:\n%s", err, content)
	}
	if err := session.WaitStable(); err != nil {
		return err
	}

	return nil
}

// toggleSingleJobSelection presses space to toggle selection on the first job.
func toggleSingleJobSelection(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	// Move down to second job (away from first to ensure we're on a specific job)
	if err := session.SendKeys("Down"); err != nil {
		return fmt.Errorf("failed to send Down key: %w", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Press space to select
	if err := session.SendKeys("Space"); err != nil {
		return fmt.Errorf("failed to send Space key: %w", err)
	}

	time.Sleep(500 * time.Millisecond)
	return nil
}

// verifySingleSelection verifies that SEL column appears with one checkbox.
func verifySingleSelection(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	if err := session.WaitStable(); err != nil {
		return err
	}

	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	return ctx.Verify(func(v *verify.Collector) {
		v.Contains("SEL column visible", content, "SEL")
		// Use theme.IconSelect which is the nerd font checkbox icon
		v.Contains("checkbox present", content, theme.IconSelect)
	})
}

// deselectSingleJob presses space again to deselect.
func deselectSingleJob(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	if err := session.SendKeys("Space"); err != nil {
		return fmt.Errorf("failed to send Space key: %w", err)
	}

	time.Sleep(500 * time.Millisecond)
	return nil
}

// verifyNoSelection verifies SEL column disappears when no jobs selected.
func verifyNoSelection(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	if err := session.WaitStable(); err != nil {
		return err
	}

	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	// SEL column should not be visible when nothing is selected
	if strings.Contains(content, "SEL") {
		return fmt.Errorf("expected SEL column to disappear when no jobs selected")
	}

	return nil
}

// selectAllJobs presses 'a' to select all jobs.
func selectAllJobs(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	if err := session.SendKeys("a"); err != nil {
		return fmt.Errorf("failed to send 'a' key: %w", err)
	}

	time.Sleep(500 * time.Millisecond)
	return nil
}

// verifyAllJobsSelected verifies all jobs have checkboxes.
func verifyAllJobsSelected(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	if err := session.WaitStable(); err != nil {
		return err
	}

	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	return ctx.Verify(func(v *verify.Collector) {
		v.Contains("SEL column visible", content, "SEL")
		// Count the number of selected checkbox icons - should have 4
		checkboxCount := strings.Count(content, theme.IconSelect)
		v.Equal("four jobs selected", 4, checkboxCount)
	})
}

// deselectAllJobs presses 'N' to deselect all jobs.
func deselectAllJobs(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	if err := session.SendKeys("N"); err != nil {
		return fmt.Errorf("failed to send 'N' key: %w", err)
	}

	time.Sleep(500 * time.Millisecond)
	return nil
}

// verifyAllJobsDeselected verifies SEL column disappears after deselecting all.
func verifyAllJobsDeselected(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	if err := session.WaitStable(); err != nil {
		return err
	}

	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	// SEL column should not be visible
	if strings.Contains(content, "SEL") {
		return fmt.Errorf("expected SEL column to disappear after deselect all")
	}

	return nil
}

// selectTwoJobs selects the first two jobs using space.
func selectTwoJobs(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	// Cursor starts at bottom (04-implement.md), navigate to first job
	// Go up 3 times: 04 -> 03 -> 02 -> 01
	for i := 0; i < 3; i++ {
		if err := session.SendKeys("Up"); err != nil {
			return fmt.Errorf("failed to navigate up: %w", err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Select first job (01-cx.md)
	if err := session.SendKeys("Space"); err != nil {
		return fmt.Errorf("failed to send Space key for first job: %w", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Move to second job
	if err := session.SendKeys("Down"); err != nil {
		return fmt.Errorf("failed to send Down key: %w", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Select second job (02-spec.md)
	if err := session.SendKeys("Space"); err != nil {
		return fmt.Errorf("failed to send Space key for second job: %w", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Store which jobs were selected for verification
	ctx.Set("selected_jobs", []string{"01-cx.md", "02-spec.md"})

	return nil
}

// archiveSelectedJobs presses 'X' and confirms to archive selected jobs.
func archiveSelectedJobs(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	// Press 'X' to archive
	if err := session.SendKeys("X"); err != nil {
		return fmt.Errorf("failed to send 'X' key: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Confirm the archive action with 'y'
	if err := session.SendKeys("y"); err != nil {
		return fmt.Errorf("failed to confirm archive: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	return nil
}

// verifyJobsArchived verifies the selected jobs are archived.
func verifyJobsArchived(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)
	planPath := ctx.GetString("plan_path")
	selectedJobs := ctx.Get("selected_jobs").([]string)

	if err := session.WaitStable(); err != nil {
		return err
	}

	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	return ctx.Verify(func(v *verify.Collector) {
		// Jobs should not be visible in TUI
		v.Equal("01-cx.md not in TUI", false, strings.Contains(content, "01-cx.md"))
		v.Equal("02-spec.md not in TUI", false, strings.Contains(content, "02-spec.md"))

		// Jobs should be in .archive directory
		for _, jobName := range selectedJobs {
			archivePath := filepath.Join(planPath, ".archive", jobName)
			v.Equal(fmt.Sprintf("%s exists in archive", jobName), nil, fs.AssertExists(archivePath))
		}
	})
}

// setStatusToHold presses 'S' and selects 'hold' status.
func setStatusToHold(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	// Press 'S' to open status picker
	if err := session.SendKeys("S"); err != nil {
		return fmt.Errorf("failed to send 'S' key: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Wait for status picker to appear (status is displayed as "On Hold")
	if err := session.WaitForText("On Hold", 5*time.Second); err != nil {
		return fmt.Errorf("On Hold status not found in picker: %w", err)
	}

	// Navigate to 'On Hold' status (index 2 in the list: Pending, Todo, On Hold, ...)
	for i := 0; i < 2; i++ {
		if err := session.SendKeys("Down"); err != nil {
			return fmt.Errorf("failed to navigate in status picker: %w", err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Select hold with Enter
	if err := session.SendKeys("Enter"); err != nil {
		return fmt.Errorf("failed to select hold status: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	return nil
}

// verifyStatusUpdatedToHold verifies both selected jobs have status 'hold'.
func verifyStatusUpdatedToHold(ctx *harness.Context) error {
	planPath := ctx.GetString("plan_path")
	selectedJobs := ctx.Get("selected_jobs").([]string)

	// Load jobs first to handle errors properly
	jobs := make(map[string]*orchestration.Job)
	for _, jobName := range selectedJobs {
		jobPath := filepath.Join(planPath, jobName)
		job, err := orchestration.LoadJob(jobPath)
		if err != nil {
			return fmt.Errorf("failed to load %s: %w", jobName, err)
		}
		jobs[jobName] = job
	}

	return ctx.Verify(func(v *verify.Collector) {
		for jobName, job := range jobs {
			v.Equal(fmt.Sprintf("%s has status 'hold'", jobName), orchestration.JobStatusHold, job.Status)
		}
	})
}

// markTwoJobsCompleted marks the first two jobs as completed.
func markTwoJobsCompleted(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	// Mark first job completed with 'c'
	if err := session.SendKeys("c"); err != nil {
		return fmt.Errorf("failed to mark first job completed: %w", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Move to second job
	if err := session.SendKeys("Down"); err != nil {
		return fmt.Errorf("failed to move to second job: %w", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Mark second job completed
	if err := session.SendKeys("c"); err != nil {
		return fmt.Errorf("failed to mark second job completed: %w", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Move back to first job
	if err := session.SendKeys("Up"); err != nil {
		return fmt.Errorf("failed to move back to first job: %w", err)
	}
	time.Sleep(300 * time.Millisecond)

	return nil
}

// createXMLJobFromSelected presses 'x' and enters a title for the XML job.
func createXMLJobFromSelected(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	// Press 'x' to create XML job
	if err := session.SendKeys("x"); err != nil {
		return fmt.Errorf("failed to send 'x' key: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Enter title for the job
	if err := session.SendKeys("XML Summary Job"); err != nil {
		return fmt.Errorf("failed to enter job title: %w", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Confirm with Enter
	if err := session.SendKeys("Enter"); err != nil {
		return fmt.Errorf("failed to confirm job creation: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	ctx.Set("xml_job_title", "XML Summary Job")
	return nil
}

// verifyXMLJobDependencies verifies the XML job has the correct dependencies.
func verifyXMLJobDependencies(ctx *harness.Context) error {
	planPath := ctx.GetString("plan_path")
	selectedJobs := ctx.Get("selected_jobs").([]string)

	// Find the newly created XML job
	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	var xmlJob *orchestration.Job
	for _, job := range plan.Jobs {
		if job.Title == "XML Summary Job" {
			xmlJob = job
			break
		}
	}

	if xmlJob == nil {
		return fmt.Errorf("XML job not found in plan")
	}

	return ctx.Verify(func(v *verify.Collector) {
		v.Equal("XML job has agent-xml template", "agent-xml", xmlJob.Template)
		v.Equal("XML job has correct dependency count", len(selectedJobs), len(xmlJob.DependsOn))
		// Dependencies use job IDs (like "cx-abc123") not filenames (like "01-cx.md")
		// Extract job name prefix from filename (e.g., "cx" from "01-cx.md")
		for _, selectedFile := range selectedJobs {
			// Extract name by removing numeric prefix and .md suffix
			// "01-cx.md" -> "cx", "02-spec.md" -> "spec"
			name := strings.TrimSuffix(selectedFile, ".md")
			if idx := strings.Index(name, "-"); idx != -1 {
				name = name[idx+1:]
			}
			v.Contains(fmt.Sprintf("XML job depends on %s", name), strings.Join(xmlJob.DependsOn, ","), name+"-")
		}
	})
}

// createImplementJobFromSelected presses 'i' and enters a title for the implement job.
func createImplementJobFromSelected(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	// Press 'i' to create implement job
	if err := session.SendKeys("i"); err != nil {
		return fmt.Errorf("failed to send 'i' key: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Enter title for the job
	if err := session.SendKeys("Batch Implementation"); err != nil {
		return fmt.Errorf("failed to enter job title: %w", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Confirm with Enter
	if err := session.SendKeys("Enter"); err != nil {
		return fmt.Errorf("failed to confirm job creation: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	ctx.Set("implement_job_title", "Batch Implementation")
	return nil
}

// verifyImplementJobDependencies verifies the implement job has the correct dependencies.
func verifyImplementJobDependencies(ctx *harness.Context) error {
	planPath := ctx.GetString("plan_path")
	selectedJobs := ctx.Get("selected_jobs").([]string)

	// Find the newly created implement job
	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	var implementJob *orchestration.Job
	for _, job := range plan.Jobs {
		if job.Title == "Batch Implementation" {
			implementJob = job
			break
		}
	}

	if implementJob == nil {
		return fmt.Errorf("implement job not found in plan")
	}

	return ctx.Verify(func(v *verify.Collector) {
		v.Equal("implement job has interactive_agent type", orchestration.JobTypeInteractiveAgent, implementJob.Type)
		v.Equal("implement job has correct dependency count", len(selectedJobs), len(implementJob.DependsOn))
		// Dependencies use job IDs (like "cx-abc123") not filenames (like "01-cx.md")
		for _, selectedFile := range selectedJobs {
			// Extract job name prefix from filename
			name := strings.TrimSuffix(selectedFile, ".md")
			if idx := strings.Index(name, "-"); idx != -1 {
				name = name[idx+1:]
			}
			v.Contains(fmt.Sprintf("implement job depends on %s", name), strings.Join(implementJob.DependsOn, ","), name+"-")
		}
	})
}

// navigateToSecondJob moves cursor to the second job (02-spec.md).
// Note: Cursor starts at the bottom-most job (04-implement.md), so we need to go Up twice.
func navigateToSecondJob(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	// Cursor starts at bottom (04-implement.md), go up twice to reach 02-spec.md
	// 04 → 03 → 02
	for i := 0; i < 2; i++ {
		if err := session.SendKeys("Up"); err != nil {
			return fmt.Errorf("failed to navigate up: %w", err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	ctx.Set("cursor_job", "02-spec.md")
	return nil
}

// archiveCursorJob presses 'X' to archive the job under cursor.
func archiveCursorJob(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	// Press 'X' to archive
	if err := session.SendKeys("X"); err != nil {
		return fmt.Errorf("failed to send 'X' key: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Confirm the archive action with 'y' (not Enter)
	if err := session.SendKeys("y"); err != nil {
		return fmt.Errorf("failed to confirm archive: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	return nil
}

// verifySingleJobArchived verifies only the cursor job was archived.
func verifySingleJobArchived(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)
	planPath := ctx.GetString("plan_path")
	cursorJob := ctx.GetString("cursor_job")

	if err := session.WaitStable(); err != nil {
		return err
	}

	content, err := session.Capture(tui.WithCleanedOutput())
	if err != nil {
		return err
	}

	return ctx.Verify(func(v *verify.Collector) {
		// Cursor job should not be visible in TUI
		v.Equal(fmt.Sprintf("%s not in TUI", cursorJob), false, strings.Contains(content, cursorJob))

		// Other jobs should still be visible
		v.Contains("01-cx.md still visible", content, "01-cx.md")
		v.Contains("03-generate-plan.md still visible", content, "03-generate-plan.md")

		// Cursor job should be in archive
		archivePath := filepath.Join(planPath, ".archive", cursorJob)
		v.Equal(fmt.Sprintf("%s exists in archive", cursorJob), nil, fs.AssertExists(archivePath))
	})
}

// setStatusToHoldCursor presses 'S' and sets status for cursor job.
func setStatusToHoldCursor(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	// Press 'S' to open status picker
	if err := session.SendKeys("S"); err != nil {
		return fmt.Errorf("failed to send 'S' key: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Wait for status picker to appear (status is displayed as "On Hold")
	if err := session.WaitForText("On Hold", 5*time.Second); err != nil {
		return fmt.Errorf("On Hold status not found in picker: %w", err)
	}

	// Navigate to 'On Hold' status (index 2 in the list: Pending, Todo, On Hold, ...)
	for i := 0; i < 2; i++ {
		if err := session.SendKeys("Down"); err != nil {
			return fmt.Errorf("failed to navigate in status picker: %w", err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Select hold with Enter
	if err := session.SendKeys("Enter"); err != nil {
		return fmt.Errorf("failed to select hold status: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	return nil
}

// verifySingleJobStatusUpdated verifies only the cursor job status changed.
func verifySingleJobStatusUpdated(ctx *harness.Context) error {
	planPath := ctx.GetString("plan_path")
	cursorJob := ctx.GetString("cursor_job")

	// Load jobs first to handle errors properly
	cursorJobPath := filepath.Join(planPath, cursorJob)
	job, err := orchestration.LoadJob(cursorJobPath)
	if err != nil {
		return fmt.Errorf("failed to load %s: %w", cursorJob, err)
	}

	otherJobPath := filepath.Join(planPath, "01-cx.md")
	otherJob, err := orchestration.LoadJob(otherJobPath)
	if err != nil {
		return fmt.Errorf("failed to load 01-cx.md: %w", err)
	}

	return ctx.Verify(func(v *verify.Collector) {
		v.Equal(fmt.Sprintf("%s has status 'hold'", cursorJob), orchestration.JobStatusHold, job.Status)
		v.Equal("01-cx.md still has status 'pending'", orchestration.JobStatusPending, otherJob.Status)
	})
}

// changeTypeToOneshot presses 'Y' and selects 'oneshot' type.
func changeTypeToOneshot(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	// Press 'Y' to open type picker
	if err := session.SendKeys("Y"); err != nil {
		return fmt.Errorf("failed to send 'Y' key: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Wait for type picker to appear (types are capitalized)
	if err := session.WaitForText("Oneshot", 5*time.Second); err != nil {
		return fmt.Errorf("Oneshot type not found in picker: %w", err)
	}

	// Navigate to 'oneshot' type (assuming it's the second option after shell)
	if err := session.SendKeys("Down"); err != nil {
		return fmt.Errorf("failed to navigate in type picker: %w", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Select oneshot with Enter
	if err := session.SendKeys("Enter"); err != nil {
		return fmt.Errorf("failed to select oneshot type: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	return nil
}

// verifyTypeUpdatedToOneshot verifies both selected jobs have type 'oneshot'.
func verifyTypeUpdatedToOneshot(ctx *harness.Context) error {
	planPath := ctx.GetString("plan_path")
	selectedJobs := ctx.Get("selected_jobs").([]string)

	// Load jobs first to handle errors properly
	jobs := make(map[string]*orchestration.Job)
	for _, jobName := range selectedJobs {
		jobPath := filepath.Join(planPath, jobName)
		job, err := orchestration.LoadJob(jobPath)
		if err != nil {
			return fmt.Errorf("failed to load %s: %w", jobName, err)
		}
		jobs[jobName] = job
	}

	return ctx.Verify(func(v *verify.Collector) {
		for jobName, job := range jobs {
			v.Equal(fmt.Sprintf("%s has type 'oneshot'", jobName), orchestration.JobTypeOneshot, job.Type)
		}
	})
}

// changeTemplateToAgentXml presses 'E' and selects 'agent-xml' template.
func changeTemplateToAgentXml(ctx *harness.Context) error {
	session := ctx.Get("tui_session").(*tui.Session)

	time.Sleep(500 * time.Millisecond)

	// Press 'E' to open template picker
	if err := session.SendKeys("E"); err != nil {
		return fmt.Errorf("failed to send 'E' key: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Wait for template picker to appear (template is displayed as "Agent XML")
	if err := session.WaitForText("Agent XML", 5*time.Second); err != nil {
		return fmt.Errorf("Agent XML template not found in picker: %w", err)
	}

	// Navigate to 'Agent XML' template (index 1 in list: No Template, Agent XML, ...)
	if err := session.SendKeys("Down"); err != nil {
		return fmt.Errorf("failed to navigate in template picker: %w", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Select agent-xml with Enter
	if err := session.SendKeys("Enter"); err != nil {
		return fmt.Errorf("failed to select agent-xml template: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	return nil
}

// verifyTemplateUpdatedToAgentXml verifies both selected jobs have template 'agent-xml'.
func verifyTemplateUpdatedToAgentXml(ctx *harness.Context) error {
	planPath := ctx.GetString("plan_path")
	selectedJobs := ctx.Get("selected_jobs").([]string)

	// Load jobs first to handle errors properly
	jobs := make(map[string]*orchestration.Job)
	for _, jobName := range selectedJobs {
		jobPath := filepath.Join(planPath, jobName)
		job, err := orchestration.LoadJob(jobPath)
		if err != nil {
			return fmt.Errorf("failed to load %s: %w", jobName, err)
		}
		jobs[jobName] = job
	}

	return ctx.Verify(func(v *verify.Collector) {
		for jobName, job := range jobs {
			v.Equal(fmt.Sprintf("%s has template 'agent-xml'", jobName), "agent-xml", job.Template)
		}
	})
}
