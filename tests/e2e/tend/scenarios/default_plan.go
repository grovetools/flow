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

// DefaultPlanWorkflowScenario tests the default plan feature that automatically creates
// and uses a "default" plan when no plan is specified and no active job is set.
var DefaultPlanWorkflowScenario = harness.NewScenario(
	"default-plan-workflow",
	"Verifies the complete lifecycle of the default plan feature, from lazy creation to precedence rules.",
	[]string{"core", "plan", "default-plan", "ux"},
	[]harness.Step{
		// Setup mocks for LLM operations
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),

		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "default-plan-project")
			if err != nil {
				return err
			}

			// Store the expected default plan path for later verification
			defaultPlanPath := filepath.Join(notebooksRoot, "workspaces", "default-plan-project", "plans", "default")
			ctx.Set("default_plan_path", defaultPlanPath)

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
			defaultPlanPath := ctx.GetString("default_plan_path")

			// Run 'flow add' without specifying a plan - should trigger default plan creation
			cmd := ctx.Bin("add",
				"--title", "First Default Job",
				"-p", "My first job added to default plan")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("add command failed: %w", err)
			}

			// Verify stderr contains the notification message
			if err := ctx.Check("notification message in stderr", assert.Contains(result.Stderr, "No active plan set. Using default plan at:")); err != nil {
				return err
			}

			// Verify the default plan directory was created
			if err := ctx.Check("default plan directory exists", fs.AssertExists(defaultPlanPath)); err != nil {
				return err
			}

			// Verify .grove-plan.yml exists in the default plan directory
			planConfigPath := filepath.Join(defaultPlanPath, ".grove-plan.yml")
			if err := ctx.Check("plan config file exists", fs.AssertExists(planConfigPath)); err != nil {
				return err
			}

			// Verify a job file was created
			jobFiles, err := fs.ListFiles(defaultPlanPath)
			if err != nil {
				return fmt.Errorf("listing default plan files: %w", err)
			}

			foundJob := false
			for _, f := range jobFiles {
				if strings.HasSuffix(f, ".md") && strings.Contains(f, "first-default-job") {
					foundJob = true
					ctx.Set("first_job_file", f)
					break
				}
			}

			if !foundJob {
				return fmt.Errorf("job file '01-first-default-job.md' not found in default plan")
			}

			return nil
		}),

		harness.NewStep("Verify state commands are unaffected by implicit default", func(ctx *harness.Context) error {
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
				v.Contains("no active job set after using default", result.Stdout, "No active job set")
			})
		}),

		harness.NewStep("Verify default plan is discoverable by 'flow list'", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("list")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("list command failed: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("default plan in list", result.Stdout, "default")
			})
		}),

		harness.NewStep("Subsequent use of 'flow add' does not re-notify", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			defaultPlanPath := ctx.GetString("default_plan_path")

			// Run another 'flow add' - should NOT trigger notification again
			cmd := ctx.Bin("add",
				"--title", "Second Default Job",
				"-p", "My second job added to default plan")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("add command failed: %w", err)
			}

			// Verify stderr does NOT contain the notification message
			if strings.Contains(result.Stderr, "No active plan set. Using default plan at:") {
				return fmt.Errorf("expected no notification on subsequent use, but got: %s", result.Stderr)
			}

			// Verify a second job file was created
			jobFiles, err := fs.ListFiles(defaultPlanPath)
			if err != nil {
				return fmt.Errorf("listing default plan files: %w", err)
			}

			foundSecondJob := false
			for _, f := range jobFiles {
				if strings.HasSuffix(f, ".md") && strings.Contains(f, "second-default-job") {
					foundSecondJob = true
					break
				}
			}

			if !foundSecondJob {
				return fmt.Errorf("second job file not found in default plan")
			}

			return nil
		}),

		harness.NewStep("Verify explicit plan directory takes precedence", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			defaultPlanPath := ctx.GetString("default_plan_path")

			// Initialize a new, separate plan
			cmd := ctx.Bin("plan", "init", "explicit-plan")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			explicitPlanPath := filepath.Join(notebooksRoot, "workspaces", "default-plan-project", "plans", "explicit-plan")
			ctx.Set("explicit_plan_path", explicitPlanPath)

			// Count jobs in default plan before adding to explicit plan
			defaultJobsBefore, err := fs.ListFiles(defaultPlanPath)
			if err != nil {
				return fmt.Errorf("listing default plan files: %w", err)
			}
			defaultJobCountBefore := 0
			for _, f := range defaultJobsBefore {
				if strings.HasSuffix(f, ".md") && !strings.HasPrefix(f, ".") {
					defaultJobCountBefore++
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

			// Verify no new jobs were added to default plan
			defaultJobsAfter, err := fs.ListFiles(defaultPlanPath)
			if err != nil {
				return fmt.Errorf("listing default plan files after: %w", err)
			}
			defaultJobCountAfter := 0
			for _, f := range defaultJobsAfter {
				if strings.HasSuffix(f, ".md") && !strings.HasPrefix(f, ".") {
					defaultJobCountAfter++
				}
			}

			if defaultJobCountAfter != defaultJobCountBefore {
				return fmt.Errorf("expected %d jobs in default plan, got %d (explicit plan should not affect default)",
					defaultJobCountBefore, defaultJobCountAfter)
			}

			return nil
		}),

		harness.NewStep("Verify active plan takes precedence over default", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			explicitPlanPath := ctx.GetString("explicit_plan_path")
			defaultPlanPath := ctx.GetString("default_plan_path")

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

			// Count jobs in default plan before adding
			defaultJobsBefore, err := fs.ListFiles(defaultPlanPath)
			if err != nil {
				return fmt.Errorf("listing default plan files: %w", err)
			}
			defaultJobCountBefore := 0
			for _, f := range defaultJobsBefore {
				if strings.HasSuffix(f, ".md") && !strings.HasPrefix(f, ".") {
					defaultJobCountBefore++
				}
			}

			// Add a job without specifying a directory - should use active plan, not default
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

			// Verify no new jobs were added to default plan
			defaultJobsAfter, err := fs.ListFiles(defaultPlanPath)
			if err != nil {
				return fmt.Errorf("listing default plan files after: %w", err)
			}
			defaultJobCountAfter := 0
			for _, f := range defaultJobsAfter {
				if strings.HasSuffix(f, ".md") && !strings.HasPrefix(f, ".") {
					defaultJobCountAfter++
				}
			}

			if defaultJobCountAfter != defaultJobCountBefore {
				return fmt.Errorf("expected %d jobs in default plan, got %d (active plan should take precedence)",
					defaultJobCountBefore, defaultJobCountAfter)
			}

			return nil
		}),

		harness.NewStep("Verify 'flow unset' reverts to default behavior", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			defaultPlanPath := ctx.GetString("default_plan_path")

			// Unset the active plan
			cmd := ctx.Bin("unset")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("unset command failed: %w", err)
			}

			// Add one final job without specifying a directory - should go to default plan
			cmd = ctx.Bin("add",
				"--title", "Back to Default",
				"-p", "Job added after unsetting active plan")
			cmd.Dir(projectDir)
			result = cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("add after unset failed: %w", err)
			}

			// Verify the job was created in default plan
			defaultJobFiles, err := fs.ListFiles(defaultPlanPath)
			if err != nil {
				return fmt.Errorf("listing default plan files: %w", err)
			}

			foundBackToDefaultJob := false
			for _, f := range defaultJobFiles {
				if strings.HasSuffix(f, ".md") && strings.Contains(f, "back-to-default") {
					foundBackToDefaultJob = true
					break
				}
			}

			if !foundBackToDefaultJob {
				return fmt.Errorf("job not found in default plan after unsetting active plan")
			}

			return nil
		}),

		harness.NewStep("Verify 'flow status' works with default plan", func(ctx *harness.Context) error {
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

			// Verify the JSON output contains jobs from the default plan
			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("first job in status output", result.Stdout, "first-default-job")
				v.Contains("second job in status output", result.Stdout, "second-default-job")
				v.Contains("back-to-default job in status output", result.Stdout, "back-to-default")
			})
		}),
	},
)
