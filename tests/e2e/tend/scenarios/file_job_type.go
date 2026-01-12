package scenarios

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/harness"
	"github.com/mattsolo1/grove-tend/pkg/tui"
	"github.com/mattsolo1/grove-tend/pkg/verify"
)

// FileJobTypeScenario tests the file job type - a non-executable job for context/reference content.
var FileJobTypeScenario = harness.NewScenario(
	"file-job-type",
	"Validates the file job type: creation, non-executability, and dependency behavior.",
	[]string{"core", "cli", "file-job"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, _, err := setupDefaultEnvironment(ctx, "file-job-project")
			return err
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),

		harness.NewStep("Verify file type in job list", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// Run flow plan jobs list
			cmd := ctx.Bin("plan", "jobs", "list")
			cmd.Dir(projectDir)
			result := cmd.Run()

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan jobs list failed: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("file type listed", result.Stdout, "file")
				v.Contains("file description present", result.Stdout, "context")
			})
		}),

		harness.NewStep("Initialize plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "file-job-plan")
			cmd.Dir(projectDir)
			if result := cmd.Run(); result.Error != nil {
				return fmt.Errorf("plan init failed: %w", result.Error)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "file-job-project", "plans", "file-job-plan")
			ctx.Set("plan_path", planPath)
			return nil
		}),

		harness.NewStep("Add file job via CLI", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "add", "file-job-plan",
				"--type", "file",
				"--title", "Context Reference",
				"-p", "This is reference content for downstream jobs.")
			cmd.Dir(projectDir)
			result := cmd.Run()

			ctx.ShowCommandOutput("flow plan add --type file", result.Stdout, result.Stderr)
			return result.AssertSuccess()
		}),

		harness.NewStep("Verify file job created with completed status", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// Find the file job
			jobPath := filepath.Join(planPath, "01-context-reference.md")
			ctx.Set("file_job_path", jobPath)

			if err := fs.AssertExists(jobPath); err != nil {
				return fmt.Errorf("file job not created: %w", err)
			}

			// Load and verify job properties
			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("failed to load file job: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("job type is file", orchestration.JobTypeFile, job.Type)
				v.Equal("job status is completed", orchestration.JobStatusCompleted, job.Status)
				v.Equal("job title matches", "Context Reference", job.Title)
			})
		}),

		harness.NewStep("Verify file job is not runnable", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			jobPath := filepath.Join(planPath, "01-context-reference.md")
			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("failed to load file job: %w", err)
			}

			if job.IsRunnable() {
				return fmt.Errorf("file job should not be runnable")
			}
			return nil
		}),

		harness.NewStep("Attempt to run file job directly", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			fileJobPath := ctx.GetString("file_job_path")

			// Try to run the file job directly
			cmd := ctx.Bin("run", fileJobPath, "-y")
			cmd.Dir(projectDir)
			result := cmd.Run()

			ctx.ShowCommandOutput("flow run <file-job>", result.Stdout, result.Stderr)

			// The command should fail or indicate the job is not runnable
			// Check stderr or stdout for appropriate message
			output := result.Stdout + result.Stderr
			if !strings.Contains(strings.ToLower(output), "not runnable") &&
				!strings.Contains(strings.ToLower(output), "cannot run") &&
				!strings.Contains(strings.ToLower(output), "no runnable") {
				return fmt.Errorf("expected error about job not being runnable, got: %s", output)
			}

			return nil
		}),

		harness.NewStep("Add agent job depending on file job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "add", "file-job-plan",
				"--type", "oneshot",
				"--title", "Implementation",
				"-p", "Use the context reference to implement.",
				"-d", "01-context-reference.md")
			cmd.Dir(projectDir)
			result := cmd.Run()

			ctx.ShowCommandOutput("flow plan add with file dependency", result.Stdout, result.Stderr)
			return result.AssertSuccess()
		}),

		harness.NewStep("Verify dependent job is runnable", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// Load the plan
			plan, err := orchestration.LoadPlan(planPath)
			if err != nil {
				return fmt.Errorf("failed to load plan: %w", err)
			}

			// Find the implementation job
			var implJob *orchestration.Job
			for _, job := range plan.Jobs {
				if job.Title == "Implementation" {
					implJob = job
					break
				}
			}

			if implJob == nil {
				return fmt.Errorf("implementation job not found")
			}

			return ctx.Verify(func(v *verify.Collector) {
				// The implementation job should have the file job as dependency
				v.Equal("has dependency", 1, len(implJob.DependsOn))
				// The job should be runnable since file dependency is completed
				v.Equal("is runnable", true, implJob.IsRunnable())
			})
		}),

		harness.NewStep("Verify flow run --next skips file jobs", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			// Create a plan with only a file job to test --next behavior
			cmd := ctx.Bin("plan", "init", "file-only-plan")
			cmd.Dir(projectDir)
			if result := cmd.Run(); result.Error != nil {
				return fmt.Errorf("plan init failed: %w", result.Error)
			}

			notebooksRoot := ctx.GetString("notebooks_root")
			fileOnlyPlanPath := filepath.Join(notebooksRoot, "workspaces", "file-job-project", "plans", "file-only-plan")

			// Add only a file job
			cmd = ctx.Bin("plan", "add", "file-only-plan",
				"--type", "file",
				"--title", "Only File Job",
				"-p", "This is the only job in the plan.")
			cmd.Dir(projectDir)
			if result := cmd.Run(); result.Error != nil {
				return fmt.Errorf("failed to add file job: %w", result.Error)
			}

			// Try flow run --next - should report no runnable jobs
			cmd = ctx.Bin("plan", "run", "--next", fileOnlyPlanPath)
			cmd.Dir(projectDir)
			result := cmd.Run()

			ctx.ShowCommandOutput("flow plan run --next (file only)", result.Stdout, result.Stderr)

			output := result.Stdout + result.Stderr
			if !strings.Contains(strings.ToLower(output), "no runnable") &&
				!strings.Contains(strings.ToLower(output), "no jobs") {
				return fmt.Errorf("expected message about no runnable jobs, got: %s", output)
			}

			// Verify the file job was NOT run (still completed, not running/failed)
			fileJobPath := filepath.Join(fileOnlyPlanPath, "01-only-file-job.md")
			job, err := orchestration.LoadJob(fileJobPath)
			if err != nil {
				return fmt.Errorf("failed to load file job: %w", err)
			}

			if job.Status != orchestration.JobStatusCompleted {
				return fmt.Errorf("file job status changed unexpectedly: %s", job.Status)
			}

			// Restore context for subsequent steps
			ctx.Set("plan_path", planPath)
			return nil
		}),

		harness.NewStep("Add file job without prompt (allowed)", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// File jobs should be allowed without a prompt
			cmd := ctx.Bin("plan", "add", "file-job-plan",
				"--type", "file",
				"--title", "Empty Context File")
			cmd.Dir(projectDir)
			result := cmd.Run()

			ctx.ShowCommandOutput("flow plan add --type file (no prompt)", result.Stdout, result.Stderr)
			return result.AssertSuccess()
		}),

		harness.NewStep("Verify empty file job created successfully", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// Find the empty context file job
			jobPath := filepath.Join(planPath, "03-empty-context-file.md")
			if err := fs.AssertExists(jobPath); err != nil {
				return fmt.Errorf("empty file job not created: %w", err)
			}

			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("failed to load empty file job: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("type is file", orchestration.JobTypeFile, job.Type)
				v.Equal("status is completed", orchestration.JobStatusCompleted, job.Status)
			})
		}),
	},
)

