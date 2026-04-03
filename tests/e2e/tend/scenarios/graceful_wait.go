package scenarios

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/grovetools/core/config"
	"github.com/grovetools/tend/pkg/assert"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"
)

// GracefulWaitRunNextScenario verifies that `flow plan run -y` (runNextJobs mode)
// exits gracefully with code 0 when interactive jobs are running but no new jobs are runnable.
var GracefulWaitRunNextScenario = harness.NewScenario(
	"graceful-wait-run-next",
	"Tests that plan run -y exits gracefully (exit 0) when interactive jobs are running",
	[]string{"graceful-wait", "interactive", "run-next"},
	[]harness.Step{
		harness.NewStep("Setup environment", func(ctx *harness.Context) error {
			projectName := "graceful-wait-next-project"
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, projectName)
			if err != nil {
				return err
			}

			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Graceful Wait Test\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			groveConfig := &config.Config{
				Name:    projectName,
				Version: "1.0",
				Extensions: map[string]interface{}{
					"flow": map[string]interface{}{
						"interactive_provider": "claude",
					},
				},
			}
			if err := fs.WriteGroveConfig(projectDir, groveConfig); err != nil {
				return err
			}

			ctx.Set("notebooks_root", notebooksRoot)
			ctx.Set("project_name", projectName)
			return nil
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "claude"},
			harness.Mock{CommandName: "tmux"},
		),

		harness.NewStep("Initialize plan and add jobs", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			projectName := ctx.GetString("project_name")

			planName := "graceful-wait-plan"
			cmd := ctx.Bin("plan", "init", planName, "--worktree")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", planName)
			ctx.Set("plan_path", planPath)
			ctx.Set("plan_name", planName)

			// Add interactive_agent job (no dependencies)
			cmd = ctx.Bin("plan", "add", planName,
				"--type", "interactive_agent",
				"--title", "Interactive Job",
				"-p", "Test interactive agent")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}

			// Add followup job depending on interactive job
			cmd = ctx.Bin("plan", "add", planName,
				"--type", "shell",
				"--title", "Followup Job",
				"-p", "echo done",
				"--depends-on", "01-interactive-job.md")
			cmd.Dir(projectDir)
			result = cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Run interactive job to put it in running state", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			jobPath, err := findJobByPrefix(planPath, "01-")
			if err != nil {
				return err
			}
			ctx.Set("interactive_job_path", jobPath)

			cmd := ctx.Bin("plan", "run", jobPath)
			cmd.Dir(projectDir)
			cmd.Timeout(60 * time.Second)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("interactive job should launch successfully: %w", err)
			}

			return assert.YAMLField(jobPath, "status", "running", "interactive job should be running")
		}),

		harness.NewStep("Run next jobs and verify graceful exit", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planName := ctx.GetString("plan_name")

			// Remove lock file if exists to allow run-next to proceed
			interactiveJobPath := ctx.GetString("interactive_job_path")
			fs.RemoveIfExists(interactiveJobPath + ".lock")

			// Run plan run -y (next mode, no specific target)
			cmd := ctx.Bin("plan", "run", planName, "-y")
			cmd.Dir(projectDir)
			cmd.Timeout(30 * time.Second)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			combined := result.Stdout + result.Stderr

			return ctx.Verify(func(v *verify.Collector) {
				v.True("exit code is 0", result.ExitCode == 0)
				v.Contains("shows info about running jobs", combined, "still running")
			})
		}),
	},
)

// GracefulWaitSingleJobScenario verifies that running a single interactive_agent job
// logs "Job running" instead of "Job completed".
var GracefulWaitSingleJobScenario = harness.NewScenario(
	"graceful-wait-single-job",
	"Tests that running a single interactive_agent job shows 'Job running' not 'Job completed'",
	[]string{"graceful-wait", "interactive", "single-job"},
	[]harness.Step{
		harness.NewStep("Setup environment", func(ctx *harness.Context) error {
			projectName := "graceful-wait-single-project"
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, projectName)
			if err != nil {
				return err
			}

			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Graceful Wait Single Job Test\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			groveConfig := &config.Config{
				Name:    projectName,
				Version: "1.0",
				Extensions: map[string]interface{}{
					"flow": map[string]interface{}{
						"interactive_provider": "claude",
					},
				},
			}
			if err := fs.WriteGroveConfig(projectDir, groveConfig); err != nil {
				return err
			}

			ctx.Set("notebooks_root", notebooksRoot)
			ctx.Set("project_name", projectName)
			return nil
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "claude"},
			harness.Mock{CommandName: "tmux"},
		),

		harness.NewStep("Initialize plan and add interactive job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			projectName := ctx.GetString("project_name")

			planName := "graceful-single-plan"
			cmd := ctx.Bin("plan", "init", planName, "--worktree")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", planName)
			ctx.Set("plan_path", planPath)
			ctx.Set("plan_name", planName)

			cmd = ctx.Bin("plan", "add", planName,
				"--type", "interactive_agent",
				"--title", "Interactive Job",
				"-p", "Test interactive agent single run")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Run single interactive job and verify output", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			jobPath, err := findJobByPrefix(planPath, "01-")
			if err != nil {
				return err
			}

			cmd := ctx.Bin("plan", "run", jobPath)
			cmd.Dir(projectDir)
			cmd.Timeout(60 * time.Second)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			combined := result.Stdout + result.Stderr

			return ctx.Verify(func(v *verify.Collector) {
				v.True("exit code is 0", result.ExitCode == 0)
				v.Contains("shows 'Job running' message", combined, "Job running")
				v.NotContains("does not show 'Job completed'", combined, "Job completed")
			})
		}),
	},
)
