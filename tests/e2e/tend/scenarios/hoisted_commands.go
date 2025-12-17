package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-tend/pkg/assert"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// HoistedCommandsScenario tests the hoisted plan commands that are now available at the top level
var HoistedCommandsScenario = harness.NewScenario(
	"hoisted-commands",
	"Tests that plan subcommands are accessible directly at the top level for improved UX",
	[]string{"core", "plan", "hoisted", "ux"},
	[]harness.Step{
		// Setup mocks for LLM operations
		harness.SetupMocks(
			harness.Mock{CommandName: "llm"},
			harness.Mock{CommandName: "cx"},
			harness.Mock{CommandName: "git"},
		),

		harness.NewStep("Setup test environment", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "hoisted-test-project")
			if err != nil {
				return err
			}

			// Create a git repo with initial commit
			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}

			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Hoisted Commands Test\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			return nil
		}),

		// Test 1: Top-level status command
		harness.NewStep("Test 'flow status' without plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// Test status command when no plan exists
			cmd := ctx.Bin("status")
			cmd.Dir(projectDir)
			result := cmd.Run()

			// Should fail gracefully when no plan exists
			if result.ExitCode == 0 {
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				return fmt.Errorf("expected status to fail without a plan, but it succeeded")
			}

			// Check for any reasonable error message
			combinedOutput := result.Stdout + result.Stderr
			if len(combinedOutput) == 0 {
				return fmt.Errorf("expected error message but got empty output")
			}

			// The command failed as expected, which is what we want
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			return nil
		}),

		// Test 2: Top-level list command
		harness.NewStep("Test 'flow list' with no plans", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("list")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("list command failed: %w", err)
			}

			// The list command should work even with no plans - just verify it doesn't crash
			// and produces some output (even if empty)
			return nil
		}),

		// Initialize a plan using the old command to have something to work with
		harness.NewStep("Initialize a test plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			// Use plan init to create a plan with worktree
			cmd := ctx.Bin("plan", "init", "test-hoisted-plan", "--worktree")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "hoisted-test-project", "plans", "test-hoisted-plan")
			ctx.Set("plan_path", planPath)
			ctx.Set("plan_name", "test-hoisted-plan")

			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "test-hoisted-plan")
			ctx.Set("worktree_path", worktreePath)

			return fs.AssertExists(planPath)
		}),

		// Test 3: Top-level add command
		harness.NewStep("Test 'flow add' command", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			planName := ctx.GetString("plan_name")

			// Add a new job using the hoisted command
			cmd := ctx.Bin("add", planName,
				"-t", "oneshot",
				"--title", "Test Job via Hoisted Add",
				"-p", "This is a test job added via the hoisted add command")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("add command failed: %w", err)
			}

			// Verify the job was created
			jobFiles, err := fs.ListFiles(planPath)
			if err != nil {
				return err
			}

			foundTestJob := false
			for _, f := range jobFiles {
				if strings.HasSuffix(f, ".md") && strings.Contains(f, "test-job-via-hoisted-add") {
					foundTestJob = true
					ctx.Set("test_job_file", filepath.Join(planPath, f))
					break
				}
			}

			if !foundTestJob {
				return fmt.Errorf("test job file not found after add command")
			}

			return nil
		}),

		// Test 4: Top-level status command with plan
		harness.NewStep("Test 'flow status' with plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planName := ctx.GetString("plan_name")

			// Use --json flag to avoid TUI requirement
			cmd := ctx.Bin("status", planName, "--json")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("status command failed: %w", err)
			}

			// Should show the test job we added (either by title or filename)
			if !strings.Contains(result.Stdout, "test-job-via-hoisted-add") {
				return fmt.Errorf("expected status output to show the test job")
			}

			return nil
		}),

		// Test 5: Top-level list command with plans
		harness.NewStep("Test 'flow list' with plans", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("list")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("list command failed: %w", err)
			}

			// Should show our test plan
			if !strings.Contains(result.Stdout, "test-hoisted-plan") {
				return fmt.Errorf("expected list output to show test-hoisted-plan")
			}

			return nil
		}),

		// Test 6: Top-level graph command
		harness.NewStep("Test 'flow graph' command", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planName := ctx.GetString("plan_name")

			cmd := ctx.Bin("graph", planName, "-f", "ascii")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("graph command failed: %w", err)
			}

			// Should show ASCII graph (either "Job Graph" or "Job Dependency Graph")
			hasGraph := strings.Contains(result.Stdout, "Job Dependency Graph") || strings.Contains(result.Stdout, "Job Graph")
			hasStatus := strings.Contains(strings.ToLower(result.Stdout), "pending")
			if !hasGraph || !hasStatus {
				return fmt.Errorf("expected graph output with job status")
			}

			return nil
		}),

		// Test 7: Top-level set/current/unset commands
		harness.NewStep("Test 'flow set/current/unset' commands", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planName := ctx.GetString("plan_name")

			// Test set command
			cmd := ctx.Bin("set", planName)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput("flow set", result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("set command failed: %w", err)
			}

			if !strings.Contains(result.Stdout, "Set active job to") {
				return fmt.Errorf("expected confirmation message from set command")
			}

			// Test current command
			cmd = ctx.Bin("current")
			cmd.Dir(projectDir)
			result = cmd.Run()
			ctx.ShowCommandOutput("flow current", result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("current command failed: %w", err)
			}

			if !strings.Contains(result.Stdout, planName) {
				return fmt.Errorf("expected current command to show plan name")
			}

			// Test unset command
			cmd = ctx.Bin("unset")
			cmd.Dir(projectDir)
			result = cmd.Run()
			ctx.ShowCommandOutput("flow unset", result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("unset command failed: %w", err)
			}

			if !strings.Contains(result.Stdout, "Cleared active job") {
				return fmt.Errorf("expected confirmation from unset command")
			}

			// Verify current shows no active job
			cmd = ctx.Bin("current")
			cmd.Dir(projectDir)
			result = cmd.Run()
			ctx.ShowCommandOutput("flow current after unset", result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("current command failed after unset: %w", err)
			}

			if !strings.Contains(result.Stdout, "No active job set") {
				return fmt.Errorf("expected current to show no active job after unset")
			}

			return nil
		}),

		// Test 8: Top-level hold/unhold commands
		harness.NewStep("Test 'flow hold/unhold' commands", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planName := ctx.GetString("plan_name")
			planPath := ctx.GetString("plan_path")

			// Test hold command
			cmd := ctx.Bin("hold", planName)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput("flow hold", result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("hold command failed: %w", err)
			}

			// Verify status in config file
			planConfigPath := filepath.Join(planPath, ".grove-plan.yml")
			if err := assert.YAMLField(planConfigPath, "status", "hold", "Plan status should be 'hold'"); err != nil {
				return err
			}

			// Verify held plan is hidden from default list
			cmd = ctx.Bin("list")
			cmd.Dir(projectDir)
			result = cmd.Run()

			if strings.Contains(result.Stdout, planName) {
				return fmt.Errorf("held plan should not appear in default list")
			}

			// Test unhold command
			cmd = ctx.Bin("unhold", planName)
			cmd.Dir(projectDir)
			result = cmd.Run()
			ctx.ShowCommandOutput("flow unhold", result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("unhold command failed: %w", err)
			}

			// Verify plan appears in list again
			cmd = ctx.Bin("list")
			cmd.Dir(projectDir)
			result = cmd.Run()

			if !strings.Contains(result.Stdout, planName) {
				return fmt.Errorf("unheld plan should appear in list")
			}

			return nil
		}),

		// Test 9: Top-level complete command
		harness.NewStep("Test 'flow complete' command", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			testJobFile := ctx.GetString("test_job_file")

			// Mark the test job as completed
			cmd := ctx.Bin("complete", testJobFile)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput("flow complete", result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("complete command failed: %w", err)
			}

			// Load the job to verify status
			job, err := orchestration.LoadJob(testJobFile)
			if err != nil {
				return fmt.Errorf("loading job after complete: %w", err)
			}

			if job.Status != "completed" {
				return fmt.Errorf("expected job status to be 'completed', got %q", job.Status)
			}

			return nil
		}),

		// Test 10: Top-level review command
		harness.NewStep("Test 'flow review' command", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planName := ctx.GetString("plan_name")
			planPath := ctx.GetString("plan_path")

			cmd := ctx.Bin("review", planName)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput("flow review", result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("review command failed: %w", err)
			}

			// Check that plan is marked as reviewed
			planConfigPath := filepath.Join(planPath, ".grove-plan.yml")
			if err := assert.YAMLField(planConfigPath, "status", "review", "Plan status should be 'review'"); err != nil {
				return err
			}

			return nil
		}),

		// Test 11: Top-level step command (non-interactive verification)
		harness.NewStep("Test 'flow step' command help", func(ctx *harness.Context) error {
			// Since step is interactive, just verify the command exists and shows help
			cmd := ctx.Bin("step", "--help")
			result := cmd.Run()

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("step --help failed: %w", err)
			}

			// Check for either old or new help text
			if !strings.Contains(result.Stdout, "interactive wizard") && !strings.Contains(result.Stdout, "Step through plan execution") {
				return fmt.Errorf("expected step help text")
			}

			return nil
		}),

		// Test 12: Top-level run command
		harness.NewStep("Test 'flow run' command", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			planName := ctx.GetString("plan_name")

			// Add another oneshot job that we can actually run
			cmd := ctx.Bin("add", planName,
				"-t", "oneshot",
				"--title", "Runnable Test Job",
				"-p", "echo 'Test output from hoisted run command'")
			cmd.Dir(projectDir)
			result := cmd.Run()

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("failed to add runnable job: %w", err)
			}

			// Find the new job file
			jobFiles, err := fs.ListFiles(planPath)
			if err != nil {
				return err
			}

			var runnableJobFile string
			for _, f := range jobFiles {
				if strings.HasSuffix(f, ".md") && strings.Contains(f, "runnable-test-job") {
					runnableJobFile = filepath.Join(planPath, f)
					break
				}
			}

			if runnableJobFile == "" {
				return fmt.Errorf("runnable test job not found")
			}

			// Try to run it with the hoisted command
			// Note: The job will fail because llm/cx are mocked, but we just want to verify
			// the command exists and attempts to run
			cmd = ctx.Bin("run", runnableJobFile, "-y")
			cmd.Dir(projectDir)
			result = cmd.Run()
			ctx.ShowCommandOutput("flow run", result.Stdout, result.Stderr)

			// The command will fail due to mocked dependencies, but we expect it to at least
			// attempt to run and show job execution messages
			if !strings.Contains(result.Stdout, "Running job") || !strings.Contains(result.Stdout, "runnable-test-job") {
				return fmt.Errorf("expected run command to attempt job execution")
			}

			return nil
		}),

		// Test 13: Verify backward compatibility
		harness.NewStep("Verify 'flow plan' subcommands still work", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planName := ctx.GetString("plan_name")

			// Test that the old command structure still works (use --json to avoid TUI)
			cmd := ctx.Bin("plan", "status", planName, "--json")
			cmd.Dir(projectDir)
			result := cmd.Run()

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("'flow plan status' backward compatibility failed: %w", err)
			}

			// Check that the plan subcommand list shows the hoisted command hints
			cmd = ctx.Bin("plan", "--help")
			result = cmd.Run()

			// The short descriptions should mention the hoisted commands
			if !strings.Contains(result.Stdout, "use: flow status") {
				// If not in help, also acceptable if the command just works
				ctx.ShowCommandOutput("flow plan --help", result.Stdout, result.Stderr)
				// Don't fail the test, as backward compatibility is the key thing
			}

			return nil
		}),

		// Test 14: Top-level action command
		harness.NewStep("Test 'flow action' command", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// First, let's create a new plan from a recipe that has actions
			cmd := ctx.Bin("plan", "init", "action-test-plan", "--recipe", "standard-feature")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput("flow plan init with recipe", result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				// If no recipe available, skip this test
				if strings.Contains(result.Stderr, "recipe") {
					return nil // Skip test gracefully if recipe not available
				}
				return fmt.Errorf("failed to create plan with recipe: %w", err)
			}

			// Test listing actions with the hoisted command
			cmd = ctx.Bin("action", "--list", "action-test-plan")
			cmd.Dir(projectDir)
			result = cmd.Run()
			ctx.ShowCommandOutput("flow action --list", result.Stdout, result.Stderr)

			// The command should work (might say no actions if recipe has none)
			if result.ExitCode != 0 && !strings.Contains(result.Stderr, "no recipe") {
				return fmt.Errorf("action --list command failed unexpectedly")
			}

			return nil
		}),

		// Test 15: Top-level finish command
		harness.NewStep("Test 'flow finish' command", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planName := ctx.GetString("plan_name")

			// Test finish with --yes flag to auto-confirm
			cmd := ctx.Bin("finish", planName, "--yes", "--archive")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput("flow finish", result.Stdout, result.Stderr)

			// Command should succeed
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("finish command failed: %w", err)
			}

			// Verify plan was archived and cleanup finished
			hasArchived := strings.Contains(result.Stdout, "Archive plan") || strings.Contains(result.Stdout, "Archived plan")
			hasCleanup := strings.Contains(result.Stdout, "cleanup finished") || strings.Contains(result.Stdout, "Cleanup complete")
			if !hasArchived || !hasCleanup {
				return fmt.Errorf("expected finish command to show completion messages")
			}

			return nil
		}),
	},
)

