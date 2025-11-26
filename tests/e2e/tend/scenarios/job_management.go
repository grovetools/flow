package scenarios

import (
	"fmt"
	"path/filepath"

	"github.com/mattsolo1/grove-core/config"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-tend/pkg/assert"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

var JobManagementScenario = harness.NewScenario(
	"job-management-commands",
	"Tests the `flow plan jobs` subcommands for renaming and updating dependencies.",
	[]string{"core", "plan", "jobs", "cli"},
	[]harness.Step{
		harness.NewStep("Setup plan with complex dependencies", func(ctx *harness.Context) error {
			// Standard setup for home dir and project dir
			homeDir := ctx.NewDir("home")
			ctx.Set("home_dir", homeDir)
			if err := fs.CreateDir(homeDir); err != nil {
				return err
			}

			projectDir := ctx.NewDir("job-mgmt-project")
			ctx.Set("project_dir", projectDir)
			if err := fs.CreateDir(projectDir); err != nil {
				return err
			}
			if _, err := git.SetupTestRepo(projectDir); err != nil {
				return err
			}

			// Configure a centralized notebook location
			notebooksRoot := filepath.Join(homeDir, "notebooks")
			configDir := filepath.Join(homeDir, ".config", "grove")
			notebookConfig := &config.NotebooksConfig{
				Definitions: map[string]*config.Notebook{"default": {RootDir: notebooksRoot}},
				Rules:       &config.NotebookRules{Default: "default"},
			}
			globalCfg := &config.Config{Version: "1.0", Notebooks: notebookConfig}
			if err := fs.WriteGroveConfig(configDir, globalCfg); err != nil {
				return err
			}

			// Initialize the plan
			initCmd := ctx.Command("flow", "plan", "init", "job-mgmt-plan")
			initCmd.Dir(projectDir).Env("HOME=" + homeDir)
			if result := initCmd.Run(); result.Error != nil {
				return fmt.Errorf("plan init failed: %w", result.Error)
			}
			planPath := filepath.Join(notebooksRoot, "workspaces", "job-mgmt-project", "plans", "job-mgmt-plan")
			ctx.Set("plan_path", planPath)

			// Add Job A: 01-setup.md
			jobA := ctx.Command("flow", "plan", "add", "job-mgmt-plan", "--type", "shell", "--title", "Setup", "-p", "echo 'setup'")
			jobA.Dir(projectDir).Env("HOME=" + homeDir)
			resultA := jobA.Run()
			ctx.ShowCommandOutput(jobA.String(), resultA.Stdout, resultA.Stderr)
			if resultA.Error != nil {
				return fmt.Errorf("failed to add Job A: %w\nStderr: %s", resultA.Error, resultA.Stderr)
			}

			// Add Job B: 02-build.md (depends on A)
			jobB := ctx.Command("flow", "plan", "add", "job-mgmt-plan", "--type", "shell", "--title", "Build", "-p", "echo 'build'", "-d", "01-setup.md")
			jobB.Dir(projectDir).Env("HOME=" + homeDir)
			resultB := jobB.Run()
			ctx.ShowCommandOutput(jobB.String(), resultB.Stdout, resultB.Stderr)
			if resultB.Error != nil {
				return fmt.Errorf("failed to add Job B: %w\nStderr: %s", resultB.Error, resultB.Stderr)
			}

			// Add Job C: 03-run-tests.md (depends on B)
			jobC := ctx.Command("flow", "plan", "add", "job-mgmt-plan", "--type", "shell", "--title", "Run Tests", "-p", "echo 'test'", "-d", "02-build.md")
			jobC.Dir(projectDir).Env("HOME=" + homeDir)
			resultC := jobC.Run()
			ctx.ShowCommandOutput(jobC.String(), resultC.Stdout, resultC.Stderr)
			if resultC.Error != nil {
				return fmt.Errorf("failed to add Job C: %w\nStderr: %s", resultC.Error, resultC.Stderr)
			}

			// Manually add prompt_source to job C to test rename update behavior
			jobCPath := filepath.Join(planPath, "03-run-tests.md")
			jobCData, err := orchestration.LoadJob(jobCPath)
			if err != nil {
				return fmt.Errorf("failed to load job C: %w", err)
			}
			jobCData.PromptSource = []string{"01-setup.md"}
			if err := orchestration.SaveJob(jobCData); err != nil {
				return fmt.Errorf("failed to save job C: %w", err)
			}

			// Add Job D: 04-deploy.md (depends on C)
			jobD := ctx.Command("flow", "plan", "add", "job-mgmt-plan", "--type", "shell", "--title", "Deploy", "-p", "echo 'deploy'", "-d", "03-run-tests.md")
			jobD.Dir(projectDir).Env("HOME=" + homeDir)
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
			cmd := ctx.Command("flow", "plan", "jobs", "rename", jobB_old_path, "Build Artifacts")
			cmd.Dir(projectDir).Env("HOME=" + ctx.GetString("home_dir"))
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

		harness.NewStep("Test rename with special characters and prompt_source update", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			jobA_old_path := filepath.Join(planPath, "01-setup.md")
			jobC_path := filepath.Join(planPath, "03-run-tests.md")

			cmd := ctx.Command("flow", "plan", "jobs", "rename", jobA_old_path, "Setup & Configure Environment")
			cmd.Dir(projectDir).Env("HOME=" + ctx.GetString("home_dir"))
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

			// Verify prompt_source in job C was updated
			jobC, err := orchestration.LoadJob(jobC_path)
			if err != nil {
				return err
			}
			if len(jobC.PromptSource) != 1 || jobC.PromptSource[0] != jobA_new_filename {
				return fmt.Errorf("prompt_source in 03-run-tests.md was not updated, got: %v", jobC.PromptSource)
			}

			return nil
		}),

		harness.NewStep("Test rename with very long title", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			jobD_old_path := filepath.Join(planPath, "04-deploy.md")

			// Try a very long title
			longTitle := "This is a Very Long Title That Should Be Truncated To A Reasonable Length For The Filename But Still Stored In Full In The Frontmatter"
			cmd := ctx.Command("flow", "plan", "jobs", "rename", jobD_old_path, longTitle)
			cmd.Dir(projectDir).Env("HOME=" + ctx.GetString("home_dir"))
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

			cmd := ctx.Command("flow", "plan", "jobs", "rename", nonExistentPath, "New Title")
			cmd.Dir(projectDir).Env("HOME=" + ctx.GetString("home_dir"))
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
			cmd := ctx.Command("flow", "plan", "jobs", "update-deps", deployJobPath)
			cmd.Dir(projectDir).Env("HOME=" + ctx.GetString("home_dir"))
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
			cmd := ctx.Command("flow", "plan", "jobs", "update-deps", deployJobPath, setupJob.Filename, buildJob.Filename)
			cmd.Dir(projectDir).Env("HOME=" + ctx.GetString("home_dir"))
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

		harness.NewStep("Test update-deps error on invalid dependency", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			// Load plan to find deploy job
			plan, err := orchestration.LoadPlan(planPath)
			if err != nil {
				return err
			}

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

			cmd := ctx.Command("flow", "plan", "jobs", "update-deps", deployJobPath, "99-does-not-exist.md")
			cmd.Dir(projectDir).Env("HOME=" + ctx.GetString("home_dir"))
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertFailure(); err != nil {
				return err
			}
			return assert.Contains(result.Stderr, "dependency not found")
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
			cmd := ctx.Command("flow", "plan", "jobs", "rename", setupJobPath, "Initial Setup")
			cmd.Dir(projectDir).Env("HOME=" + ctx.GetString("home_dir"))
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
			cmd := ctx.Command("flow", "plan", "jobs", "update-deps", testJobPath, setupJob.Filename, "02-build-artifacts.md")
			cmd.Dir(projectDir).Env("HOME=" + ctx.GetString("home_dir"))
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
