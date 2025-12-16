package scenarios

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-tend/pkg/assert"
	"github.com/mattsolo1/grove-tend/pkg/fs"
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
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "failure-project")
			if err != nil {
				return err
			}

			// Initialize the plan
			initCmd := ctx.Bin("plan", "init", "failure-plan")
			initCmd.Dir(projectDir)
			if result := initCmd.Run(); result.Error != nil {
				return fmt.Errorf("plan init failed: %w", result.Error)
			}

			// Add jobs to the plan
			planPath := filepath.Join(notebooksRoot, "workspaces", "failure-project", "plans", "failure-plan")
			ctx.Set("plan_path", planPath)

			// Job A: Succeeds
			jobA := ctx.Bin("plan", "add", "failure-plan", "--type", "shell", "--title", "setup", "-p", "echo 'setup complete' > setup.txt")
			jobA.Dir(projectDir)
			if result := jobA.Run(); result.Error != nil {
				return fmt.Errorf("failed to add job A: %w", result.Error)
			}

			// Job B: Fails (depends on A)
			jobB := ctx.Bin("plan", "add", "failure-plan", "--type", "shell", "--title", "main-task", "-p", "echo 'task failed' && exit 1", "-d", "01-setup.md")
			jobB.Dir(projectDir)
			if result := jobB.Run(); result.Error != nil {
				return fmt.Errorf("failed to add job B: %w", result.Error)
			}

			// Job C: Depends on B
			jobC := ctx.Bin("plan", "add", "failure-plan", "--type", "shell", "--title", "cleanup", "-p", "echo 'cleanup complete'", "-d", "02-main-task.md")
			jobC.Dir(projectDir)
			if result := jobC.Run(); result.Error != nil {
				return fmt.Errorf("failed to add job C: %w", result.Error)
			}

			// Job D: Independent job that should succeed
			jobD := ctx.Bin("plan", "add", "failure-plan", "--type", "shell", "--title", "independent-task", "-p", "echo 'independent task complete'")
			jobD.Dir(projectDir)
			if result := jobD.Run(); result.Error != nil {
				return fmt.Errorf("failed to add job D: %w", result.Error)
			}

			return nil
		}),

		// Step 2: Run the plan and verify the failure state.
		harness.NewStep("Run plan and verify failure", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			cmd := ctx.Bin("plan", "run", "--all", "--yes")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// The command itself should fail because one of its jobs failed.
			if err := result.AssertFailure(); err != nil {
				return fmt.Errorf("expected plan run to fail, but it succeeded: %w", err)
			}

			// Use `flow plan status --format json` to get detailed job statuses.
			statusCmd := ctx.Bin("plan", "status", planPath, "--format", "json")
			statusCmd.Dir(projectDir)
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

			cmd := ctx.Bin("plan", "run", "--all", "--yes")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("expected plan run to succeed after fix, but it failed: %w", err)
			}

			// Use `flow plan status --format json` again to verify final statuses.
			statusCmd := ctx.Bin("plan", "status", planPath, "--format", "json")
			statusCmd.Dir(projectDir)
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

