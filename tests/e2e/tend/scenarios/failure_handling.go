package scenarios

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/tend/pkg/assert"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"
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

			// Use `flow plan status --json` to get detailed job statuses.
			statusCmd := ctx.Bin("plan", "status", planPath, "--json")
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

			// After UpdateJobMetadata rewrites frontmatter, the prompt may have
			// been moved to the body (YAML round-trip can strip quotes from values
			// containing special chars). Replace "exit 1" in both frontmatter and body.
			fixedContent := strings.Replace(content, "status: failed", "status: pending", 1)
			fixedContent = strings.ReplaceAll(fixedContent, "&& exit 1", "&& exit 0")

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

			// Use `flow plan status --json` again to verify final statuses.
			statusCmd := ctx.Bin("plan", "status", planPath, "--json")
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

			// Replace "exit 1" with "exit 0" everywhere (prompt may be in body
			// after YAML round-trip from UpdateJobMetadata).
			// Importantly, do NOT reset the status - leave it as 'failed'.
			fixedContent := strings.ReplaceAll(content, "&& exit 1", "&& exit 0")

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

			// Verify the auto-reset message was shown
			if err := assert.Contains(result.Stdout, "Auto-resetting job"); err != nil {
				return fmt.Errorf("expected auto-reset message in output: %w", err)
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
			// Replace "exit 1" everywhere (prompt may be in body after YAML round-trip)
			fixedContent := strings.ReplaceAll(content, "&& exit 1", "&& exit 0")
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

// ExplicitTargetStatusHandlingScenario tests edge cases when explicitly targeting
// jobs with various statuses: completed (skip), abandoned (auto-reset), mixed targets,
// and verifying --all does NOT auto-reset.
var ExplicitTargetStatusHandlingScenario = harness.NewScenario(
	"explicit-target-status-handling",
	"Tests completed-skip, abandoned-reset, mixed targets, and --all behavior.",
	[]string{"core", "orchestration", "failure", "rerun"},
	[]harness.Step{
		// Step 1: Set up a plan with jobs in diverse statuses.
		harness.NewStep("Setup plan with diverse job statuses", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "status-project")
			if err != nil {
				return err
			}

			// Initialize the plan
			initCmd := ctx.Bin("plan", "init", "status-plan")
			initCmd.Dir(projectDir)
			if result := initCmd.Run(); result.Error != nil {
				return fmt.Errorf("plan init failed: %w", result.Error)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "status-project", "plans", "status-plan")
			ctx.Set("plan_path", planPath)

			// Job A: will succeed → completed
			jobA := ctx.Bin("plan", "add", "status-plan", "--type", "shell", "--title", "job-a", "-p", "echo 'job a done'")
			jobA.Dir(projectDir)
			if result := jobA.Run(); result.Error != nil {
				return fmt.Errorf("failed to add job A: %w", result.Error)
			}

			// Job B: will fail → failed
			jobB := ctx.Bin("plan", "add", "status-plan", "--type", "shell", "--title", "job-b", "-p", "echo 'failing' && exit 1")
			jobB.Dir(projectDir)
			if result := jobB.Run(); result.Error != nil {
				return fmt.Errorf("failed to add job B: %w", result.Error)
			}

			// Job C: will be manually set to abandoned
			jobC := ctx.Bin("plan", "add", "status-plan", "--type", "shell", "--title", "job-c", "-p", "echo 'job c done'")
			jobC.Dir(projectDir)
			if result := jobC.Run(); result.Error != nil {
				return fmt.Errorf("failed to add job C: %w", result.Error)
			}

			// Job D: stays pending
			jobD := ctx.Bin("plan", "add", "status-plan", "--type", "shell", "--title", "job-d", "-p", "echo 'job d done'")
			jobD.Dir(projectDir)
			if result := jobD.Run(); result.Error != nil {
				return fmt.Errorf("failed to add job D: %w", result.Error)
			}

			// Run --all to get A completed and B failed (C and D will also run)
			runCmd := ctx.Bin("plan", "run", "--all", "--yes")
			runCmd.Dir(projectDir)
			result := runCmd.Run()
			ctx.ShowCommandOutput(runCmd.String(), result.Stdout, result.Stderr)

			// Verify Job A completed, Job B failed
			jobAObj, err := orchestration.LoadJob(filepath.Join(planPath, "01-job-a.md"))
			if err != nil {
				return fmt.Errorf("loading job A: %w", err)
			}
			if jobAObj.Status != orchestration.JobStatusCompleted {
				return fmt.Errorf("expected job A completed, got %s", jobAObj.Status)
			}

			jobBObj, err := orchestration.LoadJob(filepath.Join(planPath, "02-job-b.md"))
			if err != nil {
				return fmt.Errorf("loading job B: %w", err)
			}
			if jobBObj.Status != orchestration.JobStatusFailed {
				return fmt.Errorf("expected job B failed, got %s", jobBObj.Status)
			}

			// Manually set Job C to abandoned status
			jobCPath := filepath.Join(planPath, "03-job-c.md")
			content, err := fs.ReadString(jobCPath)
			if err != nil {
				return fmt.Errorf("reading job C: %w", err)
			}
			content = strings.Replace(content, "status: completed", "status: abandoned", 1)
			content = strings.Replace(content, "status: pending", "status: abandoned", 1)
			if err := fs.WriteString(jobCPath, content); err != nil {
				return fmt.Errorf("writing job C: %w", err)
			}

			return nil
		}),

		// Step 2: Target a completed job → should skip with warning.
		harness.NewStep("Target completed job is skipped", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			cmd := ctx.Bin("plan", "run", filepath.Join(planPath, "01-job-a.md"), "--yes")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// Should succeed (skipping is not an error)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("expected success when targeting completed job: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("skip warning shown", result.Stdout, "Skipping job")
				v.Contains("already completed reason", result.Stdout, "already completed")
				v.Contains("no valid jobs message", result.Stdout, "No valid jobs to run.")
			})
		}),

		// Step 3: Target an abandoned job → should auto-reset and run.
		harness.NewStep("Target abandoned job is auto-reset", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			cmd := ctx.Bin("plan", "run", filepath.Join(planPath, "03-job-c.md"), "--yes")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("expected abandoned job to run after auto-reset: %w", err)
			}

			// Verify auto-reset message
			if err := assert.Contains(result.Stdout, "Auto-resetting job"); err != nil {
				return fmt.Errorf("expected auto-reset message: %w", err)
			}
			if err := assert.Contains(result.Stdout, "from abandoned to pending"); err != nil {
				return fmt.Errorf("expected 'from abandoned to pending' in output: %w", err)
			}

			// Verify on-disk status is now completed
			job, err := orchestration.LoadJob(filepath.Join(planPath, "03-job-c.md"))
			if err != nil {
				return err
			}
			if job.Status != orchestration.JobStatusCompleted {
				return fmt.Errorf("expected job C completed after run, got %s", job.Status)
			}

			return nil
		}),

		// Step 4: Target mixed jobs (completed + failed) → skip completed, auto-reset failed.
		harness.NewStep("Mixed targets: skip completed, reset failed", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			// Fix job B so it will succeed when retried
			jobBPath := filepath.Join(planPath, "02-job-b.md")
			content, err := fs.ReadString(jobBPath)
			if err != nil {
				return err
			}
			fixedContent := strings.Replace(content, "exit 1", "exit 0", 1)
			if err := fs.WriteString(jobBPath, fixedContent); err != nil {
				return err
			}

			cmd := ctx.Bin("plan", "run", filepath.Join(planPath, "01-job-a.md"), filepath.Join(planPath, "02-job-b.md"), "--yes")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("expected mixed target run to succeed: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("completed job skipped", result.Stdout, "Skipping job")
				v.Contains("failed job auto-reset", result.Stdout, "Auto-resetting job")
			})
		}),

		// Step 5: Verify --all does NOT auto-reset failed jobs.
		harness.NewStep("--all flag does not auto-reset failed jobs", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			// Add a new job that will fail
			jobE := ctx.Bin("plan", "add", "status-plan", "--type", "shell", "--title", "job-e-fail", "-p", "echo 'will fail' && exit 1")
			jobE.Dir(projectDir)
			if result := jobE.Run(); result.Error != nil {
				return fmt.Errorf("failed to add job E: %w", result.Error)
			}

			// Run --all to execute pending jobs (job E will fail)
			runCmd := ctx.Bin("plan", "run", "--all", "--yes")
			runCmd.Dir(projectDir)
			result := runCmd.Run()
			ctx.ShowCommandOutput(runCmd.String(), result.Stdout, result.Stderr)

			// Should fail because job E fails
			if err := result.AssertFailure(); err != nil {
				return fmt.Errorf("expected plan run --all to fail due to job E: %w", err)
			}

			// Verify no auto-reset message (--all doesn't target specific jobs)
			if err := assert.NotContains(result.Stdout, "Auto-resetting job"); err != nil {
				return fmt.Errorf("--all should not auto-reset jobs: %w", err)
			}

			// Verify job E is in failed status on disk
			jobEPath, err := findJobByPrefix(planPath, "05-job-e")
			if err != nil {
				return err
			}
			job, err := orchestration.LoadJob(jobEPath)
			if err != nil {
				return err
			}
			if job.Status != orchestration.JobStatusFailed {
				return fmt.Errorf("expected job E to remain failed, got %s", job.Status)
			}

			return nil
		}),

		// Step 6: Verify disk persistence of auto-reset.
		harness.NewStep("Verify auto-reset persists to disk", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// Find job E and fix it so it won't fail again
			jobEPath, err := findJobByPrefix(planPath, "05-job-e")
			if err != nil {
				return err
			}

			content, err := fs.ReadString(jobEPath)
			if err != nil {
				return err
			}
			fixedContent := strings.Replace(content, "exit 1", "exit 0", 1)
			if err := fs.WriteString(jobEPath, fixedContent); err != nil {
				return err
			}

			// Before running, verify it's still failed on disk
			job, err := orchestration.LoadJob(jobEPath)
			if err != nil {
				return err
			}
			if err := assert.Equal(orchestration.JobStatusFailed, job.Status); err != nil {
				return fmt.Errorf("precondition: job E should be failed: %w", err)
			}

			// Run targeting job E → should auto-reset
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Bin("plan", "run", jobEPath, "--yes")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("expected auto-reset + run to succeed: %w", err)
			}

			// Verify disk status is now completed (auto-reset persisted, then ran successfully)
			job, err = orchestration.LoadJob(jobEPath)
			if err != nil {
				return err
			}
			if job.Status != orchestration.JobStatusCompleted {
				return fmt.Errorf("expected job E completed after auto-reset and run, got %s", job.Status)
			}

			return nil
		}),
	},
)