// FileJobTypeTUIScenario tests the file job type in the TUI type picker.
var FileJobTypeTUIScenario = harness.NewScenarioWithOptions(
	"file-job-type-tui",
	"Verifies file job type appears in the TUI type picker.",
	[]string{"tui", "file-job", "type-picker"},
	[]harness.Step{
		harness.NewStep("Setup plan with jobs", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "file-tui-project")
			if err != nil {
				return err
			}

			// Initialize the plan
			initCmd := ctx.Bin("plan", "init", "file-tui-plan")
			initCmd.Dir(projectDir)
			if result := initCmd.Run(); result.Error != nil {
				return fmt.Errorf("plan init failed: %w", result.Error)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "file-tui-project", "plans", "file-tui-plan")
			ctx.Set("plan_path", planPath)

			// Add a shell job that we can change to file type
			addCmd := ctx.Bin("plan", "add", "file-tui-plan", "--type", "shell", "--title", "Test Job", "-p", "echo test")
			addCmd.Dir(projectDir)
			if result := addCmd.Run(); result.Error != nil {
				return fmt.Errorf("failed to add job: %w", result.Error)
			}

			return nil
		}),

		harness.NewStep("Launch status TUI", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			homeDir := ctx.GetString("home_dir")

			flowBinary, err := findFlowBinary()
			if err != nil {
				return err
			}

			wrapperScript := filepath.Join(ctx.RootDir, "run-flow-file-tui")
			scriptContent := fmt.Sprintf("#!/bin/bash\nexport HOME=%s\ncd %s\nexec %s plan status file-tui-plan\n", homeDir, projectDir, flowBinary)
			if err := fs.WriteString(wrapperScript, scriptContent); err != nil {
				return fmt.Errorf("failed to create wrapper script: %w", err)
			}
			if err := os.Chmod(wrapperScript, 0755); err != nil {
				return fmt.Errorf("failed to make wrapper script executable: %w", err)
			}

			session, err := ctx.StartTUI(wrapperScript, []string{})
			if err != nil {
				return fmt.Errorf("failed to start TUI: %w", err)
			}
			ctx.Set("tui_session", session)

			if err := session.WaitForText("Plan Status", 10*time.Second); err != nil {
				content, _ := session.Capture()
				return fmt.Errorf("TUI did not load: %w\nContent:\n%s", err, content)
			}
			return session.WaitStable()
		}),

		harness.NewStep("Open type picker with 'Y'", func(ctx *harness.Context) error {
			session := ctx.Get("tui_session").(*tui.Session)

			time.Sleep(500 * time.Millisecond)

			if err := session.SendKeys("Y"); err != nil {
				return fmt.Errorf("failed to send 'Y' key: %w", err)
			}

			time.Sleep(500 * time.Millisecond)
			return nil
		}),

		harness.NewStep("Verify File type in picker", func(ctx *harness.Context) error {
			session := ctx.Get("tui_session").(*tui.Session)

			if err := session.WaitStable(); err != nil {
				return err
			}

			content, err := session.Capture(tui.WithCleanedOutput())
			if err != nil {
				return err
			}

			return ctx.Verify(func(v *verify.Collector) {
				// The type picker should show "File" as an option
				v.Contains("File type in picker", content, "File")
			})
		}),

		harness.NewStep("Select File type", func(ctx *harness.Context) error {
			session := ctx.Get("tui_session").(*tui.Session)

			// Navigate to find "File" option - it should be near the end of the list
			// Types are: Shell, Oneshot, Chat, Agent, Interactive Agent, Headless Agent, Generate Recipe, File
			// So we need to go down several times
			for i := 0; i < 7; i++ {
				if err := session.SendKeys("Down"); err != nil {
					return fmt.Errorf("failed to navigate down: %w", err)
				}
				time.Sleep(150 * time.Millisecond)
			}

			// Select with Enter
			if err := session.SendKeys("Enter"); err != nil {
				return fmt.Errorf("failed to select file type: %w", err)
			}

			time.Sleep(500 * time.Millisecond)
			return nil
		}),

		harness.NewStep("Verify job type changed to file", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			jobPath := filepath.Join(planPath, "01-test-job.md")
			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("failed to load job: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("job type is file", orchestration.JobTypeFile, job.Type)
			})
		}),

		harness.NewStep("Quit TUI", func(ctx *harness.Context) error {
			session := ctx.Get("tui_session").(*tui.Session)
			return session.SendKeys("q")
		}),
	},
	true,  // localOnly = true, requires tmux
	false, // explicitOnly = false
)
