package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/assert"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/harness"
	"github.com/mattsolo1/grove-tend/pkg/verify"
)

// RollingPlanName is the name of the auto-created rolling plan.
const RollingPlanName = "rolling"

// RollingPlanWorkflowScenario tests the rolling plan feature that automatically creates
// and uses a "rolling" plan when no plan is specified and no active job is set.
var RollingPlanWorkflowScenario = harness.NewScenario(
	"rolling-plan-workflow",
	"Verifies the complete lifecycle of the rolling plan feature, from lazy creation to precedence rules.",
	[]string{"core", "plan", "rolling-plan", "ux"},
	[]harness.Step{
		// Setup mocks for LLM operations
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),

		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "rolling-plan-project")
			if err != nil {
				return err
			}

			// Store the expected rolling plan path for later verification
			rollingPlanPath := filepath.Join(notebooksRoot, "workspaces", "rolling-plan-project", "plans", RollingPlanName)
			ctx.Set("rolling_plan_path", rollingPlanPath)

			// Verify no active plan is set initially
			cmd := ctx.Bin("current")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("current command failed: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("no active job set initially", result.Stdout, "No active job set")
			})
		}),

		harness.NewStep("First use of 'flow add' triggers lazy creation", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			rollingPlanPath := ctx.GetString("rolling_plan_path")

			// Run 'flow add' without specifying a plan - should trigger rolling plan creation
			cmd := ctx.Bin("add",
				"--title", "First Rolling Job",
				"-p", "My first job added to rolling plan")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("add command failed: %w", err)
			}

			// Verify stderr contains the notification message
			if err := ctx.Check("notification message in stderr", assert.Contains(result.Stderr, "No active plan set. Using rolling plan at:")); err != nil {
				return err
			}

			// Verify the rolling plan directory was created
			if err := ctx.Check("rolling plan directory exists", fs.AssertExists(rollingPlanPath)); err != nil {
				return err
			}

			// Verify .grove-plan.yml exists in the rolling plan directory
			planConfigPath := filepath.Join(rollingPlanPath, ".grove-plan.yml")
			if err := ctx.Check("plan config file exists", fs.AssertExists(planConfigPath)); err != nil {
				return err
			}

			// Verify a job file was created
			jobFiles, err := fs.ListFiles(rollingPlanPath)
			if err != nil {
				return fmt.Errorf("listing rolling plan files: %w", err)
			}

			foundJob := false
			for _, f := range jobFiles {
				if strings.HasSuffix(f, ".md") && strings.Contains(f, "first-rolling-job") {
					foundJob = true
					ctx.Set("first_job_file", f)
					break
				}
			}

			if !foundJob {
				return fmt.Errorf("job file '01-first-rolling-job.md' not found in rolling plan")
			}

			return nil
		}),

		harness.NewStep("Verify state commands are unaffected by implicit rolling plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// 'flow current' should still show no active job
			cmd := ctx.Bin("current")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("current command failed: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("no active job set after using rolling plan", result.Stdout, "No active job set")
			})
		}),

		harness.NewStep("Verify rolling plan is discoverable by 'flow list'", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("list")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("list command failed: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("rolling plan in list", result.Stdout, RollingPlanName)
			})
		}),

		harness.NewStep("Subsequent use of 'flow add' does not re-notify", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			rollingPlanPath := ctx.GetString("rolling_plan_path")

			// Run another 'flow add' - should NOT trigger notification again
			cmd := ctx.Bin("add",
				"--title", "Second Rolling Job",
				"-p", "My second job added to rolling plan")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("add command failed: %w", err)
			}

			// Verify stderr does NOT contain the notification message
			if strings.Contains(result.Stderr, "No active plan set. Using rolling plan at:") {
				return fmt.Errorf("expected no notification on subsequent use, but got: %s", result.Stderr)
			}

			// Verify a second job file was created
			jobFiles, err := fs.ListFiles(rollingPlanPath)
			if err != nil {
				return fmt.Errorf("listing rolling plan files: %w", err)
			}

			foundSecondJob := false
			for _, f := range jobFiles {
				if strings.HasSuffix(f, ".md") && strings.Contains(f, "second-rolling-job") {
					foundSecondJob = true
					break
				}
			}

			if !foundSecondJob {
				return fmt.Errorf("second job file not found in rolling plan")
			}

			return nil
		}),

		harness.NewStep("Verify explicit plan directory takes precedence", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			rollingPlanPath := ctx.GetString("rolling_plan_path")

			// Initialize a new, separate plan
			cmd := ctx.Bin("plan", "init", "explicit-plan")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			explicitPlanPath := filepath.Join(notebooksRoot, "workspaces", "rolling-plan-project", "plans", "explicit-plan")
			ctx.Set("explicit_plan_path", explicitPlanPath)

			// Count jobs in rolling plan before adding to explicit plan
			rollingJobsBefore, err := fs.ListFiles(rollingPlanPath)
			if err != nil {
				return fmt.Errorf("listing rolling plan files: %w", err)
			}
			rollingJobCountBefore := 0
			for _, f := range rollingJobsBefore {
				if strings.HasSuffix(f, ".md") && !strings.HasPrefix(f, ".") {
					rollingJobCountBefore++
				}
			}

			// Add a job, explicitly targeting the new plan
			cmd = ctx.Bin("add", "explicit-plan",
				"--title", "Explicit Job",
				"-p", "Job added to explicit plan")
			cmd.Dir(projectDir)
			result = cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("add to explicit plan failed: %w", err)
			}

			// Verify the job was created in explicit-plan
			explicitJobFiles, err := fs.ListFiles(explicitPlanPath)
			if err != nil {
				return fmt.Errorf("listing explicit plan files: %w", err)
			}

			foundExplicitJob := false
			for _, f := range explicitJobFiles {
				if strings.HasSuffix(f, ".md") && strings.Contains(f, "explicit-job") {
					foundExplicitJob = true
					break
				}
			}

			if !foundExplicitJob {
				return fmt.Errorf("job not found in explicit plan")
			}

			// Verify no new jobs were added to rolling plan
			rollingJobsAfter, err := fs.ListFiles(rollingPlanPath)
			if err != nil {
				return fmt.Errorf("listing rolling plan files after: %w", err)
			}
			rollingJobCountAfter := 0
			for _, f := range rollingJobsAfter {
				if strings.HasSuffix(f, ".md") && !strings.HasPrefix(f, ".") {
					rollingJobCountAfter++
				}
			}

			if rollingJobCountAfter != rollingJobCountBefore {
				return fmt.Errorf("expected %d jobs in rolling plan, got %d (explicit plan should not affect rolling)",
					rollingJobCountBefore, rollingJobCountAfter)
			}

			return nil
		}),

		harness.NewStep("Verify active plan takes precedence over rolling plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			explicitPlanPath := ctx.GetString("explicit_plan_path")
			rollingPlanPath := ctx.GetString("rolling_plan_path")

			// Set the active plan
			cmd := ctx.Bin("set", "explicit-plan")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("set command failed: %w", err)
			}

			// Verify current shows the active plan
			cmd = ctx.Bin("current")
			cmd.Dir(projectDir)
			result = cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := ctx.Check("active plan shown in current", assert.Contains(result.Stdout, "explicit-plan")); err != nil {
				return err
			}

			// Count jobs in rolling plan before adding
			rollingJobsBefore, err := fs.ListFiles(rollingPlanPath)
			if err != nil {
				return fmt.Errorf("listing rolling plan files: %w", err)
			}
			rollingJobCountBefore := 0
			for _, f := range rollingJobsBefore {
				if strings.HasSuffix(f, ".md") && !strings.HasPrefix(f, ".") {
					rollingJobCountBefore++
				}
			}

			// Add a job without specifying a directory - should use active plan, not rolling
			cmd = ctx.Bin("add",
				"--title", "Active Job",
				"-p", "Job added to active plan")
			cmd.Dir(projectDir)
			result = cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("add with active plan failed: %w", err)
			}

			// Verify the job was created in explicit-plan (the active plan)
			explicitJobFiles, err := fs.ListFiles(explicitPlanPath)
			if err != nil {
				return fmt.Errorf("listing explicit plan files: %w", err)
			}

			foundActiveJob := false
			for _, f := range explicitJobFiles {
				if strings.HasSuffix(f, ".md") && strings.Contains(f, "active-job") {
					foundActiveJob = true
					break
				}
			}

			if !foundActiveJob {
				return fmt.Errorf("job not found in active plan (explicit-plan)")
			}

			// Verify no new jobs were added to rolling plan
			rollingJobsAfter, err := fs.ListFiles(rollingPlanPath)
			if err != nil {
				return fmt.Errorf("listing rolling plan files after: %w", err)
			}
			rollingJobCountAfter := 0
			for _, f := range rollingJobsAfter {
				if strings.HasSuffix(f, ".md") && !strings.HasPrefix(f, ".") {
					rollingJobCountAfter++
				}
			}

			if rollingJobCountAfter != rollingJobCountBefore {
				return fmt.Errorf("expected %d jobs in rolling plan, got %d (active plan should take precedence)",
					rollingJobCountBefore, rollingJobCountAfter)
			}

			return nil
		}),

		harness.NewStep("Verify 'flow unset' reverts to rolling plan behavior", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			rollingPlanPath := ctx.GetString("rolling_plan_path")

			// Unset the active plan
			cmd := ctx.Bin("unset")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("unset command failed: %w", err)
			}

			// Add one final job without specifying a directory - should go to rolling plan
			cmd = ctx.Bin("add",
				"--title", "Back to Rolling",
				"-p", "Job added after unsetting active plan")
			cmd.Dir(projectDir)
			result = cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("add after unset failed: %w", err)
			}

			// Verify the job was created in rolling plan
			rollingJobFiles, err := fs.ListFiles(rollingPlanPath)
			if err != nil {
				return fmt.Errorf("listing rolling plan files: %w", err)
			}

			foundBackToRollingJob := false
			for _, f := range rollingJobFiles {
				if strings.HasSuffix(f, ".md") && strings.Contains(f, "back-to-rolling") {
					foundBackToRollingJob = true
					break
				}
			}

			if !foundBackToRollingJob {
				return fmt.Errorf("job not found in rolling plan after unsetting active plan")
			}

			return nil
		}),

		harness.NewStep("Verify 'flow status' works with rolling plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// Ensure no active plan is set
			cmd := ctx.Bin("current")
			cmd.Dir(projectDir)
			result := cmd.Run()

			if !strings.Contains(result.Stdout, "No active job set") {
				// Unset if needed
				cmd = ctx.Bin("unset")
				cmd.Dir(projectDir)
				cmd.Run()
			}

			// Run status with --json to avoid TUI
			cmd = ctx.Bin("status", "--json")
			cmd.Dir(projectDir)
			result = cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("status command failed: %w", err)
			}

			// Verify the JSON output contains jobs from the rolling plan
			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("first job in status output", result.Stdout, "first-rolling-job")
				v.Contains("second job in status output", result.Stdout, "second-rolling-job")
				v.Contains("back-to-rolling job in status output", result.Stdout, "back-to-rolling")
			})
		}),
	},
)
