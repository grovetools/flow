package scenarios

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/tend/pkg/assert"
	"github.com/grovetools/tend/pkg/harness"
)

var AbandonedJobsScenario = harness.NewScenario(
	"abandoned-jobs",
	"Tests the abandoned job status feature including dependency resolution and status display",
	[]string{"core", "orchestration", "abandoned"},
	[]harness.Step{
		harness.NewStep("Setup plan with job dependencies", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "abandoned-project")
			if err != nil {
				return err
			}

			// Initialize the plan
			initCmd := ctx.Bin("plan", "init", "abandoned-plan")
			initCmd.Dir(projectDir)
			if result := initCmd.Run(); result.Error != nil {
				return fmt.Errorf("plan init failed: %w", result.Error)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "abandoned-project", "plans", "abandoned-plan")
			ctx.Set("plan_path", planPath)

			// Add Job A: Independent job that will be abandoned
			jobA := ctx.Bin("plan", "add", "abandoned-plan", "--type", "shell", "--title", "Job to Abandon", "-p", "echo 'This job will be abandoned'")
			jobA.Dir(projectDir)
			resultA := jobA.Run()
			ctx.ShowCommandOutput(jobA.String(), resultA.Stdout, resultA.Stderr)
			if resultA.Error != nil {
				return fmt.Errorf("failed to add Job A: %w", resultA.Error)
			}

			// Add Job B: Depends on Job A
			jobB := ctx.Bin("plan", "add", "abandoned-plan", "--type", "shell", "--title", "Dependent Job", "-p", "echo 'This depends on abandoned job'", "-d", "01-job-to-abandon.md")
			jobB.Dir(projectDir)
			resultB := jobB.Run()
			ctx.ShowCommandOutput(jobB.String(), resultB.Stdout, resultB.Stderr)
			if resultB.Error != nil {
				return fmt.Errorf("failed to add Job B: %w", resultB.Error)
			}

			// Add Job C: Also depends on Job A
			jobC := ctx.Bin("plan", "add", "abandoned-plan", "--type", "shell", "--title", "Another Dependent", "-p", "echo 'Also depends on abandoned'", "-d", "01-job-to-abandon.md")
			jobC.Dir(projectDir)
			resultC := jobC.Run()
			ctx.ShowCommandOutput(jobC.String(), resultC.Stdout, resultC.Stderr)
			if resultC.Error != nil {
				return fmt.Errorf("failed to add Job C: %w", resultC.Error)
			}

			// Add Job D: Independent job
			jobD := ctx.Bin("plan", "add", "abandoned-plan", "--type", "shell", "--title", "Independent Job", "-p", "echo 'Independent job'")
			jobD.Dir(projectDir)
			if result := jobD.Run(); result.Error != nil {
				return fmt.Errorf("failed to add Job D: %w", result.Error)
			}

			// Add Job E: Depends on both B and C (transitive dependency on A)
			jobE := ctx.Bin("plan", "add", "abandoned-plan", "--type", "shell", "--title", "Transitive Dependent", "-p", "echo 'Depends on B and C'", "-d", "02-dependent-job.md,03-another-dependent.md")
			jobE.Dir(projectDir)
			if result := jobE.Run(); result.Error != nil {
				return fmt.Errorf("failed to add Job E: %w", result.Error)
			}

			return nil
		}),

		harness.NewStep("Mark job as abandoned and verify file changes", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// Load the plan to get job details
			plan, err := orchestration.LoadPlan(planPath)
			if err != nil {
				return fmt.Errorf("loading plan: %w", err)
			}

			// Find Job A (01-job-to-abandon.md)
			var jobA *orchestration.Job
			for _, job := range plan.Jobs {
				if job.Filename == "01-job-to-abandon.md" {
					jobA = job
					break
				}
			}
			if jobA == nil {
				return fmt.Errorf("could not find job to abandon")
			}

			// Mark the job as abandoned using StatePersister
			sp := orchestration.NewStatePersister()
			if err := sp.UpdateJobStatus(jobA, orchestration.JobStatusAbandoned); err != nil {
				return fmt.Errorf("updating job status to abandoned: %w", err)
			}

			// Verify the job file contains the abandoned note
			jobPath := filepath.Join(planPath, jobA.Filename)
			content, err := os.ReadFile(jobPath)
			if err != nil {
				return fmt.Errorf("reading abandoned job file: %w", err)
			}

			if !strings.Contains(string(content), "This job was abandoned by the user") {
				return fmt.Errorf("abandoned job file does not contain expected note")
			}

			// Verify status in frontmatter
			if err := assert.YAMLField(jobPath, "status", "abandoned"); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Test dependency resolution with abandoned job", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// Reload the plan to get updated status
			plan, err := orchestration.LoadPlan(planPath)
			if err != nil {
				return fmt.Errorf("loading plan: %w", err)
			}

			// Find Job B (depends on abandoned Job A)
			var jobB *orchestration.Job
			for _, job := range plan.Jobs {
				if job.Filename == "02-dependent-job.md" {
					jobB = job
					break
				}
			}
			if jobB == nil {
				return fmt.Errorf("could not find dependent job B")
			}

			// Verify that Job B is runnable despite depending on abandoned Job A
			if !jobB.IsRunnable() {
				return fmt.Errorf("Job B should be runnable after its dependency was abandoned")
			}

			// Find Job C (also depends on abandoned Job A)
			var jobC *orchestration.Job
			for _, job := range plan.Jobs {
				if job.Filename == "03-another-dependent.md" {
					jobC = job
					break
				}
			}
			if jobC == nil {
				return fmt.Errorf("could not find dependent job C")
			}

			// Verify that Job C is also runnable
			if !jobC.IsRunnable() {
				return fmt.Errorf("Job C should be runnable after its dependency was abandoned")
			}

			return nil
		}),

		harness.NewStep("Run dependent jobs and verify they execute", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// Run Job B (depends on abandoned job)
			runB := ctx.Bin("plan", "run", "abandoned-plan/02-dependent-job.md", "--yes")
			runB.Dir(projectDir)
			resultB := runB.Run()
			ctx.ShowCommandOutput(runB.String(), resultB.Stdout, resultB.Stderr)
			if resultB.Error != nil {
				return fmt.Errorf("Job B should run despite abandoned dependency: %w", resultB.Error)
			}

			// Run Job C (also depends on abandoned job)
			runC := ctx.Bin("plan", "run", "abandoned-plan/03-another-dependent.md", "--yes")
			runC.Dir(projectDir)
			resultC := runC.Run()
			ctx.ShowCommandOutput(runC.String(), resultC.Stdout, resultC.Stderr)
			if resultC.Error != nil {
				return fmt.Errorf("Job C should run despite abandoned dependency: %w", resultC.Error)
			}

			// Now run Job E which depends on B and C
			runE := ctx.Bin("plan", "run", "abandoned-plan/05-transitive-dependent.md", "--yes")
			runE.Dir(projectDir)
			resultE := runE.Run()
			ctx.ShowCommandOutput(runE.String(), resultE.Stdout, resultE.Stderr)
			if resultE.Error != nil {
				return fmt.Errorf("Job E should run after B and C complete: %w", resultE.Error)
			}

			return nil
		}),

		harness.NewStep("Test plan list shows abandoned count", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// Run plan list command
			listCmd := ctx.Bin("plan", "list")
			listCmd.Dir(projectDir)
			result := listCmd.Run()
			ctx.ShowCommandOutput(listCmd.String(), result.Stdout, result.Stderr)
			if result.Error != nil {
				return fmt.Errorf("plan list failed: %w", result.Error)
			}

			// Verify output contains abandoned count
			if !strings.Contains(result.Stdout, "1 abandoned") {
				return fmt.Errorf("plan list should show '1 abandoned' in summary, got: %s", result.Stdout)
			}

			return nil
		}),

		harness.NewStep("Test plan status shows abandoned job correctly", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// Run plan status command with --json flag to avoid TUI
			statusCmd := ctx.Bin("plan", "status", "abandoned-plan", "--json")
			statusCmd.Dir(projectDir)
			result := statusCmd.Run()
			ctx.ShowCommandOutput(statusCmd.String(), result.Stdout, result.Stderr)
			if result.Error != nil {
				return fmt.Errorf("plan status failed: %w", result.Error)
			}

			// Verify abandoned job is shown with correct status
			if !strings.Contains(result.Stdout, "abandoned") {
				return fmt.Errorf("plan status should show abandoned job status")
			}

			return nil
		}),

		harness.NewStep("Test abandoning multiple jobs", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// Load the plan
			plan, err := orchestration.LoadPlan(planPath)
			if err != nil {
				return fmt.Errorf("loading plan: %w", err)
			}

			// Mark Job D (independent job) as abandoned too
			var jobD *orchestration.Job
			for _, job := range plan.Jobs {
				if job.Filename == "04-independent-job.md" {
					jobD = job
					break
				}
			}
			if jobD == nil {
				return fmt.Errorf("could not find independent job")
			}

			sp := orchestration.NewStatePersister()
			if err := sp.UpdateJobStatus(jobD, orchestration.JobStatusAbandoned); err != nil {
				return fmt.Errorf("updating job D status to abandoned: %w", err)
			}

			// Verify both jobs have abandoned notes
			jobDPath := filepath.Join(planPath, jobD.Filename)
			content, err := os.ReadFile(jobDPath)
			if err != nil {
				return fmt.Errorf("reading abandoned job D file: %w", err)
			}

			if !strings.Contains(string(content), "This job was abandoned by the user") {
				return fmt.Errorf("abandoned job D file does not contain expected note")
			}

			return nil
		}),

		harness.NewStep("Verify plan list shows multiple abandoned jobs", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// Run plan list command again
			listCmd := ctx.Bin("plan", "list")
			listCmd.Dir(projectDir)
			result := listCmd.Run()
			ctx.ShowCommandOutput(listCmd.String(), result.Stdout, result.Stderr)
			if result.Error != nil {
				return fmt.Errorf("plan list failed: %w", result.Error)
			}

			// Verify output shows 2 abandoned jobs
			if !strings.Contains(result.Stdout, "2 abandoned") {
				return fmt.Errorf("plan list should show '2 abandoned' in summary after abandoning second job, got: %s", result.Stdout)
			}

			return nil
		}),

		harness.NewStep("Test that abandoned note is not duplicated on multiple updates", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// Load the plan
			plan, err := orchestration.LoadPlan(planPath)
			if err != nil {
				return fmt.Errorf("loading plan: %w", err)
			}

			// Find Job D which was already abandoned
			var jobD *orchestration.Job
			for _, job := range plan.Jobs {
				if job.Filename == "04-independent-job.md" {
					jobD = job
					break
				}
			}
			if jobD == nil {
				return fmt.Errorf("could not find job D")
			}

			// Try to mark it as abandoned again (simulating multiple updates)
			sp := orchestration.NewStatePersister()
			if err := sp.UpdateJobStatus(jobD, orchestration.JobStatusAbandoned); err != nil {
				return fmt.Errorf("re-updating job D status to abandoned: %w", err)
			}

			// Read the file and count occurrences of the note
			jobDPath := filepath.Join(planPath, jobD.Filename)
			content, err := os.ReadFile(jobDPath)
			if err != nil {
				return fmt.Errorf("reading job D file: %w", err)
			}

			noteCount := strings.Count(string(content), "This job was abandoned by the user")
			if noteCount != 1 {
				return fmt.Errorf("abandoned note should appear exactly once, but found %d occurrences", noteCount)
			}

			return nil
		}),
	},
)