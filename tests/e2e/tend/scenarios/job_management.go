package scenarios

import (
	"fmt"
	"path/filepath"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/tend/pkg/assert"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
)

var JobManagementScenario = harness.NewScenario(
	"job-management-commands",
	"Tests the `flow plan jobs` subcommands for renaming and updating dependencies.",
	[]string{"core", "plan", "jobs", "cli"},
	[]harness.Step{
		harness.NewStep("Setup plan with complex dependencies", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "job-mgmt-project")
			if err != nil {
				return err
			}

			// Initialize the plan
			initCmd := ctx.Bin("plan", "init", "job-mgmt-plan")
			initCmd.Dir(projectDir)
			if result := initCmd.Run(); result.Error != nil {
				return fmt.Errorf("plan init failed: %w", result.Error)
			}
			planPath := filepath.Join(notebooksRoot, "workspaces", "job-mgmt-project", "plans", "job-mgmt-plan")
			ctx.Set("plan_path", planPath)

			// Add Job A: 01-setup.md
			jobA := ctx.Bin("plan", "add", "job-mgmt-plan", "--type", "shell", "--title", "Setup", "-p", "echo 'setup'")
			jobA.Dir(projectDir)
			resultA := jobA.Run()
			ctx.ShowCommandOutput(jobA.String(), resultA.Stdout, resultA.Stderr)
			if resultA.Error != nil {
				return fmt.Errorf("failed to add Job A: %w\nStderr: %s", resultA.Error, resultA.Stderr)
			}

			// Add Job B: 02-build.md (depends on A)
			jobB := ctx.Bin("plan", "add", "job-mgmt-plan", "--type", "shell", "--title", "Build", "-p", "echo 'build'", "-d", "01-setup.md")
			jobB.Dir(projectDir)
			resultB := jobB.Run()
			ctx.ShowCommandOutput(jobB.String(), resultB.Stdout, resultB.Stderr)
			if resultB.Error != nil {
				return fmt.Errorf("failed to add Job B: %w\nStderr: %s", resultB.Error, resultB.Stderr)
			}

			// Add Job C: 03-run-tests.md (depends on B)
			jobC := ctx.Bin("plan", "add", "job-mgmt-plan", "--type", "shell", "--title", "Run Tests", "-p", "echo 'test'", "-d", "02-build.md")
			jobC.Dir(projectDir)
			resultC := jobC.Run()
			if resultC.Error != nil {
				return fmt.Errorf("failed to add Job C: %w\nStderr: %s\nStdout: %s", resultC.Error, resultC.Stderr, resultC.Stdout)
			}

			// Add Job D: 04-deploy.md (depends on C)
			jobD := ctx.Bin("plan", "add", "job-mgmt-plan", "--type", "shell", "--title", "Deploy", "-p", "echo 'deploy'", "-d", "03-run-tests.md")
			jobD.Dir(projectDir)
			if result := jobD.Run(); result.Error != nil {
				return fmt.Errorf("failed to add Job D: %w", result.Error)
			}

			return nil
		}),

		harness.NewStep("Test job rename and cascade update", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			jobB_old_path := filepath.Join(planPath, "02-build.md")
			jobC_path := filepath.Join(planPath, "03-run-tests.md")

			// Rename 02-build.md
			cmd := ctx.Bin("plan", "jobs", "rename", jobB_old_path, "Build Artifacts")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Verify old file is gone and new one exists
			jobB_new_filename := "02-build-artifacts.md"
			jobB_new_path := filepath.Join(planPath, jobB_new_filename)
			ctx.Set("jobB_new_path", jobB_new_path)
			if err := fs.AssertNotExists(jobB_old_path); err != nil {
				return err
			}
			if err := fs.AssertExists(jobB_new_path); err != nil {
				return err
			}

			// Verify title in new file
			if err := assert.YAMLField(jobB_new_path, "title", "Build Artifacts"); err != nil {
				return err
			}

			// Verify dependency in job C was updated
			jobC, err := orchestration.LoadJob(jobC_path)
			if err != nil {
				return err
			}
			if len(jobC.DependsOn) != 1 || jobC.DependsOn[0] != jobB_new_filename {
				return fmt.Errorf("dependency in 03-run-tests.md was not updated, got: %v", jobC.DependsOn)
			}

			return nil
		}),

		harness.NewStep("Test rename with special characters", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			jobA_old_path := filepath.Join(planPath, "01-setup.md")

			cmd := ctx.Bin("plan", "jobs", "rename", jobA_old_path, "Setup & Configure Environment")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Verify filename was sanitized (should remove ampersand)
			jobA_new_filename := "01-setup-configure-environment.md"
			jobA_new_path := filepath.Join(planPath, jobA_new_filename)
			if err := fs.AssertExists(jobA_new_path); err != nil {
				return err
			}

			// Verify old filename is gone
			if err := fs.AssertNotExists(jobA_old_path); err != nil {
				return err
			}

			// Verify title in new file contains the special character
			if err := assert.YAMLField(jobA_new_path, "title", "Setup & Configure Environment"); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Test rename with very long title", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			jobD_old_path := filepath.Join(planPath, "04-deploy.md")

			// Try a very long title
			longTitle := "This is a Very Long Title That Should Be Truncated To A Reasonable Length For The Filename But Still Stored In Full In The Frontmatter"
			cmd := ctx.Bin("plan", "jobs", "rename", jobD_old_path, longTitle)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Find the new file (we don't know exact name due to truncation)
			// But we know it should start with 04- and no longer be 04-deploy.md
			if err := fs.AssertNotExists(jobD_old_path); err != nil {
				return err
			}

			// Load plan to find the renamed job
			plan, err := orchestration.LoadPlan(planPath)
			if err != nil {
				return err
			}

			// Find job by checking if title matches
			var renamedJob *orchestration.Job
			for _, job := range plan.Jobs {
				if job.Title == longTitle {
					renamedJob = job
					break
				}
			}

			if renamedJob == nil {
				return fmt.Errorf("could not find renamed job with long title")
			}

			// Verify the full title is in frontmatter
			jobPath := filepath.Join(planPath, renamedJob.Filename)
			if err := assert.YAMLField(jobPath, "title", longTitle); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Test rename error on non-existent job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			nonExistentPath := filepath.Join(planPath, "99-does-not-exist.md")

			cmd := ctx.Bin("plan", "jobs", "rename", nonExistentPath, "New Title")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertFailure(); err != nil {
				return err
			}
			return assert.Contains(result.Stderr, "job not found")
		}),

		harness.NewStep("Test clearing dependencies", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			// Load plan to find the current deploy job (it was renamed in previous step)
			plan, err := orchestration.LoadPlan(planPath)
			if err != nil {
				return err
			}

			// Find the deploy job (starts with 04-)
			var deployJob *orchestration.Job
			for _, job := range plan.Jobs {
				if job.Filename[:3] == "04-" {
					deployJob = job
					break
				}
			}

			if deployJob == nil {
				return fmt.Errorf("could not find deploy job")
			}

			deployJobPath := filepath.Join(planPath, deployJob.Filename)

			// Clear dependencies
			cmd := ctx.Bin("plan", "jobs", "update-deps", deployJobPath)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Load job and verify dependencies are empty
			updatedJob, err := orchestration.LoadJob(deployJobPath)
			if err != nil {
				return err
			}
			if len(updatedJob.DependsOn) != 0 {
				return fmt.Errorf("dependencies should be empty, got: %v", updatedJob.DependsOn)
			}

			return nil
		}),

		harness.NewStep("Test setting multiple dependencies", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			// Load plan to find current job names
			plan, err := orchestration.LoadPlan(planPath)
			if err != nil {
				return err
			}

			// Find the jobs we need
			var setupJob, buildJob, deployJob *orchestration.Job
			for _, job := range plan.Jobs {
				if job.Filename[:3] == "01-" {
					setupJob = job
				} else if job.Filename[:3] == "02-" {
					buildJob = job
				} else if job.Filename[:3] == "04-" {
					deployJob = job
				}
			}

			if setupJob == nil || buildJob == nil || deployJob == nil {
				return fmt.Errorf("could not find required jobs")
			}

			deployJobPath := filepath.Join(planPath, deployJob.Filename)

			// Set multiple dependencies
			cmd := ctx.Bin("plan", "jobs", "update-deps", deployJobPath, setupJob.Filename, buildJob.Filename)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Load job and verify dependencies
			updatedJob, err := orchestration.LoadJob(deployJobPath)
			if err != nil {
				return err
			}
			if len(updatedJob.DependsOn) != 2 {
				return fmt.Errorf("expected 2 dependencies, got: %v", updatedJob.DependsOn)
			}
			if updatedJob.DependsOn[0] != setupJob.Filename || updatedJob.DependsOn[1] != buildJob.Filename {
				return fmt.Errorf("dependencies were not set correctly, got: %v", updatedJob.DependsOn)
			}

			return nil
		}),


		harness.NewStep("Test renaming a job that other jobs depend on", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			// Load plan to find current state
			plan, err := orchestration.LoadPlan(planPath)
			if err != nil {
				return err
			}

			// Find the setup job (currently has a long name from earlier rename)
			var setupJob, buildJob, deployJob *orchestration.Job
			for _, job := range plan.Jobs {
				if job.Filename[:3] == "01-" {
					setupJob = job
				} else if job.Filename[:3] == "02-" {
					buildJob = job
				} else if job.Filename[:3] == "04-" {
					deployJob = job
				}
			}

			if setupJob == nil || buildJob == nil || deployJob == nil {
				return fmt.Errorf("could not find required jobs")
			}

			setupJobPath := filepath.Join(planPath, setupJob.Filename)

			// Rename setup job (which deploy job now depends on)
			cmd := ctx.Bin("plan", "jobs", "rename", setupJobPath, "Initial Setup")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Verify new filename
			newSetupFilename := "01-initial-setup.md"
			newSetupPath := filepath.Join(planPath, newSetupFilename)
			if err := fs.AssertExists(newSetupPath); err != nil {
				return err
			}

			// Reload deploy job and verify its dependencies were updated
			deployJobPath := filepath.Join(planPath, deployJob.Filename)
			updatedDeployJob, err := orchestration.LoadJob(deployJobPath)
			if err != nil {
				return err
			}

			// Deploy job should now reference the new setup filename
			found := false
			for _, dep := range updatedDeployJob.DependsOn {
				if dep == newSetupFilename {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("deploy job dependencies were not updated after setup rename, got: %v", updatedDeployJob.DependsOn)
			}

			return nil
		}),

		harness.NewStep("Test adding dependency to job that creates no cycle", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			// Load plan
			plan, err := orchestration.LoadPlan(planPath)
			if err != nil {
				return err
			}

			// Find run-tests job (03-run-tests.md)
			var testJob, setupJob *orchestration.Job
			for _, job := range plan.Jobs {
				if job.Filename[:3] == "03-" {
					testJob = job
				} else if job.Filename[:3] == "01-" {
					setupJob = job
				}
			}

			if testJob == nil || setupJob == nil {
				return fmt.Errorf("could not find required jobs")
			}

			testJobPath := filepath.Join(planPath, testJob.Filename)

			// Add setup as a dependency to run-tests (in addition to existing build dependency)
			// Run-tests currently depends on build, now will depend on both setup and build
			cmd := ctx.Bin("plan", "jobs", "update-deps", testJobPath, setupJob.Filename, "02-build-artifacts.md")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Verify the run-tests job now has both dependencies
			updatedTestJob, err := orchestration.LoadJob(testJobPath)
			if err != nil {
				return err
			}
			if len(updatedTestJob.DependsOn) != 2 {
				return fmt.Errorf("expected 2 dependencies, got: %v", updatedTestJob.DependsOn)
			}

			return nil
		}),
	},
)