// FailedJobRerunnableScenario tests that jobs in failed status can be re-run directly.
var FailedJobRerunnableScenario = harness.NewScenario(
	"failed-job-rerunnable",
	"Tests that failed jobs can be re-run directly without manual status reset.",
	[]string{"core", "orchestration", "failure", "rerun"},
	[]harness.Step{
		// Step 1: Set up a plan with a job that will fail.
		harness.NewStep("Setup plan with a failing job", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "rerun-project")
			if err != nil {
				return err
			}

			// Initialize the plan
			initCmd := ctx.Bin("plan", "init", "rerun-plan")
			initCmd.Dir(projectDir)
			if result := initCmd.Run(); result.Error != nil {
				return fmt.Errorf("plan init failed: %w", result.Error)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "rerun-project", "plans", "rerun-plan")
			ctx.Set("plan_path", planPath)

			// Add a failing job
			jobA := ctx.Bin("plan", "add", "rerun-plan", "--type", "shell", "--title", "failing-task", "-p", "echo 'this will fail' && exit 1")
			jobA.Dir(projectDir)
			if result := jobA.Run(); result.Error != nil {
				return fmt.Errorf("failed to add failing job: %w", result.Error)
			}

			return nil
		}),

		// Step 2: Run the job and verify it fails.
		harness.NewStep("Run job and verify failure", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			cmd := ctx.Bin("plan", "run", "--next", "--yes")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// The command should fail
			if err := result.AssertFailure(); err != nil {
				return fmt.Errorf("expected job to fail, but it succeeded: %w", err)
			}

			// Verify job status is failed
			job, err := orchestration.LoadJob(filepath.Join(planPath, "01-failing-task.md"))
			if err != nil {
				return err
			}
			if job.Status != orchestration.JobStatusFailed {
				return fmt.Errorf("expected job status to be 'failed', but got '%s'", job.Status)
			}

			return nil
		}),

		// Step 3: Fix the job by editing its prompt (without changing status).
		harness.NewStep("Fix the job without changing status", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			jobPath := filepath.Join(planPath, "01-failing-task.md")

			content, err := fs.ReadString(jobPath)
			if err != nil {
				return err
			}

			// Replace the failing command with a successful one
			// Importantly, do NOT reset the status - leave it as 'failed'
			fixedContent := strings.Replace(content, "exit 1", "exit 0", 1)

			return fs.WriteString(jobPath, fixedContent)
		}),

		// Step 4: Verify the job status is still 'failed'.
		harness.NewStep("Verify job is still in failed status", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			job, err := orchestration.LoadJob(filepath.Join(planPath, "01-failing-task.md"))
			if err != nil {
				return err
			}
			if job.Status != orchestration.JobStatusFailed {
				return fmt.Errorf("expected job status to still be 'failed', but got '%s'", job.Status)
			}
			return nil
		}),

		// Step 5: Re-run the failed job by specifying its path and verify it succeeds.
		harness.NewStep("Re-run failed job and verify success", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			// Run the specific failed job by path
			jobPath := filepath.Join(planPath, "01-failing-task.md")
			cmd := ctx.Bin("plan", "run", jobPath, "--yes")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("expected job to succeed after fix, but it failed: %w", err)
			}

			// Verify job status is now completed
			job, err := orchestration.LoadJob(filepath.Join(planPath, "01-failing-task.md"))
			if err != nil {
				return err
			}
			if job.Status != orchestration.JobStatusCompleted {
				return fmt.Errorf("expected job status to be 'completed', but got '%s'", job.Status)
			}

			return nil
		}),

		// Step 6: Test that running a specific failed job by title works.
		harness.NewStep("Test running a specific failed job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			// Add another failing job
			jobB := ctx.Bin("plan", "add", "rerun-plan", "--type", "shell", "--title", "another-failing-task", "-p", "echo 'fail again' && exit 1")
			jobB.Dir(projectDir)
			if result := jobB.Run(); result.Error != nil {
				return fmt.Errorf("failed to add second failing job: %w", result.Error)
			}

			// Run it and let it fail
			runCmd := ctx.Bin("plan", "run", "--next", "--yes")
			runCmd.Dir(projectDir)
			runResult := runCmd.Run()
			ctx.ShowCommandOutput(runCmd.String(), runResult.Stdout, runResult.Stderr)
			if err := runResult.AssertFailure(); err != nil {
				return fmt.Errorf("expected second job to fail: %w", err)
			}

			// Fix it
			jobPath := filepath.Join(planPath, "02-another-failing-task.md")
			content, err := fs.ReadString(jobPath)
			if err != nil {
				return err
			}
			fixedContent := strings.Replace(content, "exit 1", "exit 0", 1)
			if err := fs.WriteString(jobPath, fixedContent); err != nil {
				return err
			}

			// Run the specific failed job by filename
			rerunCmd := ctx.Bin("plan", "run", jobPath, "--yes")
			rerunCmd.Dir(projectDir)
			rerunResult := rerunCmd.Run()
			ctx.ShowCommandOutput(rerunCmd.String(), rerunResult.Stdout, rerunResult.Stderr)
			if err := rerunResult.AssertSuccess(); err != nil {
				return fmt.Errorf("expected specific failed job to run successfully: %w", err)
			}

			// Verify status
			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return err
			}
			if job.Status != orchestration.JobStatusCompleted {
				return fmt.Errorf("expected job status to be 'completed', but got '%s'", job.Status)
			}

			return nil
		}),
	},
)
