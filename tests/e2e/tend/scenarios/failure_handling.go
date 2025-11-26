package scenarios

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-core/config"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-tend/pkg/assert"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// JobFailureAndRecoveryScenario tests how the orchestrator handles job failures and recovery.
var JobFailureAndRecoveryScenario = harness.NewScenario(
	"job-failure-and-recovery",
	"Tests orchestrator resilience to job failures and subsequent recovery.",
	[]string{"core", "orchestration", "failure"},
	[]harness.Step{
		// Step 1: Set up the sandboxed environment with a git repo and a plan.
		harness.NewStep("Setup plan with a failing job", func(ctx *harness.Context) error {
			// Standard setup for home dir and project dir
			homeDir := ctx.NewDir("home")
			ctx.Set("home_dir", homeDir)
			if err := fs.CreateDir(homeDir); err != nil {
				return err
			}

			projectDir := ctx.NewDir("failure-project")
			ctx.Set("project_dir", projectDir)
			if err := fs.CreateDir(projectDir); err != nil {
				return err
			}
			if _, err := git.SetupTestRepo(projectDir); err != nil {
				return err
			}

			// Configure a centralized notebook location in the sandboxed global config
			notebooksRoot := filepath.Join(homeDir, "notebooks")
			configDir := filepath.Join(homeDir, ".config", "grove")
			notebookConfig := &config.NotebooksConfig{
				Definitions: map[string]*config.Notebook{
					"default": {RootDir: notebooksRoot},
				},
				Rules: &config.NotebookRules{Default: "default"},
			}
			globalCfg := &config.Config{Version: "1.0", Notebooks: notebookConfig}
			if err := fs.WriteGroveConfig(configDir, globalCfg); err != nil {
				return err
			}

			// Initialize the plan
			initCmd := ctx.Command("flow", "plan", "init", "failure-plan")
			initCmd.Dir(projectDir).Env("HOME=" + homeDir)
			if result := initCmd.Run(); result.Error != nil {
				return fmt.Errorf("plan init failed: %w", result.Error)
			}

			// Add jobs to the plan
			planPath := filepath.Join(notebooksRoot, "workspaces", "failure-project", "plans", "failure-plan")
			ctx.Set("plan_path", planPath)

			// Job A: Succeeds
			jobA := ctx.Command("flow", "plan", "add", "failure-plan", "--type", "shell", "--title", "setup", "-p", "echo 'setup complete' > setup.txt")
			jobA.Dir(projectDir).Env("HOME=" + homeDir)
			if result := jobA.Run(); result.Error != nil {
				return fmt.Errorf("failed to add job A: %w", result.Error)
			}

			// Job B: Fails (depends on A)
			jobB := ctx.Command("flow", "plan", "add", "failure-plan", "--type", "shell", "--title", "main-task", "-p", "echo 'task failed' && exit 1", "-d", "01-setup.md")
			jobB.Dir(projectDir).Env("HOME=" + homeDir)
			if result := jobB.Run(); result.Error != nil {
				return fmt.Errorf("failed to add job B: %w", result.Error)
			}

			// Job C: Depends on B
			jobC := ctx.Command("flow", "plan", "add", "failure-plan", "--type", "shell", "--title", "cleanup", "-p", "echo 'cleanup complete'", "-d", "02-main-task.md")
			jobC.Dir(projectDir).Env("HOME=" + homeDir)
			if result := jobC.Run(); result.Error != nil {
				return fmt.Errorf("failed to add job C: %w", result.Error)
			}

			// Job D: Independent job that should succeed
			jobD := ctx.Command("flow", "plan", "add", "failure-plan", "--type", "shell", "--title", "independent-task", "-p", "echo 'independent task complete'")
			jobD.Dir(projectDir).Env("HOME=" + homeDir)
			if result := jobD.Run(); result.Error != nil {
				return fmt.Errorf("failed to add job D: %w", result.Error)
			}

			return nil
		}),

		// Step 2: Run the plan and verify the failure state.
		harness.NewStep("Run plan and verify failure", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			cmd := ctx.Command("flow", "plan", "run", "--all", "--yes")
			cmd.Dir(projectDir).Env("HOME=" + ctx.GetString("home_dir"))
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// The command itself should fail because one of its jobs failed.
			if err := result.AssertFailure(); err != nil {
				return fmt.Errorf("expected plan run to fail, but it succeeded: %w", err)
			}

			// Use `flow plan status --format json` to get detailed job statuses.
			statusCmd := ctx.Command("flow", "plan", "status", planPath, "--format", "json")
			statusCmd.Dir(projectDir).Env("HOME=" + ctx.GetString("home_dir"))
			statusResult := statusCmd.Run()
			if statusResult.Error != nil {
				return fmt.Errorf("failed to get plan status: %w", statusResult.Error)
			}

			var status struct {
				Jobs []*orchestration.Job `json:"jobs"`
			}
			if err := json.Unmarshal([]byte(statusResult.Stdout), &status); err != nil {
				return fmt.Errorf("failed to parse plan status JSON: %w", err)
			}

			jobStatuses := make(map[string]orchestration.JobStatus)
			for _, job := range status.Jobs {
				jobStatuses[job.Title] = job.Status
			}

			// Assert statuses
			if err := assert.Equal(orchestration.JobStatusCompleted, jobStatuses["setup"]); err != nil {
				return err
			}
			if err := assert.Equal(orchestration.JobStatusFailed, jobStatuses["main-task"]); err != nil {
				return err
			}
			if err := assert.Equal(orchestration.JobStatusPending, jobStatuses["cleanup"]); err != nil { // Blocked jobs are still pending
				return err
			}
			if err := assert.Equal(orchestration.JobStatusCompleted, jobStatuses["independent-task"]); err != nil { // Independent job should pass
				return err
			}

			return nil
		}),

		// Step 3: "Fix" the failing job by editing its file.
		harness.NewStep("Fix the failing job", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			jobBPath := filepath.Join(planPath, "02-main-task.md")

			content, err := fs.ReadString(jobBPath)
			if err != nil {
				return err
			}

			// Replace "exit 1" with "exit 0"
			fixedContent := strings.Replace(content, "exit 1", "exit 0", 1)

			// Also reset the status from 'failed' back to 'pending' to allow re-running.
			fixedContent = strings.Replace(fixedContent, "status: failed", "status: pending", 1)

			return fs.WriteString(jobBPath, fixedContent)
		}),

		// Step 4: Re-run the plan and verify that all jobs now succeed.
		harness.NewStep("Re-run plan and verify success", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			cmd := ctx.Command("flow", "plan", "run", "--all", "--yes")
			cmd.Dir(projectDir).Env("HOME=" + ctx.GetString("home_dir"))
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("expected plan run to succeed after fix, but it failed: %w", err)
			}

			// Use `flow plan status --format json` again to verify final statuses.
			statusCmd := ctx.Command("flow", "plan", "status", planPath, "--format", "json")
			statusCmd.Dir(projectDir).Env("HOME=" + ctx.GetString("home_dir"))
			statusResult := statusCmd.Run()
			if statusResult.Error != nil {
				return fmt.Errorf("failed to get final plan status: %w", statusResult.Error)
			}

			var status struct {
				Jobs []*orchestration.Job `json:"jobs"`
			}
			if err := json.Unmarshal([]byte(statusResult.Stdout), &status); err != nil {
				return fmt.Errorf("failed to parse final plan status JSON: %w", err)
			}

			for _, job := range status.Jobs {
				if job.Status != orchestration.JobStatusCompleted {
					return fmt.Errorf("expected job '%s' to be 'completed', but got '%s'", job.Title, job.Status)
				}
			}

			return nil
		}),
	},
)