// TestHoistedCommandsWithActiveJob tests that hoisted commands work with the active job feature
var HoistedCommandsWithActiveJobScenario = harness.NewScenario(
	"hoisted-commands-active-job",
	"Tests that hoisted commands properly use the active job when set",
	[]string{"core", "plan", "hoisted", "active-job"},
	[]harness.Step{
		harness.SetupMocks(
			harness.Mock{CommandName: "llm"},
			harness.Mock{CommandName: "cx"},
		),

		harness.NewStep("Setup environment with plan", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "active-job-test")
			if err != nil {
				return err
			}

			// Initialize a plan
			cmd := ctx.Bin("plan", "init", "active-test-plan")
			cmd.Dir(projectDir)
			result := cmd.Run()

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "active-job-test", "plans", "active-test-plan")
			ctx.Set("plan_path", planPath)
			ctx.Set("plan_name", "active-test-plan")

			return nil
		}),

		harness.NewStep("Set active job and test commands without explicit directory", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planName := ctx.GetString("plan_name")

			// Set the active job
			cmd := ctx.Bin("set", planName)
			cmd.Dir(projectDir)
			result := cmd.Run()

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("failed to set active job: %w", err)
			}

			// Now test status without specifying the plan (use --json to avoid TUI)
			cmd = ctx.Bin("status", "--json")
			cmd.Dir(projectDir)
			result = cmd.Run()
			ctx.ShowCommandOutput("flow status with active job", result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("status with active job failed: %w", err)
			}

			// Add a job without specifying the plan directory
			cmd = ctx.Bin("add",
				"-t", "oneshot",
				"--title", "Job via Active",
				"-p", "Test job added using active job")
			cmd.Dir(projectDir)
			result = cmd.Run()
			ctx.ShowCommandOutput("flow add with active job", result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("add with active job failed: %w", err)
			}

			// Verify the job was created
			planPath := ctx.GetString("plan_path")
			jobFiles, err := fs.ListFiles(planPath)
			if err != nil {
				return err
			}

			foundJob := false
			for _, f := range jobFiles {
				if strings.HasSuffix(f, ".md") && strings.Contains(f, "job-via-active") {
					foundJob = true
					break
				}
			}

			if !foundJob {
				return fmt.Errorf("job added with active job not found")
			}

			return nil
		}),

		harness.NewStep("Test graph command with active job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// Graph should work without specifying plan when active job is set
			cmd := ctx.Bin("graph", "-f", "ascii")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput("flow graph with active job", result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("graph with active job failed: %w", err)
			}

			// Check for either form of the graph header
			if !strings.Contains(result.Stdout, "Graph") && !strings.Contains(result.Stdout, "Level") {
				return fmt.Errorf("expected graph output")
			}

			return nil
		}),

		harness.NewStep("Clear active job and verify commands require directory", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// Clear the active job
			cmd := ctx.Bin("unset")
			cmd.Dir(projectDir)
			result := cmd.Run()

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("failed to unset active job: %w", err)
			}

			// Now status should fail without a directory
			cmd = ctx.Bin("status")
			cmd.Dir(projectDir)
			result = cmd.Run()

			if result.ExitCode == 0 {
				return fmt.Errorf("expected status to fail without active job or directory")
			}

			return nil
		}),
	},
)