// StatusErrorDetailsScenario tests that failed jobs include last_error and log_path in JSON status output.
var StatusErrorDetailsScenario = harness.NewScenario(
	"status-error-details",
	"Tests that plan status --json includes last_error and log_path for failed jobs.",
	[]string{"core", "orchestration", "failure", "status", "json"},
	[]harness.Step{
		// Step 1: Setup a plan with a successful and a failing job.
		harness.NewStep("Setup plan with success and failure jobs", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "error-details-project")
			if err != nil {
				return err
			}

			initCmd := ctx.Bin("plan", "init", "error-details-plan")
			initCmd.Dir(projectDir)
			if result := initCmd.Run(); result.Error != nil {
				return fmt.Errorf("plan init failed: %w", result.Error)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "error-details-project", "plans", "error-details-plan")
			ctx.Set("plan_path", planPath)

			// Job A: Succeeds
			jobA := ctx.Bin("plan", "add", "error-details-plan", "--type", "shell", "--title", "success-job", "-p", "echo 'all good'")
			jobA.Dir(projectDir)
			if result := jobA.Run(); result.Error != nil {
				return fmt.Errorf("failed to add success job: %w", result.Error)
			}

			// Job B: Fails
			jobB := ctx.Bin("plan", "add", "error-details-plan", "--type", "shell", "--title", "failing-job", "-p", "echo 'about to fail' && exit 1")
			jobB.Dir(projectDir)
			if result := jobB.Run(); result.Error != nil {
				return fmt.Errorf("failed to add failing job: %w", result.Error)
			}

			return nil
		}),

		// Step 2: Run the plan (expecting failure).
		harness.NewStep("Run plan and expect failure", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "run", "--all", "--yes")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertFailure(); err != nil {
				return fmt.Errorf("expected plan run to fail: %w", err)
			}

			return nil
		}),

		// Step 3: Verify JSON status output contains error details for failing job and not for success.
		harness.NewStep("Verify JSON status has error details", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			statusCmd := ctx.Bin("plan", "status", planPath, "--json")
			statusCmd.Dir(projectDir)
			result := statusCmd.Run()
			ctx.ShowCommandOutput(statusCmd.String(), result.Stdout, result.Stderr)

			if result.Error != nil {
				return fmt.Errorf("status command failed: %w", result.Error)
			}

			var status struct {
				Jobs []struct {
					Title     string `json:"title"`
					Status    string `json:"status"`
					LastError string `json:"last_error"`
					LogPath   string `json:"log_path"`
				} `json:"jobs"`
			}
			if err := json.Unmarshal([]byte(result.Stdout), &status); err != nil {
				return fmt.Errorf("failed to parse JSON: %w", err)
			}

			if err := assert.Equal(2, len(status.Jobs)); err != nil {
				return fmt.Errorf("expected 2 jobs: %w", err)
			}

			// Build a map for easier lookup
			jobsByTitle := make(map[string]struct {
				Status    string
				LastError string
				LogPath   string
			})
			for _, j := range status.Jobs {
				jobsByTitle[j.Title] = struct {
					Status    string
					LastError string
					LogPath   string
				}{j.Status, j.LastError, j.LogPath}
			}

			successJob := jobsByTitle["success-job"]
			failingJob := jobsByTitle["failing-job"]

			return ctx.Verify(func(v *verify.Collector) {
				// Successful job should not have error fields
				v.Equal("success job has completed status", string(orchestration.JobStatusCompleted), successJob.Status)
				v.Equal("success job has no last_error", "", successJob.LastError)
				v.Equal("success job has no log_path", "", successJob.LogPath)

				// Failing job should have error details
				v.Equal("failing job has failed status", string(orchestration.JobStatusFailed), failingJob.Status)
				v.Contains("failing job last_error mentions exit status", failingJob.LastError, "exit status 1")
				v.NotEqual("failing job has log_path", "", failingJob.LogPath)
			})
		}),

		// Step 4: Verify the log file referenced by log_path actually exists.
		harness.NewStep("Verify log file exists on disk", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			statusCmd := ctx.Bin("plan", "status", planPath, "--json")
			statusCmd.Dir(projectDir)
			result := statusCmd.Run()

			if result.Error != nil {
				return fmt.Errorf("status command failed: %w", result.Error)
			}

			var status struct {
				Jobs []struct {
					Title   string `json:"title"`
					Status  string `json:"status"`
					LogPath string `json:"log_path"`
				} `json:"jobs"`
			}
			if err := json.Unmarshal([]byte(result.Stdout), &status); err != nil {
				return fmt.Errorf("failed to parse JSON: %w", err)
			}

			for _, job := range status.Jobs {
				if job.Status != "failed" || job.LogPath == "" {
					continue
				}

				// log_path is relative to cwd (where the status command ran), resolve it
				logAbsPath := job.LogPath
				if !filepath.IsAbs(logAbsPath) {
					logAbsPath = filepath.Join(projectDir, logAbsPath)
				}

				if _, err := os.Stat(logAbsPath); err != nil {
					return fmt.Errorf("log file %s does not exist: %w", logAbsPath, err)
				}

				return nil // Found and verified at least one log file
			}

			return fmt.Errorf("no failed job with log_path found in status output")
		}),
	},
)
