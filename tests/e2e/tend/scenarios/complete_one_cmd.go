package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"

	"github.com/grovetools/flow/pkg/orchestration"
)

// CompleteOneCmdScenario tests the enhanced `flow complete` command that accepts
// a plan slug and job file in a single command, bypassing `flow plan set`.
var CompleteOneCmdScenario = harness.NewScenario(
	"complete-one-cmd",
	"Tests flow complete with positional slug, --plan flag, legacy path, active plan, and error handling",
	[]string{"core", "plan", "hoisted", "complete"},
	[]harness.Step{
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
			harness.Mock{CommandName: "git"},
		),

		harness.NewStep("Setup test environment with plan and jobs", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "complete-cmd-test")
			if err != nil {
				return err
			}

			// Create a git repo with initial commit
			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Complete One-Cmd Test\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			// Initialize a plan
			cmd := ctx.Bin("plan", "init", "test-complete-plan")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "complete-cmd-test", "plans", "test-complete-plan")
			ctx.Set("plan_path", planPath)
			ctx.Set("plan_name", "test-complete-plan")

			// Add 5 chat jobs so each test scenario has a fresh job to complete
			jobTitles := []string{
				"Positional Args Job",
				"Flag Syntax Job",
				"Legacy Path Job",
				"Active Plan Job",
				"Plan Subcommand Job",
			}

			for _, title := range jobTitles {
				cmd = ctx.Bin("add", "test-complete-plan",
					"-t", "chat",
					"--title", title,
					"-p", "Test prompt for "+title)
				cmd.Dir(projectDir)
				result = cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if err := result.AssertSuccess(); err != nil {
					return fmt.Errorf("failed to add job %q: %w", title, err)
				}
			}

			// Discover the actual job filenames created
			jobFiles, err := fs.ListFiles(planPath)
			if err != nil {
				return err
			}

			// Map job files by their slug portions
			for _, f := range jobFiles {
				if !strings.HasSuffix(f, ".md") {
					continue
				}
				switch {
				case strings.Contains(f, "positional-args-job"):
					ctx.Set("job_positional", f)
				case strings.Contains(f, "flag-syntax-job"):
					ctx.Set("job_flag", f)
				case strings.Contains(f, "legacy-path-job"):
					ctx.Set("job_legacy", f)
				case strings.Contains(f, "active-plan-job"):
					ctx.Set("job_active", f)
				case strings.Contains(f, "plan-subcommand-job"):
					ctx.Set("job_subcmd", f)
				}
			}

			// Ensure no active plan is set
			cmd = ctx.Bin("unset")
			cmd.Dir(projectDir)
			cmd.Run() // Ignore error if nothing to unset

			return nil
		}),

		// Test 1: Positional args syntax — flow complete <slug> <job-file>
		harness.NewStep("Test positional args: flow complete <slug> <job-file>", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			jobFile := ctx.GetString("job_positional")

			// Ensure no active plan
			cmd := ctx.Bin("unset")
			cmd.Dir(projectDir)
			cmd.Run()

			// Complete using positional args
			cmd = ctx.Bin("complete", "test-complete-plan", jobFile)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("complete with positional args failed: %w", err)
			}

			// Verify output mentions completion
			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("output confirms job completed", result.Stdout, "Job completed")

				// Verify the job file has status=completed
				jobPath := filepath.Join(planPath, jobFile)
				job, err := orchestration.LoadJob(jobPath)
				if err != nil {
					v.Equal("job loads successfully", nil, err)
					return
				}
				v.Equal("job status is completed", string(orchestration.JobStatusCompleted), string(job.Status))
			})
		}),

		// Test 2: Flag syntax — flow complete --plan <slug> <job-file>
		harness.NewStep("Test flag syntax: flow complete --plan <slug> <job-file>", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			jobFile := ctx.GetString("job_flag")

			// Ensure no active plan
			cmd := ctx.Bin("unset")
			cmd.Dir(projectDir)
			cmd.Run()

			// Complete using --plan flag
			cmd = ctx.Bin("complete", "--plan", "test-complete-plan", jobFile)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("complete with --plan flag failed: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("output confirms job completed", result.Stdout, "Job completed")

				jobPath := filepath.Join(planPath, jobFile)
				job, err := orchestration.LoadJob(jobPath)
				if err != nil {
					v.Equal("job loads successfully", nil, err)
					return
				}
				v.Equal("job status is completed", string(orchestration.JobStatusCompleted), string(job.Status))
			})
		}),

		// Test 3: Legacy path syntax — flow complete <slug>/<job-file>
		harness.NewStep("Test legacy path: flow complete <slug>/<job-file>", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			jobFile := ctx.GetString("job_legacy")

			// Ensure no active plan
			cmd := ctx.Bin("unset")
			cmd.Dir(projectDir)
			cmd.Run()

			// Complete using legacy path syntax (slug/job-file)
			legacyPath := "test-complete-plan/" + jobFile
			cmd = ctx.Bin("complete", legacyPath)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("complete with legacy path failed: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("output confirms job completed", result.Stdout, "Job completed")

				jobPath := filepath.Join(planPath, jobFile)
				job, err := orchestration.LoadJob(jobPath)
				if err != nil {
					v.Equal("job loads successfully", nil, err)
					return
				}
				v.Equal("job status is completed", string(orchestration.JobStatusCompleted), string(job.Status))
			})
		}),

		// Test 4: Active plan syntax — flow set <slug>, then flow complete <job-file>
		harness.NewStep("Test active plan: flow set then flow complete <job-file>", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			jobFile := ctx.GetString("job_active")

			// Set the active plan
			cmd := ctx.Bin("set", "test-complete-plan")
			cmd.Dir(projectDir)
			result := cmd.Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("set active plan failed: %w", err)
			}

			// Complete using just the job filename (relies on active plan)
			cmd = ctx.Bin("complete", jobFile)
			cmd.Dir(projectDir)
			result = cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("complete with active plan failed: %w", err)
			}

			// Cleanup: unset active plan
			cmd = ctx.Bin("unset")
			cmd.Dir(projectDir)
			cmd.Run()

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("output confirms job completed", result.Stdout, "Job completed")

				jobPath := filepath.Join(planPath, jobFile)
				job, err := orchestration.LoadJob(jobPath)
				if err != nil {
					v.Equal("job loads successfully", nil, err)
					return
				}
				v.Equal("job status is completed", string(orchestration.JobStatusCompleted), string(job.Status))
			})
		}),

		// Test 5: flow plan complete (subcommand) also supports positional args
		harness.NewStep("Test subcommand: flow plan complete <slug> <job-file>", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			jobFile := ctx.GetString("job_subcmd")

			// Ensure no active plan
			cmd := ctx.Bin("unset")
			cmd.Dir(projectDir)
			cmd.Run()

			// Complete via the plan subcommand with positional args
			cmd = ctx.Bin("plan", "complete", "test-complete-plan", jobFile)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan complete with positional args failed: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("output confirms job completed", result.Stdout, "Job completed")

				jobPath := filepath.Join(planPath, jobFile)
				job, err := orchestration.LoadJob(jobPath)
				if err != nil {
					v.Equal("job loads successfully", nil, err)
					return
				}
				v.Equal("job status is completed", string(orchestration.JobStatusCompleted), string(job.Status))
			})
		}),

		// Test 6: Error handling — invalid plan slug
		harness.NewStep("Test error: invalid plan slug", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// Ensure no active plan
			cmd := ctx.Bin("unset")
			cmd.Dir(projectDir)
			cmd.Run()

			cmd = ctx.Bin("complete", "nonexistent-plan", "some-job.md")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertFailure(); err != nil {
				return fmt.Errorf("expected failure for invalid plan slug: %w", err)
			}

			combinedOutput := result.Stdout + result.Stderr
			if !strings.Contains(combinedOutput, "load plan") && !strings.Contains(combinedOutput, "no such file") {
				return fmt.Errorf("expected error about plan loading, got: %s", combinedOutput)
			}

			return nil
		}),

		// Test 7: Error handling — invalid job filename
		harness.NewStep("Test error: invalid job filename", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// Ensure no active plan
			cmd := ctx.Bin("unset")
			cmd.Dir(projectDir)
			cmd.Run()

			cmd = ctx.Bin("complete", "test-complete-plan", "nonexistent-job.md")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertFailure(); err != nil {
				return fmt.Errorf("expected failure for invalid job: %w", err)
			}

			combinedOutput := result.Stdout + result.Stderr
			if !strings.Contains(combinedOutput, "job not found") {
				return fmt.Errorf("expected 'job not found' error, got: %s", combinedOutput)
			}

			return nil
		}),

		// Test 8: Error handling — invalid job with --plan flag
		harness.NewStep("Test error: invalid job with --plan flag", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("complete", "--plan", "test-complete-plan", "nonexistent-job.md")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertFailure(); err != nil {
				return fmt.Errorf("expected failure for invalid job with --plan: %w", err)
			}

			combinedOutput := result.Stdout + result.Stderr
			if !strings.Contains(combinedOutput, "job not found") {
				return fmt.Errorf("expected 'job not found' error, got: %s", combinedOutput)
			}

			return nil
		}),
	},
)
